"""Independent, read-only checks of migrated v0.1.x metadata copies.

The original directory contains tapes.db, captured_indices/<barcode>.schema and
job-logs/<id>.log files. The migrated directory contains tapes.db and jobs/<id>
bundles. Only completed Archive history with inline submitted sources and
unambiguous logged Media is audited; other source forms are explicitly unsupported.
This module neither runs migration nor writes reports.
"""

import collections
import contextlib
import datetime
import json
import pathlib
import posixpath
import re
import sqlite3
import subprocess
import xml.etree.ElementTree as ET


@contextlib.contextmanager
def _connect(path):
    db = sqlite3.connect(path.resolve().as_uri() + "?mode=ro", uri=True)
    db.row_factory = sqlite3.Row
    try:
        yield db
    finally:
        db.close()


def _stamp(value):
    """Convert legacy text to exact Unix nanoseconds, preserving absence/zero."""
    if value is None:
        return None
    match = re.fullmatch(
        r"(\d{4}-\d\d-\d\d)[ T](\d\d:\d\d:\d\d)(?:\.(\d{1,9}))?(Z|[+-]\d\d:\d\d)?",
        value,
    )
    assert match, "Unsupported legacy timestamp syntax"
    try:
        base = datetime.datetime.fromisoformat(
            match[1] + "T" + match[2] + (match[4] or "+00:00").replace("Z", "+00:00")
        )
    except ValueError:
        raise AssertionError("Invalid legacy timestamp") from None
    fraction = int((match[3] or "").ljust(9, "0"))
    if base == datetime.datetime(1, 1, 1, tzinfo=datetime.timezone.utc) and fraction == 0:
        return 0
    delta = base - datetime.datetime(1970, 1, 1, tzinfo=datetime.timezone.utc)
    result = (delta.days * 86400 + delta.seconds) * 10**9 + fraction
    assert -(1 << 63) <= result < (1 << 63), "Legacy timestamp is outside int64 nanoseconds"
    return result


def _varint(data, offset):
    value = shift = 0
    while True:
        assert offset < len(data), "Truncated protobuf varint"
        byte = data[offset]
        offset += 1
        value |= (byte & 127) << shift
        if byte < 128:
            assert value < (1 << 64), "Protobuf varint exceeds uint64"
            return value, offset
        shift += 7
        assert shift < 70, "Protobuf varint exceeds uint64"


def _fields(data):
    """Read only the protobuf wire values needed by this audit."""
    result = collections.defaultdict(list)
    if data is None:
        return result
    if data.startswith(b"\xffym\x02"):
        try:
            data = subprocess.run(
                ["zstd", "-dq", "--stdout"], input=data[4:], check=True, capture_output=True
            ).stdout
        except FileNotFoundError:
            raise RuntimeError("Compressed legacy metadata requires zstd on PATH") from None
        except subprocess.CalledProcessError:
            raise AssertionError("Cannot decode compressed legacy metadata") from None
    offset = 0
    while offset < len(data):
        tag, offset = _varint(data, offset)
        number, wire = tag >> 3, tag & 7
        assert number, "Invalid protobuf field number"
        if wire == 0:
            value, offset = _varint(data, offset)
        elif wire in (1, 2, 5):
            if wire == 2:
                size, offset = _varint(data, offset)
            else:
                size = 8 if wire == 1 else 4
            assert offset + size <= len(data), "Truncated protobuf field"
            value = data[offset:offset + size]
            offset += size
        else:
            raise AssertionError("Unsupported protobuf wire type")
        result[number].append(value)
    return result


def _indexes(original, original_directory):
    result = {}
    for tape in original.execute("SELECT id,barcode FROM tapes"):
        path = original_directory / "captured_indices" / (tape["barcode"] + ".schema")
        if not path.exists() or path.stat().st_size == 0:
            continue
        values = {}

        def walk(directory, base=""):
            contents = directory.find("contents")
            if contents is None:
                return
            for item in contents:
                name = item.findtext("name")
                if item.tag == "directory":
                    walk(item, posixpath.join(base, name))
                elif item.tag == "file":
                    values[posixpath.join(base, name)] = int(item.findtext("length"))

        directory = ET.parse(path).getroot().find("directory")
        assert directory is not None, "Captured LTFS index has no root directory"
        walk(directory)
        result[tape["id"]] = values
    return result


