"""Host-local, one-shot LTFS EW experiment on an explicitly authorized scratch cartridge.

Run in tmux. All paths and cartridge identities are supplied by the operator.
No YATM service or catalog is used. Evidence and the written Tape are retained.
"""

import argparse
import fcntl
import hashlib
import json
import os
from pathlib import Path
import re
import subprocess
import time

from physical_tape_observe import idle, observe
from physical_performance_fixture import decode_log_page
from physical_capacity_probe import snapshot
from physical_ew_probe import record, run as write_probe


def run(args):
    root = args.output.resolve()
    root.mkdir(mode=0o700, parents=False, exist_ok=False)
    work, mount = root / "work", root / "mount"
    work.mkdir()
    mount.mkdir()
    with (root / "execution.lock").open("x") as lock, (root / "lifecycle.jsonl").open("x") as events:
        fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
        def stage(name, argv, timeout=900):
            row = {"event": name, "argv": list(map(str, argv)), "request_start_ns": time.time_ns(),
                   "monotonic_start_ns": time.monotonic_ns()}
            (root / (name + ".started.json")).write_text(json.dumps(row) + "\n")
            with (root / (name + ".stdout")).open("xb") as stdout, (root / (name + ".stderr")).open("xb") as stderr:
                result = subprocess.run(argv, stdout=stdout, stderr=stderr, timeout=timeout)
            row.update(returncode=result.returncode, request_end_ns=time.time_ns(), monotonic_end_ns=time.monotonic_ns())
            record(events, row)
            if result.returncode:
                raise RuntimeError(f"{name} failed; evidence and device state retained")
            return (root / (name + ".stdout")).read_bytes()

        def status(phase, **fields):
            row = {"phase": phase, "timestamp_ns": time.time_ns(), **fields}
            temporary = root / "status.next"
            temporary.write_text(json.dumps(row, indent=2) + "\n")
            temporary.replace(root / "status.json")
            record(events, row)

        owner = None
        try:
            status("identity")
            idle(args.sg_device)
            # LOAD is a lifecycle operation performed only by this executor.
            stage("load", ["sg_raw", "--timeout=600", str(args.sg_device), "1b", "00", "00", "00", "01", "00"])
            idle(args.sg_device)
            identity = stage("identity", [str(args.lto_info), "-f", str(args.device)]).decode()
            match = re.search(r"^\s*Barcode\s*:\s*(\S+)\s*$", identity, re.M)
            if not match or match.group(1) not in (args.barcode, args.barcode + "L5"):
                raise RuntimeError("Loaded cartridge does not match the explicitly authorized identity")
            observe(args.sg_device, root / "before-format")
            capacities = decode_log_page((root / "before-format/log-31.bin").read_bytes(), 0x31)
            native = (capacities["3"] + capacities["4"]) * 1024**2
            if args.expected_native_min_bytes and native < args.expected_native_min_bytes:
                raise RuntimeError("Observed native capacity is below the planned full-capacity bound")
            inputs = {"args": {key: str(value) if isinstance(value, Path) else value for key, value in vars(args).items()},
                      "native_capacity_before_format": native, "sha256": {}}
            for path in (args.ltfs, args.mkltfs, args.trace_library, Path(__file__), Path(__file__).with_name("physical_ew_probe.py")):
                digest = hashlib.sha256()
                with path.open("rb") as source:
                    for block in iter(lambda: source.read(1024 * 1024), b""):
                        digest.update(block)
                inputs["sha256"][str(path)] = digest.hexdigest()
            (root / "inputs.json").write_text(json.dumps(inputs, indent=2) + "\n")
            status("format", native_capacity_bytes=native)
            idle(args.sg_device)
            stage("format", [str(args.mkltfs), "-f", "-d", str(args.sg_device), "-s", args.barcode,
                             "-n", "EW measurement", "-r", "size=1M"], timeout=1200)
            status("mount")
            idle(args.sg_device)
            environment = os.environ.copy()
            environment["LD_PRELOAD"] = str(args.trace_library.resolve())
            environment["YATM_SG_TRACE"] = str(root / "sg.jsonl")
            environment["YATM_SG_TRACE_POSITION"] = "1"
            argv = [str(args.ltfs), "-f", "-s", "-o", "devname=" + str(args.sg_device),
                    "-o", "noatime", "-o", "sync_type=unmount", "-o", "work_directory=" + str(work),
                    "-o", "capture_index=" + str(work), "-o", "min_pool_size=256", "-o", "max_pool_size=1024",
                    "-o", "eject", str(mount)]
            (root / "mount-argv.json").write_text(json.dumps(argv) + "\n")
            with (root / "ltfs.log").open("xb") as log:
                owner = subprocess.Popen(argv, stdout=log, stderr=subprocess.STDOUT, env=environment)
            (root / "ltfs.pid").write_text(str(owner.pid) + "\n")
            deadline = time.monotonic() + 900
            while not os.path.ismount(mount):
                if owner.poll() is not None:
                    raise RuntimeError("LTFS exited before the mount became available")
                if time.monotonic() > deadline:
                    raise RuntimeError("LTFS mount remains incomplete; retain owner for diagnosis")
                time.sleep(1)
            status("writing", ltfs_pid=owner.pid)
            probe_args = argparse.Namespace(mount=mount, work_directory=work, output=root / "capture",
                                            sg_trace=root / "sg.jsonl", barcode=args.barcode,
                                            file_bytes=args.file_bytes, max_bytes=args.max_bytes, sample_period=1)
            result = write_probe(probe_args)
            status("finalizing", write_status=result["status"], total_accepted_bytes=result["total_accepted_bytes"])
            final = snapshot(str(work), mount, pages=("31", "0C", "17", "02", "34"))
            (root / "before-unmount.json").write_text(json.dumps(final, indent=2) + "\n")
            # Normal unmount performs the one final Index synchronization. Keep the
            # SG observer in the owning process until LTFS releases the device.
            stage("unmount", ["umount", "--", str(mount)], timeout=1200)
            owner.wait(timeout=1200)
            if owner.returncode:
                raise RuntimeError("LTFS exited unsuccessfully after unmount")
            idle(args.sg_device)
            device_status = stage("ejected-status", ["mt", "-f", str(args.device), "status"]).decode()
            if "DR_OPEN" not in device_status:
                raise RuntimeError("Normal eject was not confirmed; no forced release attempted")
            status("complete", total_accepted_bytes=result["total_accepted_bytes"],
                   write_status=result["status"], ejected=True,
                   note="Analyze raw sense to classify EW. Written Tape and all evidence retained; no automatic reformat.")
        except BaseException as error:
            status("needs-inspection", error=str(error), error_type=type(error).__name__,
                   ltfs_pid=owner.pid if owner else None,
                   ltfs_running=owner is not None and owner.poll() is None)
            raise


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    for name in ("output", "device", "sg-device", "ltfs", "mkltfs", "lto-info", "trace-library"):
        parser.add_argument("--" + name, required=True, type=Path)
    parser.add_argument("--barcode", required=True)
    parser.add_argument("--authorize-erase", required=True, help="Repeat the authorized scratch barcode")
    parser.add_argument("--expected-native-min-bytes", type=int, default=0)
    parser.add_argument("--file-bytes", type=int, default=4 * 1024**3)
    parser.add_argument("--max-bytes", type=int, default=2 * 1024**4)
    args = parser.parse_args()
    if not re.fullmatch(r"[A-Za-z0-9]{6}", args.barcode) or args.authorize_erase != args.barcode:
        parser.error("Explicit matching six-character scratch barcode is required")
    if args.file_bytes <= 0 or args.max_bytes <= 0:
        parser.error("Workload bounds must be positive")
    run(args)


if __name__ == "__main__":
    main()
