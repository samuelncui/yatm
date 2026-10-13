"""Local parser and orchestration safety tests; never hardware acceptance."""

import base64
import copy
from contextlib import closing
import hashlib
import json
from pathlib import Path
import sqlite3
import struct
import tempfile
from types import SimpleNamespace
import unittest
from unittest.mock import Mock, patch

from package_physical_cases import PhysicalCases, baseline_files, run_stage
from package_physical_evidence import (check_positions, check_restore_database,
                                       check_signature, format_partition_map, library_positions, parse_index, relative_path,
                                       seal_evidence, verify_evidence)


def encoded(data):
    return base64.b64encode(data).decode()


class EvidenceTests(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory()
        self.addCleanup(self.temporary.cleanup)
        self.directory = Path(self.temporary.name)
        self.digest = hashlib.sha256(b"abc").digest()
        self.extent = {"partition": "b", "startBlock": "4", "byteOffset": "7", "byteCount": "3"}
        self.order = b"b" + struct.pack(">QQ", 4, 7)
        self.position = {"media_id": 9, "path": "dataset/a.bin", "size": 3, "hash": encoded(self.digest),
                         "storage_order": encoded(self.order), "storage_metadata": {"ltfs": {"extents": [self.extent]}}}
        self.item = {"status": "COPY_STATUS_SUBMITTED", "media_id": "9", "size_bytes": "3",
                     "file": {"media_path": "dataset/a.bin", "target_path": "dataset/a.bin",
                              "expected": {"sha256": encoded(self.digest)}}}
        self.positions = {"dataset/a.bin": self.position}
        self.index = parse_index(b'''<ltfsindex><directory><name>root</name><contents>
          <directory><name>dataset</name><contents><file><name>a.bin</name><length>3</length>
          <extentinfo><extent><partition>b</partition><startblock>4</startblock><byteoffset>7</byteoffset>
          <bytecount>3</bytecount><fileoffset>0</fileoffset></extent></extentinfo></file>
          <file><name>empty</name><length>0</length></file></contents></directory>
          </contents></directory></ltfsindex>''')

    def test_index_and_export_preserve_extent_order_and_zero_file(self):
        records = [{"type": "header", "format": "yatm-library-backup"},
                   {"type": "position", "data": self.position},
                   {"type": "position", "data": {"media_id": 10, "path": "foreign"}}, {"type": "end"}]
        positions = library_positions("\n".join(map(json.dumps, records)), "9")
        self.assertEqual(positions, self.positions)
        check_positions([self.item], positions, self.index, "9")
        empty = {"status": "COPY_STATUS_SUBMITTED", "media_id": "9", "size_bytes": "0",
                 "file": {"media_path": "dataset/empty", "expected": {"sha256": encoded(hashlib.sha256(b"").digest())}}}
        positions["dataset/empty"] = {"hash": empty["file"]["expected"]["sha256"]}
        check_positions([empty], positions, self.index, "9")

    def test_size_only_policy_checks_non_text_small_files(self):
        mapping = {"index": "a", "data": "b"}
        with self.assertRaisesRegex(RuntimeError, "placement"):
            check_positions([self.item], self.positions, self.index, "9", mapping)
        self.position["storage_metadata"]["ltfs"]["extents"][0]["partition"] = "a"
        self.position["storage_order"] = encoded(b"a" + self.order[1:])
        self.index["dataset/a.bin"]["extents"][0]["partition"] = "a"
        check_positions([self.item], self.positions, self.index, "9", mapping)

    def test_size_only_policy_keeps_large_files_on_data(self):
        size = 2 * 1024**2
        self.item["size_bytes"] = str(size)
        self.position["size"] = size
        self.position["storage_metadata"]["ltfs"]["extents"][0]["byteCount"] = str(size)
        self.index["dataset/a.bin"]["size"] = size
        self.index["dataset/a.bin"]["extents"][0]["byte_count"] = size
        mapping = {"index": "a", "data": "b"}
        check_positions([self.item], self.positions, self.index, "9", mapping)
        with self.assertRaisesRegex(RuntimeError, "placement"):
            check_positions([self.item], self.positions, self.index, "9", {"index": "b", "data": "a"})

    def test_partition_roles_come_from_format_log_not_fixture_placement(self):
        log = "LTFS15010I Creating data partition a on SCSI partition 0.\nLTFS15011I Creating index partition b on SCSI partition 1.\n"
        self.assertEqual(format_partition_map(log), {"data": "a", "index": "b"})
        for bad in ("", log.splitlines()[0], log + "LTFS15010I Creating data partition c on SCSI partition 2.\n",
                    log.replace("index partition b", "index partition a"),
                    log.replace("SCSI partition 1", "SCSI partition 0")):
            with self.subTest(log=bad), self.assertRaises(RuntimeError):
                format_partition_map(bad)

    def test_rejects_missing_export_tail_duplicate_and_unsafe_paths(self):
        with self.assertRaisesRegex(RuntimeError, "incomplete"):
            library_positions('{"type":"header","format":"yatm-library-backup"}', "9")
        for path in ("../secret", "/etc/passwd", "x/../../y", "", "x\x00y"):
            with self.subTest(path=path), self.assertRaises(RuntimeError):
                relative_path(path)
        xml = b'<ltfsindex><directory><contents><file><name>..</name><length>0</length></file></contents></directory></ltfsindex>'
        with self.assertRaisesRegex(RuntimeError, "name"):
            parse_index(xml)

    def test_rejects_extent_mismatch_gap_wrong_order_and_placement(self):
        for mutate in (lambda p: p.update(storage_order=encoded(b"a" + self.order[1:])),
                       lambda p: p["storage_metadata"]["ltfs"]["extents"][0].update(byteCount="2")):
            changed = copy.deepcopy(self.positions)
            mutate(changed["dataset/a.bin"])
            with self.assertRaises(RuntimeError):
                check_positions([self.item], changed, self.index, "9")
        with self.assertRaisesRegex(RuntimeError, "placement"):
            check_positions([self.item], self.positions, self.index, "9", {"index": "a", "data": "b"})
        with self.assertRaisesRegex(RuntimeError, "write order"):
            check_positions([self.item, self.item], self.positions, self.index, "9")
        changed = copy.deepcopy(self.positions)
        changed["dataset/a.bin"]["storage_metadata"]["ltfs"]["extents"][0]["fileOffsetBytes"] = "1"
        index = copy.deepcopy(self.index)
        index["dataset/a.bin"]["extents"][0]["file_offset_bytes"] = 1
        with self.assertRaisesRegex(RuntimeError, "gap"):
            check_positions([self.item], changed, index, "9")

    def test_restore_copy_reads_local_snapshot_without_mutation(self):
        path = self.directory / "state.db"
        with closing(sqlite3.connect(path)) as db:
            db.execute("CREATE TABLE copies(id INTEGER, media_id INTEGER, media_path TEXT, storage_order BLOB)")
            db.execute("INSERT INTO copies VALUES(1, 9, ?, ?)", ("dataset/a.bin", self.order))
            db.commit()
        before = path.read_bytes()
        self.assertEqual(check_restore_database(path, self.positions, ["dataset/a.bin"], "9"), ["dataset/a.bin"])
        self.assertEqual(path.read_bytes(), before)
        with self.assertRaisesRegex(RuntimeError, "selection"):
            check_restore_database(path, self.positions, ["missing"], "9")

    def test_signature_and_evidence_checksums_fail_closed(self):
        signature = b"ACPS\x01\x01\x00\x00" + struct.pack(">qq", 3, 0) + self.digest
        check_signature(signature, 3, self.digest.hex())
        for invalid in (signature[:-1], b"WRNG" + signature[4:], signature[:24] + b"x" * 32):
            with self.assertRaises(RuntimeError):
                check_signature(invalid, 3, self.digest.hex())
        (self.directory / "report.json").write_text("{}")
        seal_evidence(self.directory)
        verify_evidence(self.directory)
        (self.directory / "report.json").write_text("changed")
        with self.assertRaisesRegex(RuntimeError, "checksum"):
            verify_evidence(self.directory)


class StageSafetyTests(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory()
        self.addCleanup(self.temporary.cleanup)
        self.test = Mock()
        self.test.args = SimpleNamespace(physical_stage="baseline", physical_device="/dev/nst0",
                                         physical_barcode="ABC001", out=Path(self.temporary.name),
                                         syscall_trace=None, no_syscall_trace=True, require_sg_trace=False)
        self.test.root = "/isolated/run"
        self.test.install = "/isolated/run/install"
        self.state = {"partition_map": {"index": "a", "data": "b"}}
        self.save = Mock()
        self.cases = PhysicalCases(self.test, self.state, self.save)

    def test_initial_empty_barcode_checks_assigned_registration_before_format(self):
        self.state.update(prepare_complete=True, format_job_id="44")
        self.test.one.side_effect = [{"identity": ""}, {"identity": "ABC001"}]
        # Stop at the write boundary: the test never formats or accesses a device.
        self.cases.format = Mock(side_effect=RuntimeError("format boundary reached"))
        with self.assertRaisesRegex(RuntimeError, "format boundary reached"):
            self.cases.baseline()
        self.test.one.assert_called_with("media", "inspect", "tape", "--device", "/dev/nst0",
                                         "--identity", "ABC001")
        self.cases.format.assert_called_once_with({"id": "44"}, "ABC001")
        self.assertTrue(self.state["baseline_started"])

    def test_initial_empty_barcode_still_rejects_registered_media_or_probe_failure(self):
        self.state.update(prepare_complete=True, format_job_id="44")
        self.cases.format = Mock()
        for reply in ({"identity": "ABC001", "media": {"id": "8"}}, RuntimeError("probe failed")):
            with self.subTest(reply=reply):
                self.test.one.side_effect = [{"identity": ""}, reply]
                with self.assertRaises(RuntimeError):
                    self.cases.baseline()
        self.cases.format.assert_not_called()
        self.save.assert_not_called()

    def test_reload_requires_the_written_barcode_without_manual_fallback(self):
        self.test.one.return_value = {"identity": ""}
        with self.assertRaisesRegex(RuntimeError, "different barcode"):
            self.cases.inspect()
        self.test.one.assert_called_once_with("media", "inspect", "tape", "--device", "/dev/nst0")

    def test_completed_restore_never_rereads_or_runs_again(self):
        self.state["restore_complete"] = True
        self.cases.restore_selection("restore_job_id", [], "baseline", "restore_complete")
        self.test.one.assert_not_called()
        self.test.remote.assert_not_called()

    def test_stream_size_above_32_bits_hashes_inline(self):
        size = 64 * 1024**3
        self.test.remote.side_effect = [SimpleNamespace(stdout=b"a" * 64 + b"  -\n"), SimpleNamespace(stdout=str(size).encode())]
        manifest = self.cases.stream_fixture("/isolated/run/fixtures/source/capacity/chunk-000.bin", size, 1000000)
        self.assertEqual(manifest, {"size": size, "stream": 1000000, "sha256": "a" * 64})
        command = self.test.remote.call_args_list[0].args[0]
        self.assertIn(str(size // 1024**2), command)
        self.assertIn("tee -- \"$1\" | sha256sum", command[2])
        self.assertEqual(self.test.remote.call_count, 2)

    def test_existing_restore_never_submits_a_second_media_attempt(self):
        self.state.update(restore_job_id="44", restore_job_id_submitted=True)
        self.cases.check_media = Mock()
        self.cases.settled = Mock(side_effect=RuntimeError("failed attempt"))
        with self.assertRaisesRegex(RuntimeError, "failed attempt"):
            self.cases.restore_selection("restore_job_id", [], "baseline", "restore_complete")
        self.test.cli.assert_not_called()
        self.test.one.assert_not_called()

    def test_stream_fixture_cannot_escape_owned_root(self):
        for path in ("/outside/file", self.test.root + "/../outside"):
            with self.subTest(path=path), self.assertRaises(RuntimeError):
                self.cases.stream_fixture(path, 1024**2, 0)
        self.test.remote.assert_not_called()

    def test_cli_barcode_refusal_requires_exact_observable_safety_error(self):
        self.test.url = "http://127.0.0.1:1234"
        job = {"id": "44", "status": "JOB_STATUS_READY"}
        self.test.one.return_value = {"job": job}
        self.test.remote.return_value = SimpleNamespace(stderr=json.dumps({"code": "safety", "error":
            'Tape barcode does not match the inspected identity, requested="BAD999" inspected="ABC001"'}).encode())
        self.cases.inspect = Mock(return_value={"identity": "ABC001", "media": {"id": "9"}})
        self.cases.archive_items = Mock(return_value=[{"id": "1", "status": "COPY_STATUS_PENDING"}])
        self.cases.media = Mock(return_value={"id": "9", "identity": "ABC001"})
        self.cases.barcode_refusal(job)
        self.test.cli.assert_not_called()
        self.assertEqual(self.test.remote.call_args_list[0].kwargs["expected"], 2)
        self.test.remote.return_value = SimpleNamespace(stderr=b'{"code":"usage","error":"bad flag"}')
        with self.assertRaisesRegex(RuntimeError, "identity refusal"):
            self.cases.barcode_refusal(job)

    def test_barcode_refusal_rejects_active_job_and_changed_media(self):
        job = {"id": "44", "status": "JOB_STATUS_READY", "phase": "JOB_PHASE_QUEUED"}
        self.test.one.return_value = {"job": job}
        with self.assertRaisesRegex(RuntimeError, "idle prepared"):
            self.cases.barcode_refusal(job)
        self.test.remote.assert_not_called()
        job.pop("phase")
        self.test.url = "http://127.0.0.1:1234"
        self.cases.archive_items = Mock(return_value=[{"id": "1", "status": "COPY_STATUS_PENDING"}])
        self.cases.media = Mock(side_effect=[{"id": "9"}, {"id": "10"}])
        self.test.remote.return_value = SimpleNamespace(stderr=json.dumps({"code": "safety", "error":
            'Tape barcode does not match the inspected identity, requested="BAD999" inspected="ABC001"'}).encode())
        with self.assertRaisesRegex(RuntimeError, "manifest or durable Media"):
            self.cases.barcode_refusal(job)

    def test_preview_uses_direct_settings_and_verifies_installed_image_support(self):
        settings = {"generators": [{"image": {"format": "png"}}], "concurrency": 2}
        updated = {**settings, "enabled": True, "command": self.test.install + "/yatm-preview"}
        self.test.one.side_effect = [settings, updated, {"available": True, "kinds": ["image", "video"]}]
        self.cases.configure_preview()
        sent = json.loads(self.test.write.call_args.args[1])
        self.assertNotIn("settings", sent)
        self.assertEqual(sent, updated)
        self.test.one.side_effect = [{"settings": settings}]
        with self.assertRaisesRegex(RuntimeError, "direct PreviewSettings"):
            self.cases.configure_preview()

    def test_prepared_manifest_checks_source_ids_and_pending_state_before_write(self):
        self.cases.source = "7"
        self.state["fixture_manifest"] = {"dataset/a.bin": {"size": 3, "sha256": hashlib.sha256(b"abc").hexdigest()}}
        good = {"status": "COPY_STATUS_PENDING", "size_bytes": "3", "file": {
            "target_path": "Unforged/Archive/dataset/a.bin", "source_path": self.test.root + "/fixtures/source/dataset/a.bin",
            "expected": {"file_id": "8", "original_location_id": "7", "sha256": encoded(hashlib.sha256(b"abc").digest())}}}
        self.cases.archive_items = Mock(return_value=[good])
        self.cases.check_prepared({"id": "44"}, "dataset")
        mutations = [lambda i: i["file"].update(source_path="/outside/a.bin"),
                     lambda i: i["file"]["expected"].update(original_location_id="99"),
                     lambda i: i["file"]["expected"].update(file_id="0"),
                     lambda i: i.update(status="COPY_STATUS_SUBMITTED"),
                     lambda i: i.update(media_id="9"),
                     lambda i: i["file"]["expected"].update(sha256=encoded(b"x" * 32))]
        for mutate in mutations:
            changed = copy.deepcopy(good)
            mutate(changed)
            self.cases.archive_items.return_value = [changed]
            with self.assertRaises(RuntimeError):
                self.cases.check_prepared({"id": "44"}, "dataset")
        self.test.remote.assert_not_called()
        self.test.cli.assert_not_called()

    def test_baseline_after_append_cannot_inspect_or_format(self):
        for key in ("append_job_id", "baseline_complete"):
            with self.subTest(key=key):
                state = {**self.state, key: "1"}
                with self.assertRaisesRegex(RuntimeError, "without replaying writes"):
                    PhysicalCases(self.test, state, self.save).baseline()
        self.test.one.assert_not_called()
        self.test.cli.assert_not_called()

    def test_existing_media_and_wrong_barcode_block_before_baseline_mutation(self):
        self.state.update(prepare_complete=True, format_job_id="44")
        for reply in ({"identity": "ABC001", "media": {"id": "8"}}, {"identity": "DEF002"}):
            self.test.one.return_value = reply
            with self.assertRaises(RuntimeError):
                self.cases.baseline()
        self.test.write.assert_not_called()
        self.test.cli.assert_not_called()
        self.save.assert_not_called()

    def test_resumed_format_failure_never_reloads_or_formats_again(self):
        self.state.update(prepare_complete=True, format_job_id="44", baseline_started=True)
        self.cases.settled = Mock(side_effect=RuntimeError("FORMAT did not complete"))
        self.cases.format = Mock()
        self.cases.inspect = Mock()
        with self.assertRaisesRegex(RuntimeError, "FORMAT did not complete"):
            self.cases.baseline()
        self.cases.settled.assert_called_once_with({"id": "44"})
        self.cases.format.assert_not_called()
        self.cases.inspect.assert_not_called()
        self.test.cli.assert_not_called()

    def test_completed_format_reaches_evidence_without_repeating_physical_write(self):
        encryption = "test-key"
        self.state.update(prepare_complete=True, format_job_id="44", baseline_started=True,
                          media_id="9", encryption_sha256=hashlib.sha256(encryption.encode()).hexdigest())
        self.cases.settled = Mock()
        self.cases.ejected = Mock()
        self.cases.format = Mock()
        self.cases.inspect = Mock()
        self.cases.media = Mock(return_value={"id": "9", "profile": {"tape": {"encryption": encryption}}})
        self.cases.archive_items = Mock(return_value=[{"id": "1"}])
        self.cases.check_fixture = Mock()
        self.cases.export = Mock(return_value={"dataset/a.bin": {}})
        self.cases.index = Mock(side_effect=RuntimeError("stop at evidence boundary"))
        with self.assertRaisesRegex(RuntimeError, "stop at evidence boundary"):
            self.cases.baseline()
        self.cases.index.assert_called_once_with({"id": "44"})
        self.cases.settled.assert_called_once_with({"id": "44"})
        self.cases.ejected.assert_called_once_with()
        self.cases.format.assert_not_called()
        self.cases.inspect.assert_not_called()
        self.test.cli.assert_not_called()
        self.test.remote.assert_not_called()

        self.state["encryption_sha256"] = "different"
        with self.assertRaisesRegex(RuntimeError, "different Media or encryption profile"):
            self.cases.baseline()
        self.cases.index.assert_called_once()

    def test_unprepared_baseline_does_not_touch_device(self):
        with self.assertRaisesRegex(RuntimeError, "nonphysical preparation"):
            self.cases.baseline()
        self.test.one.assert_not_called()
        self.test.cli.assert_not_called()

    def test_prepare_creates_ready_jobs_without_device_operations(self):
        def one(*args):
            if args[:2] == ("job", "list"):
                return {}
            if args[:2] == ("location", "create"):
                return {"location": {"id": "1" if args[3] == "Physical source" else "2"}}
            if args[:2] == ("location", "get"):
                name = "source" if args[2] == "1" else "restored"
                return {"location": {"id": args[2], "root_path": self.test.root + "/fixtures/" + name},
                        "accessibility": {"accessible": True}}
            if args[:2] == ("archive", "create"):
                return {"job": {"id": "44"}}
            if args[:2] == ("job", "progress"):
                return {"preview_job_id": "45"}
            self.fail("Unexpected preparation command: " + repr(args))

        self.test.one.side_effect = one
        self.test.remote.side_effect = lambda argv: self.assertEqual(argv[0], "mkdir")
        self.cases.configure_preview = Mock()
        self.cases.settled = Mock()
        self.cases.check_prepared = Mock()
        self.cases.capture_job = Mock()
        self.cases.prepare()
        self.assertTrue(self.state["prepare_complete"])
        self.assertEqual(self.state["format_job_id"], "44")
        self.assertEqual(self.state["preview_job_id"], "45")
        self.assertNotIn("baseline_started", self.state)
        self.assertNotIn("media_id", self.state)
        self.test.cli.assert_not_called()
        self.cases.check_prepared.assert_called_once_with({"id": "44"}, "dataset")
        self.cases.capture_job.assert_called_once_with({"id": "45"})

    def test_identity_change_and_uninitialized_resume_fail_closed(self):
        self.test.args.physical_stage = "restore"
        for state in ({}, {"physical_device": "/dev/nst1"}):
            with self.assertRaises(RuntimeError):
                run_stage(self.test, state, self.save)
        self.test.one.assert_not_called()

    def test_created_job_is_saved_before_any_later_operation(self):
        self.test.one.return_value = {"job": {"id": "123"}}
        self.save.side_effect = lambda: self.assertEqual(self.state["format_job_id"], "123")
        self.cases.created("format_job_id", "archive", "create")
        self.save.assert_called_once()
        with self.assertRaisesRegex(RuntimeError, "already exists"):
            self.cases.created("format_job_id", "archive", "create")
        self.test.one.assert_called_once()

    def test_capture_job_preserves_log_bytes_across_text_page_boundaries(self):
        # A CLI byte page would split the first UTF-8 character; JSON cannot preserve it.
        log = b"x" * (4 * 1024**2 - 1) + "归档完成\n".encode() + b"YWJj\n\xff"
        root = self.test.install + "/work/jobs/44"
        files = {self.test.root + "/tmp/physical-job-44.db": b"database snapshot",
                 root + "/job.json": b'{"id":"44"}', root + "/job.log": log,
                 root + "/tapes/ABC001/sg-trace.jsonl": b'{"event":"init"}\n{"raw":"ff"}\n\xff',
                 root + "/tapes/ABC001/sg-trace.jsonl.error": b"observer failed\n\xff",
                 root + "/tapes/ABC001/sg-trace.jsonl.backup": b"not selected"}

        def remote(argv):
            if argv[0] == "sha256sum":
                return SimpleNamespace(stdout=(hashlib.sha256(files[argv[-1]]).hexdigest() + "  file\n").encode())
            if argv[0] == "cat":
                return SimpleNamespace(stdout=files[argv[-1]])
            if argv[0] == "find":
                return SimpleNamespace(stdout=b"\x00".join(path.encode() for path in files if "/tapes/" in path) + b"\x00")
            self.assertIn(argv[0], ("sqlite3", "test"))
            return SimpleNamespace(stdout=b"")

        self.test.one.return_value = {"job": {"id": "44", "status": "JOB_STATUS_COMPLETED"}}
        self.test.remote.side_effect = remote
        directory = self.cases.capture_job({"id": "44"})
        self.assertEqual((directory / "job.log").read_bytes(), log)
        self.test.one.assert_called_once_with("job", "get", "44")
        for name in ("sg-trace.jsonl", "sg-trace.jsonl.error"):
            self.assertEqual((directory / "tapes/ABC001" / name).read_bytes(), files[root + "/tapes/ABC001/" + name])
        self.assertFalse((directory / "tapes/ABC001/sg-trace.jsonl.backup").exists())
        seal_evidence(self.cases.evidence)
        verify_evidence(self.cases.evidence)
        self.cases.tape_evidence("44", verify=True)
        (directory / "tapes/ABC001/sg-trace.jsonl.error").unlink()
        with self.assertRaisesRegex(RuntimeError, "Live Tape evidence is missing"):
            self.cases.tape_evidence("44", verify=True)

    def test_capture_failure_prevents_all_cleanup_deletes(self):
        self.state.update(restore_complete=True, completed_stages=["prepare", "baseline", "restore"])
        self.test.one.side_effect = [{"jobs": [{"id": "1", "status": "JOB_STATUS_COMPLETED"},
                                               {"id": "2", "status": "JOB_STATUS_READY"}]}, {"devices": ["/dev/nst0"]}]
        self.cases.ejected = Mock()
        self.cases.export = Mock()
        self.cases.capture_job = Mock(side_effect=RuntimeError("snapshot failed"))
        with self.assertRaisesRegex(RuntimeError, "snapshot failed"):
            self.cases.cleanup()
        self.test.cli.assert_not_called()
        self.assertNotIn("evidence_complete", self.state)

    def cleanup_fixture(self, capacity=False):
        self.state.update(restore_complete=True, completed_stages=["prepare", "baseline", "restore"])
        if capacity:
            self.state.update(performance=True, capacity_write_observed=True, capacity_device_released=True,
                              capacity_restore_complete=True, capacity_verify_complete=True,
                              completed_stages=["performance-prepare", "performance-format", "capacity-verify"])
        jobs = [{"id": "1", "status": "JOB_STATUS_READY", "error": "capacity exhausted"},
                {"id": "2", "status": "JOB_STATUS_COMPLETED"}]
        def one(*args):
            if args[:2] == ("job", "list"):
                return {"jobs": jobs}
            if args[:3] == ("tape", "device", "list"):
                return {"devices": ["/dev/nst0"]}
            if args[:2] == ("job", "get"):
                return {"job": next(job for job in jobs if job["id"] == args[2])}
            self.fail("Unexpected cleanup request: " + repr(args))
        self.test.one.side_effect = one
        self.cases.export = Mock(side_effect=lambda name: self.cases.keep(name, b"library backup"))
        def capture(job):
            self.cases.keep_json("jobs/" + job["id"] + "/catalog.json", job)
            self.cases.keep("jobs/" + job["id"] + "/state.db", b"snapshot")
        self.cases.capture_job = Mock(side_effect=capture)
        self.cases.ejected = Mock()
        self.cases.tape_evidence = Mock()
        self.state.update(commit="a" * 40, version="v1.0.0-alpha.2", remote_root=self.test.root)
        self.cases.keep("source/packages.sha256", ("b" * 64 + "  main.tar.gz\n").encode())
        self.cases.keep("source/controller.sha256", ("c" * 64 + "  e2e/physical_package_acceptance.py\n").encode())
        stages = (["performance-prepare", "performance-format", "capacity-write", "capacity-verify"] if capacity
                  else ["prepare", "baseline", "restore"])
        for stage in stages:
            status = "failed" if stage == "capacity-write" else "passed"
            self.cases.keep_json("stages/" + stage + "/report.json", {
                "commit": self.state["commit"], "version": self.state["version"], "remote_root": self.test.root,
                "archive": {"path": "main.tar.gz", "sha256": "b" * 64}, "duration_seconds": 123.5,
                "status": status, "cases": [{"name": "physical-" + stage, "status": status}]})
        return jobs

    def test_cleanup_seals_and_verifies_both_current_sequences(self):
        for capacity in (False, True):
            with self.subTest(capacity=capacity):
                self.state.clear()
                self.test.reset_mock()
                jobs = self.cleanup_fixture(capacity)
                def delete(*args):
                    self.assertEqual(args[:2], ("job", "delete"))
                    verify_evidence(self.cases.evidence)
                    self.assertTrue(self.state["evidence_complete"])
                self.test.cli.side_effect = delete
                with patch.object(self.cases, "cleanup_evidence", wraps=self.cases.cleanup_evidence) as prerequisites, \
                        patch.object(self.cases, "syscall_evidence", wraps=self.cases.syscall_evidence) as observers:
                    self.cases.cleanup()
                prerequisites.assert_called_once_with()
                observers.assert_called_once_with()
                self.assertTrue(self.state["cleanup_complete"])
                self.assertEqual(self.state["deleted_job_ids"], [job["id"] for job in jobs])
                self.assertEqual(self.test.cli.call_count, 2)
                self.assertTrue(all(call.args[0][:3] == ["test", "!", "-e"]
                                    for call in self.test.remote.call_args_list))
                self.test.check_cleanup_paths.assert_called_once()
                self.cases.ejected.assert_called_once()

    def seal_without_deleting(self):
        jobs = self.cleanup_fixture(capacity=True)
        self.test.cli.side_effect = RuntimeError("delete interrupted")
        with self.assertRaisesRegex(RuntimeError, "delete interrupted"):
            self.cases.cleanup()
        self.test.cli.reset_mock(side_effect=True)
        return jobs

    def test_cleanup_rejects_changed_evidence_and_manifest_before_deletion(self):
        self.seal_without_deleting()
        snapshot = self.cases.evidence / "jobs/1/state.db"
        snapshot.write_bytes(b"changed")
        with self.assertRaisesRegex(RuntimeError, "checksum"):
            self.cases.cleanup()
        self.test.cli.assert_not_called()
        seal_evidence(self.cases.evidence)
        with self.assertRaisesRegex(RuntimeError, "manifest changed"):
            self.cases.cleanup()
        self.test.cli.assert_not_called()

    def test_cleanup_rechecks_live_catalog_after_sealing(self):
        jobs = self.seal_without_deleting()
        jobs[0]["phase"] = "JOB_PHASE_COPYING_TO_MEDIA"
        with self.assertRaisesRegex(RuntimeError, "active or uncertain"):
            self.cases.cleanup()
        jobs[0].pop("phase")
        jobs[0]["error"] = "another attempt"
        with self.assertRaisesRegex(RuntimeError, "changed after evidence"):
            self.cases.cleanup()
        jobs.append({"id": "3", "status": "JOB_STATUS_COMPLETED"})
        with self.assertRaisesRegex(RuntimeError, "catalog changed"):
            self.cases.cleanup()
        self.test.cli.assert_not_called()

    def test_cleanup_resumes_only_remaining_preserved_jobs(self):
        jobs = self.seal_without_deleting()
        jobs.pop(0)
        self.state["deleted_job_ids"] = ["1"]
        self.cases.capture_job.reset_mock()
        self.cases.cleanup()
        self.test.cli.assert_called_once_with("job", "delete", "2")
        self.cases.capture_job.assert_not_called()

    def test_cleanup_rejects_incomplete_readback_uncertain_device_and_mounts(self):
        self.cleanup_fixture(capacity=True)
        for key in ("capacity_write_observed", "capacity_device_released", "capacity_restore_complete", "capacity_verify_complete"):
            self.state[key] = False
            with self.subTest(key=key), self.assertRaisesRegex(RuntimeError, "completed baseline or capacity readback"):
                self.cases.cleanup()
            self.state[key] = True
        self.test.one.assert_not_called()
        self.cases.ejected = Mock(wraps=PhysicalCases.ejected.__get__(self.cases))
        self.test.one.side_effect = [{"jobs": []}, {"devices": []}]
        with self.assertRaisesRegex(RuntimeError, "owner remains uncertain"):
            self.cases.cleanup()
        self.cases.ejected.assert_called_once_with()
        self.test.remote.assert_not_called()
        self.cleanup_fixture(capacity=True)
        self.test.check_cleanup_paths.side_effect = RuntimeError("owned mount remains")
        with self.assertRaisesRegex(RuntimeError, "owned mount remains"):
            self.cases.cleanup()
        self.cases.capture_job.assert_not_called()
        self.test.cli.assert_not_called()

    def syscall_files(self, files, active=False, corrupt=False):
        self.test.args.no_syscall_trace = False
        self.test.args.syscall_trace = list(files)
        def remote(argv, **kwargs):
            if argv[0] == "fuser":
                return SimpleNamespace(returncode=0 if active else 1, stdout=b"", stderr=b"")
            if argv[0] == "sha256sum":
                return SimpleNamespace(stdout=(hashlib.sha256(files[argv[-1]]).hexdigest() + "  file\n").encode())
            if argv[0] == "cat":
                return SimpleNamespace(stdout=b"truncated" if corrupt else files[argv[-1]])
            self.assertEqual(argv[0], "test")
            return SimpleNamespace(stdout=b"")
        self.test.remote.side_effect = remote

    def test_cleanup_preserves_supplied_inside_and_outside_syscall_evidence(self):
        self.cleanup_fixture(capacity=True)
        files = {self.test.root + "/syscall.trace": b"write(3, ...) = 10\n\xff",
                 "/preserved/syscall.trace.123": b"statfs(...) = 0\n"}
        self.syscall_files(files)
        self.cases.cleanup()
        for source, content in files.items():
            self.assertEqual((self.cases.evidence / "observers/syscall" / Path(source).name).read_bytes(), content)
        verify_evidence(self.cases.evidence)
        self.assertEqual(self.test.cli.call_count, 2)
        self.assertTrue(all(call.args[:2] == ("job", "delete") for call in self.test.cli.call_args_list))

    def test_active_corrupt_or_ambiguous_syscall_evidence_prevents_deletion(self):
        self.cleanup_fixture(capacity=True)
        files = {self.test.root + "/syscall.trace": b"exact trace"}
        for active, corrupt, message in ((True, False, "active or ownership"), (False, True, "checksum mismatch")):
            self.syscall_files(files, active=active, corrupt=corrupt)
            with self.subTest(active=active), self.assertRaisesRegex(RuntimeError, message):
                self.cases.cleanup()
            self.assertNotIn("evidence_complete", self.state)
        self.syscall_files({"/first/trace": b"one", "/second/trace": b"two"})
        with self.assertRaisesRegex(RuntimeError, "duplicate filenames"):
            self.cases.cleanup()
        self.test.cli.assert_not_called()

    def test_changed_syscall_trace_after_sealing_blocks_resumed_deletion(self):
        files = {self.test.root + "/syscall.trace": b"original complete trace"}
        self.syscall_files(files)
        self.seal_without_deleting()
        files[self.test.root + "/syscall.trace"] = b"different trace"
        with self.assertRaisesRegex(RuntimeError, "Syscall evidence differs"):
            self.cases.cleanup()
        self.test.cli.assert_not_called()

    def test_cleanup_requires_original_source_stage_timings_and_requested_sg(self):
        self.cleanup_fixture(capacity=True)
        path = self.cases.evidence / "source/controller.sha256"
        data = path.read_bytes()
        path.unlink()
        with self.assertRaisesRegex(RuntimeError, "source/controller.sha256"):
            self.cases.cleanup()
        path.write_bytes(data)
        path = self.cases.evidence / "stages/capacity-write/report.json"
        report = json.loads(path.read_text())
        for update in ({"duration_seconds": None}, {"duration_seconds": float("nan")},
                       {"remote_root": "/another/run"}, {"cases": []}):
            path.write_text(json.dumps({**report, **update}))
            with self.subTest(update=update), self.assertRaises(RuntimeError):
                self.cases.cleanup()
        path.write_text(json.dumps(report))
        self.test.args.require_sg_trace = True
        self.state.update(capacity_archive_job_id="1", capacity_restore_job_id="2")
        with self.assertRaisesRegex(RuntimeError, "SG observer source/binary identities"):
            self.cases.cleanup()
        with (self.cases.evidence / "source/controller.sha256").open("a") as output:
            output.write("d" * 64 + "  e2e/physical_sg_trace.c\n" + "e" * 64 + "  physical_sg_trace.so\n")
        with self.assertRaisesRegex(RuntimeError, "Required capacity SG trace"):
            self.cases.cleanup()
        self.test.cli.assert_not_called()
        self.assertNotIn("evidence_complete", self.state)
        for identifier in ("1", "2"):
            self.cases.keep("jobs/" + identifier + "/tapes/ABC001/sg-trace.jsonl", b'{"event":"init"}\n')
        self.cases.keep("jobs/1/tapes/ABC001/sg-trace.jsonl.error", b"observer error retained for review")
        self.cases.cleanup()
        verify_evidence(self.cases.evidence)

    def test_old_seal_missing_live_sg_evidence_cannot_delete_jobs(self):
        self.seal_without_deleting()
        self.cases.tape_evidence = lambda identifier, verify=False: PhysicalCases.tape_evidence(self.cases, identifier, verify)
        self.test.remote.side_effect = lambda argv: SimpleNamespace(stdout=(
            (self.test.install + "/work/jobs/" + argv[1].split("/")[-1] + "/tapes/ABC001/sg-trace.jsonl\x00").encode()
            if argv[0] == "find" else b""))
        with self.assertRaisesRegex(RuntimeError, "Live Tape evidence is missing"):
            self.cases.cleanup()
        self.test.cli.assert_not_called()

    def test_ejected_checks_lease_once_before_reading_status(self):
        events = []
        def one(*args):
            events.append("lease")
            return {"devices": ["/dev/nst0"]}
        def remote(argv):
            self.assertEqual(events, ["lease"])
            events.append("status")
            return SimpleNamespace(stdout=b"DR_OPEN")
        self.test.one.side_effect = one
        self.test.remote.side_effect = remote
        self.cases.ejected()
        self.assertEqual(events, ["lease", "status"])
        self.test.one.assert_called_once_with("tape", "device", "list")
        self.test.remote.assert_called_once_with(["mt", "-f", "/dev/nst0", "status"])

    def test_no_giant_source_fixture(self):
        files = baseline_files()
        self.assertLess(sum(map(len, files.values())), 6 * 1024**2)
        self.assertEqual(files["dataset/empty.bin"], b"")
        self.assertTrue(files["dataset/nested/payload.png"].startswith(b"\x89PNG\r\n\x1a\n"))


if __name__ == "__main__":
    unittest.main()
