"""Capture bounded read-only SCSI observations from an idle, unmounted Tape drive.

Run on the hardware host after checking Jobs and cartridge identity. This helper
does not load, move, mount, write or configure the Tape. Retain output privately.
Mounted LTFS observations use physical_performance_fixture.sample instead.
"""

import argparse
import json
from pathlib import Path
import stat
import subprocess
import time


LOG_PAGES = (0x02, 0x0C, 0x17, 0x31, 0x34, 0x3E)


def supported_pages(raw):
    if len(raw) < 4 or raw[:2] != b"\0\0":
        raise ValueError("Invalid supported LOG pages header")
    if int.from_bytes(raw[2:4], "big") != len(raw) - 4:
        raise ValueError("Incomplete supported LOG pages response")
    return set(raw[4:])


def idle(device):
    if not stat.S_ISCHR(device.stat().st_mode):
        raise ValueError("Expected an existing SCSI generic character device")
    scsi = Path("/sys/class/scsi_generic") / device.name / "device"
    if (scsi / "type").read_text().strip() != "1":
        raise ValueError("The SCSI generic device is not a Tape drive")
    aliases = [Path("/dev") / path.name for path in (scsi / "scsi_tape").iterdir()]
    if not aliases:
        raise ValueError("Tape device aliases are unavailable")
    result = subprocess.run(["fuser", "-v", str(device), *map(str, aliases)],
                            capture_output=True, timeout=10)
    if result.returncode != 1:
        raise RuntimeError("Device is in use or its ownership check failed: " +
                           result.stderr.decode(errors="replace"))


def observe(device, output, cartridge_memory=False, position_probe=False):
    device = Path(device).resolve(strict=True)
    idle(device)
    output = Path(output)
    output.mkdir(mode=0o700, parents=False, exist_ok=False)
    with (output / "commands.jsonl").open("x") as evidence:
        def capture(name, args):
            idle(device)
            started = time.monotonic_ns()
            row = {"name": name, "argv": args, "start_ns": time.time_ns()}
            try:
                result = subprocess.run(args, capture_output=True, timeout=20)
                raw = result.stdout
                row.update(returncode=result.returncode, stderr=result.stderr.decode(errors="replace"))
            except subprocess.TimeoutExpired as error:
                raw = error.stdout or b""
                row.update(returncode=None, error="Command timed out",
                           stderr=(error.stderr or b"").decode(errors="replace"))
            row.update(duration_ns=time.monotonic_ns() - started, bytes=len(raw))
            (output / (name + ".bin")).write_bytes(raw)
            evidence.write(json.dumps(row) + "\n")
            evidence.flush()
            if row["returncode"] is None:
                raise RuntimeError("Timed-out device command; retain evidence and inspect the owner")
            return raw, row["returncode"]

        def read_raw(name, size, cdb):
            return capture(name, ["sg_raw", "--readonly", "--binary", "--timeout=10",
                                  "--request=" + str(size), str(device), *cdb.split()])

        def log_page(page):
            # sg_logs --raw may omit headers or print hex for vendor pages.
            # Request the complete response through one data-in CDB instead.
            return read_raw(f"log-{page:02x}", 4096,
                            f"4d 00 {0x40 | page:02x} 00 00 00 00 10 00 00")

        if position_probe:
            # Capture sense immediately after the operator's completed positioning.
            # These queries may consume pending sense; use only the idle test owner.
            read_raw("sense-fixed", 255, "03 00 00 00 ff 00")
            read_raw("sense-descriptor", 255, "03 01 00 00 ff 00")
            read_raw("position-vendor", 20, "34 01 00 00 00 00 00 00 00 00")
        read_raw("inquiry", 96, "12 00 00 00 60 00")
        raw, result = log_page(0)
        if result:
            raise RuntimeError("Supported LOG pages query failed")
        pages = supported_pages(raw)
        for page in LOG_PAGES:
            if page in pages:
                log_page(page)
        read_raw("position-short", 20, "34 00 00 00 00 00 00 00 00 00")
        read_raw("position-long", 32, "34 06 00 00 00 00 00 00 00 00")
        read_raw("position-extended", 32, "34 08 00 00 00 00 00 00 20 00")
        if cartridge_memory:
            descriptor, result = read_raw("cm-descriptor", 4, "3c 03 10 00 00 00 00 00 04 00")
            if result == 0 and len(descriptor) == 4:
                size = int.from_bytes(descriptor[1:], "big")
                if 0 < size <= 8192:
                    allocation = size.to_bytes(3, "big").hex(" ")
                    read_raw("cm", size, f"3c 02 10 00 00 00 {allocation} 00")
        idle(device)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--device", required=True, type=Path)
    parser.add_argument("--output", required=True, type=Path, help="New private host-local evidence directory")
    parser.add_argument("--cartridge-memory", action="store_true", help="Read documented HP 8-KiB CM buffer; retain identifying data privately")
    parser.add_argument("--position-probe", action="store_true", help="Probe full sense replies and vendor READ POSITION form on an idle test drive")
    args = parser.parse_args()
    observe(args.device, args.output, args.cartridge_memory, args.position_probe)


if __name__ == "__main__":
    main()
