"""Small synthetic checks of the independent audit, not of the Go migrator."""

import contextlib
import io
import json
import pathlib
import sqlite3
import subprocess
import sys
import tempfile
import unittest
from unittest import mock

import legacy_metadata as audit


class EntryPointTest(unittest.TestCase):
    def test_optimized_python_fails_before_opening_databases(self):
        result = subprocess.run(
            [sys.executable, "-B", "-O", "-c", """
import legacy_metadata
from unittest import mock
with mock.patch.object(legacy_metadata.sqlite3, "connect", side_effect=AssertionError("database opened")):
    legacy_metadata.check("unused-original", "unused-migrated")
"""],
            cwd=pathlib.Path(__file__).resolve().parent, capture_output=True, text=True,
        )
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("RuntimeError: Legacy metadata audit requires assertions", result.stderr)
        self.assertNotIn("database opened", result.stderr)
        self.assertEqual(result.stdout, "")


class TimestampTest(unittest.TestCase):
    def test_exact_nanoseconds(self):
        cases = [
            (None, None),
            ("0001-01-01 00:00:00+00:00", 0),
            ("0001-01-01T00:00:00.000000000Z", 0),
            ("1970-01-01 00:00:00", 0),
            ("1969-12-31T23:59:59.999999999Z", -1),
            ("1969-12-31 23:59:59.5", -500_000_000),
            ("1970-01-01T01:00:00.000000001+01:00", 1),
            ("1969-12-31T23:00:00.25-01:00", 250_000_000),
            ("1677-09-21T00:12:43.145224192Z", -(1 << 63)),
            ("2262-04-11T23:47:16.854775807Z", (1 << 63) - 1),
        ]
        for value, expected in cases:
            with self.subTest(value=value):
                self.assertEqual(audit._stamp(value), expected)

    def test_rejects_invalid_or_unrepresentable_times(self):
        for value in (
            "not a timestamp", "1970-02-30T00:00:00Z",
            "1970-01-01T00:00:00.0000000001Z",
            "0001-01-01T00:00:00.000000001Z",
            "1677-09-21T00:12:43.145224191Z",
            "2262-04-11T23:47:16.854775808Z",
        ):
            with self.subTest(value=value), self.assertRaises(AssertionError):
                audit._stamp(value)


class DecoderTest(unittest.TestCase):
    def test_uncompressed_values_need_no_command(self):
        with mock.patch.object(audit.subprocess, "run") as run:
            self.assertEqual(audit._fields(None), {})
            self.assertEqual(audit._fields(b""), {})
            self.assertEqual(audit._fields(b"\x08\x96\x01\x12\x02ok"), {1: [150], 2: [b"ok"]})
            run.assert_not_called()

    def test_compression_uses_path_command_only_when_needed(self):
        with mock.patch.object(audit.subprocess, "run") as run:
            run.return_value.stdout = b"\x08\x96\x01"
            self.assertEqual(audit._fields(b"\xffym\x02synthetic compressed bytes"), {1: [150]})
            run.assert_called_once_with(
                ["zstd", "-dq", "--stdout"], input=b"synthetic compressed bytes",
                check=True, capture_output=True,
            )

    def test_compression_errors_are_explicit_without_decoder_output(self):
        failures = [
            (FileNotFoundError(), RuntimeError, "requires zstd on PATH"),
            (subprocess.CalledProcessError(1, "zstd", stderr=b"synthetic content"),
             AssertionError, "Cannot decode compressed legacy metadata"),
        ]
        for error, kind, message in failures:
            with self.subTest(kind=kind), mock.patch.object(audit.subprocess, "run", side_effect=error):
                with self.assertRaisesRegex(kind, message) as caught:
                    audit._fields(b"\xffym\x02synthetic compressed bytes")
                self.assertNotIn("synthetic content", str(caught.exception))

    def test_incomplete_wire_values_do_not_pass(self):
        for data in (b"\x08\x80", b"\x0a\x02x", b"\x09x", b"\x0dx", b"\x0f", b"\x00"):
            with self.subTest(data=data), self.assertRaises(AssertionError):
                audit._fields(data)


