"""Capacity stage regressions with tiny manifests and no service or device access."""

import base64
from contextlib import redirect_stdout
import io
import json
from pathlib import Path
import tempfile
from types import SimpleNamespace
import unittest
from unittest import mock

from package_acceptance import sha256
import package_physical_capacity as capacity
import physical_capacity_probe as probe
from physical_package_acceptance import physical_arguments, validate_state


class CapacityTests(unittest.TestCase):
    def setUp(self):
        temporary = tempfile.TemporaryDirectory()
        self.addCleanup(temporary.cleanup)
        self.root = Path(temporary.name)
        self.state = {"source_location": "1", "restored_location": "2", "media_id": "3",
                      "format_job_id": "4", "physical_evidence_dir": str(self.root / "evidence"),
                      "performance_format_complete": True, "performance_metrics_helper": "/owned/tmp/metrics.py"}
        self.test = mock.Mock(root="/owned", install="/owned/install", url="http://127.0.0.1:1234", report={})
        self.test.args = SimpleNamespace(physical_barcode="TST001", physical_device="/dev/nst0",
                                         capacity_files=24, capacity_source_reflink=False,
                                         out=self.root, capacity_preflight=self.root / "preflight.json")
        self.state.update(install="/owned/install", physical_barcode="TST001")
        self.preflight = {"timestamp_ns": 1234, "status": "mounted",
                          "argv": ["work_directory=/owned/install/work/jobs/4/tapes/TST001"],
                          "pages": {"31": {"raw": "3100", "parameters": {str(n): n for n in range(1, 5)}},
                                    "0C": {"raw": "0c00", "parameters": {str(n): n for n in (0, 1, 2, 3, 4, 5, 7)}},
                                    "17": {"raw": "17000000", "decode_error": "unsupported decoder"},
                                    "2E": {"error": "unsupported"}},
                          "statvfs": {"available_bytes": 1234, "fragment_bytes": 4096}}
        self.test.args.capacity_preflight.write_text(json.dumps(self.preflight))
        self.cases = capacity.CapacityCases(self.test, self.state, mock.Mock())
        (self.cases.evidence / "capacity").mkdir(parents=True)
        patch = mock.patch("subprocess.run", side_effect=AssertionError("Unexpected process execution"))
        patch.start()
        self.addCleanup(patch.stop)

    def items(self, count=3, submitted=2):
        rows = []
        for number in range(count):
            name = f"capacity/chunk-{number:03d}.bin"
            row = {"size_bytes": capacity.BOUNDARY_CHUNK_BYTES,
                   "status": "COPY_STATUS_SUBMITTED" if number < submitted else "COPY_STATUS_PENDING",
                   "file": {"source_path": "/owned/fixtures/source/" + name, "target_path": name,
                            "expected": {"sha256": base64.b64encode(bytes([number]) * 32).decode(),
                                         "file_id": str(number + 10)}}}
            if number < submitted:
                row["media_id"] = "3"
                row["file"]["media_path"] = "archive/" + name
            rows.append(row)
        return rows

    def manifest(self, count=3):
        return {f"capacity/chunk-{number:03d}.bin": {"size": capacity.BOUNDARY_CHUNK_BYTES,
                                                     "sha256": (bytes([number]) * 32).hex()}
                for number in range(count)}

    def save_manifest(self, count=3):
        path = self.cases.keep_json("capacity/source-manifest.json", self.manifest(count))
        self.state["capacity_manifest_sha256"] = sha256(path)
        return path

    def test_selected_order_size_hash_and_media(self):
        items = self.items()
        self.assertEqual(capacity.selected_items(items, self.manifest(), "/owned/fixtures/source", "3"), items[:2])
        for change in ("order", "size", "hash", "media", "source"):
            items = self.items()
            if change == "order":
                items.reverse()
            elif change == "size":
                items[0]["size_bytes"] -= 1
            elif change == "hash":
                items[0]["file"]["expected"]["sha256"] = "AA=="
            elif change == "media":
                items[0]["media_id"] = "9"
            else:
                items[0]["file"]["source_path"] = "/other/chunk.bin"
            with self.subTest(change=change), self.assertRaises(RuntimeError):
                capacity.selected_items(items, self.manifest(), "/owned/fixtures/source", "3")

    def test_observer_accepts_all_idle_states_with_error_and_preserves_timestamps(self):
        for status in ("READY", "FAILED", "COMPLETED"):
            self.test.one.side_effect = [RuntimeError("database is locked"),
                                         {"job": {"status": "JOB_STATUS_READY", "phase": "JOB_PHASE_FINALIZING_MEDIA"}},
                                         {"job": {"status": "JOB_STATUS_" + status, "error": "write or close failed"}}]
            with mock.patch.object(capacity.time, "sleep"):
                result = self.cases.observe_terminal({"id": "5"})
            self.assertEqual(result["error"], "write or close failed")
            path = self.cases.evidence / "capacity/archive-jobs.jsonl"
            rows = [json.loads(line) for line in path.read_text().splitlines()]
            self.assertEqual(len(rows), 3)
            self.assertTrue(all(row["timestamp_ns"] > 0 for row in rows))
            self.assertIn("observation_error", rows[0])
            path.unlink()
        self.test.remote.assert_not_called()

    def test_marker_precedes_first_mutation_and_prevents_replay(self):
        def space(path):
            self.assertTrue(self.state["capacity_write_started"])
            raise RuntimeError("disk unavailable")
        with mock.patch.object(self.cases, "available", side_effect=space) as available:
            with self.assertRaisesRegex(RuntimeError, "recorded a failure"):
                self.cases.capacity_write()
            with self.assertRaisesRegex(RuntimeError, "never replay"):
                self.cases.capacity_write()
        available.assert_called_once()
        self.test.remote.assert_not_called()
        self.assertIn("disk unavailable", (self.cases.evidence / "capacity/write-result.json").read_text())

    def test_generation_has_96_disjoint_ctr_ranges_and_immutable_manifest(self):
        self.test.args.capacity_file_mib = [65536] * 20 + [1024] * 76
        self.test.args.capacity_files = 96
        def fixture(path, size, stream):
            self.assertTrue(self.state["capacity_write_started"])
            return {"size": size, "sha256": "a" * 64, "stream": stream}
        with mock.patch.object(self.cases, "available", return_value=4 * 1024**4), \
                mock.patch.object(self.cases, "check_media"), \
                mock.patch.object(self.cases, "inspect", return_value={"media": {"id": "3"}}), \
                mock.patch.object(self.cases, "install_helper", return_value="/owned/tmp/sample.py"), \
                mock.patch.object(self.cases, "stream_fixture", side_effect=fixture) as stream, \
                mock.patch.object(self.cases, "created", side_effect=RuntimeError("stop before any Job")):
            with self.assertRaisesRegex(RuntimeError, "recorded a failure"):
                self.cases.capacity_write()
        self.assertEqual(stream.call_count, 96)
        starts = [call.args[2] for call in stream.call_args_list]
        spans = [call.args[1] // 16 for call in stream.call_args_list]
        self.assertTrue(all(right >= left + span for left, right, span in zip(starts, starts[1:], spans)))
        manifest = self.cases.source_manifest()
        self.assertEqual(sum(row["size"] for row in manifest.values()), sum(self.test.args.capacity_file_mib) * 1024**2)
        path = self.cases.evidence / "capacity/source-manifest.json"
        self.assertEqual(path.stat().st_mode & 0o777, 0o400)
        path.chmod(0o600)
        path.write_text("{}")
        with self.assertRaisesRegex(RuntimeError, "manifest changed"):
            self.cases.source_manifest()

    def test_capture_keeps_items_library_progress_after_finalization_failure(self):
        current = {"status": "JOB_STATUS_READY", "error": "finalization failed"}
        self.test.one.side_effect = lambda *args, **kw: {"job": current} if args[1] == "get" else {"progress": {"current_file": "chunk-002.bin"}}
        items = self.items()
        self.save_manifest()
        self.cases.keep_json("capacity/prepared-items.json", self.items(submitted=0))
        with mock.patch.object(self.cases, "archive_items", return_value=items), \
                mock.patch.object(self.cases, "export", return_value={"retained": {}}), \
                mock.patch.object(self.cases, "capture_job", side_effect=RuntimeError("missing finalIndex")), \
                mock.patch.object(self.cases, "ejected", side_effect=RuntimeError("owner uncertain")):
            result = {"errors": {}}
            self.cases.capture_result({"id": "5"}, result)
        saved = json.loads((self.cases.evidence / "capacity/write-result.json").read_text())
        self.assertEqual(saved["items"], items)
        self.assertEqual(saved["completed_prefix"], [item["file"]["target_path"] for item in items[:2]])
        self.assertEqual(saved["progress"]["progress"]["current_file"], "chunk-002.bin")
        self.assertEqual(saved["library"], {"retained": {}})
        self.assertIn("finalIndex", saved["errors"])
        self.assertTrue(self.state["capacity_write_observed"])
        self.assertFalse(self.state["capacity_device_released"])
        self.assertEqual(self.state["capacity_published_items"], items[:2])

    def test_reflink_generation_uses_byte_sizes_and_reduced_space_budget(self):
        sizes = [64 * 1024**3] * 20 + [1024**3] * 76
        self.test.args.capacity_source_reflink = True
        self.test.args.capacity_files = len(sizes)
        self.test.args.capacity_file_mib = [size // 1024**2 for size in sizes]
        generated = {f"chunk-{number:03d}.bin": {"size": size, "sha256": f"{number:064x}"}
                     for number, size in enumerate(sizes)}
        def remote(argv, **kwargs):
            if argv[0] == "python3":
                self.assertEqual(argv, ["python3", "/owned/tmp/capacity/physical_performance_fixture.py",
                                       "capacity-reflink", "/owned/fixtures/source/capacity", json.dumps(sizes)])
                self.assertEqual(kwargs, {"timeout": 43200})
                return SimpleNamespace(stdout=json.dumps(generated).encode())
            return SimpleNamespace(stdout=b"")
        self.test.remote.side_effect = remote
        required = sum(sizes) + max(sizes) + 8 * 1024**3
        with mock.patch.object(self.cases, "available", return_value=required + 1), \
                mock.patch.object(self.cases, "check_media"), \
                mock.patch.object(self.cases, "inspect", return_value={"media": {"id": "3"}}), \
                mock.patch.object(self.cases, "install_helper", return_value="/owned/tmp/capacity/sample.py"), \
                mock.patch.object(self.cases, "stream_fixture") as stream, \
                mock.patch.object(self.cases, "created", side_effect=RuntimeError("stop before any Job")):
            with self.assertRaisesRegex(RuntimeError, "recorded a failure"):
                self.cases.capacity_write()
        stream.assert_not_called()
        self.assertEqual(self.cases.source_manifest(), {"capacity/" + name: value for name, value in generated.items()})
        self.assertEqual((self.cases.evidence / "capacity/source-manifest.json").stat().st_mode & 0o777, 0o400)

    def test_reflink_failure_or_invalid_manifest_never_falls_back_or_creates_job(self):
        self.test.args.capacity_source_reflink = True
        self.test.args.capacity_files = 1
        for reply in (RuntimeError("FICLONE unsupported"), b"not json", b'{}',
                      json.dumps({"../escape": {"size": capacity.BOUNDARY_CHUNK_BYTES, "sha256": "a" * 64}}).encode(),
                      json.dumps({"chunk-000.bin": {"size": 1, "sha256": "a" * 64}}).encode(),
                      json.dumps({"chunk-000.bin": {"size": capacity.BOUNDARY_CHUNK_BYTES, "sha256": "bad"}}).encode()):
            self.state.pop("capacity_write_started", None)
            def remote(argv, **kwargs):
                if argv[0] == "python3":
                    if isinstance(reply, Exception):
                        raise reply
                    return SimpleNamespace(stdout=reply)
                return SimpleNamespace(stdout=b"")
            self.test.remote.side_effect = remote
            with self.subTest(reply=reply), \
                    mock.patch.object(self.cases, "available", return_value=1024**4), \
                    mock.patch.object(self.cases, "check_media"), \
                    mock.patch.object(self.cases, "inspect", return_value={"media": {"id": "3"}}), \
                    mock.patch.object(self.cases, "install_helper", return_value="/owned/tmp/capacity/sample.py"), \
                    mock.patch.object(self.cases, "stream_fixture") as stream, \
                    mock.patch.object(self.cases, "created") as created:
                with self.assertRaisesRegex(RuntimeError, "recorded a failure"):
                    self.cases.capacity_write()
                with self.assertRaisesRegex(RuntimeError, "never replay"):
                    self.cases.capacity_write()
            stream.assert_not_called()
            created.assert_not_called()
            result = json.loads((self.cases.evidence / "capacity/write-result.json").read_text())
            self.assertTrue(result["errors"]["execution"])

    def test_space_check_requires_full_budget_before_generating_either_fixture(self):
        sizes = [capacity.BOUNDARY_CHUNK_BYTES] * self.test.args.capacity_files
        for reflink in (False, True):
            self.test.args.capacity_source_reflink = reflink
            self.state.pop("capacity_write_started", None)
            required = sum(sizes) + (max(sizes) if reflink else sum(sizes)) + 8 * 1024**3
            with self.subTest(reflink=reflink), mock.patch.object(self.cases, "available", return_value=required):
                with self.assertRaisesRegex(RuntimeError, "recorded a failure"):
                    self.cases.capacity_write()
            self.test.remote.assert_not_called()
            result = json.loads((self.cases.evidence / "capacity/write-result.json").read_text())
            self.assertIn(str(required), result["errors"]["execution"])

    def test_finalization_checks_real_positions_index_report_and_pending_suffix(self):
        import copy
        import struct

        items = self.items(count=3, submitted=2)
        manifest = self.manifest()
        positions = {}
        files = []
        for number, item in enumerate(items):
            item["size_bytes"] = 3
            manifest[item["file"]["target_path"]]["size"] = 3
            if number == 2:
                continue
            name = item["file"]["media_path"]
            positions[name] = {"path": name, "media_id": 3, "size": 3,
                               "hash": item["file"]["expected"]["sha256"],
                               "storage_order": base64.b64encode(b"b" + struct.pack(">QQ", number + 1, 0)).decode(),
                               "storage_metadata": {"ltfs": {"extents": [{"partition": "b", "startBlock": str(number + 1),
                                                                         "byteOffset": "0", "byteCount": "3"}]}}}
            files.append(f"<file><name>chunk-{number:03d}.bin</name><length>3</length><extentinfo><extent>"
                         f"<partition>b</partition><startblock>{number + 1}</startblock><byteoffset>0</byteoffset>"
                         "<bytecount>3</bytecount><fileoffset>0</fileoffset></extent></extentinfo></file>")
        xml = ("<ltfsindex><directory><contents><directory><name>archive</name><contents><directory>"
               "<name>capacity</name><contents>" + "".join(files) +
               "</contents></directory></contents></directory></contents></directory></ltfsindex>").encode()
        path = self.cases.keep_json("capacity/source-manifest.json", manifest)
        self.state["capacity_manifest_sha256"] = sha256(path)
        self.cases.keep_json("capacity/prepared-items.json", items)
        directory = self.cases.evidence / "jobs/5/tapes/TST001"
        directory.mkdir(parents=True)
        (directory / "TST001.schema").write_bytes(xml)
        current = {"status": "JOB_STATUS_READY", "error": "no space", "phase": "JOB_PHASE_UNSPECIFIED"}
        self.test.one.side_effect = lambda *args: {"job": current} if args[1] == "get" else {"progress": {}}
        for defect in (None, "report", "index", "staged", "suffix-media", "prefix"):
            selected, index, report = copy.deepcopy(items), xml, {"media_id": "3", "file_count": 2, "bytes": 6}
            if defect == "report":
                report["bytes"] = 9
            elif defect == "index":
                index = xml.replace(b"<length>3</length>", b"<length>2</length>", 1)
            elif defect == "staged":
                selected[-1]["status"] = "COPY_STATUS_STAGED"
            elif defect == "suffix-media":
                selected[-1]["media_id"] = "3"
            elif defect == "prefix":
                selected[0]["status"] = "COPY_STATUS_PENDING"
                selected[0].pop("media_id")
                selected[0]["file"].pop("media_path")
            (directory / "TST001.schema").write_bytes(index)
            (directory / "yatm-report.json").write_text(json.dumps(report))
            with self.subTest(defect=defect), \
                    mock.patch.object(self.cases, "archive_items", side_effect=[selected, []] if defect in (None, "report") else [selected]), \
                    mock.patch.object(self.cases, "export", return_value=positions), \
                    mock.patch.object(self.cases, "capture_job", return_value=directory), \
                    mock.patch.object(self.cases, "ejected"):
                result = {"errors": {}}
                self.cases.capture_result({"id": "5"}, result)
            self.assertEqual(bool(result["errors"]), defect is not None)
            self.assertEqual(result["outcome"], "operation-failure")
            self.assertTrue(self.state["capacity_device_released"])
        self.test.cli.assert_not_called()
        self.test.remote.assert_not_called()

    def test_active_owner_never_receives_device_query(self):
        self.test.one.return_value = {"job": {"status": "JOB_STATUS_READY", "phase": "JOB_PHASE_COPYING_TO_MEDIA"}}
        with mock.patch.object(self.cases, "archive_items", return_value=[]), \
                mock.patch.object(self.cases, "export", return_value={}), \
                mock.patch.object(self.cases, "capture_job", side_effect=RuntimeError("active")), \
                mock.patch.object(self.cases, "ejected") as ejected:
            result = {"errors": {}}
            self.cases.capture_result({"id": "5"}, result)
        ejected.assert_not_called()
        self.assertFalse(self.state["capacity_write_observed"])
        self.assertIn("uncertain", result["owner"])

    def test_verify_selects_all_published_files_and_never_replays(self):
        self.save_manifest(100)
        self.state.update(capacity_write_observed=True, capacity_device_released=True,
                          capacity_published_items=self.items(100, 100))
        def restore(key, items, directory, completion, **options):
            self.assertTrue(self.state["capacity_verify_started"])
            self.assertEqual(len(items), 100)
            self.state[completion] = True
        with mock.patch.object(self.cases, "restore_selection", side_effect=restore) as restored:
            self.cases.capacity_verify()
            with self.assertRaisesRegex(RuntimeError, "never replay"):
                self.cases.capacity_verify()
        restored.assert_called_once_with("capacity_restore_job_id", self.state["capacity_published_items"],
                                         "capacity", "capacity_restore_complete",
                                         observe=self.cases.restore_sampler, timeout=28800)

    def test_empty_readback_retains_failure_without_restore_or_verified_marker(self):
        self.save_manifest()
        self.state.update(capacity_write_observed=True, capacity_device_released=True, capacity_published_items=[])
        with mock.patch.object(self.cases, "restore_selection") as restored:
            with self.assertRaisesRegex(RuntimeError, "Restore failed"):
                self.cases.capacity_verify()
            with self.assertRaisesRegex(RuntimeError, "never replay"):
                self.cases.capacity_verify()
        restored.assert_not_called()
        result = json.loads((self.cases.evidence / "capacity/verify-result.json").read_text())
        self.assertIn("No published files", result["errors"]["restore"])
        self.assertNotIn("capacity_verify_complete", self.state)
        self.test.remote.assert_not_called()

    def test_restore_sampling_covers_admission_and_settlement_and_preserves_failure(self):
        self.save_manifest()
        self.state.update(capacity_write_observed=True, capacity_device_released=True,
                          capacity_published_items=self.items()[:2], capacity_helper="/owned/tmp/capacity/sample.py")
        job = {"id": "5"}
        events = []
        def created(key, *args):
            self.state[key] = job["id"]
            self.assertEqual(args.count("--file-id"), 2)
            return job
        def settled(current, *args, **kwargs):
            if args == ("JOB_STATUS_READY",):
                events.append("prepared")
                return
            self.assertEqual(kwargs, {"timeout": 28800})
            events.append("settled")
            raise RuntimeError("read failed")
        def submit(*args, **kwargs):
            self.assertTrue(self.state["capacity_restore_job_id_submitted"])
            events.append("submitted")
        self.test.cli.side_effect = submit
        self.test.one.return_value = {"job": {"status": "JOB_STATUS_READY"}}
        with mock.patch.object(self.cases, "check_media"), \
                mock.patch.object(self.cases, "inspect", return_value={"media": {"id": "3"}}), \
                mock.patch.object(self.cases, "created", side_effect=created), \
                mock.patch.object(self.cases, "settled", side_effect=settled), \
                mock.patch.object(self.cases, "capture_job"), \
                mock.patch.object(self.cases, "ejected"), \
                mock.patch.object(capacity, "PerformanceSampler") as sampler:
            sampler.return_value.__enter__.side_effect = lambda: events.append("sampling-start")
            sampler.return_value.__exit__.side_effect = lambda *args: events.append("sampling-stop")
            with self.assertRaisesRegex(RuntimeError, "Restore failed"):
                self.cases.capacity_verify()
            with self.assertRaisesRegex(RuntimeError, "never replay"):
                self.cases.capacity_verify()
        self.assertEqual(events, ["prepared", "sampling-start", "submitted", "settled", "sampling-stop"])
        sampler.assert_called_once_with(self.test, job, "/owned/tmp/capacity/sample.py",
                                        "/owned/install/work/jobs/5/tapes/TST001",
                                        self.cases.evidence / "capacity/restore-samples.jsonl", driver=True,
                                        resource_helper_path="/owned/tmp/metrics.py", app_period=1, driver_period=1)
        self.test.cli.assert_called_once_with("restore", "run", "tape", "5", "--device", "/dev/nst0")
        result = json.loads((self.cases.evidence / "capacity/verify-result.json").read_text())
        self.assertEqual(result["errors"]["restore"], "read failed")
        self.assertNotIn("capacity_verify_complete", self.state)

    def test_verify_preserves_error_and_refuses_uncertain_owner(self):
        self.save_manifest()
        self.state.update(capacity_write_observed=True, capacity_device_released=False,
                          capacity_published_items=self.items()[:2])
        with mock.patch.object(self.cases, "restore_selection", side_effect=RuntimeError("read failed")) as restored:
            with self.assertRaisesRegex(RuntimeError, "ownership is uncertain"):
                self.cases.capacity_verify()
            restored.assert_not_called()
            self.state["capacity_device_released"] = True
            with self.assertRaisesRegex(RuntimeError, "Restore failed"):
                self.cases.capacity_verify()
        self.assertIn("read failed", (self.cases.evidence / "capacity/verify-result.json").read_text())
        self.assertNotIn("capacity_verify_complete", self.state)

    def test_helper_keeps_import_names_and_snapshot_pages_on_statvfs_error(self):
        self.cases.install_helper()
        paths = [call.args[0] for call in self.test.write.call_args_list]
        self.assertEqual(paths, ["/owned/tmp/capacity/physical_performance_fixture.py",
                                 "/owned/tmp/capacity/physical_capacity_probe.py",
                                 "/owned/tmp/capacity/physical_capacity_boundary.py", "/owned/tmp/capacity/sample.py"])
        self.test.write.reset_mock()
        self.test.remote.reset_mock()
        self.assertEqual(self.cases.install_helper(), "/owned/tmp/capacity/sample.py")
        self.test.write.assert_not_called()
        self.test.remote.assert_not_called()
        reading = {"status": "mounted", "mount": str(self.root), "pages": {page: {"raw": "00"} for page in ("34", "0C", "31", "02")}}
        with mock.patch("physical_performance_fixture.sample", return_value=reading), \
                mock.patch.object(probe, "owner", return_value=reading) as owner, \
                mock.patch.object(probe, "read_log_xattr", return_value=b"0x0000000000000001") as alert, \
                mock.patch.object(probe.os, "statvfs", side_effect=OSError(5, "late read failure")), \
                mock.patch("sys.argv", ["sample.py", "sample", "/owned/job"]), redirect_stdout(io.StringIO()) as output:
            exec(capacity.CAPACITY_HELPER, {"__name__": "__main__"})
        owner.assert_called_once_with("/owned/job", self.root.resolve(), pages=("34", "0C", "31", "02", "17"))
        alert.assert_called_once_with(self.root.resolve(), "user.ltfs.mediaStorageAlert")
        saved = json.loads(output.getvalue())
        self.assertEqual(saved["storage_alert"]["value"], "0x0000000000000001")
        self.assertEqual(set(saved["pages"]), {"34", "0C", "31", "02"})
        self.assertEqual(saved["statvfs"]["errno"], 5)
        self.assertNotIn("available_bytes", saved["statvfs"])
        self.assertGreaterEqual(saved["statvfs_request_end_ns"], saved["statvfs_request_start_ns"])
        self.assertGreaterEqual(saved["target_listing"]["request_end_ns"],
                                saved["target_listing"]["request_start_ns"])

    def test_helper_uses_shared_listing_and_retains_pages_when_listing_fails(self):
        reading = {"status": "mounted", "mount": str(self.root), "pages": {"31": {"raw": "3100"}}}
        with mock.patch("physical_performance_fixture.sample", return_value=reading), \
                mock.patch.object(probe, "snapshot", return_value=reading), \
                mock.patch("physical_capacity_boundary.files_snapshot", side_effect=OSError(5, "late listing failure")) as listing, \
                mock.patch("sys.argv", ["sample.py", "sample", "/owned/job"]), redirect_stdout(io.StringIO()) as output:
            exec(capacity.CAPACITY_HELPER, {"__name__": "__main__"})
        listing.assert_called_once_with(self.root)
        result = json.loads(output.getvalue())
        self.assertEqual(result["pages"], reading["pages"])
        self.assertEqual(result["target_listing"]["errno"], 5)

    def test_saved_helper_must_match_owned_path_without_installing_again(self):
        for helper in ("/other/tmp/capacity/sample.py", "/owned/tmp/capacity/../sample.py", ""):
            self.state["capacity_helper"] = helper
            with self.subTest(helper=helper), self.assertRaisesRegex(RuntimeError, "outside the owned installation"):
                self.cases.install_helper()
        self.test.remote.assert_not_called()
        self.test.write.assert_not_called()

    def test_arguments_and_failed_write_can_enter_verify_without_passed_stage(self):
        state_path = self.root / "state.json"
        state_path.write_text("{}")
        options = {"host": "isolated-test", "test-parent": "/srv/acceptance", "archive": "yatm-linux-amd64-v1.0.0-alpha.2.tar.gz",
                   "version": "v1.0.0-alpha.2", "commit": "1" * 40, "out": str(self.root / "out"),
                   "state": str(state_path), "physical-device": "/dev/nst0", "physical-barcode": "TST001",
                   "physical-stage": "capacity-write"}
        args = physical_arguments(["--" + key + "=" + value for key, value in options.items()])
        self.assertEqual(args.capacity_files, 24)
        self.assertIsNone(args.preview_archive)
        argv = ["--" + key + "=" + value for key, value in options.items()]
        for sizes in ([65536] * 20 + [1024] * 76, [1] * 100):
            selected = physical_arguments(argv + ["--capacity-file-mib=" + str(size) for size in sizes])
            self.assertEqual(selected.capacity_files, len(sizes))
            self.assertEqual(selected.capacity_file_mib, sizes)
        for sizes in ([1] * 101, [0], [65537]):
            with self.subTest(sizes=sizes), self.assertRaisesRegex(RuntimeError, "at most 64 GiB"):
                physical_arguments(argv + ["--capacity-file-mib=" + str(size) for size in sizes])
        root = "/srv/acceptance/yatm-package-acceptance.12345678"
        state = {"host": args.host, "commit": args.commit, "version": args.version, "device": args.physical_device,
                 "barcode": args.physical_barcode, "mkltfs": args.mkltfs, "ltfs_binary": args.ltfs_binary,
                 "remote_root": root, "install": root + "/install", "service": "yatm-acceptance-12345678.service",
                 "url": "http://127.0.0.1:1234", "performance": True, "performance_source_read_mode": "buffered",
                 "performance_format_complete": True, "completed_stages": ["performance-prepare", "performance-format"]}
        state.update(format_job_id="4", physical_barcode="TST001")
        self.preflight["argv"] = ["work_directory=" + root + "/install/work/jobs/4/tapes/TST001"]
        args.capacity_preflight = self.root / "preflight.json"
        args.capacity_preflight.write_text(json.dumps(self.preflight))
        validate_state(args, state)
        for changed in ({"performance_format_complete": False}, {"performance_completed": ["large-random"]},
                        {"capacity_write_started": True}):
            with self.assertRaises(RuntimeError):
                validate_state(args, {**state, **changed})
        args.physical_stage = "capacity-verify"
        state.update(capacity_write_started=True, capacity_write_observed=True, capacity_device_released=True)
        validate_state(args, state)
        with self.assertRaisesRegex(RuntimeError, "uncertain"):
            validate_state(args, {**state, "capacity_device_released": False})

    def test_preflight_rejects_missing_metrics_and_foreign_owner_before_mutation(self):
        capacity.check_preflight(self.test.args.capacity_preflight, self.state)
        self.test.args.capacity_preflight.write_text(json.dumps({"snapshot": self.preflight, "attributes": {}}))
        capacity.check_preflight(self.test.args.capacity_preflight, self.state)
        for key, value in (("argv", ["work_directory=/other/job"]), ("pages", {}),
                           ("statvfs", {"error": "unavailable"})):
            self.test.args.capacity_preflight.write_text(json.dumps({**self.preflight, key: value}))
            with self.subTest(key=key), self.assertRaises(RuntimeError):
                self.cases.capacity_write()
            self.assertNotIn("capacity_write_started", self.state)
        self.test.remote.assert_not_called()

    def test_preflight_hook_envelope_requires_comparison_pages_without_live_pid(self):
        record = {"phase": "before-copy", "request_start_ns": 1000, "request_end_ns": 2000,
                  "snapshot": {**self.preflight, "pid": 999999999}, "attributes": {}, "files": {},
                  "required_probes_ok": True}
        path = self.test.args.capacity_preflight
        path.write_text(json.dumps(record))
        self.assertEqual(capacity.check_preflight(path, self.state), record)
        for page, field in (("0C", "4"), ("0C", "5"), ("0C", "7"), ("31", "1")):
            changed = json.loads(json.dumps(record))
            value = changed["snapshot"]["pages"][page]
            if page == "17":
                value.pop(field)
                value["error"] = "unsupported"
            else:
                value["parameters"].pop(field)
            path.write_text(json.dumps(changed))
            with self.subTest(page=page, field=field), self.assertRaisesRegex(RuntimeError, "page " + page):
                capacity.check_preflight(path, self.state)
        self.test.remote.assert_not_called()

    def test_uncertain_lease_prevents_physical_status_query(self):
        self.test.one.return_value = {"devices": []}
        with self.assertRaisesRegex(RuntimeError, "owner remains uncertain"):
            self.cases.ejected()
        self.test.remote.assert_not_called()


if __name__ == "__main__":
    unittest.main()
