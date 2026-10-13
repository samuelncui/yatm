"""Bounded fixture and physical-evidence semantics without Tape access."""

import base64
import hashlib
import json
from pathlib import Path
import sqlite3
import struct
import tempfile
import unittest
from unittest import mock

import physical_performance_fixture as fixture
from physical_performance_verification import Verification, index_files


def encoded(value):
    return base64.b64encode(value).decode()


def write_jsonl(path, rows):
    with path.open("w") as output:
        for row in rows:
            output.write(json.dumps(row) + "\n")


class VerificationTests(unittest.TestCase):
    def setUp(self):
        temporary = tempfile.TemporaryDirectory()
        self.addCleanup(temporary.cleanup)
        self.root = Path(temporary.name)
        self.verify = Verification(self.root / "verify.db", "9")
        self.addCleanup(self.verify.close)
        self.digest = hashlib.sha256(b"abc").digest()

    def fixture_row(self, number):
        return {"name": f"{number:05d}.bin", "size": 3, "sha256": self.digest.hex()}

    def item(self, number, submitted=False):
        name = self.fixture_row(number)["name"]
        row = {"status": "COPY_STATUS_SUBMITTED" if submitted else "COPY_STATUS_PENDING", "size_bytes": "3",
               "file": {"source_path": "/source/" + name, "target_path": "case/" + name,
                        "expected": {"file_id": str(number + 1), "sha256": encoded(self.digest)}}}
        if submitted:
            row["media_id"] = "9"
            row["file"]["media_path"] = "append/case/" + name
        return row

    def position(self, number):
        return {"media_id": 9, "path": self.item(number, True)["file"]["media_path"], "size": 3,
                "hash": encoded(self.digest), "storage_order": encoded(b"b" + struct.pack(">QQ", number + 4, 7)),
                "storage_metadata": {"ltfs": {"extents": [{"partition": "b", "startBlock": str(number + 4),
                                                          "byteOffset": "7", "byteCount": "3"}]}}}

    def fixtures(self, count=2):
        path = self.root / "fixtures.jsonl"
        write_jsonl(path, (self.fixture_row(number) for number in range(count)))
        self.verify.fixtures(path, count, count * 3)

    def archives(self, count=2):
        self.fixtures(count)
        for start in range(0, count, 1000):
            self.verify.archive_page([self.item(number) for number in range(start, min(start + 1000, count))], "/source")
        self.verify.archive_complete()
        for start in range(0, count, 1000):
            self.verify.archive_page([self.item(number, True) for number in range(start, min(start + 1000, count))], "/source", True)
        self.verify.archive_complete(True)

    def library(self, count=2, positions=None):
        header = {"type": "header", "format": "yatm-library-backup"}
        before = self.root / "before.jsonl"
        write_jsonl(before, [header, {"type": "end"}])
        self.verify.library(before, previous=True)
        after = self.root / "after.jsonl"
        rows = positions if positions is not None else (self.position(number) for number in range(count))
        write_jsonl(after, [header, *({"type": "position", "data": row} for row in rows), {"type": "end"}])
        self.verify.library(after)

    def index(self, count=2, reverse=False):
        path = self.root / "index.xml"
        with path.open("w") as output:
            output.write('<ltfsindex xmlns="urn:ltfs"><directory><name>root</name><contents><directory><name>append</name><contents><directory><name>case</name><contents>')
            for number in range(count):
                block = count - number + 3 if reverse else number + 4
                output.write(f'<file><name>{number:05d}.bin</name><length>3</length><extentinfo><extent><partition>b</partition><startblock>{block}</startblock><byteoffset>7</byteoffset><bytecount>3</bytecount><fileoffset>0</fileoffset></extent></extentinfo></file>')
            output.write('</contents></directory></contents></directory></contents></directory></ltfsindex>')
        self.verify.index(path)
        return path

    def test_cross_page_manifest_and_complete_streamed_evidence(self):
        self.archives(2003)
        self.library(2003)
        self.index(2003)
        self.verify.validate_positions("b")
        self.assertEqual(self.verify.database.execute("SELECT count(*) FROM archive").fetchone()[0], 2003)

    def test_prepared_omission_duplicate_path_and_hash_fail(self):
        self.fixtures()
        self.verify.archive_page([self.item(0)], "/source")
        with self.assertRaisesRegex(RuntimeError, "omitted"):
            self.verify.archive_complete()
        with self.assertRaisesRegex(RuntimeError, "duplicate"):
            self.verify.archive_page([self.item(0)], "/source")
        bad = self.item(1)
        bad["file"]["source_path"] = "/foreign/00001.bin"
        with self.assertRaisesRegex(RuntimeError, "source path"):
            self.verify.archive_page([bad], "/source")
        bad = self.item(1)
        bad["file"]["expected"]["sha256"] = encoded(bytes(32))
        with self.assertRaisesRegex(RuntimeError, "hash"):
            self.verify.archive_page([bad], "/source")

    def test_duplicate_fixture_and_wrong_dimensions(self):
        path = self.root / "duplicate.jsonl"
        write_jsonl(path, [self.fixture_row(0), self.fixture_row(0)])
        with self.assertRaisesRegex(RuntimeError, "Duplicate"):
            self.verify.fixtures(path, 2, 6)

    def test_duplicate_final_path_rejected(self):
        self.fixtures()
        self.verify.archive_page([self.item(0), self.item(1)], "/source")
        first, second = self.item(0, True), self.item(1, True)
        second["file"]["media_path"] = first["file"]["media_path"]
        with self.assertRaisesRegex(RuntimeError, "Duplicate finalized"):
            self.verify.archive_page([first, second], "/source", True)

    def test_library_truncation_duplicate_and_preserved_rows(self):
        self.archives()
        path = self.root / "bad.jsonl"
        header = {"type": "header", "format": "yatm-library-backup"}
        write_jsonl(path, [header])
        with self.assertRaisesRegex(RuntimeError, "incomplete"):
            self.verify.library(path, previous=True)
        write_jsonl(path, [header, {"type": "position", "data": self.position(0)},
                          {"type": "position", "data": self.position(0)}, {"type": "end"}])
        with self.assertRaisesRegex(RuntimeError, "Duplicate"):
            self.verify.library(path, previous=True)

    def test_extent_partition_and_content_mismatch_rejected(self):
        self.archives()
        self.library()
        self.index()
        with self.assertRaisesRegex(RuntimeError, "partition"):
            self.verify.validate_positions("a")
        row = self.position(0)
        row["hash"] = encoded(bytes(32))
        self.verify.database.execute("UPDATE positions SET row=? WHERE path=?", (json.dumps(row), row["path"]))
        with self.assertRaisesRegex(RuntimeError, "content"):
            self.verify.validate_positions("b")

    def test_physical_write_order_can_differ_from_archive_target_order(self):
        self.archives()
        positions = [self.position(1), self.position(0)]
        positions[0]["path"], positions[1]["path"] = positions[1]["path"], positions[0]["path"]
        self.library(positions=positions)
        self.index(reverse=True)
        self.verify.validate_positions("b")

    def test_inconsistent_or_duplicate_storage_order_is_rejected(self):
        self.archives()
        self.library()
        self.index()
        self.verify.database.execute("UPDATE positions SET storage_order=? WHERE path=?",
                                     (b"wrong", self.position(0)["path"]))
        with self.assertRaisesRegex(RuntimeError, "storage order"):
            self.verify.validate_positions("b")
        position = self.position(0)
        self.verify.database.execute("UPDATE positions SET storage_order=?",
                                     (base64.b64decode(position["storage_order"]),))
        position["path"] = self.position(1)["path"]
        self.verify.database.execute("UPDATE positions SET row=? WHERE path=?",
                                     (json.dumps(position), position["path"]))
        self.verify.database.execute("UPDATE physical SET (size,extents,storage_order)="
                                     "(SELECT size,extents,storage_order FROM physical WHERE path=?) WHERE path=?",
                                     (self.position(0)["path"], self.position(1)["path"]))
        with self.assertRaisesRegex(RuntimeError, "duplicated"):
            self.verify.validate_positions("b")

    def test_library_prior_facts_cannot_change_or_disappear(self):
        self.archives()
        old = self.position(8)
        header = {"type": "header", "format": "yatm-library-backup"}
        before, after = self.root / "prior.jsonl", self.root / "new.jsonl"
        write_jsonl(before, [header, {"type": "position", "data": old}, {"type": "end"}])
        self.verify.library(before, previous=True)
        old["size"] = 4
        write_jsonl(after, [header, {"type": "position", "data": old},
                           {"type": "position", "data": self.position(0)},
                           {"type": "position", "data": self.position(1)}, {"type": "end"}])
        with self.assertRaisesRegex(RuntimeError, "previous Positions"):
            self.verify.library(after)

    def test_previous_physical_files_must_remain_in_final_index(self):
        self.archives()
        self.library()
        self.index()
        old = self.position(8)
        self.verify.insert("previous", (old["path"], json.dumps(old)))
        with self.assertRaisesRegex(RuntimeError, "missing from final Index"):
            self.verify.validate_positions("b")

    def test_restore_omission_and_changed_hash_fail(self):
        self.archives()
        self.library()
        row = {"file": {"file_id": "1", "target_path": "case/00000.bin"},
               "status": "COPY_STATUS_COMPLETED", "candidate": {"media_id": "9", "media_path": self.position(0)["path"]},
               "actual_size_bytes": "3", "actual_sha256": encoded(bytes(32))}
        with self.assertRaisesRegex(RuntimeError, "content"):
            self.verify.restore_page([row], "/restored")
        # The failed evidence row remains for review, but completion still rejects the omitted File.
        with self.assertRaisesRegex(RuntimeError, "omitted"):
            self.verify.restore_complete(self.root / "absent.db", self.root / "absent.jsonl")

    def test_streamed_index_matches_nested_namespaces_and_detects_duplicates(self):
        self.archives()
        self.library()
        path = self.index()
        self.assertEqual([row[0] for row in index_files(path)], ["append/case/00000.bin", "append/case/00001.bin"])
        with self.assertRaisesRegex(RuntimeError, "Duplicate"):
            self.verify.index(path)

    def test_restore_content_omissions_duplicates_and_storage_order(self):
        self.archives()
        self.library()
        self.index()
        self.verify.validate_positions("b")
        rows = [{"file": {"file_id": str(number + 1), "target_path": f"case/{number:05d}.bin"},
                 "status": "COPY_STATUS_COMPLETED", "candidate": {"media_id": "9", "media_path": self.position(number)["path"]},
                 "actual_size_bytes": "3", "actual_sha256": encoded(self.digest)} for number in range(2)]
        checks = self.verify.restore_page(rows, "/restored")
        self.assertEqual(checks[0], {"path": "/restored/case/00000.bin", "size": 3, "sha256": self.digest.hex()})
        with self.assertRaisesRegex(RuntimeError, "Duplicate"):
            self.verify.restore_page([rows[0]], "/restored")
        source = self.root / "restore.db"
        with sqlite3.connect(source) as database:
            database.execute("CREATE TABLE copies(id INTEGER,media_id INTEGER,media_path TEXT,storage_order BLOB)")
            for number in (1, 0):
                position = self.position(number)
                database.execute("INSERT INTO copies VALUES(?,?,?,?)", (number + 1, 9, position["path"], base64.b64decode(position["storage_order"])))
        output = self.root / "order.jsonl"
        self.verify.restore_complete(source, output)
        self.assertEqual([json.loads(line)["media_path"] for line in output.read_text().splitlines()],
                         [self.position(0)["path"], self.position(1)["path"]])
        with sqlite3.connect(source) as database:
            database.execute("UPDATE copies SET storage_order=? WHERE id=1", (b"wrong",))
        with self.assertRaisesRegex(RuntimeError, "storage order"):
            self.verify.restore_complete(source, self.root / "bad-order.jsonl")


