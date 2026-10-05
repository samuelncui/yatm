"""Local assertions for physical Tape evidence; no host or device operations."""

import base64
from contextlib import closing
import hashlib
import json
from pathlib import PurePosixPath
import re
import shlex
import sqlite3
import struct
from urllib.parse import unquote
import xml.etree.ElementTree as ET

from package_acceptance import require, sha256


def relative_path(value):
    path = PurePosixPath(value)
    require(value and not path.is_absolute() and ".." not in path.parts and "\x00" not in value,
            "Evidence contains an unsafe relative path.")
    return path


def decode_bytes(value):
    return base64.b64decode(value or "", validate=True)


def format_partition_map(log):
    """Read mkltfs's declared roles, independently of the files used to test placement."""
    mapping, scsi = {}, {}
    for code, role in (("LTFS15010I", "data"), ("LTFS15011I", "index")):
        matches = set(re.findall(r"\b" + code + r"\s+Creating\s+" + role
                                 + r"\s+partition\s+([a-z])\s+on\s+SCSI\s+partition\s+([0-9]+)\b", log))
        require(len(matches) == 1, "FORMAT log has no unambiguous " + role + " partition declaration.")
        mapping[role], scsi[role] = next(iter(matches))
    require(mapping["data"] != mapping["index"] and scsi["data"] != scsi["index"],
            "FORMAT log assigns both roles to the same partition.")
    return mapping


def library_positions(data, media_id):
    records = [json.loads(line) for line in data.splitlines() if line.strip()]
    require(records and records[0].get("type") == "header"
            and records[0].get("format") == "yatm-library-backup"
            and records[-1].get("type") == "end", "Library export is incomplete.")
    positions = {}
    for record in records:
        row = record.get("data", {})
        if record["type"] != "position" or str(row.get("media_id")) != str(media_id) or row.get("is_dir"):
            continue
        path = str(relative_path(row["path"]))
        require(path not in positions, "Export contains duplicate Tape Positions.")
        positions[path] = row
    return positions


def parse_index(data):
    root = ET.fromstring(data)
    # ElementTree's namespace expansion must not change LTFS field lookup.
    for element in root.iter():
        element.tag = element.tag.rsplit("}", 1)[-1]
    require(root.tag == "ltfsindex" and root.find("directory") is not None, "Missing LTFS Index root.")
    entries = {}

    def name(node):
        element = node.find("name")
        require(element is not None, "LTFS entry has no name.")
        value = element.text or ""
        if element.get("percentencoded") == "true":
            value = unquote(value, errors="strict")
        require(value and value not in (".", "..") and "/" not in value and "\x00" not in value,
                "Invalid LTFS entry name.")
        return value

    def visit(directory, parent):
        for child in directory.findall("contents/directory"):
            visit(child, parent + [name(child)])
        for file in directory.findall("contents/file"):
            path = "/".join(parent + [name(file)])
            require(path not in entries, "Duplicate file in LTFS Index.")
            size = int(file.findtext("length", "-1"))
            require(size >= 0, "Invalid LTFS file length.")
            extents = []
            for extent in file.findall("extentinfo/extent"):
                extents.append({"partition": extent.findtext("partition", ""),
                                **{key: int(extent.findtext(xml, "0")) for key, xml in (
                                    ("start_block", "startblock"), ("byte_offset", "byteoffset"),
                                    ("byte_count", "bytecount"), ("file_offset_bytes", "fileoffset"))}})
            entries[path] = {"size": size, "extents": extents}
    visit(root.find("directory"), [])
    return entries


def extent_order(extents, size):
    if size == 0:
        require(not extents, "Empty file unexpectedly has LTFS extents.")
        return b""
    require(extents, "Nonempty file has no LTFS extents.")
    offset = 0
    ordered = sorted(extents, key=lambda extent: int(extent.get("file_offset_bytes", 0)))
    for extent in ordered:
        require(len(extent["partition"]) == 1 and extent["partition"].isascii(), "Invalid LTFS partition.")
        require(int(extent.get("file_offset_bytes", 0)) == offset and int(extent.get("byte_count", 0)) > 0,
                "LTFS extents have a gap, overlap or empty range.")
        require(all(0 <= int(extent.get(field, 0)) < 2**64 for field in ("start_block", "byte_offset")),
                "Invalid LTFS physical extent.")
        offset += int(extent["byte_count"])
    require(offset == size, "LTFS extents do not cover the complete file.")
    first = ordered[0]
    return first["partition"].encode("ascii") + struct.pack(">QQ", int(first.get("start_block", 0)), int(first.get("byte_offset", 0)))