def check(original_directory, migrated_directory, *, staged=False, revisions=None):
    """Audit caller-owned copies; return counts and IDs for the caller's private report.

    staged selects the catalog's _staging tables; Job bundles use the same paths.
    revisions optionally maps Job IDs to expected revisions (default: ID order).
    coverage counts audited Jobs; Restore, other-kind and other-state coverage
    remain zero. Derived directories are compared by Media/path, not their IDs,
    which a Library import can regenerate.
    Mismatches raise assertions without including names, content or encryption values.
    """
    if not __debug__:
        raise RuntimeError("Legacy metadata audit requires assertions; disable -O/PYTHONOPTIMIZE")
    original_directory = pathlib.Path(original_directory)
    migrated_directory = pathlib.Path(migrated_directory)
    result = {"independent": True, "staged": staged}
    suffix = "_staging" if staged else ""
    with _connect(original_directory / "tapes.db") as old, _connect(migrated_directory / "tapes.db") as current:
        files = {row["id"]: row for row in old.execute("SELECT * FROM files")}
        observed = list(current.execute("SELECT * FROM files" + suffix + " ORDER BY id"))
        assert {row["id"] for row in observed} == files.keys(), "File IDs differ"
        for row in observed:
            source = files[row["id"]]
            assert (row["parent_id"], row["name"]) == (source["parent_id"], source["name"]), "File identity differs"
            assert row["kind"] == (2 if source["mode"] & (1 << 31) else 1), "File kind differs"
            assert row["created_at_ns"] == row["updated_at_ns"] == _stamp(source["mod_time"]), "File times differ"
        result["files"] = len(observed)

        inventory = _indexes(old, original_directory)
        positions = {}
        omitted = []
        for row in old.execute("SELECT * FROM positions ORDER BY id"):
            index = inventory.get(row["tape_id"])
            if index is not None and index.get(row["path"]) != row["size"]:
                omitted.append(row["id"])
            else:
                positions[row["id"]] = row
        observed = list(current.execute("SELECT * FROM positions" + suffix + " WHERE is_dir=0 ORDER BY id"))
        assert {row["id"] for row in observed} == positions.keys(), "Position IDs differ"
        expected_versions = {}
        indexed = 0
        for row in observed:
            source = positions[row["id"]]
            assert (row["media_id"], row["path"], row["mode"], row["size"], row["hash"]) == (
                source["tape_id"], source["path"], source["mode"], source["size"], source["hash"]
            ), "Position attributes differ"
            assert row["mtime_ns"] == _stamp(source["mod_time"]), "Position mtime differs"
            assert row["written_at_ns"] == _stamp(source["write_time"]), "Position write time differs"
            assert row["checked_at_ns"] == row["health_job_id"] == 0, "Position health differs"
            has_index = source["tape_id"] in inventory
            assert bool(row["storage_metadata"]) == has_index, "Position storage metadata presence differs"
            indexed += has_index
            file = files.get(source["file_id"])
            match = file is not None and file["size"] == source["size"] and (
                not file["hash"] or file["hash"] == source["hash"]
            )
            signature = file["signature"] if match else None
            if not signature and len(source["hash"] or b"") == 32 and source["size"] >= 0:
                signature = b"\x01" + source["hash"] + source["size"].to_bytes(8, "big")
            assert row["signature"] == signature, "Position signature differs"
            if file is not None and signature and not (file["mode"] & (1 << 31)):
                attrs = file if match else source
                key = (file["id"], signature)
                expected_versions.setdefault(key, (source["hash"], source["size"], attrs["mode"], _stamp(attrs["mod_time"])))
        observed_versions = list(current.execute("SELECT * FROM file_versions" + suffix))
        assert len(observed_versions) == len(expected_versions), "FileVersion count differs"
        for row in observed_versions:
            key = (row["file_id"], row["signature"])
            assert key in expected_versions, "Unexpected FileVersion"
            assert (row["hash"], row["size"], row["mode"], row["mtime_ns"]) == expected_versions.pop(key), "FileVersion attributes differ"
            assert row["first_archived_at_ns"] is None and row["last_archived_at_ns"] is None, "FileVersion archive dates differ"
        result.update(positions=len(observed), omitted_positions=omitted, indexed_positions=indexed, versions=len(observed_versions))

        directories = {}
        for source in positions.values():
            parts = source["path"].split("/")
            for length in range(1, len(parts)):
                key = (source["tape_id"], "/".join(parts[:length]) + "/")
                summary = directories.setdefault(key, [0, 0, 0])
                summary[0] += source["size"]
                for offset, name in ((1, "mod_time"), (2, "write_time")):
                    value = _stamp(source[name])
                    if value and (not summary[offset] or value > summary[offset]):
                        summary[offset] = value
        observed_dirs = list(current.execute("SELECT * FROM positions" + suffix + " WHERE is_dir=1"))
        assert len(observed_dirs) == len(directories), "Directory count differs"
        for row in observed_dirs:
            key = (row["media_id"], row["path"])
            assert key in directories, "Unexpected directory Position"
            assert [row["size"], row["mtime_ns"], row["written_at_ns"]] == directories.pop(key), "Directory summary differs"
            parts = row["path"].rstrip("/").split("/")
            assert row["parent_path"] == ("/".join(parts[:-1]) + "/" if len(parts) > 1 else ""), "Directory parent differs"
        result["directories"] = len(observed_dirs)

        source_media = {row["id"]: row for row in old.execute("SELECT * FROM tapes")}
        observed_media = list(current.execute("SELECT * FROM media" + suffix))
        assert {row["id"] for row in observed_media} == source_media.keys(), "Media IDs differ"
        for row in observed_media:
            source = source_media[row["id"]]
            assert row["identity"] == source["barcode"] and row["name"] == source["name"], "Media identity differs"
            assert row["created_at_ns"] == _stamp(source["create_time"]), "Media creation time differs"
            assert row["destroyed_at_ns"] == _stamp(source["destroy_time"]), "Media destruction time differs"
            assert row["capacity_bytes"] == source["capacity_bytes"], "Media capacity differs"
            profile = _fields(_fields(row["profile"])[1][0])
            assert profile.get(2, [b""])[0] == source["encryption"].encode(), "Media encryption differs"
            assert profile[3][0] == (b"ltfs_v1" if row["id"] in inventory else b"ltfs_v0"), "Media storage format differs"
            written = (
                sum(p["size"] for p in positions.values() if p["tape_id"] == row["id"])
                if row["id"] in inventory else source["writen_bytes"]
            )
            assert row["written_bytes"] == written, "Media written bytes differ"
        result["media"] = len(observed_media)

        physical_media = collections.defaultdict(set)
        for position in positions.values():
            physical_media[(position["path"], position["size"])].add(position["tape_id"])
        jobs = list(old.execute("SELECT * FROM jobs WHERE status!=255 ORDER BY id"))
        current_jobs = list(current.execute("SELECT * FROM jobs" + suffix + " ORDER BY id"))
        assert [row["id"] for row in current_jobs] == [row["id"] for row in jobs], "Job IDs differ"
        result["jobs"] = []
        for ordinal, (source, row) in enumerate(zip(jobs, current_jobs), 1):
            if (original_directory / "jobs" / str(source["id"]) / "state.db").exists():
                raise NotImplementedError("Transitional Job manifests are outside the inline Archive audit scope")
            # Frozen legacy JobStatus: COMPLETED=4, DELETED=255 (excluded above).
            if source["status"] != 4:
                raise NotImplementedError(
                    f"Unsupported legacy Job state for Job ID {source['id']} "
                    f"(state {source['status']}); only completed Archive Jobs are audited"
                )
            state = _fields(source["state"])
            if set(state) != {1}:
                raise NotImplementedError(
                    f"Unsupported legacy Job kind for Job ID {source['id']}; "
                    "only completed Archive Jobs are audited"
                )
            assert len(state[1]) == 1, "Ambiguous legacy Archive state"
            directory = migrated_directory / "jobs" / str(source["id"])
            with _connect(directory / "state.db") as db:
                job = db.execute("SELECT * FROM job").fetchone()
                assert job is not None, "Missing Job record"
                if job["kind"] != 1 or job["status"] != 4:
                    raise NotImplementedError(
                        f"Unsupported Job kind/state for Job ID {source['id']} "
                        f"(kind {job['kind']}, state {job['status']}); "
                        "only completed Archive Jobs are audited"
                    )
                sources = _fields(state[1][0])[2]
                assert job["priority"] == source["priority"], "Archive Job priority differs"
                assert row["created_at_ns"] == _stamp(source["create_time"]), "Job creation time differs"
                assert row["updated_at_ns"] == max(_stamp(source["create_time"]), _stamp(source["update_time"])), "Job update time differs"
                assert row["revision"] == (revisions or {}).get(source["id"], ordinal), "Job revision differs"
                metadata = json.loads((directory / "job.json").read_text())
                assert int(metadata["created_at_ns"]) == _stamp(source["create_time"]), "Job bundle creation time differs"
                previous_log = original_directory / "job-logs" / (str(source["id"]) + ".log")
                current_log = directory / "job.log"
                assert previous_log.exists() == current_log.exists(), "Job log presence differs"
                logged_media = set()
                if previous_log.exists():
                    log = previous_log.read_bytes()
                    assert log == current_log.read_bytes(), "Job log differs"
                    logged_media = {int(value) for value in re.findall(rb"create tape success, tape_id=\s*([1-9][0-9]*)\b", log)}
                items = list(db.execute("SELECT * FROM items ORDER BY id"))
                assert len(items) == len(sources), "Archive item count differs"
                for number, (item, raw) in enumerate(zip(items, sources), 1):
                    expected = _fields(raw)
                    if expected.get(3, [0]) != [4]:
                        raise NotImplementedError("Only submitted inline Archive sources are audited")
                    location = _fields(expected[1][0])
                    base = location.get(1, [b""])[0].decode()
                    parts = [part.decode() for part in location[2]]
                    path = posixpath.normpath(posixpath.join(*parts))
                    assert item["id"] == number and item["size"] == expected.get(2, [0])[0], "Archive item identity or size differs"
                    assert item["target_path"] == item["media_path"] == path and item["status"] == 4, "Archive item path or status differs"
                    assert _fields(item["data"])[1][0].decode() == posixpath.normpath(posixpath.join(base, *parts)), "Archive item source differs"
                    matching = physical_media[(path, item["size"])] & logged_media
                    assert matching == {item["media_id"]}, "Archive item has no unique matching logged Position"
            result["jobs"].append({
                "id": source["id"], "items": len(items),
                "log_bytes": current_log.stat().st_size if current_log.exists() else None,
            })
        result["coverage"] = {
            "completed_archive_jobs": len(jobs), "restore_jobs": 0,
            "other_job_kinds": 0, "other_job_states": 0,
        }
    return result
