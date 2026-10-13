"""Small host-local capacity observations on an already owned LTFS mount.

No device commands or lifecycle operations are performed. Files and evidence are
retained. Explicit scene-boundary LTFS sync is an intervention, not normal Close.
"""

import argparse
import hashlib
import json
import os
from pathlib import Path
import time
import uuid

from physical_performance_fixture import read_log_xattr, sample


SCENE_BYTES = 128 * 1024**2
SMALL_BYTES = 256 * 1024
BUFFER_BYTES = 256 * 1024


def owner(work_directory, mount, pages=("34", "0C")):
    observation = sample(str(work_directory), pages=pages)
    if observation.get("status") != "mounted" or Path(observation["mount"]).resolve() != mount:
        raise RuntimeError("Requested mount does not belong to the exact LTFS work_directory")
    return observation


def snapshot(work_directory, mount, pages=("34", "0C", "31")):
    # The raw TapeAlert page can clear flags. Let LTFS consume and retain them in
    # its own cache, rather than hiding an alert from the filesystem's error path.
    observation = owner(work_directory, mount, pages=tuple(page for page in pages if page != "2E"))
    alert = {"request_start_ns": time.time_ns()}
    try:
        alert["value"] = read_log_xattr(mount, "user.ltfs.mediaStorageAlert").decode("ascii")
    except (OSError, UnicodeError) as error:
        alert["error"] = str(error)
    alert["request_end_ns"] = time.time_ns()
    observation["storage_alert"] = alert
    if "parameters" in observation["pages"].get("31", {}):
        observation["pages"]["31"]["units"] = "MiB"
    observation["statvfs_request_start_ns"] = time.time_ns()
    started = time.monotonic_ns()
    try:
        space = os.statvfs(mount)
    except OSError as error:
        # Late LTFS errors must not discard the raw pages already collected.
        observation["statvfs"] = {"timestamp_ns": time.time_ns(), "error": str(error), "errno": error.errno}
        return observation
    finally:
        observation["statvfs_request_end_ns"] = time.time_ns()
        observation["statvfs_request_duration_ns"] = time.monotonic_ns() - started
    observation["statvfs"] = {"timestamp_ns": time.time_ns(), "fragment_bytes": space.f_frsize,
                               "total_bytes": space.f_blocks * space.f_frsize,
                               "free_bytes": space.f_bfree * space.f_frsize,
                               "available_bytes": space.f_bavail * space.f_frsize}
    return observation


def block(scene, number, offset, length, content):
    compressed = content == "compressible" or (content == "alternating" and (number + offset // BUFFER_BYTES) % 2 == 1)
    seed = f"yatm-capacity-v1:{scene}:{number}:{offset}".encode()
    if not compressed:
        return hashlib.shake_256(seed).digest(length)
    random = hashlib.shake_256(seed).digest((length + 1) // 2)
    return b"".join(random[start:start + 4096] * 2 for start in range(0, len(random), 4096))[:length]


def write_file(path, scene, number, size, content):
    digest = hashlib.sha256()
    target = path.open("xb")
    try:
        for offset in range(0, size, BUFFER_BYTES):
            data = block(scene, number, offset, min(BUFFER_BYTES, size - offset), content)
            if target.write(data) != len(data):
                raise OSError("Short capacity fixture write")
            digest.update(data)
    except BaseException as error:
        try:
            target.close()
        except BaseException as close_error:
            raise error from close_error
        raise
    started = time.monotonic_ns()
    target.close()
    return {"path": str(path), "size": size, "sha256": digest.hexdigest(),
            "close_duration_ns": time.monotonic_ns() - started}


def sync_request(mount, scene, clean=False):
    reading = {"event": "explicit-sync", "scene": scene, "clean_index_probe": clean,
               "request_start_ns": time.time_ns()}
    started = time.monotonic_ns()
    try:
        os.setxattr(mount, "user.ltfs.sync", b"YATM capacity probe scene boundary")
        reading["success"] = True
    except OSError as error:
        reading.update(success=False, error=str(error), errno=error.errno)
    finally:
        reading.update(request_end_ns=time.time_ns(), request_duration_ns=time.monotonic_ns() - started)
    return reading


def run_probe(work_directory, mount, output, compression_label):
    work_directory, mount, output = Path(work_directory).resolve(), Path(mount).resolve(), Path(output).resolve()
    if not work_directory.is_dir() or not mount.is_dir():
        raise ValueError("Existing work_directory and mount directories are required")
    if output == mount or mount in output.parents:
        raise ValueError("Evidence output must remain outside the LTFS mount")
    initial = snapshot(work_directory, mount)
    directory = mount / ("yatm-capacity-probe-" + uuid.uuid4().hex)
    with output.open("x", encoding="utf-8") as evidence:
        def record(value):
            evidence.write(json.dumps(value) + "\n")
            evidence.flush()
        record({"event": "start", "work_directory": str(work_directory), "mount": str(mount),
                "compression_label": compression_label, "fixture_directory": str(directory),
                "scene_bytes": SCENE_BYTES, "small_file_bytes": SMALL_BYTES,
                "buffer_bytes": BUFFER_BYTES, "snapshot": initial})
        directory.mkdir()
        try:
            for content in ("random", "compressible", "alternating"):
                for layout in ("small", "large"):
                    scene = content + "-" + layout
                    before = snapshot(work_directory, mount)
                    scene_directory = directory / scene
                    scene_directory.mkdir()
                    size = SMALL_BYTES if layout == "small" else SCENE_BYTES
                    write_start_ns, write_started = time.time_ns(), time.monotonic_ns()
                    rows = [write_file(scene_directory / f"{number:05d}.bin", scene, number, size, content)
                            for number in range(SCENE_BYTES // size)]
                    write_end_ns, write_duration_ns = time.time_ns(), time.monotonic_ns() - write_started
                    record({"event": "ordinary-close", "scene": scene,
                            "write_start_ns": write_start_ns, "write_end_ns": write_end_ns,
                            "write_duration_ns": write_duration_ns, "logical_bytes": sum(row["size"] for row in rows),
                            "files": rows, "close_duration_ns": sum(row["close_duration_ns"] for row in rows),
                            "before": before, "after": snapshot(work_directory, mount)})
                    owner(work_directory, mount)
                    reading = sync_request(mount, scene)
                    record(reading)
                    if not reading["success"]:
                        raise OSError(reading.get("errno"), reading["error"])
                    record({"event": "post-sync", "scene": scene, "snapshot": snapshot(work_directory, mount)})
            record({"event": "clean-sync-before", "snapshot": snapshot(work_directory, mount)})
            owner(work_directory, mount)
            reading = sync_request(mount, "final-clean", clean=True)
            record(reading)
            if not reading["success"]:
                raise OSError(reading.get("errno"), reading["error"])
            record({"event": "clean-sync-after", "snapshot": snapshot(work_directory, mount)})
            record({"event": "completed", "logical_bytes": 6 * SCENE_BYTES})
        except BaseException as error:
            record({"event": "failed", "error": str(error), "type": type(error).__name__,
                    "retention": "Files, mount and evidence retained; owner handles cleanup"})
            raise
    return directory


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--work-directory", required=True, type=Path)
    parser.add_argument("--mount", required=True, type=Path)
    parser.add_argument("--output", required=True, type=Path, help="New JSON Lines evidence file outside the mount")
    parser.add_argument("--compression-label", required=True, help="Recorded operator label; does not change drive settings")
    args = parser.parse_args()
    run_probe(args.work_directory, args.mount, args.output, args.compression_label)


if __name__ == "__main__":
    main()