def check_positions(items, positions, index, media_id, partition_map=None):
    """Compare submitted files with exported durable facts and the actual final Index."""
    last_order = {}
    for item in items:
        path = item["file"]["media_path"]
        require(item["status"] == "COPY_STATUS_SUBMITTED" and str(item.get("media_id")) == str(media_id),
                "Archive item was not submitted to the assigned Tape.")
        require(path in positions and path in index, "Submitted file is missing from Position or final Index.")
        position, physical = positions[path], index[path]
        size = int(item.get("size_bytes", 0))
        require(int(position.get("size", 0)) == physical["size"] == size
                and position["hash"] == item["file"]["expected"]["sha256"],
                "Position/Index facts differ from the submitted file.")
        # The Library row uses snake_case, but its embedded StorageMetadata uses protojson's camelCase.
        stored = position.get("storage_metadata", {}).get("ltfs", {}).get("extents", [])
        extents = [{"partition": extent["partition"], **{key: int(extent.get(wire, 0)) for key, wire in (
            ("start_block", "startBlock"), ("byte_offset", "byteOffset"),
            ("byte_count", "byteCount"), ("file_offset_bytes", "fileOffsetBytes"))}} for extent in stored]
        normalized = lambda rows: sorted((e["partition"], *(int(e.get(k, 0)) for k in
                                         ("start_block", "byte_offset", "byte_count", "file_offset_bytes"))) for e in rows)
        require(normalized(extents) == normalized(physical["extents"]), "Persisted extents differ from the Index.")
        order = extent_order(extents, size)
        require(decode_bytes(position.get("storage_order")) == order, "Persisted storage order differs from extents.")
        if not order:
            continue
        partition = chr(order[0])
        require(partition not in last_order or last_order[partition] < order,
                "Physical write order differs from deterministic Archive order.")
        last_order[partition] = order
        if partition_map:
            kind = "index" if size <= 1024**2 and path.endswith(".txt") else "data"
            require(partition == partition_map[kind], "Format-time partition placement rule was not retained.")


def submitted_prefix(items, media_id):
    prefix, pending = [], False
    for item in items:
        if item["status"] == "COPY_STATUS_SUBMITTED":
            require(not pending and str(item.get("media_id")) == str(media_id) and item["file"].get("media_path"),
                    "Submitted items are not one continuous prefix on the assigned Tape.")
            prefix.append(item)
        else:
            pending = True
            require(item["status"] == "COPY_STATUS_PENDING" and not item.get("media_id")
                    and not item["file"].get("media_path"), "Unfinalized suffix was published or remains staged.")
    require(0 < len(prefix) < len(items), "Physical boundary needs both a submitted prefix and pending suffix.")
    return prefix


def check_checkpoint(log, report, job, items, media_id, progress):
    require(job["status"] == "JOB_STATUS_READY" and job.get("error")
            and job.get("phase", "JOB_PHASE_UNSPECIFIED") == "JOB_PHASE_UNSPECIFIED",
            "Full Archive has not settled at its failed Media boundary.")
    prefix = submitted_prefix(items, media_id)
    count, size = len(prefix), sum(int(item["size_bytes"]) for item in prefix)
    checkpoints = []
    for line in log.decode().splitlines():
        if "archive_media_checkpoint" not in line:
            continue
        fields = dict(field.split("=", 1) for field in shlex.split(line) if "=" in field)
        if fields.get("event") == "archive_media_checkpoint":
            checkpoints.append(fields)
    expected = {"reason": "no_space", "media_id": str(media_id), "files": str(count), "bytes": str(size)}
    require(checkpoints and all(checkpoints[-1].get(k) == v for k, v in expected.items())
            and checkpoints[-1].get("error"), "Missing complete durable no_space checkpoint.")
    require(str(report["media_id"]) == str(media_id) and int(report["file_count"]) == count
            and int(report["bytes"]) == size and report.get("termination_error"), "Archive report differs from checkpoint.")
    require(progress.get("total_known") and int(progress["total_file_count"]) == len(items)
            and int(progress["total_bytes"]) == sum(int(i["size_bytes"]) for i in items)
            and int(progress["copied_file_count"]) == count and int(progress["copied_bytes"]) == size,
            "Archive progress counts an unfinalized suffix.")
    return prefix


def check_restore_database(path, positions, selected_paths, media_id):
    with closing(sqlite3.connect(path.resolve().as_uri() + "?mode=ro", uri=True)) as database:
        require(database.execute("PRAGMA quick_check").fetchall() == [("ok",)], "Copied Job database is inconsistent.")
        rows = database.execute("SELECT media_path, storage_order FROM copies WHERE media_id = ? "
                                "ORDER BY storage_order, media_path, id", (int(media_id),)).fetchall()
    require(len(rows) == len(selected_paths) and {row[0] for row in rows} == set(selected_paths),
            "Restore Copy selection differs from selected archived paths.")
    require(all(order == decode_bytes(positions[name].get("storage_order")) for name, order in rows),
            "Restore Copy did not retain the Position storage order.")
    return [row[0] for row in rows]


def check_signature(data, size, digest):
    require(len(data) == 56 and data[:8] == b"ACPS\x01\x01\x00\x00", "Invalid restored ACP signature xattr.")
    require(struct.unpack(">q", data[8:16])[0] == size and data[24:].hex() == digest,
            "Restored signature xattr differs from source size/SHA-256.")


def seal_evidence(directory):
    paths = sorted(path for path in directory.rglob("*") if path.is_file() and path.name != "SHA256SUMS")
    require(paths, "No evidence was captured.")
    manifest = "".join(f"{sha256(path)}  {path.relative_to(directory).as_posix()}\n" for path in paths)
    (directory / "SHA256SUMS").write_text(manifest)
    verify_evidence(directory)
    return hashlib.sha256(manifest.encode()).hexdigest()


def verify_evidence(directory):
    lines = (directory / "SHA256SUMS").read_text().splitlines()
    require(lines, "Evidence checksum manifest is empty.")
    listed = set()
    for line in lines:
        digest, name = line.split("  ", 1)
        path = directory / relative_path(name)
        require(name not in listed, "Evidence manifest repeats an artifact.")
        listed.add(name)
        require(path.is_file() and not path.is_symlink() and sha256(path) == digest,
                "Preserved evidence failed checksum verification; retain all Jobs.")
    actual = {p.relative_to(directory).as_posix() for p in directory.rglob("*")
              if p.is_file() and p.name != "SHA256SUMS"}
    require(listed == actual, "Evidence manifest does not cover every preserved artifact.")
