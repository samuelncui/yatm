"""Stream synthetic ordinary files to an explicitly owned scratch LTFS mount until failure.

This is a drive-boundary experiment, not Archive acceptance. It deliberately has
no capacity admission check. Lifecycle operations remain with the host executor.
"""

import argparse
import hashlib
import json
import os
from pathlib import Path
import subprocess
import tempfile
import threading
import time

from physical_capacity_boundary import ATTRIBUTES
from physical_capacity_probe import snapshot


BLOCK_BYTES = 1024 * 1024


def record(output, value):
    output.write(json.dumps(value, separators=(",", ":")) + "\n")
    output.flush()


def operation(output, event, action, **context):
    row = {"event": event, "request_start_ns": time.time_ns(),
           "monotonic_start_ns": time.monotonic_ns(), **context}
    try:
        result = action()
        row["success"] = True
        if isinstance(result, int):
            row["returned_bytes"] = result
        return result
    except BaseException as error:
        row.update(success=False, error=str(error), error_type=type(error).__name__)
        if isinstance(error, OSError):
            row["errno"] = error.errno
        raise
    finally:
        row.update(request_end_ns=time.time_ns(), monotonic_end_ns=time.monotonic_ns())
        record(output, row)


def copy_stream(source, directory, output, file_bytes, max_bytes, write=os.write, close=os.close, check=None):
    """One stream, bounded ordinary files, full write/Close/rename evidence; never retry failure."""
    total, completed, failed = 0, [], None
    while total < max_bytes:
        number, offset, fd, temporary = len(completed), 0, None, None
        digest = hashlib.sha256()
        final = directory / f"{number:06d}.bin"
        try:
            fd, temporary = operation(output, "open", lambda: tempfile.mkstemp(prefix=".tmp_", dir=directory),
                                      file_index=number, total_accepted_bytes=total)
            while offset < file_bytes and total < max_bytes:
                if check:
                    check()
                length = min(BLOCK_BYTES, file_bytes - offset, max_bytes - total)
                block = source.read(length)
                if len(block) != length:
                    raise RuntimeError("Synthetic source ended before the frozen workload bound")
                view = memoryview(block)
                while view:
                    wrote = operation(output, "write", lambda: write(fd, view),
                                      file_index=number, file_offset=offset, requested_bytes=len(view),
                                      total_accepted_bytes=total)
                    if not 0 < wrote <= len(view):
                        raise OSError("Invalid write result; stopping the experiment")
                    digest.update(view[:wrote])
                    offset, total = offset + wrote, total + wrote
                    view = view[wrote:]
            descriptor, fd = fd, None
            operation(output, "close", lambda: close(descriptor), file_index=number,
                      file_offset=offset, total_accepted_bytes=total)
            operation(output, "rename", lambda: os.rename(temporary, final),
                      file_index=number, file_offset=offset, total_accepted_bytes=total)
            temporary = None
            completed.append({"name": final.name, "size": offset, "sha256": digest.hexdigest()})
            record(output, {"event": "file-completed", "timestamp_ns": time.time_ns(),
                            "file_index": number, "total_accepted_bytes": total, **completed[-1]})
        except Exception as error:
            failed = {"file_index": number, "file_offset": offset, "error": str(error),
                      "errno": getattr(error, "errno", None), "temporary": str(temporary) if temporary else None}
            if fd is not None:
                descriptor, fd = fd, None
                try:
                    operation(output, "close-after-error", lambda: close(descriptor),
                              file_index=number, file_offset=offset, total_accepted_bytes=total)
                except Exception as close_error:
                    failed["close_error"] = str(close_error)
            if temporary is not None:
                try:
                    operation(output, "delete-partial", lambda: os.unlink(temporary),
                              file_index=number, file_offset=offset, total_accepted_bytes=total)
                except OSError as delete_error:
                    failed["delete_error"] = str(delete_error)
            break
    return {"status": "write-stopped" if failed else "workload-bound-reached",
            "total_accepted_bytes": total, "completed": completed, "failure": failed,
            "note": "Close/rename success is not verified physical completion or Archive publication."}


def sample_loop(work, mount, output, stop, period):
    with output.open("x") as evidence:
        while not stop.is_set():
            started = time.monotonic_ns()
            row = {"source": "drive", "request_start_ns": time.time_ns(), "monotonic_start_ns": started}
            try:
                row["sample"] = snapshot(str(work), mount, pages=("31", "0C", "17", "02", "34"))
            except Exception as error:
                row["error"] = str(error)
            row.update(request_end_ns=time.time_ns(), monotonic_end_ns=time.monotonic_ns())
            record(evidence, row)
            stop.wait(max(0, period - (time.monotonic_ns() - started) / 1e9))


