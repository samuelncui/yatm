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
from unittest.mock import Mock

from package_physical_cases import BOUNDARY_CHUNK_BYTES, PhysicalCases, baseline_files, run_stage
from package_physical_evidence import (check_checkpoint, check_positions, check_restore_database,
                                       check_signature, format_partition_map, library_positions, parse_index, relative_path,
                                       seal_evidence, submitted_prefix, verify_evidence)


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
        check_positions([self.item], positions, self.index, "9", {"index": "a", "data": "b"})
        empty = {"status": "COPY_STATUS_SUBMITTED", "media_id": "9", "size_bytes": "0",
                 "file": {"media_path": "dataset/empty", "expected": {"sha256": encoded(hashlib.sha256(b"").digest())}}}
        positions["dataset/empty"] = {"hash": empty["file"]["expected"]["sha256"]}
        check_positions([empty], positions, self.index, "9")

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
            check_positions([self.item], self.positions, self.index, "9", {"index": "b", "data": "a"})
        with self.assertRaisesRegex(RuntimeError, "write order"):
            check_positions([self.item, self.item], self.positions, self.index, "9")
        changed = copy.deepcopy(self.positions)
        changed["dataset/a.bin"]["storage_metadata"]["ltfs"]["extents"][0]["fileOffsetBytes"] = "1"
        index = copy.deepcopy(self.index)
        index["dataset/a.bin"]["extents"][0]["file_offset_bytes"] = 1
        with self.assertRaisesRegex(RuntimeError, "gap"):
            check_positions([self.item], changed, index, "9")

    def test_checkpoint_requires_published_prefix_and_complete_diagnostic(self):
        pending = {"status": "COPY_STATUS_PENDING", "size_bytes": "3", "file": {}}
        items = [self.item, pending]
        job = {"status": "JOB_STATUS_READY", "error": "full"}
        report = {"media_id": 9, "file_count": 1, "bytes": 3, "termination_error": "full"}
        progress = {"total_known": True, "total_file_count": "2", "total_bytes": "6",
                    "copied_file_count": "1", "copied_bytes": "3"}
        line = b'event=archive_media_checkpoint reason=no_space media_id=9 files=1 bytes=3 error="device full"\n'
        self.assertEqual(check_checkpoint(line, report, job, items, "9", progress), [self.item])
        with self.assertRaisesRegex(RuntimeError, "complete durable"):
            check_checkpoint(line.split(b" error=")[0], report, job, items, "9", progress)
        with self.assertRaisesRegex(RuntimeError, "continuous prefix"):
            submitted_prefix([pending, self.item], "9")
        with self.assertRaisesRegex(RuntimeError, "staged"):
            submitted_prefix([self.item, {**pending, "status": "COPY_STATUS_STAGED"}], "9")
        with self.assertRaisesRegex(RuntimeError, "unfinalized"):
            check_checkpoint(line, report, job, items, "9", {**progress, "copied_bytes": "6"})

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
                                         physical_barcode="ABC001", out=Path(self.temporary.name))
        self.test.root = "/isolated/run"
        self.test.install = "/isolated/run/install"
        self.state = {"partition_map": {"index": "a", "data": "b"}}
        self.save = Mock()
        self.cases = PhysicalCases(self.test, self.state, self.save)

    def test_existing_full_job_only_validates_checkpoint(self):
        self.state.update(restore_complete=True, full_archive_job_id="123")
        self.cases.full_checkpoint = Mock()
        self.cases.full_write()
        self.cases.full_checkpoint.assert_called_once_with()
        self.test.remote.assert_not_called()
        self.test.cli.assert_not_called()
        self.save.assert_not_called()

    def test_interrupted_prefill_never_repeats_device_work(self):
        self.state["prefill_started"] = True
        with self.assertRaisesRegex(RuntimeError, "already started"):
            self.cases.prefill(4 * 1024**3)
        self.test.stop_owned.assert_not_called()
        self.test.remote.assert_not_called()

    def test_completed_restore_never_rereads_or_runs_again(self):
        self.state["restore_complete"] = True
        self.cases.restore_selection("restore_job_id", [], "baseline", "restore_complete")
        self.test.one.assert_not_called()
        self.test.remote.assert_not_called()

    def test_stream_size_above_32_bits_and_filler_has_no_hash_pass(self):
        size = 2 * 1024**4
        self.test.remote.side_effect = [SimpleNamespace(stdout=b""), SimpleNamespace(stdout=str(size).encode())]
        manifest = self.cases.stream_fixture("/isolated/run/prefill-mount/filler.bin", size, 1000000, False)
        self.assertEqual(manifest, {"size": size, "stream": 1000000})
        command = self.test.remote.call_args_list[0].args[0]
        self.assertIn(str(size // 1024**2), command)
        self.assertNotIn("sha256sum", command[2])
        self.assertEqual(self.test.remote.call_count, 2)

    def test_prefill_stream_failure_retains_guard_without_restart_or_unmount(self):
        self.state.update(media_id="9")
        self.cases.check_media = Mock()
        self.cases.inspect = Mock(return_value={"media": {"id": "9", "profile": {"tape": {"encryption": "v1:" + "f" * 64}}}})
        self.cases.tape_script = Mock()
        self.cases.available = Mock(return_value=100 * 1024**3)
        self.cases.stream_fixture = Mock(side_effect=RuntimeError("stream failed"))
        mount = self.test.root + "/prefill-mount"
        responses = [b"", b'{"barcode":"ABC001"}', b"", b"",
                     json.dumps({"filesystems": [{"target": mount, "fstype": "fuse.ltfs"}]}).encode()]
        self.test.remote.side_effect = [SimpleNamespace(stdout=value) for value in responses]
        self.test.stop_owned.side_effect = lambda: self.assertTrue(self.state["prefill_started"])
        with self.assertRaisesRegex(RuntimeError, "stream failed"):
            self.cases.prefill(4 * 1024**3)
        self.assertTrue(self.state["prefill_started"])
        self.assertNotIn("prefill_complete", self.state)
        self.test.start_owned.assert_not_called()
        self.assertNotIn("umount", [call.args[0] for call in self.cases.tape_script.call_args_list])
        key_write = self.test.remote.call_args_list[2]
        self.assertNotIn("f" * 64, repr(key_write.args))
        self.assertEqual(key_write.kwargs["body"], b"f" * 64)

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

    def test_resumed_baseline_cannot_inspect_or_format(self):
        for key in ("baseline_started", "format_job_id", "append_job_id", "media_id"):
            with self.subTest(key=key):
                state = {**self.state, key: "1"}
                with self.assertRaisesRegex(RuntimeError, "never format"):
                    PhysicalCases(self.test, state, self.save).baseline()
        self.test.one.assert_not_called()
        self.test.cli.assert_not_called()

    def test_existing_media_and_wrong_barcode_block_before_baseline_mutation(self):
        for reply in ({"identity": "ABC001", "media": {"id": "8"}}, {"identity": "DEF002"}):
            self.test.one.return_value = reply
            with self.assertRaises(RuntimeError):
                self.cases.baseline()
        self.test.write.assert_not_called()
        self.test.cli.assert_not_called()
        self.save.assert_not_called()

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

    def test_capture_failure_prevents_all_cleanup_deletes(self):
        self.state["full_verify_complete"] = True
        self.test.one.return_value = {"jobs": [{"id": "1"}, {"id": "2"}]}
        self.cases.export = Mock()
        self.cases.capture_job = Mock(side_effect=RuntimeError("snapshot failed"))
        with self.assertRaisesRegex(RuntimeError, "snapshot failed"):
            self.cases.cleanup()
        self.test.cli.assert_not_called()
        self.assertNotIn("evidence_complete", self.state)

    def test_no_giant_source_fixture(self):
        files = baseline_files()
        self.assertLess(sum(map(len, files.values())), 6 * 1024**2)
        self.assertEqual(files["dataset/empty.bin"], b"")
        self.assertTrue(files["dataset/nested/payload.png"].startswith(b"\x89PNG\r\n\x1a\n"))
        self.assertEqual(BOUNDARY_CHUNK_BYTES, 4294967296)


if __name__ == "__main__":
    unittest.main()