class FixtureDimensionsTests(unittest.TestCase):
    def test_exact_large_dimensions_without_generating_data(self):
        for size, count in ((4, 16384), (32, 131072), (64, 262144)):
            self.assertEqual(fixture.dimensions("small-random", size), (count, 256 * 1024, False))
            self.assertEqual(fixture.dimensions("large-random", size), (size, 1024**3, False))
        with self.assertRaises(ValueError):
            fixture.dimensions("small-random", 16)

    def test_incremental_fixture_jsonl_identity_and_content(self):
        with tempfile.TemporaryDirectory() as root, mock.patch.dict(fixture.MATRIX, {"tiny": (3, 17, False)}):
            directory, manifest = Path(root) / "source", Path(root) / "source.jsonl"
            result = fixture.generate_jsonl(directory, "tiny", 4, str(manifest))
            self.assertEqual((result["count"], result["logical_bytes"]), (3, 51))
            self.assertEqual(result["sha256"], hashlib.sha256(manifest.read_bytes()).hexdigest())
            for line in manifest.read_text().splitlines():
                row = json.loads(line)
                self.assertEqual(hashlib.sha256((directory / row["name"]).read_bytes()).hexdigest(), row["sha256"])
            with self.assertRaisesRegex(ValueError, "outside"):
                fixture.generate_jsonl(Path(root) / "next", "tiny", 4, str(Path(root) / "next" / "manifest"))
            with self.assertRaisesRegex(ValueError, "64-file"):
                fixture.verify([{}] * 65)


if __name__ == "__main__":
    unittest.main()
