"""Bounded deterministic fixture generation, content checks and owned LTFS xattrs."""

import ctypes
import errno
import fcntl
import hashlib
import json
import os
from pathlib import Path
import sys
import tempfile
import time


MATRIX = {"small-random": (16384, 256 * 1024, False),
          "small-compressible": (16384, 256 * 1024, True),
          "large-random": (4, 1024**3, False),
          "large-compressible": (4, 1024**3, True)}


def dimensions(case, size_gib=4):
    size_gib = int(size_gib)
    if size_gib not in (4, 32, 64):
        raise ValueError("Performance size must be 4, 32 or 64 GiB")
    count, size, compressible = MATRIX[case]
    return count * (size_gib // 4), size, compressible


def fixture_records(directory, case, size_gib=4):
    count, size, compressible = dimensions(case, size_gib)
    directory = Path(directory)
    directory.mkdir(parents=True, exist_ok=False)
    width = max(5, len(str(count - 1)))
    for number in range(count):
        path = directory / f"{number:0{width}d}.bin"
        digest = hashlib.sha256()
        with path.open("xb") as target:
            for offset in range(0, size, 256 * 1024):
                length = min(size - offset, 256 * 1024)
                seed = f"yatm-performance-v1:{case}:{number}:{offset}".encode()
                block = hashlib.shake_256(seed).digest((length + 1) // 2 if compressible else length)
                if compressible:
                    block = b"".join(block[start:start + 4096] * 2
                                     for start in range(0, len(block), 4096))[:length]
                if target.write(block) != len(block):
                    raise OSError("Short performance fixture write")
                digest.update(block)
        yield {"name": path.name, "size": size, "sha256": digest.hexdigest()}


def generate(directory, case):
    # Compatibility for the original four-GiB helper; long runs use JSON Lines.
    return {row["name"]: {"size": row["size"], "sha256": row["sha256"]}
            for row in fixture_records(directory, case)}


def generate_jsonl(directory, case, size_gib, manifest):
    if Path(directory).resolve() in Path(manifest).resolve().parents:
        raise ValueError("Fixture manifest must remain outside the selected source directory")
    count, total, digest = 0, 0, hashlib.sha256()
    with Path(manifest).open("xb") as output:
        for row in fixture_records(directory, case, size_gib):
            line = (json.dumps(row) + "\n").encode()
            output.write(line)
            digest.update(line)
            count += 1
            total += row["size"]
    return {"count": count, "logical_bytes": total, "manifest": manifest, "sha256": digest.hexdigest()}


def stream_sha256(source):
    digest = hashlib.sha256()
    for block in iter(lambda: source.read(1024 * 1024), b""):
        digest.update(block)
    return digest.hexdigest()


def capacity_reflink(directory, sizes):
    """Ordinary incompressible files with shared source extents and distinct content.

    Cloning saves only source-disk space. Archive reads every byte normally. The
    fresh per-file prefix changes its signature; the shared body is pseudorandom,
    not holes or a short repeating pattern. Unsupported cloning fails explicitly.
    """
    if sys.platform != "linux":
        raise OSError(errno.ENOTSUP, "Capacity reflinks require Linux FICLONE")
    if not 1 <= len(sizes) <= 100 or any(type(size) is not int or not 0 < size <= 64 * 1024**3 for size in sizes):
        raise ValueError("Choose 1–100 positive capacity sizes, at most 64 GiB each")
    directory = Path(directory)
    directory.mkdir(parents=True, exist_ok=True)
    if any(directory.iterdir()):
        raise ValueError("Capacity source directory must be empty")

    manifest = {}
    with tempfile.TemporaryFile(dir=directory.parent) as base:
        size = max(sizes)
        for offset in range(0, size, 1024 * 1024):
            block = hashlib.shake_256(f"yatm-capacity-base-v1:{offset}".encode()).digest(min(size - offset, 1024 * 1024))
            if base.write(block) != len(block):
                raise OSError("Short capacity base write")
        base.flush()
        for number, size in enumerate(sizes):
            name = f"chunk-{number:03d}.bin"
            with (directory / name).open("x+b") as target:
                fcntl.ioctl(target.fileno(), 0x40049409, base.fileno())  # Linux FICLONE; no copy fallback.
                target.truncate(size)
                prefix = hashlib.shake_256(f"yatm-capacity-file-v1:{number}".encode()).digest(min(size, 64 * 1024))
                if target.write(prefix) != len(prefix):
                    raise OSError("Short capacity prefix write")
                target.flush()
                target.seek(0)
                digest = stream_sha256(target)
            manifest[name] = {"size": size, "sha256": digest}
    return manifest


def verify(rows):
    if len(rows) > 64:
        raise ValueError("Restore verification exceeds the 64-file batch bound")
    results = []
    for row in rows:
        with open(row["path"], "rb") as source:
            digest = stream_sha256(source)
        results.append({"path": row["path"], "size": os.stat(row["path"]).st_size,
                        "sha256": digest})
    return results


def decode_log_page(raw, page):
    """Validate a complete binary LOG SENSE page before exposing HP counters."""
    # HP LTO-5 Host Interface Guide, pp. 58-59: 02:0..6 are uint32
    # counters (including data sets and CCQ sets, not byte counts).
    formats = {0x34: {0: 2}, 0x0C: {code: 8 for code in range(4)},
               0x31: {code: 4 for code in range(1, 5)},
               0x02: {code: 4 for code in range(7)}}
    if page not in formats:
        raise ValueError("Unsupported metric page")
    required = formats[page]
    # HP LTO-6 Host Interface Guide, pp. 64-65; also observed on HP LTO-5.
    # Unknown optional encodings remain absent without hiding required counters.
    optional = {0x34: {code: (2, (0x40, 0x60) if code == 4 else (0x60,)) for code in range(1, 5)},
                0x0C: {code: (4, (0x60,)) for code in range(4, 9)}}.get(page, {})
    if len(raw) < 4 or raw[0] & 0x3F != page or raw[1] != 0:
        raise ValueError("Wrong or truncated LOG page header")
    if int.from_bytes(raw[2:4], "big") != len(raw) - 4:
        raise ValueError("LOG page advertised length differs from raw bytes")
    values, optional_seen, offset = {}, set(), 4
    while offset < len(raw):
        if len(raw) - offset < 4:
            raise ValueError("Truncated LOG parameter header")
        code = int.from_bytes(raw[offset:offset + 2], "big")
        control, width = raw[offset + 2:offset + 4]
        offset += 4
        if offset + width > len(raw):
            raise ValueError("Truncated LOG parameter value")
        if code in required:
            if code in values or width != required[code]:
                raise ValueError("Duplicate metric parameter or wrong width")
            if page == 0x34 and control != 0x60:
                raise ValueError("Unexpected performance parameter control")
            values[code] = int.from_bytes(raw[offset:offset + width], "big")
        elif code in optional:
            expected_width, controls = optional[code]
            if code in optional_seen:
                values.pop(code, None)
            elif width == expected_width and control in controls:
                values[code] = int.from_bytes(raw[offset:offset + width], "big")
            optional_seen.add(code)
        offset += width
    if not required.keys() <= values.keys():
        raise ValueError("Missing required metric parameters")
    return {str(code): value for code, value in values.items()}


def read_log_xattr(mount, name):
    """Read once with Linux's full xattr bound, including pages larger than 128 bytes.

    This LTFS returns the complete page length even for a smaller caller buffer.
    FUSE reports EIO instead of the ERANGE that os.getxattr's small first read expects.
    A single large read also avoids consuming read-cleared counters twice.
    """
    if sys.platform != "linux":
        raise OSError(errno.ENOTSUP, "Owned LTFS observations require Linux")
    getxattr = ctypes.CDLL(None, use_errno=True).getxattr
    getxattr.argtypes = [ctypes.c_char_p, ctypes.c_char_p, ctypes.c_void_p, ctypes.c_size_t]
    getxattr.restype = ctypes.c_ssize_t
    buffer = ctypes.create_string_buffer(64 * 1024)
    length = getxattr(os.fsencode(mount), os.fsencode(name), buffer, len(buffer))
    if length < 0:
        error = ctypes.get_errno()
        raise OSError(error, os.strerror(error), os.fspath(mount))
    if length > len(buffer):
        raise OSError(errno.EOVERFLOW, "LOG page exceeds the Linux xattr bound")
    return buffer.raw[:length]


def sample(tape_directory, pages=("34", "0C")):
    # The work_directory option ties the mount to this exact admitted Job, not another LTFS process.
    result = {"timestamp_ns": time.time_ns(), "status": "not-mounted", "pages": {}}
    marker = ("work_directory=" + tape_directory).encode()
    for process in Path("/proc").glob("[0-9]*/cmdline"):
        try:
            argv = process.read_bytes().split(b"\0")
        except OSError:
            continue
        if not any(marker == arg for arg in argv):
            continue
        argv = [arg for arg in argv if arg]
        if not argv or Path(os.fsdecode(argv[0])).name != "ltfs":
            continue
        mount = os.fsdecode(argv[-1])
        result.update(status="mounted", mount=mount, pid=int(process.parent.name),
                      argv=[os.fsdecode(arg) for arg in argv])
        try:
            status = (process.parent / "status").read_text().splitlines()
            result["memory_kib"] = {line.split(":", 1)[0]: int(line.split()[1]) for line in status
                                    if line.startswith(("VmRSS:", "VmHWM:"))}
            result["open_descriptors"] = len(list((process.parent / "fd").iterdir()))
        except OSError as error:
            result["resource_error"] = str(error)
        for page in pages:
            name = "user.ltfs.vendor.IBM.logPage." + page + ".00"
            reading = {"request_start_ns": time.time_ns()}
            started = time.monotonic_ns()
            try:
                raw = read_log_xattr(mount, name)
                reading.update(encoding="hex", raw=raw.hex())
            except OSError as error:
                reading.update(error=str(error), errno=error.errno)
            finally:
                reading.update(request_end_ns=time.time_ns(), request_duration_ns=time.monotonic_ns() - started)
            if "raw" in reading:
                try:
                    reading["parameters"] = decode_log_page(raw, int(page, 16))
                except ValueError as error:
                    reading["decode_error"] = str(error)
            result["pages"][page] = reading
        break
    return result


if __name__ == "__main__":
    command, *arguments = sys.argv[1:]
    if command == "generate":
        output = generate(*arguments)
    elif command == "generate-jsonl":
        output = generate_jsonl(*arguments)
    elif command == "capacity-reflink":
        output = capacity_reflink(arguments[0], json.loads(arguments[1]))
    elif command == "verify":
        output = verify(json.load(sys.stdin))
    elif command == "sample":
        output = sample(*arguments)
    else:
        raise SystemExit("Unknown fixture operation")
    print(json.dumps(output))
