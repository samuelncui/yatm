"""Record passive capacity evidence before copying and before normal LTFS unmount.

Use only with the bounded synthetic capacity fixture. This helper never writes to
the mount, flushes data, or accesses a device outside the owning LTFS process.
"""

import argparse
import json
import os
from pathlib import Path
import stat
import time

from physical_capacity_probe import snapshot


PAGES = ("31", "0C", "17", "02", "34", "2E")
ATTRIBUTES = ("volumeSerial", "volumeUUID", "volumeBlocksize", "volumeCompression",
              "partitionMap", "indexGeneration", "indexLocation", "indexPrevious")


def files_snapshot(mount):
    """Bounded metadata observation, retaining temporary files and read failures."""
    mount = Path(mount)
    result = {"request_start_ns": time.time_ns(), "rows": [], "errors": []}
    pending = [mount]
    while pending:
        directory = pending.pop()
        try:
            with os.scandir(directory) as entries:
                for entry in entries:
                    if len(result["rows"]) >= 128:
                        result["errors"].append("Fixture exceeds the 128-entry observation bound")
                        pending.clear()
                        break
                    row = {"path": str(Path(entry.path).relative_to(mount))}
                    result["rows"].append(row)
                    try:
                        info = entry.stat(follow_symlinks=False)
                        row.update(size=info.st_size, mode=info.st_mode, mtime_ns=info.st_mtime_ns)
                        if stat.S_ISDIR(info.st_mode):
                            pending.append(Path(entry.path))
                    except OSError as error:
                        row.update(error=str(error), errno=error.errno)
        except OSError as error:
            result["errors"].append({"path": str(directory.relative_to(mount)),
                                     "error": str(error), "errno": error.errno})
    result["request_end_ns"] = time.time_ns()
    return result


def capture(work_directory, mount, output, phase):
    mount, output = Path(mount).resolve(), Path(output).resolve()
    if output == mount or mount in output.parents:
        raise ValueError("Evidence must remain outside the LTFS mount")
    if phase not in ("before-copy", "before-unmount"):
        raise ValueError("Unknown capacity observation phase")
    record = {"phase": phase, "request_start_ns": time.time_ns(), "errors": []}
    with output.open("x") as evidence:
        try:
            record["snapshot"] = snapshot(work_directory, mount, pages=PAGES)
        except Exception as error:
            record["errors"].append(str(error))
        record["attributes"] = {}
        for name in ATTRIBUTES:
            reading = {"request_start_ns": time.time_ns()}
            try:
                reading["value"] = os.getxattr(mount, "user.ltfs." + name).decode("utf-8")
            except (OSError, UnicodeError) as error:
                reading["error"] = str(error)
            reading["request_end_ns"] = time.time_ns()
            record["attributes"][name] = reading
        record["files"] = files_snapshot(mount)
        observation = record.get("snapshot", {})
        pages = observation.get("pages", {})
        record["required_probes_ok"] = (
            observation.get("status") == "mounted"
            and all(pages.get(page, {}).get("parameters") for page in ("31", "0C"))
            and {"4", "5", "7"} <= pages.get("0C", {}).get("parameters", {}).keys()
            and bool(pages.get("17", {}).get("raw"))
            and not pages.get("17", {}).get("error")
            and "available_bytes" in observation.get("statvfs", {}))
        record["request_end_ns"] = time.time_ns()
        json.dump(record, evidence, indent=2)
        evidence.write("\n")
    if phase == "before-copy" and not record["required_probes_ok"]:
        raise RuntimeError("Capacity preflight failed; inspect the saved observation before copying")
    return record


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--work-directory", required=True, type=Path)
    parser.add_argument("--mount", required=True, type=Path)
    parser.add_argument("--output", required=True, type=Path)
    parser.add_argument("--phase", required=True, choices=("before-copy", "before-unmount"))
    args = parser.parse_args()
    capture(str(args.work_directory.resolve()), args.mount, args.output, args.phase)


if __name__ == "__main__":
    main()