def trace_preflight(path):
    """Require a real owner-side position reply before spending a tape on this capture."""
    found, initialized = False, False
    if Path(str(path) + ".error").exists():
        raise RuntimeError("SG observer reports an error; inspect it before payload writing")
    with path.open() as source:
        for line in source:
            row = json.loads(line)
            if row.get("event") == "init" and row.get("ready") and row.get("position_enabled"):
                initialized = True
            if (row.get("cdb", "").startswith("3400") and row.get("rc") == 0
                    and row.get("status") == 0 and row.get("host_status") == 0
                    and len(bytes.fromhex(row.get("data", ""))) == 20):
                found = True
    if not found or not initialized:
        raise RuntimeError("No successful owner-side short READ POSITION in SG trace")


def run(args):
    mount, work, output = args.mount.resolve(), args.work_directory.resolve(), args.output.resolve()
    if not os.path.ismount(mount) or mount == output or mount in output.parents:
        raise ValueError("An actual LTFS mount and off-Tape evidence directory are required")
    output.mkdir(mode=0o700, parents=False, exist_ok=False)
    initial = snapshot(str(work), mount, pages=("31", "0C", "17", "02", "34"))
    if not initial.get("pages", {}).get("31", {}).get("parameters") or not initial.get("pages", {}).get("0C", {}).get("parameters"):
        raise RuntimeError("Required capacity probes are unavailable")
    attributes = {name: os.getxattr(mount, "user.ltfs." + name).decode() for name in ATTRIBUTES}
    if attributes["volumeSerial"] != args.barcode:
        raise RuntimeError("Mounted cartridge identity does not match the authorized scratch barcode")
    trace_preflight(args.sg_trace)
    metadata = {"started_ns": time.time_ns(), "initial": initial, "attributes": attributes,
                "work_directory": str(work), "mount": str(mount), "file_bytes": args.file_bytes,
                "max_bytes": args.max_bytes, "sample_period_seconds": args.sample_period,
                "payload": "AES-256-CTR stream from zeros; synthetic fixed key/IV; no source disk writes",
                "scope": "LTFS EW experiment; no software admission; not YATM end-to-end acceptance"}
    (output / "metadata.json").write_text(json.dumps(metadata, indent=2) + "\n")
    directory = mount / "ew-probe"
    directory.mkdir()
    stop = threading.Event()
    sampler = threading.Thread(target=sample_loop, args=(work, mount, output / "samples.jsonl", stop, args.sample_period))
    sampler.start()
    next_check = 0

    def check_trace():
        nonlocal next_check
        now = time.monotonic()
        if now < next_check:
            return
        next_check = now + 1
        if Path(str(args.sg_trace) + ".error").exists():
            raise RuntimeError("SG observation failed; stop this measurement and retain the actual error")

    try:
        with open("/dev/zero", "rb") as zero, (output / "generator.stderr").open("xb") as errors:
            generator = subprocess.Popen(["openssl", "enc", "-aes-256-ctr", "-nosalt", "-K", "19" * 32,
                                          "-iv", "37" * 16], stdin=zero, stdout=subprocess.PIPE, stderr=errors,
                                         bufsize=BLOCK_BYTES)
            try:
                with (output / "events.jsonl").open("x") as events:
                    result = copy_stream(generator.stdout, directory, events, args.file_bytes, args.max_bytes,
                                         check=check_trace)
                    record(events, {"event": "final", "timestamp_ns": time.time_ns(), **result})
                (output / "write-result.json").write_text(json.dumps(result, indent=2) + "\n")
            finally:
                generator.terminate()
                generator.stdout.close()
                generator.wait(timeout=10)
        # Observe the failed tail without a new write, repeated sync, or Media replay.
        stop.wait(15)
    finally:
        stop.set()
        sampler.join(timeout=120)
        if sampler.is_alive():
            raise RuntimeError("Sampler remains blocked; retain mount and inspect LTFS before lifecycle actions")
    return result


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--mount", required=True, type=Path)
    parser.add_argument("--work-directory", required=True, type=Path)
    parser.add_argument("--output", required=True, type=Path)
    parser.add_argument("--sg-trace", required=True, type=Path)
    parser.add_argument("--barcode", required=True)
    parser.add_argument("--file-bytes", type=int, default=4 * 1024**3)
    parser.add_argument("--max-bytes", type=int, default=2 * 1024**4)
    parser.add_argument("--sample-period", type=float, default=1)
    args = parser.parse_args()
    if args.file_bytes <= 0 or args.max_bytes <= 0 or args.sample_period < 0.5:
        parser.error("Positive workload sizes and sample period of at least 0.5 seconds are required")
    result = run(args)
    print(json.dumps({key: value for key, value in result.items() if key != "completed"}))


if __name__ == "__main__":
    main()
