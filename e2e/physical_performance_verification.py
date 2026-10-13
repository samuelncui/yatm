"""Disk-backed, bounded validation of physical performance evidence."""

from contextlib import closing
import json
from pathlib import Path
import sqlite3
from urllib.parse import unquote
import xml.etree.ElementTree as ET

from package_acceptance import require
from package_physical_evidence import decode_bytes, extent_order, relative_path


def json_lines(path):
    with Path(path).open() as source:
        for line in source:
            if line.strip():
                yield json.loads(line)


def index_files(path):
    """Yield one file, retaining directory depth and that file's extents only."""
    stack, directories = [], []
    saw_root = False
    for event, node in ET.iterparse(path, events=("start", "end")):
        node.tag = node.tag.rsplit("}", 1)[-1]
        if event == "start":
            stack.append(node)
            if len(stack) == 1:
                require(node.tag == "ltfsindex", "Missing LTFS Index root.")
            if node.tag == "directory":
                directories.append({"path": directories[-1]["path"] if directories else "", "root": not directories})
                saw_root = True
            continue
        parent = stack[-2] if len(stack) > 1 else None
        if node.tag == "name" and parent is not None and parent.tag == "directory" and not directories[-1]["root"]:
            value = node.text or ""
            if node.get("percentencoded") == "true":
                value = unquote(value, errors="strict")
            require(value and value not in (".", "..") and "/" not in value and "\x00" not in value,
                    "Invalid LTFS directory name.")
            directories[-1]["path"] = "/".join(filter(None, (directories[-1]["path"], value)))
        if node.tag == "file":
            name = node.find("name")
            require(name is not None, "LTFS file has no name.")
            value = name.text or ""
            if name.get("percentencoded") == "true":
                value = unquote(value, errors="strict")
            require(value and value not in (".", "..") and "/" not in value and "\x00" not in value,
                    "Invalid LTFS file name.")
            filename = "/".join(filter(None, (directories[-1]["path"], value)))
            size = int(node.findtext("length", "-1"))
            require(size >= 0, "Invalid LTFS file length.")
            extents = [{"partition": extent.findtext("partition", ""),
                        **{key: int(extent.findtext(xml, "0")) for key, xml in (
                            ("start_block", "startblock"), ("byte_offset", "byteoffset"),
                            ("byte_count", "bytecount"), ("file_offset_bytes", "fileoffset"))}}
                       for extent in node.findall("extentinfo/extent")]
            yield filename, size, extents
            parent.remove(node)
            node.clear()
        elif node.tag == "directory":
            directories.pop()
            if parent is not None:
                parent.remove(node)
            node.clear()
        stack.pop()
    require(saw_root, "Missing LTFS Index directory.")