class MetadataAuditTest(unittest.TestCase):
    def setUp(self):
        temporary = tempfile.TemporaryDirectory(prefix="legacy-audit-")
        self.addCleanup(temporary.cleanup)
        self.root = pathlib.Path(temporary.name)
        # URI punctuation also exercises read-only SQLite filenames.
        self.original = self.root / "original #?%"
        self.migrated = self.root / "migrated #?%"
        self.original.mkdir()
        self.migrated.mkdir()
        self.catalog = self.migrated / "tapes.db"
        self.bundle = self.migrated / "jobs" / "1"
        self.bundle.mkdir(parents=True)
        self.state_db = self.bundle / "state.db"
        file_hash = b"x" * 32
        signature = b"opaque fixture signature"
        modified = "1969-12-31 23:59:59.999999999+00:00"
        written = "1970-01-01T01:00:01.000000002+01:00"
        # Literal wire fixture: Archive -> one source -> Location(source, folder/sample), size 3.
        state = b"\x0a\x20\x12\x1e\x0a\x18\x0a\x06source\x12\x06folder\x12\x06sample\x10\x03\x18\x04"
        with contextlib.closing(sqlite3.connect(self.original / "tapes.db")) as db, db:
            db.executescript("""
                CREATE TABLE files (id INTEGER PRIMARY KEY, parent_id, name, mode, size, hash, signature, mod_time);
                CREATE TABLE tapes (id INTEGER PRIMARY KEY, barcode, name, create_time, destroy_time,
                                    capacity_bytes, writen_bytes, encryption);
                CREATE TABLE positions (id INTEGER PRIMARY KEY, file_id, tape_id, path, mode, size,
                                        hash, mod_time, write_time);
                CREATE TABLE jobs (id INTEGER PRIMARY KEY, status, priority, create_time, update_time, state);
                INSERT INTO tapes VALUES (1, 'SYNTHETIC', 'Synthetic tape',
                    '0001-01-01 00:00:00+00:00', NULL, 100, 99, '');
                INSERT INTO jobs VALUES (2, 255, 0, NULL, NULL, X'1200');
            """)
            db.executemany("INSERT INTO files VALUES (?,?,?,?,?,?,?,?)", [
                (1, 0, "folder", 1 << 31, 0, None, None, modified),
                (2, 1, "sample", 420, 3, file_hash, signature, modified),
            ])
            db.executemany("INSERT INTO positions VALUES (?,?,?,?,?,?,?,?,?)", [
                (1, 2, 1, "folder/sample", 420, 3, file_hash, modified, written),
                (2, 2, 1, "folder/missing", 420, 5, file_hash, modified, written),
                (3, 2, 1, "folder/wrong-size", 420, 8, file_hash, modified, written),
            ])
            db.execute("INSERT INTO jobs VALUES (1,4,3,?,?,?)", (
                "1970-01-01T00:00:03.000000004Z", "1970-01-01T00:00:02Z", state,
            ))
        indices = self.original / "captured_indices"
        indices.mkdir()
        self.index = indices / "SYNTHETIC.schema"
        self.index.write_text("""<ltfsindex><directory><name>root</name><contents>
            <directory><name>folder</name><contents>
                <file><name>sample</name><length>3</length></file>
                <file><name>wrong-size</name><length>9</length></file>
            </contents></directory>
        </contents></directory></ltfsindex>""")
        logs = self.original / "job-logs"
        logs.mkdir()
        self.log = b"synthetic history\ncreate tape success, tape_id= 1\n"
        (logs / "1.log").write_bytes(self.log)
        (self.bundle / "job.log").write_bytes(self.log)
        (self.bundle / "job.json").write_text(json.dumps({"created_at_ns": "3000000004"}))

        # Only the columns consumed by the audit are needed; expected rows are hand-authored.
        with contextlib.closing(sqlite3.connect(self.catalog)) as db, db:
            db.executescript("""
                CREATE TABLE files (id INTEGER PRIMARY KEY, parent_id, name, kind, created_at_ns, updated_at_ns);
                CREATE TABLE positions (id INTEGER PRIMARY KEY, media_id, path, parent_path, is_dir,
                    mode, size, hash, signature, mtime_ns, written_at_ns, checked_at_ns, health_job_id, storage_metadata);
                CREATE TABLE file_versions (file_id, signature, hash, size, mode, mtime_ns,
                    first_archived_at_ns, last_archived_at_ns);
                CREATE TABLE media (id INTEGER PRIMARY KEY, identity, name, created_at_ns,
                    destroyed_at_ns, capacity_bytes, written_bytes, profile);
                CREATE TABLE jobs (id INTEGER PRIMARY KEY, created_at_ns, updated_at_ns, revision);
                INSERT INTO files VALUES (1, 0, 'folder', 2, -1, -1), (2, 1, 'sample', 1, -1, -1);
                INSERT INTO jobs VALUES (1, 3000000004, 3000000004, 1);
            """)
            db.executemany("INSERT INTO positions VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?)", [
                (1, 1, "folder/sample", "folder/", 0, 420, 3, file_hash, signature,
                 -1, 1_000_000_002, 0, 0, b"synthetic extents"),
                (9, 1, "folder/", "", 1, 1 << 31, 3, None, None, -1, 1_000_000_002, 0, 0, None),
            ])
            db.execute("INSERT INTO file_versions VALUES (?,?,?,?,?,?,?,?)", (
                2, signature, file_hash, 3, 420, -1, None, None,
            ))
            db.execute("INSERT INTO media VALUES (1,'SYNTHETIC','Synthetic tape',0,NULL,100,3,?)", (
                b"\x0a\x09\x1a\x07ltfs_v1",
            ))
        with contextlib.closing(sqlite3.connect(self.state_db)) as db, db:
            db.executescript("""
                CREATE TABLE job (kind, status, priority);
                INSERT INTO job VALUES (1, 4, 3);
                CREATE TABLE items (id INTEGER PRIMARY KEY, size, target_path, media_path, status, media_id, data);
            """)
            db.execute("INSERT INTO items VALUES (1,3,'folder/sample','folder/sample',4,1,?)", (
                b"\x0a\x14source/folder/sample",
            ))

    def sql(self, path, statement, parameters=()):
        with contextlib.closing(sqlite3.connect(path)) as db, db:
            db.execute(statement, parameters)

    def test_report_is_silent_and_does_not_change_inputs(self):
        before = {path: path.read_bytes() for path in self.root.rglob("*") if path.is_file()}
        stdout, stderr = io.StringIO(), io.StringIO()
        with contextlib.redirect_stdout(stdout), contextlib.redirect_stderr(stderr):
            with mock.patch.object(audit.subprocess, "run") as run:
                result = audit.check(self.original, self.migrated)
                run.assert_not_called()
        self.assertEqual(result, {
            "independent": True, "staged": False, "files": 2, "positions": 1,
            "omitted_positions": [2, 3], "indexed_positions": 1, "versions": 1,
            "directories": 1, "media": 1,
            "jobs": [{"id": 1, "items": 1, "log_bytes": len(self.log)}],
            "coverage": {"completed_archive_jobs": 1, "restore_jobs": 0,
                         "other_job_kinds": 0, "other_job_states": 0},
        })
        self.assertEqual(stdout.getvalue() + stderr.getvalue(), "")
        self.assertEqual(before, {path: path.read_bytes() for path in self.root.rglob("*") if path.is_file()})

    def test_catalog_mismatches_are_detected(self):
        baseline = self.catalog.read_bytes()
        cases = [
            ("UPDATE files SET created_at_ns=0 WHERE id=2", "File times differ"),
            ("UPDATE positions SET id=7 WHERE id=1", "Position IDs differ"),
            ("UPDATE positions SET signature=X'00' WHERE id=1", "Position signature differs"),
            ("UPDATE file_versions SET mtime_ns=0", "FileVersion attributes differ"),
            ("UPDATE positions SET size=4 WHERE is_dir=1", "Directory summary differs"),
            ("UPDATE media SET written_bytes=99", "Media written bytes differ"),
            ("UPDATE jobs SET revision=2", "Job revision differs"),
        ]
        for statement, message in cases:
            with self.subTest(message=message):
                try:
                    self.sql(self.catalog, statement)
                    with self.assertRaisesRegex(AssertionError, message):
                        audit.check(self.original, self.migrated)
                finally:
                    self.catalog.write_bytes(baseline)

    def test_captured_index_is_independent_evidence(self):
        self.index.write_text(self.index.read_text().replace("<length>3</length>", "<length>4</length>"))
        with self.assertRaisesRegex(AssertionError, "Position IDs differ"):
            audit.check(self.original, self.migrated)

    def test_derived_directory_ids_may_change_after_import(self):
        self.sql(self.catalog, "UPDATE positions SET id=100 WHERE is_dir=1")
        self.assertEqual(audit.check(self.original, self.migrated)["directories"], 1)

    def test_staging_and_revision_override(self):
        for table in ("files", "positions", "file_versions", "media", "jobs"):
            self.sql(self.catalog, f"ALTER TABLE {table} RENAME TO {table}_staging")
        self.sql(self.catalog, "UPDATE jobs_staging SET revision=17")
        result = audit.check(str(self.original), str(self.migrated), staged=True, revisions={1: 17})
        self.assertTrue(result["staged"])
        self.assertEqual(result["jobs"][0]["items"], 1)

    def test_archive_items_are_checked_against_sources_and_positions(self):
        baseline = self.state_db.read_bytes()
        for statement, message in (
            ("UPDATE items SET target_path='changed'", "Archive item path or status differs"),
            ("UPDATE items SET data=X'0A0178'", "Archive item source differs"),
            ("UPDATE items SET media_id=2", "Archive item has no unique matching logged Position"),
        ):
            with self.subTest(message=message):
                try:
                    self.sql(self.state_db, statement)
                    with self.assertRaisesRegex(AssertionError, message):
                        audit.check(self.original, self.migrated)
                finally:
                    self.state_db.write_bytes(baseline)

    def test_archive_log_bytes_and_presence_must_match(self):
        log = self.bundle / "job.log"
        log.write_bytes(b"changed synthetic history\n")
        with self.assertRaisesRegex(AssertionError, "Job log differs"):
            audit.check(self.original, self.migrated)
        log.unlink()
        with self.assertRaisesRegex(AssertionError, "Job log presence differs"):
            audit.check(self.original, self.migrated)
        (self.original / "job-logs" / "1.log").unlink()
        with self.assertRaisesRegex(AssertionError, "no unique matching logged Position"):
            audit.check(self.original, self.migrated)

    def test_unsubmitted_source_cannot_be_hidden_by_submitted_output(self):
        for state in (b"\x0a\x1e\x12\x1c\x0a\x18\x0a\x06source\x12\x06folder\x12\x06sample\x10\x03",
                      b"\x0a\x20\x12\x1e\x0a\x18\x0a\x06source\x12\x06folder\x12\x06sample\x10\x03\x18\x02"):
            with self.subTest(state=state):
                self.sql(self.original / "tapes.db", "UPDATE jobs SET state=? WHERE id=1", (state,))
                with self.assertRaisesRegex(NotImplementedError, "Only submitted inline Archive"):
                    audit.check(self.original, self.migrated)

    def test_transitional_manifest_is_not_treated_as_an_empty_inline_job(self):
        directory = self.original / "jobs" / "1"
        directory.mkdir(parents=True)
        (directory / "state.db").write_bytes(b"synthetic transitional manifest")
        self.sql(self.original / "tapes.db", "UPDATE jobs SET state=X'0A00' WHERE id=1")
        self.sql(self.state_db, "DELETE FROM items")
        with self.assertRaisesRegex(NotImplementedError, "Transitional Job manifests"):
            audit.check(self.original, self.migrated)

    def test_matching_content_on_another_media_does_not_establish_job_ownership(self):
        self.sql(self.original / "tapes.db", """INSERT INTO tapes
            SELECT 2,'SECOND',name,create_time,destroy_time,capacity_bytes,writen_bytes,encryption FROM tapes WHERE id=1""")
        self.sql(self.original / "tapes.db", """INSERT INTO positions
            SELECT 4,file_id,2,path,mode,size,hash,mod_time,write_time FROM positions WHERE id=1""")
        (self.index.parent / "SECOND.schema").write_bytes(self.index.read_bytes())
        self.sql(self.catalog, """INSERT INTO media
            SELECT 2,'SECOND',name,created_at_ns,destroyed_at_ns,capacity_bytes,written_bytes,profile FROM media WHERE id=1""")
        for source, target in ((1, 4), (9, 10)):
            self.sql(self.catalog, """INSERT INTO positions
                SELECT ?,2,path,parent_path,is_dir,mode,size,hash,signature,mtime_ns,written_at_ns,
                    checked_at_ns,health_job_id,storage_metadata FROM positions WHERE id=?""", (target, source))
        self.assertEqual(audit.check(self.original, self.migrated)["media"], 2)
        self.sql(self.state_db, "UPDATE items SET media_id=2")
        with self.assertRaisesRegex(AssertionError, "no unique matching logged Position"):
            audit.check(self.original, self.migrated)
        log = self.log + b"create tape success, tape_id= 2\n"
        (self.original / "job-logs" / "1.log").write_bytes(log)
        (self.bundle / "job.log").write_bytes(log)
        with self.assertRaisesRegex(AssertionError, "no unique matching logged Position"):
            audit.check(self.original, self.migrated)

    def test_unsupported_job_kinds_are_explicit(self):
        for state in (b"\x12\x00", b"\x1a\x00", b""):
            with self.subTest(state=state):
                self.sql(self.original / "tapes.db", "UPDATE jobs SET state=? WHERE id=1", (state,))
                with self.assertRaisesRegex(NotImplementedError, "Unsupported legacy Job kind.*1.*only completed Archive"):
                    audit.check(self.original, self.migrated)

    def test_unsupported_bundle_kinds_and_states_precede_item_assumptions(self):
        # A missing Archive items table must not obscure an unsupported Job boundary.
        self.sql(self.state_db, "DROP TABLE items")
        for kind, status in ((2, 4), (3, 4), (1, 1), (1, 2), (1, 5)):
            with self.subTest(kind=kind, status=status):
                self.sql(self.state_db, "UPDATE job SET kind=?,status=?", (kind, status))
                with self.assertRaisesRegex(NotImplementedError, "Unsupported Job kind/state.*only completed Archive"):
                    audit.check(self.original, self.migrated)

    def test_unfinished_legacy_jobs_cannot_be_hidden_by_completed_output(self):
        # Frozen JobStatus values: JOB_DRAFT, NOT_READY, JOB_PENDING, PROCESSING, JOB_FAILED.
        # The migrated bundle still says COMPLETED for every case.
        for status in (0, 1, 2, 3, 127):
            with self.subTest(status=status):
                self.sql(self.original / "tapes.db", "UPDATE jobs SET status=? WHERE id=1", (status,))
                with self.assertRaisesRegex(NotImplementedError, "Unsupported legacy Job state.*only completed Archive"):
                    audit.check(self.original, self.migrated)

    def test_committed_legacy_wal_is_read(self):
        with contextlib.closing(sqlite3.connect(self.original / "tapes.db")) as db:
            db.execute("PRAGMA journal_mode=WAL")
            db.execute("PRAGMA wal_autocheckpoint=0")
            db.execute("UPDATE jobs SET priority=5 WHERE id=1")
            db.commit()
            self.assertGreater((self.original / "tapes.db-wal").stat().st_size, 0)
            self.sql(self.state_db, "UPDATE job SET priority=5")
            self.assertEqual(audit.check(self.original, self.migrated)["jobs"][0]["id"], 1)

    def test_connection_is_read_only_and_closes_on_failure(self):
        with self.assertRaisesRegex(sqlite3.OperationalError, "readonly"):
            with audit._connect(self.original / "tapes.db") as db:
                db.execute("DELETE FROM jobs")
        with self.assertRaisesRegex(sqlite3.ProgrammingError, "closed"):
            db.execute("SELECT * FROM jobs")


if __name__ == "__main__":
    unittest.main()