class Verification:
    def __init__(self, path, media_id):
        self.database = sqlite3.connect(path)
        self.database.execute("PRAGMA cache_size=-8192")
        self.database.execute("PRAGMA temp_store=FILE")
        self.media_id = str(media_id)
        self.database.executescript("""
            CREATE TABLE fixture(name TEXT PRIMARY KEY, size INTEGER, hash TEXT,
                prepared INTEGER DEFAULT 0, submitted INTEGER DEFAULT 0);
            CREATE TABLE archive(name TEXT PRIMARY KEY, file_id TEXT UNIQUE, source TEXT, target TEXT,
                media_path TEXT UNIQUE, row TEXT);
            CREATE TABLE previous(path TEXT PRIMARY KEY, row TEXT);
            CREATE TABLE positions(path TEXT PRIMARY KEY, row TEXT, storage_order BLOB);
            CREATE TABLE physical(path TEXT PRIMARY KEY, size INTEGER, extents TEXT, storage_order BLOB);
            CREATE TABLE restored(file_id TEXT PRIMARY KEY);
        """)

    def close(self):
        self.database.commit()
        self.database.close()

    def insert(self, table, values):
        try:
            self.database.execute("INSERT INTO " + table + " VALUES (" + ",".join("?" for _ in values) + ")", values)
        except sqlite3.IntegrityError as error:
            raise RuntimeError("Duplicate evidence row in " + table) from error

    def fixtures(self, path, count, total):
        found, size = 0, 0
        for row in json_lines(path):
            name = str(relative_path(row["name"]))
            require("/" not in name and int(row["size"]) >= 0 and len(bytes.fromhex(row["sha256"])) == 32,
                    "Invalid source fixture row.")
            self.insert("fixture", (name, int(row["size"]), row["sha256"], 0, 0))
            found += 1
            size += int(row["size"])
        require(found == count and size == total, "Source fixture count or total differs from selected size.")
        self.database.commit()

    def archive_page(self, rows, source_directory, submitted=False):
        phase = "submitted" if submitted else "prepared"
        for item in rows:
            file = item["file"]
            name = Path(file["source_path"]).name
            saved = self.database.execute("SELECT size,hash," + phase + " FROM fixture WHERE name=?", (name,)).fetchone()
            require(saved is not None and not saved[2], "Archive has an unknown or duplicate fixture.")
            require(file["source_path"] == source_directory + "/" + name
                    and int(item.get("size_bytes", 0)) == saved[0]
                    and decode_bytes(file["expected"]["sha256"]).hex() == saved[1],
                    "Archive source path, size or hash differs from fixture.")
            file_id = str(file["expected"]["file_id"])
            require(int(file_id) > 0, "Archive has missing File identity.")
            target = str(relative_path(file["target_path"]))
            if not submitted:
                require(item["status"] == "COPY_STATUS_PENDING" and not item.get("media_id") and not file.get("media_path"),
                        "Prepared Archive is not pending.")
                self.insert("archive", (name, file_id, file["source_path"], target, None, None))
            else:
                require(item["status"] == "COPY_STATUS_SUBMITTED" and str(item.get("media_id")) == self.media_id,
                        "Archive item was not submitted to the assigned Tape.")
                saved_archive = self.database.execute("SELECT file_id,source,target FROM archive WHERE name=?", (name,)).fetchone()
                require(saved_archive == (file_id, file["source_path"], target), "Finalization changed prepared Archive identity.")
                path = str(relative_path(file["media_path"]))
                try:
                    self.database.execute("UPDATE archive SET media_path=?,row=? WHERE name=?",
                                          (path, json.dumps(item, sort_keys=True), name))
                except sqlite3.IntegrityError as error:
                    raise RuntimeError("Duplicate finalized Archive path") from error
            self.database.execute("UPDATE fixture SET " + phase + "=1 WHERE name=?", (name,))
        self.database.commit()

    def archive_complete(self, submitted=False):
        phase = "submitted" if submitted else "prepared"
        require(self.database.execute("SELECT count(*) FROM fixture WHERE " + phase + "=0").fetchone()[0] == 0,
                "Archive omitted fixture files.")

    def library(self, path, previous=False):
        table = "previous" if previous else "positions"
        header, ended = False, False
        for record in json_lines(path):
            require(not ended, "Library export has trailing records.")
            if not header:
                require(record.get("type") == "header" and record.get("format") == "yatm-library-backup",
                        "Library export has no header.")
                header = True
                continue
            if record.get("type") == "end":
                ended = True
                continue
            row = record.get("data", {})
            if record.get("type") != "position" or str(row.get("media_id")) != self.media_id or row.get("is_dir"):
                continue
            filename = str(relative_path(row["path"]))
            encoded = json.dumps(row, sort_keys=True)
            self.insert(table, (filename, encoded) if previous else (filename, encoded, decode_bytes(row.get("storage_order"))))
        require(header and ended, "Library export is incomplete.")
        self.database.commit()
        if not previous:
            require(not self.database.execute("SELECT 1 FROM previous p LEFT JOIN positions n ON p.path=n.path "
                                               "WHERE n.path IS NULL OR p.row<>n.row LIMIT 1").fetchone(),
                    "APPEND changed or omitted previous Positions.")
            actual = self.database.execute("SELECT count(*) FROM positions").fetchone()[0]
            expected = self.database.execute("SELECT (SELECT count(*) FROM previous)+(SELECT count(*) FROM archive)").fetchone()[0]
            require(actual == expected, "APPEND published unexpected Position count.")

    def index(self, path):
        for filename, size, extents in index_files(path):
            self.insert("physical", (filename, size, json.dumps(extents), extent_order(extents, size)))
        self.database.commit()

    def physical_position(self, position, size, extents_json, actual_order):
        require(extents_json is not None and int(position.get("size", 0)) == size,
                "Position missing from final Index or has a different size.")
        extents = json.loads(extents_json)
        persisted = [{"partition": row["partition"], **{key: int(row.get(wire, 0)) for key, wire in (
            ("start_block", "startBlock"), ("byte_offset", "byteOffset"), ("byte_count", "byteCount"),
            ("file_offset_bytes", "fileOffsetBytes"))}}
                     for row in position.get("storage_metadata", {}).get("ltfs", {}).get("extents", [])]
        normalize = lambda rows: sorted((row["partition"], *(row[key] for key in
                                   ("start_block", "byte_offset", "byte_count", "file_offset_bytes"))) for row in rows)
        require(normalize(persisted) == normalize(extents) and decode_bytes(position.get("storage_order")) == actual_order,
                "Persisted extent/order differs from final Index.")
        return extents

    def validate_positions(self, data_partition):
        # APPEND must preserve physical evidence for all earlier published files, too.
        for position_json, size, extents_json, actual_order in self.database.execute(
                "SELECT p.row,i.size,i.extents,i.storage_order FROM previous p LEFT JOIN physical i ON p.path=i.path"):
            self.physical_position(json.loads(position_json), size, extents_json, actual_order)
        last_order = None
        query = "SELECT a.row,p.row,p.storage_order,i.size,i.extents,i.storage_order FROM archive a " \
                "LEFT JOIN positions p ON a.media_path=p.path LEFT JOIN physical i ON a.media_path=i.path ORDER BY p.storage_order,a.media_path"
        for item_json, position_json, stored_order, size, extents_json, actual_order in self.database.execute(query):
            require(position_json is not None and extents_json is not None, "Submitted file missing from Position or final Index.")
            item, position = json.loads(item_json), json.loads(position_json)
            require(int(position.get("size", 0)) == size == int(item.get("size_bytes", 0))
                    and position["hash"] == item["file"]["expected"]["sha256"], "Position/Index content differs from Archive.")
            extents = self.physical_position(position, size, extents_json, actual_order)
            require(all(row["partition"] == data_partition for row in extents), "Performance file used a different partition.")
            require(stored_order == actual_order and (last_order is None or actual_order > last_order),
                    "Persisted storage order is inconsistent or duplicated.")
            last_order = actual_order

    def restore_page(self, rows, target_directory):
        checks = []
        for row in rows:
            file_id = str(row["file"]["file_id"])
            saved = self.database.execute("SELECT a.media_path,f.size,f.hash FROM archive a JOIN fixture f ON a.name=f.name "
                                          "WHERE a.file_id=?", (file_id,)).fetchone()
            require(saved is not None, "Restore selected a File outside the Archive.")
            self.insert("restored", (file_id,))
            require(row["status"] == "COPY_STATUS_COMPLETED" and not row.get("damaged")
                    and str(row["candidate"]["media_id"]) == self.media_id and row["candidate"]["media_path"] == saved[0]
                    and int(row.get("actual_size_bytes", 0)) == saved[1]
                    and decode_bytes(row["actual_sha256"]).hex() == saved[2], "Restore failed content/copy verification.")
            checks.append({"path": target_directory + "/" + str(relative_path(row["file"]["target_path"])),
                           "size": saved[1], "sha256": saved[2]})
        self.database.commit()
        return checks

    def restore_complete(self, database_path, output):
        require(self.database.execute("SELECT count(*) FROM restored").fetchone()[0] ==
                self.database.execute("SELECT count(*) FROM archive").fetchone()[0], "Restore omitted archived Files.")
        with closing(sqlite3.connect(Path(database_path).resolve().as_uri() + "?mode=ro", uri=True)) as source, Path(output).open("x") as evidence:
            require(source.execute("PRAGMA quick_check").fetchone() == ("ok",), "Restore database is inconsistent.")
            count = 0
            previous = None
            for path, order in source.execute("SELECT media_path,storage_order FROM copies WHERE media_id=? ORDER BY storage_order,media_path,id", (int(self.media_id),)):
                saved = self.database.execute("SELECT p.storage_order FROM archive a JOIN positions p ON a.media_path=p.path "
                                              "WHERE a.media_path=?", (path,)).fetchone()
                require(saved is not None and saved[0] == order and (previous is None or order > previous),
                        "Restore Copy selection or storage order differs from finalized Archive.")
                previous = order
                evidence.write(json.dumps({"media_path": path, "storage_order": order.hex()}) + "\n")
                count += 1
            require(count == self.database.execute("SELECT count(*) FROM archive").fetchone()[0], "Restore Copy count differs from Archive.")
