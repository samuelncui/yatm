"""Local tar-controller regressions; all device, process and service calls are stubbed."""

import base64
from contextlib import ExitStack, closing
import hashlib
import io
import json
import os
from pathlib import Path
import shutil
import sqlite3
import subprocess
import tempfile
from types import SimpleNamespace
import unittest
from unittest import mock

from package_physical_cases import check_cleanup_state
from package_physical_tar import TarCases, fill_restore_sample, model_change, parse_mam_barcode, read_observations, run_stage
from physical_package_acceptance import PhysicalAcceptance, physical_arguments, public_tape_data, validate_state
from physical_tar_recovery import (MAX_RECORD_BYTES, PublicReader, check, check_boundary, check_extracted, check_fill,
                                   check_fill_members, generate, main as recovery_main, parse_status)


def status(flags="EOF ONLINE IM_REP_EN", file=1, block=0, size=0):
    return (f"SCSI 2 tape drive:\nFile number={file}, block number={block}, partition=0.\n"
            f"Tape block size {size} bytes. Density code 0x58 (LTO-5).\n"
            "Soft error count since last status=0\nGeneral status bits on (81010000):\n " + flags + "\n")


def library_export(key, name="Test Tape"):
    """Private Library export reply; API Media replies carry only encryption_key_saved."""
    return [{"type": "header", "format": "yatm-library-backup"},
            {"type": "media", "data": {"id": 9, "identity": "TST001", "name": name,
                                       "profile": {"tape": {"encryption": key}}}},
            {"type": "end"}]


def recovery_response(argv):
    return SimpleNamespace(stdout=b"/owned/tmp/public-key.12345678\n" if argv[0] == "mktemp" else b"")


class TarArgumentsTests(unittest.TestCase):
    def setUp(self):
        temporary = tempfile.TemporaryDirectory()
        self.addCleanup(temporary.cleanup)
        self.directory = Path(temporary.name)
        self.options = {"host": "local", "test-parent": "/srv/acceptance",
                        "archive": "yatm-linux-amd64-v1.0.0-alpha.2.tar.gz", "version": "v1.0.0-alpha.2",
                        "commit": "1" * 40, "out": str(self.directory / "out"),
                        "state": str(self.directory / "state.json"), "physical-device": "/dev/nst0",
                        "physical-barcode": "TST001", "physical-format": "tar_pax_v1",
                        "record-mode": "variable", "record-bytes": 262144, "physical-stage": "prepare",
                        "sg-device": "/dev/sg3"}
        blocker = mock.patch("package_acceptance.subprocess.run", side_effect=AssertionError("Unexpected process execution"))
        blocker.start()
        self.addCleanup(blocker.stop)

    def arguments(self, **changes):
        options = {**self.options, **changes}
        return physical_arguments(["--" + key if value is True else "--" + key + "=" + str(value)
                                   for key, value in options.items() if value is not None])

    def state(self, args):
        root = "/srv/acceptance/yatm-package-acceptance.12345678"
        return {"host": args.host, "commit": args.commit, "version": args.version, "device": args.physical_device,
                "barcode": args.physical_barcode, "physical_format": args.physical_format,
                "record_mode": args.record_mode, "record_bytes": args.record_bytes, "sg_device": args.sg_device,
                "mkltfs": args.mkltfs, "ltfs_binary": args.ltfs_binary, "remote_root": root, "install": root + "/install",
                "service": "yatm-acceptance-12345678.service", "url": "http://127.0.0.1:23456", "completed_stages": ["prepare"]}

    def test_tar_requires_explicit_frozen_records_and_local_execution(self):
        self.assertIsNone(self.arguments().preview_archive)
        self.assertEqual(self.arguments(**{"record-mode": "fixed"}).physical_format, "tar_pax_v1")
        for change in ({"record-mode": None}, {"record-bytes": None}, {"record-bytes": 513},
                       {"record-bytes": 0}, {"record-bytes": MAX_RECORD_BYTES + 512}, {"host": "nas"}):
            with self.subTest(change=change), self.assertRaises(RuntimeError):
                self.arguments(**change)

    def test_tar_rejects_even_explicit_default_ltfs_options(self):
        for change in ({"mkltfs": "mkltfs"}, {"ltfs-binary": "ltfs"}, {"performance-size-gib": 4},
                       {"capacity-files": 24}, {"no-performance-driver-sampling": True},
                       {"physical-stage": "performance-prepare"},
                       {"preview-archive": "yatm-preview-linux-amd64-v1.0.0-alpha.2.tar.gz"}):
            with self.subTest(change=change), self.assertRaisesRegex(RuntimeError, "does not accept"):
                self.arguments(**change)

    def test_state_rejects_format_records_and_admitted_stage_replay_before_host_calls(self):
        prepared = self.arguments()
        state = self.state(prepared)
        Path(prepared.state).write_text(json.dumps(state))
        args = self.arguments(**{"physical-stage": "baseline"})
        validate_state(args, state)
        for index, change in enumerate(({"physical_format": "ltfs_v1"}, {"record_mode": "fixed"}, {"record_bytes": 524288},
                                       {"sg_device": "/dev/sg4"},
                                       {"tar_baseline_started": True}, {"completed_stages": ["prepare", "baseline"]})):
            with self.subTest(change=change):
                args.state.write_text(json.dumps({**state, **change}))
                args.out = self.directory / ("out-" + str(index))
                controller = PhysicalAcceptance(args)
                with mock.patch.object(controller, "remote") as remote, mock.patch.object(controller, "start_owned") as start:
                    with self.assertRaises(RuntimeError):
                        controller.execute()
                remote.assert_not_called()
                start.assert_not_called()

    def test_tar_configuration_installs_encrypt_and_owner_check_requires_it(self):
        controller = PhysicalAcceptance(self.arguments())
        state = self.state(controller.args)
        controller.root, controller.install = state["remote_root"], state["install"]
        controller.service, controller.url = state["service"], state["url"]
        config = {"database": {"dialect": "sqlite", "dsn": "./catalog.db", "sqlite_wal": False},
                  "paths": {"work": controller.install + "/work", "access": [{"root": controller.root + "/fixtures"}]},
                  "domain": controller.url, "tape_devices": [], "scripts": {"mount": "production-script"}}
        adapter = b"#!/bin/sh\nexit 0\n"
        def remote(argv, **kwargs):
            return SimpleNamespace(stdout=json.dumps(config).encode() if argv == ["cat", controller.root + "/config.yaml"]
                                   else adapter if argv[0] == "cat" else b"")
        with mock.patch.object(controller, "remote", side_effect=remote) as invoke, mock.patch.object(controller, "write") as write:
            controller.configure_tape()
        self.assertEqual([call.args[0] for call in invoke.call_args_list if call.args[0][0] == "cat"],
                         [["cat", controller.root + "/config.yaml"],
                          ["cat", controller.root + "/package/templates/scripts/encrypt"]])
        self.assertEqual(write.call_args_list[0].args, (controller.root + "/tape-scripts/encrypt", adapter))
        configured = json.loads(write.call_args.args[1])
        expected_scripts = {"encrypt": controller.root + "/tape-scripts/encrypt"}
        self.assertEqual(configured["scripts"], expected_scripts)
        self.assertEqual(configured["tape_devices"], ["/dev/nst0"])
        self.assertTrue(configured["database"]["sqlite_wal"])
        for scripts in (expected_scripts, {}, {**expected_scripts, "mount": "production-script"}):
            configured["scripts"] = scripts
            with mock.patch.object(controller, "remote_batch", return_value=[
                    SimpleNamespace(stdout=(controller.install + "/" + controller.service).encode()),
                    SimpleNamespace(stdout=json.dumps(configured).encode())]), \
                    mock.patch.object(controller, "remote", return_value=SimpleNamespace(
                        stdout=(controller.install + "/" + controller.service).encode())):
                if scripts != expected_scripts:
                    with self.assertRaisesRegex(RuntimeError, "configuration changed"):
                        controller.verify_owner()
                else:
                    controller.verify_owner()

    def test_cli_keys_are_redacted_before_transcripts_but_retained_only_in_memory(self):
        controller = PhysicalAcceptance(self.arguments())
        key = "v1:" + "ab" * 32
        raw = json.dumps({"media": [{"profile": {"tape": {"format": "tar_pax_v1", "encryption": key}}}]}).encode()
        result = subprocess.CompletedProcess(["stub-cli"], 0, raw, ("adapter error " + key).encode())
        record = controller.command_record(["stub-cli"])
        returned = controller.command_result(record, (result, 0.1), 0, 5)
        self.assertEqual(returned.stdout, raw)
        self.assertNotIn(key.encode(), (controller.args.out / record["stdout"]).read_bytes())
        self.assertNotIn(key.encode(), (controller.args.out / record["stderr"]).read_bytes())
        self.assertNotIn("encryption", json.loads((controller.args.out / record["stdout"]).read_bytes())["media"][0]["profile"]["tape"])
        self.assertNotIn(key, (controller.args.out / "report.json").read_text())
        controller.install, controller.url = "/owned/install", "http://127.0.0.1:23456"
        with mock.patch.object(controller, "invoke", return_value=(result, 0.1)):
            private = controller.private_cli("media", "list")
            public = controller.one("media", "list")
        self.assertEqual(private[0]["media"][0]["profile"]["tape"]["encryption"], key)
        self.assertNotIn(key, json.dumps(public))
        for transcript in controller.args.out.glob("*.std*"):
            self.assertNotIn(key.encode(), transcript.read_bytes())
        timeout = subprocess.TimeoutExpired(["stub-cli"], 5, output=raw, stderr=raw)
        record = controller.command_record(["stub-cli"])
        with self.assertRaisesRegex(RuntimeError, "timed out"):
            controller.command_result(record, (timeout, 0.1), 0, 5)
        for channel in ("stdout", "stderr"):
            self.assertNotIn(key.encode(), (controller.args.out / record[channel]).read_bytes())

    def test_restore_dispatches_tar_and_preserves_report_without_ltfs_construction(self):
        args = self.arguments()
        state = self.state(args)
        state.update(completed_stages=["prepare", "baseline"], physical_evidence_dir=str(self.directory / "preserved"))
        args.state.write_text(json.dumps(state))
        args = self.arguments(**{"physical-stage": "restore"})
        controller = PhysicalAcceptance(args)
        with mock.patch.object(controller, "verify_owner"), mock.patch.object(controller, "start_owned"), \
                mock.patch.object(controller, "stop_owned"), mock.patch("package_physical_tar.run_stage") as tar, \
                mock.patch("package_physical_cases.run_stage", side_effect=AssertionError("LTFS dispatch")):
            controller.execute()
        tar.assert_called_once()
        report = json.loads((self.directory / "preserved/stages/restore/report.json").read_text())
        self.assertEqual(report["status"], "passed")
        self.assertEqual(report["physical_format"], "tar_pax_v1")


class TarWorkflowTests(unittest.TestCase):
    def setUp(self):
        temporary = tempfile.TemporaryDirectory()
        self.addCleanup(temporary.cleanup)
        self.directory = Path(temporary.name)
        self.test = mock.Mock()
        self.test.args = SimpleNamespace(physical_device="/dev/nst0", physical_barcode="TST001", physical_format="tar_pax_v1",
                                         physical_stage="restore", record_mode="variable", record_bytes=262144,
                                         host="local",
                                         out=self.directory, no_syscall_trace=True, syscall_trace=None, require_sg_trace=False)
        self.test.root, self.test.install = "/owned", "/owned/install"
        self.state = {"media_id": "9", "source_location": "1", "restored_location": "2", "baseline_complete": True}
        self.cases = TarCases(self.test, self.state, mock.Mock())
        self.cases.ensure_ready = mock.Mock()

    def test_ready_check_loads_only_once_after_normal_unload_without_watchdog(self):
        self.test.one.return_value = {"devices": [{"device": "/dev/nst0", "busy": False}]}
        self.test.remote.side_effect = [SimpleNamespace(stdout=status("DR_OPEN", file=-1, block=-1).encode()),
                                        SimpleNamespace(stdout=status("BOT ONLINE", file=0).encode())]
        TarCases.ensure_ready(self.cases)
        self.test.run.assert_called_once_with(["mt", "-f", "/dev/nst0", "load"], timeout=None)
        self.test.run.reset_mock()
        self.test.remote.side_effect = [SimpleNamespace(stdout=status("ONLINE", file=0).encode())]
        TarCases.ensure_ready(self.cases)
        self.test.run.assert_not_called()
        self.test.remote.side_effect = [SimpleNamespace(stdout=status("DR_OPEN", file=-1, block=-1).encode())] * 2
        with self.assertRaisesRegex(RuntimeError, "did not become ready"):
            TarCases.ensure_ready(self.cases)
        self.test.run.assert_called_once()


    def baseline_fixture(self):
        self.state.update(prepare_complete=True, format_job_id="1", append_job_id="2")
        self.state.pop("baseline_complete", None)
        self.cases.inspect = mock.Mock()
        self.cases.settled = mock.Mock()
        self.cases.check_device_released = mock.Mock()
        self.cases.completed_archive = mock.Mock(return_value={})
        self.cases.export = mock.Mock(return_value={})
        self.cases.check_positions = mock.Mock(return_value={})
        self.cases.check_submitted_manifest = mock.Mock(return_value={})
        self.cases.device_settings = mock.Mock()
        self.cases.mam = mock.Mock(side_effect=[{"present": False, "length": 0, "value": ""},
                                                {"present": True, "length": 32, "value": "TST001"}])
        self.cases.refused_write = mock.Mock(return_value={"refusal": "cli"})
        self.cases.check_append_log = mock.Mock(return_value={"tail_verified": True})
        profile = {"format": "tar_pax_v1", "tar": {"initialization_id": "frozen",
                   "record_mode": "TAPE_RECORD_MODE_VARIABLE", "record_bytes": self.test.args.record_bytes}}
        captured = self.directory / "captured"
        captured.mkdir()
        self.cases.capture_job = mock.Mock(return_value=captured)
        self.test.one.return_value = {"media": [{"id": "9", "identity": "TST001", "encryption_key_saved": True,
                                                  "profile": {"tape": profile}}]}
        return profile

    def test_explicit_record_policy_keeps_format_flags_and_never_changes_settings(self):
        self.baseline_fixture()
        self.cases.baseline()
        commands = [call.args for call in self.test.cli.call_args_list]
        self.assertEqual(commands[0][commands[0].index("--record-mode") + 1], "variable")
        self.assertEqual(commands[0][commands[0].index("--record-bytes") + 1], "262144")
        self.assertFalse(any("--hardware-encryption" in command or "--encryption-algorithm" in command for command in commands))
        self.assertNotIn("--record-mode", commands[1])
        self.assertNotIn("--record-bytes", commands[1])
        self.assertFalse(any("--device-json" in call.args for call in self.test.one.call_args_list))
        self.assertEqual([call.args[0] for call in self.cases.mam.call_args_list], ["format-before", "format-after"])
        self.assertEqual([call.args[0] for call in self.cases.refused_write.call_args_list], ["format", "append"])
        self.cases.check_append_log.assert_called_once()
        self.assertTrue(self.state["baseline_complete"])
        self.assertEqual(self.cases.check_submitted_manifest.call_count, 2)

    def test_restart_reuses_all_readback_paths(self):
        self.state.update(format_items={"format/one": {}, "format/two": {}}, append_items={"append/one": {}})
        self.cases.restore_selection, self.cases.scan, self.cases.public_recovery = mock.Mock(), mock.Mock(), mock.Mock()
        self.cases.device_settings, self.cases.learning = mock.Mock(), mock.Mock()
        self.cases.restore()
        self.assertEqual(self.cases.restore_selection.call_args_list, [
            mock.call("restore-full", ["append/one", "format/one", "format/two"]),
            mock.call("restore-selected", ["append/one", "format/two"])])
        self.cases.learning.assert_called_once_with(["append/one", "format/one", "format/two"])
        self.assertEqual(self.cases.scan.call_args_list, [mock.call(verify=False), mock.call(verify=True)])
        self.cases.public_recovery.assert_called_once()
        self.assertTrue(self.state["restore_complete"])

    def test_tar_cleanup_requires_independent_recovery_and_never_ejects(self):
        self.state.update(physical_format="tar_pax_v1", restore_complete=True, completed_stages=["prepare", "baseline", "restore"])
        with self.assertRaisesRegex(RuntimeError, "Cleanup requires"):
            self.cases.cleanup()
        self.test.one.assert_not_called()
        self.state["tar_recovery_complete"] = True
        check_cleanup_state(self.state)
        self.test.one.return_value = {"devices": [{"device": "/dev/nst0", "busy": False}]}
        self.cases.check_device_released()
        self.test.remote.assert_not_called()
        for devices in ([], [{"device": "/dev/nst0", "busy": True}]):
            self.test.one.return_value = {"devices": devices}
            with self.assertRaisesRegex(RuntimeError, "known idle"):
                self.cases.check_device_released()

    def test_stage_admission_marker_precedes_reads_and_prevents_replay(self):
        self.state.update(format_items={"format/one": {}}, append_items={"append/one": {}})
        self.cases.device_settings = mock.Mock()
        self.cases.restore_selection = mock.Mock(side_effect=RuntimeError("read boundary"))
        with self.assertRaisesRegex(RuntimeError, "read boundary"):
            self.cases.restore()
        self.assertTrue(self.state["tar_restore_started"])
        with self.assertRaisesRegex(RuntimeError, "unsubmitted"):
            self.cases.restore()
        self.cases.restore_selection.assert_called_once()

    def test_full_and_selected_restore_keep_two_identification_routes(self):
        item = {"file": {"expected": {"file_id": "3", "sha256": "YQ=="}, "media_path": "archive/member"}, "size_bytes": "1"}
        self.state.update(format_items={"format/one": item}, append_items={})
        row = {"file": {"file_id": "3", "file_version_id": "4", "target_path": "format/one"},
               "status": "COPY_STATUS_COMPLETED", "candidate": {"media_id": "9", "media_path": "archive/member"},
               "actual_sha256": "YQ==", "actual_size_bytes": "1"}
        self.cases.settled = mock.Mock()
        self.cases.check_device_released = mock.Mock()
        self.cases.capture_job = mock.Mock()
        self.cases.page = mock.Mock(return_value=[row])
        self.test.one.return_value = {"job": {"id": "7"}}
        self.cases.restore_selection("full", ["format/one"])
        self.cases.restore_selection("selected", ["format/one"])
        commands = [call.args for call in self.test.cli.call_args_list]
        # Both Restores identify the cartridge by its cartridge memory barcode; neither supplies a Media hint.
        self.assertEqual(commands, [("restore", "run", "tape", "7", "--device", "/dev/nst0")] * 2)
        for filename in ("full", "selected"):
            selection = json.loads((self.directory / "physical-evidence" / (filename + "-selection.json")).read_text())
            self.assertEqual(selection["identification"], "automatic cartridge memory barcode")
            self.assertGreaterEqual(selection["seconds"], 0)

    def test_position_checks_reject_hash_member_and_unpaired_anchor_false_positives(self):
        items = {"format/one": {"file": {"expected": {"sha256": "YQ=="}, "media_path": "archive/one"}, "size_bytes": "1"}}
        position = {"hash": "YQ==", "size": 1, "storage_metadata": {"tar": {"tape_file": "1"}}}
        self.assertEqual(self.cases.check_positions({"archive/one": position}, items)["paired_anchors"], 0)
        for changed in ({"hash": "Yg=="}, {"size": 2}, {"storage_metadata": {"tar": {"header": {"logical_object": "2"}}}}):
            with self.subTest(changed=changed), self.assertRaises(RuntimeError):
                self.cases.check_positions({"archive/one": {**position, **changed}}, items)
        with self.assertRaises(RuntimeError):
            self.cases.check_positions({}, items)

    def test_tar_artifact_capture_collects_only_read_observations(self):
        self.cases.copy_evidence = mock.Mock()
        path = "/owned/install/work/jobs/7/tapes/TST001/read-observations.jsonl"
        self.test.remote.return_value = SimpleNamespace(returncode=1, stdout=b"")
        self.cases.tape_evidence("7")
        self.cases.tape_evidence("7", verify=True)
        self.cases.copy_evidence.assert_not_called()
        self.assertEqual({tuple(call.args[0]) for call in self.test.remote.call_args_list}, {("test", "-f", path)})
        self.test.remote.return_value = SimpleNamespace(returncode=0, stdout=b"")
        self.cases.tape_evidence("7")
        self.cases.copy_evidence.assert_called_once_with(path, "jobs/7/tapes/TST001/read-observations.jsonl")
        self.cases.keep("jobs/8/tapes/TST001/read-observations.jsonl", b"{}\n")
        self.test.remote.return_value = SimpleNamespace(returncode=1, stdout=b"")
        with self.assertRaisesRegex(RuntimeError, "disappeared"):
            self.cases.tape_evidence("8", verify=True)

    def test_media_and_library_evidence_never_save_encoded_key(self):
        key = "v1:" + "ab" * 32
        profile = {"format": "tar_pax_v1",
                   "tar": {"initialization_id": "frozen", "record_mode": "TAPE_RECORD_MODE_VARIABLE",
                           "record_bytes": 262144, "encryption_algorithm": "AES"}}
        media = {"id": "9", "identity": "TST001", "encryption_key_saved": True,
                 "profile": {"tape": {**profile, "unknown_secret": key}}}
        self.test.one.return_value = {"media": [media]}
        identifier, public = self.cases.media()
        self.assertEqual(identifier, "9")
        self.assertNotIn(key, json.dumps(public))
        self.assertEqual(public["tar"]["encryption_algorithm"], "AES")
        self.assertNotIn("unknown_secret", public)
        # Media replies only report whether the Library retains the key.
        del media["encryption_key_saved"]
        with self.assertRaisesRegex(RuntimeError, "Frozen tar profile/options differ"):
            self.cases.media()
        self.test.private_cli.assert_not_called()
        # The Library export still carries the key; its one key field must not reach evidence.
        profile["encryption"] = key
        self.test.cli.return_value = [{"type": "header", "format": "yatm-library-backup"},
                                      {"type": "media", "data": {"profile": {"tape": profile}}}, {"type": "end"}]
        self.cases.export("public-library.jsonl")
        self.assertNotIn(key, (self.cases.evidence / "public-library.jsonl").read_text())
        self.test.remote.return_value = SimpleNamespace(stdout=json.dumps({"profile": {"tape": profile}}).encode())
        self.cases.copy_evidence("/owned/job.json", "jobs/1/job.json")
        self.assertNotIn(key, (self.cases.evidence / "jobs/1/job.json").read_text())

    def test_submitted_manifest_checks_existing_item_rows_after_storage_publication(self):
        captured = self.directory / "job"
        captured.mkdir()
        database = captured / "state.db"
        with closing(sqlite3.connect(database)) as db:
            db.execute("CREATE TABLE items (id INTEGER, status INTEGER, size INTEGER, media_id INTEGER, "
                       "target_path TEXT, media_path TEXT, result BLOB)")
            db.execute("INSERT INTO items VALUES (7,4,1,9,'format/one','archive/one',NULL)")
            db.commit()
        items = {"format/one": {"id": "7", "status": "COPY_STATUS_SUBMITTED", "size_bytes": "1", "media_id": "9",
                  "file": {"target_path": "format/one", "media_path": "archive/one"}}}
        self.assertEqual(self.cases.check_submitted_manifest(captured, items),
                         {"members": 1, "pending": 0, "submitted_results_cleared": True})
        for column, value in (("status", 3), ("size", 2), ("media_id", 10), ("target_path", "other"),
                              ("media_path", "other"), ("result", b"staged-storage")):
            with self.subTest(column=column), closing(sqlite3.connect(database)) as db:
                db.execute("BEGIN")
                db.execute("UPDATE items SET " + column + "=?", (value,))
                db.commit()
                with self.assertRaisesRegex(RuntimeError, "submitted Item"):
                    self.cases.check_submitted_manifest(captured, items)
                db.execute("DELETE FROM items")
                db.execute("INSERT INTO items VALUES (7,4,1,9,'format/one','archive/one',NULL)")
                db.commit()
        with self.assertRaisesRegex(RuntimeError, "manifest membership"):
            self.cases.check_submitted_manifest(captured, {})
        self.test.remote.assert_not_called()

    def test_submitted_empty_member_accepts_omitted_protobuf_zero_size(self):
        captured = self.directory / "empty-job"
        captured.mkdir()
        database = captured / "state.db"
        with closing(sqlite3.connect(database)) as db:
            db.execute("CREATE TABLE items (id INTEGER, status INTEGER, size INTEGER, media_id INTEGER, "
                       "target_path TEXT, media_path TEXT, result BLOB)")
            db.execute("INSERT INTO items VALUES (1,4,0,9,'short/empty','recorded/empty',NULL)")
            db.commit()
        # ProtoJSON omits scalar zero; absence must mean zero rather than an unknown file size.
        items = {"short/empty": {"id": "1", "status": "COPY_STATUS_SUBMITTED", "media_id": "9",
                 "file": {"target_path": "short/empty", "media_path": "recorded/empty"}}}
        self.assertEqual(self.cases.check_submitted_manifest(captured, items),
                         {"members": 1, "pending": 0, "submitted_results_cleared": True})
        with closing(sqlite3.connect(database)) as db:
            db.execute("UPDATE items SET size=1")
            db.commit()
        with self.assertRaisesRegex(RuntimeError, "submitted Item"):
            self.cases.check_submitted_manifest(captured, items)
        self.test.remote.assert_not_called()

    def test_public_reader_uses_saved_key_and_incomplete_oracle_keeps_service_stopped(self):
        profile = {"initialization_id": "frozen", "record_mode": "TAPE_RECORD_MODE_VARIABLE", "record_bytes": 262144}
        self.state["tar_profile"] = {"format": "tar_pax_v1", "tar": profile}
        self.test.private_cli.return_value = library_export("v1:" + "ab" * 32)
        review = {"identity": {"format": "tar_pax_v1", "barcode": "TST001", "profile": profile},
                  "record_setting_restored": True, "end": "DRIVER_REPORTED_EOD", "files": [{"boundary": "FILEMARK"}] * 3}
        self.cases.check_device_released = mock.Mock()
        self.test.remote.side_effect = lambda argv, **kwargs: recovery_response(argv)
        for mutation in ({"end": "UNKNOWN"}, {"record_setting_restored": False}, {"files": []},
                         {"identity": {"format": "tar_pax_v1", "barcode": "OTHER1", "profile": profile}}):
            self.cases.keep_json("public-recovery/review.json", {**review, **mutation})
            with self.subTest(mutation=mutation), self.assertRaisesRegex(RuntimeError, "acceptance is incomplete"):
                self.cases.public_recovery()
            self.assertNotIn("tar_recovery_complete", self.state)
            self.test.start_owned.assert_not_called()
        self.cases.keep_json("public-recovery/review.json", review)
        self.cases.public_recovery()
        self.assertTrue(self.state["tar_recovery_complete"])
        self.test.start_owned.assert_called_once()
        reader_commands = [call.args[0] for call in self.test.run.call_args_list if call.args[0][0] == "python3"]
        self.assertTrue(reader_commands)
        for argv in reader_commands:
            self.assertEqual([arg for arg in argv if arg.startswith("--")],
                             ["--device", "--record-mode", "--record-bytes", "--fixture", "--out",
                              "--encrypt-script", "--key-file", "--barcode", "--tape-name", "--script-directory"])

    def test_public_recovery_copies_only_bounded_receipts_and_keeps_payload_on_host(self):
        profile = {"initialization_id": "frozen", "record_mode": "TAPE_RECORD_MODE_VARIABLE", "record_bytes": 262144}
        self.state["tar_profile"] = {"format": "tar_pax_v1", "tar": profile}
        self.test.private_cli.return_value = library_export("v1:" + "ab" * 32)
        review = {"identity": {"format": "tar_pax_v1", "barcode": "TST001", "profile": profile},
                  "record_setting_restored": True, "end": "DRIVER_REPORTED_EOD", "files": [{"boundary": "FILEMARK"}] * 3}
        self.cases.keep_json("public-recovery/review.json", review)
        output = "/owned/tmp/public-recovery"
        receipts = ["review.json", "commands.json", "read-1.out", "read-1.err"]
        payloads = ["file-1/archive.tar", "file-1/extracted/member", "tail.bin"]
        self.cases.check_device_released = mock.Mock()
        self.cases.copy_evidence = mock.Mock()

        def remote(argv, **kwargs):
            if argv[0] == "mktemp":
                return recovery_response(argv)
            if argv[0] == "find":
                return SimpleNamespace(stdout=b"\0".join((output + "/" + name).encode() for name in receipts + payloads))
            if argv[0] == "stat":
                self.assertIn(argv[-1], [output + "/" + name for name in receipts])
                return SimpleNamespace(stdout=b"32\n")
            return SimpleNamespace(stdout=b"")

        self.test.remote.side_effect = remote
        self.cases.public_recovery()
        self.assertEqual([call.args for call in self.cases.copy_evidence.call_args_list],
                         [(output + "/" + name, "public-recovery/" + name) for name in receipts])
        self.assertIn(mock.call(["rm", "-rf", "--", "/owned/tmp/public-key.12345678"]), self.test.remote.call_args_list)

        self.cases.copy_evidence.reset_mock()
        self.test.start_owned.reset_mock()
        self.test.remote.side_effect = lambda argv, **kwargs: SimpleNamespace(
            stdout=(str((16 << 20) + 1).encode() if argv[0] == "stat" else
                    (output + "/review.json").encode() if argv[0] == "find" else
                    recovery_response(argv).stdout))
        with self.assertRaisesRegex(RuntimeError, "evidence allowance"):
            self.cases.public_recovery()
        self.cases.copy_evidence.assert_not_called()
        self.test.start_owned.assert_not_called()


    def test_public_recovery_waits_without_watchdog_and_cleans_local_key_on_success_or_failure(self):
        key = "ab" * 32
        profile = {"initialization_id": "frozen", "record_mode": "TAPE_RECORD_MODE_VARIABLE", "record_bytes": 262144}
        self.state["tar_profile"] = {"format": "tar_pax_v1", "tar": profile}
        self.test.private_cli.return_value = library_export("v1:" + key, name="Tape name *")
        review = {"identity": {"format": "tar_pax_v1", "barcode": "TST001", "profile": profile},
                  "record_setting_restored": True, "end": "DRIVER_REPORTED_EOD", "files": [{"boundary": "FILEMARK"}] * 3}
        self.cases.keep_json("public-recovery/review.json", review)
        for failure in (False, True):
            seen = []
            self.state.pop("tar_recovery_complete", None)
            self.test.remote.reset_mock()
            self.test.run.reset_mock()
            self.test.start_owned.reset_mock()
            def remote(argv, **kwargs):
                self.assertNotIn(key, " ".join(argv))
                self.assertNotEqual(argv[0], "python3", "Public recovery must not use the remote watchdog")
                if argv[0] == "sh":
                    self.assertEqual(argv, ["sh", "-c", 'umask 077; cat > "$1" && chmod 600 "$1"',
                                            "yatm-public-key", "/owned/tmp/public-key.12345678/key"])
                    self.assertEqual(kwargs["body"], key.encode())
                return recovery_response(argv)
            def run(argv, **kwargs):
                self.assertEqual(argv[0], "python3")
                self.assertEqual(kwargs, {"timeout": None})
                self.assertNotIn(key, " ".join(argv))
                path = argv[argv.index("--key-file") + 1]
                seen.append(path)
                self.assertEqual(path, "/owned/tmp/public-key.12345678/key")
                self.assertEqual(argv[argv.index("--script-directory") + 1], "/owned/install")
                if failure:
                    raise RuntimeError("stub reader failed")
                return SimpleNamespace(stdout=b"")
            self.test.remote.side_effect = remote
            self.test.run.side_effect = run
            with self.subTest(failure=failure):
                if failure:
                    with self.assertRaisesRegex(RuntimeError, "stub reader failed"):
                        self.cases.public_recovery()
                    self.assertNotIn("tar_recovery_complete", self.state)
                    self.test.start_owned.assert_not_called()
                else:
                    self.cases.public_recovery()
                    self.assertTrue(self.state["tar_recovery_complete"])
                    self.test.start_owned.assert_called_once()
                self.assertEqual(len(seen), 1)
                self.test.run.assert_called_once()
                self.assertIn(mock.call(["rm", "-rf", "--", "/owned/tmp/public-key.12345678"]),
                              self.test.remote.call_args_list)
        self.assertNotIn(key, json.dumps(self.state))


class PublicBoundaryTests(unittest.TestCase):

    def test_configured_recovery_uses_script_contract_and_redacts_its_output(self):
        for exit_code in (0, 17):
            with self.subTest(exit_code=exit_code), tempfile.TemporaryDirectory() as directory:
                root = Path(directory)
                key_file = root / "key"
                key = b"ab" * 32
                key_file.write_bytes(key)
                key_file.chmod(0o600)
                reader = PublicReader("/dev/stub", "variable", 262144, root / "fixture", root)
                reader.encryption = {"key_file": str(key_file), "encrypt_script": "/configured/encrypt",
                                     "script_directory": "/service cwd", "barcode": "TST001", "tape_name": "Tape * name"}
                process = mock.Mock(returncode=exit_code)
                process.communicate.return_value = (key, b"diagnostic v1:" + key)
                with mock.patch("physical_tar_recovery.subprocess.Popen", return_value=process) as start:
                    if exit_code:
                        with self.assertRaisesRegex(RuntimeError, "adapter failed"):
                            reader.configure_encryption()
                    else:
                        reader.configure_encryption()
                self.assertEqual(start.call_args.args[0][-2:], ["/service cwd", "/configured/encrypt"])
                environment = start.call_args.kwargs["env"]
                self.assertEqual({name: environment[name] for name in
                                  ("DEVICE", "KEY_FILE", "TAPE_BARCODE", "TAPE_NAME", "TAPE_DIR")},
                                 {"DEVICE": "/dev/stub", "KEY_FILE": str(key_file), "TAPE_BARCODE": "TST001",
                                  "TAPE_NAME": "Tape * name", "TAPE_DIR": str(root.resolve())})
                for name in ("commands.json", "configure-encryption.out", "configure-encryption.err"):
                    self.assertNotIn(key, (root / name).read_bytes())
                self.assertFalse(reader.unsettled)
                key_file.chmod(0o644)
                with mock.patch("physical_tar_recovery.subprocess.Popen") as start:
                    with self.assertRaisesRegex(RuntimeError, "owner-only"):
                        reader.configure_encryption()
                    start.assert_not_called()

    def test_partial_encryption_options_stop_before_any_reader(self):
        with mock.patch("physical_tar_recovery.PublicReader") as reader:
            with self.assertRaisesRegex(RuntimeError, "together"):
                recovery_main(["--device", "/dev/stub", "--record-mode", "variable", "--record-bytes", "262144",
                               "--fixture", "/unused/fixture", "--out", "/unused/output", "--key-file", "/unused/key"])
            reader.assert_not_called()

    def test_three_file_copy_requires_actual_filemarks_and_eod(self):
        for mode in ("fixed", "variable"):
            with self.subTest(mode=mode), tempfile.TemporaryDirectory() as directory:
                output = Path(directory) / "reader"
                output.mkdir()
                selected = 262144 if mode == "fixed" else 0
                reader = PublicReader("/dev/nst0", mode, 262144, Path(directory) / "fixture.json", output)
                reader.prepare = mock.Mock(return_value=({}, ["/dev/nst0"], 1024))

                def command(name, argv, expected=0):
                    observed = ""
                    if name.startswith("read-"):
                        filename = next(arg[3:] for arg in argv if arg.startswith("of="))
                        Path(filename).write_bytes(b"" if name == "read-tail" else bytes(reader.record_bytes))
                    elif name == "selected-status":
                        observed = status("ONLINE", file=0, size=selected)
                    elif name.startswith("post-close-"):
                        observed = status(file=int(name.rsplit("-", 1)[1]) + 1, size=selected)
                    elif name == "post-tail":
                        observed = status("EOD ONLINE", file=3, block=-1, size=selected)
                    elif name == "restored-status":
                        observed = status("EOD ONLINE", file=3, block=-1, size=1024)
                    elif name == "extract-0":
                        label = output / "file-0" / "extracted" / "YATM"
                        label.mkdir()
                        (label / "identity.json").write_text('{"format":"tar_pax_v1"}')
                        (label / "README.txt").write_text("Public recovery fixture")
                    return subprocess.CompletedProcess(argv, expected, observed.encode(), b"")

                with mock.patch.object(reader, "run", side_effect=command) as run, \
                        mock.patch.object(Path, "read_bytes", side_effect=AssertionError("Archive checksum must stream")), \
                        mock.patch("physical_tar_recovery.check_extracted") as check:
                    result = reader.read()
                reads = [call.args[1] for call in run.call_args_list if call.args[0].startswith("read-")]
                self.assertEqual(len(reads), 4)
                self.assertTrue(all("conv=excl" in argv and not any(arg.startswith("count=") for arg in argv)
                                    for argv in reads))
                self.assertEqual([row["boundary"] for row in result["files"]], ["FILEMARK"] * 3)
                self.assertEqual(result["end"], "DRIVER_REPORTED_EOD")
                self.assertTrue(result["record_setting_restored"])
                self.assertEqual([call.args[2] for call in check.call_args_list], ["format", "append"])
                self.assertTrue(all(row["sha256"] == hashlib.sha256(bytes(reader.record_bytes)).hexdigest()
                                    for row in result["files"]))

    def test_success_exit_with_diagnostic_never_passes_public_command_boundary(self):
        with tempfile.TemporaryDirectory() as directory:
            reader = PublicReader("/dev/nst0", "variable", 262144, Path(directory) / "fixture.json", directory)
            process = mock.Mock(returncode=0)
            process.communicate.return_value = (b"", b"unknown driver warning")
            with mock.patch("physical_tar_recovery.subprocess.Popen", return_value=process) as run:
                with self.assertRaisesRegex(RuntimeError, "emitted a diagnostic"):
                    reader.run("read-0", ["dd", "if=/dev/nst0"])
            run.assert_called_once_with(["dd", "if=/dev/nst0"], stdout=-1, stderr=-1)
            self.assertEqual((Path(directory) / "read-0.err").read_bytes(), b"unknown driver warning")
            self.assertFalse(reader.unsettled)
            self.assertFalse((Path(directory) / "review.json").exists())

    def test_interrupted_execution_keeps_receipt_and_blocks_further_commands(self):
        with tempfile.TemporaryDirectory() as directory:
            reader = PublicReader("/dev/nst0", "variable", 262144, Path(directory) / "fixture.json", directory)
            interruption = KeyboardInterrupt("injected interruption")
            process = mock.Mock()
            process.communicate.side_effect = [interruption, RuntimeError("completion still unknown")]
            def start(*args, **kwargs):
                command = json.loads((Path(directory) / "commands.json").read_text())[-1]
                self.assertEqual(command, {"name": "read-0", "argv": ["dd", "if=/dev/nst0"], "unsettled": True})
                return process
            with mock.patch("physical_tar_recovery.subprocess.Popen", side_effect=start) as run:
                with self.assertRaises(KeyboardInterrupt) as raised:
                    reader.run("read-0", ["dd", "if=/dev/nst0"])
                with self.assertRaisesRegex(RuntimeError, "completion is unknown"):
                    reader.status("forbidden-status")
            self.assertIs(raised.exception, interruption)
            run.assert_called_once()
            process.kill.assert_not_called()
            process.terminate.assert_not_called()
            commands = json.loads((Path(directory) / "commands.json").read_text())
            self.assertEqual(len(commands), 1)
            self.assertTrue(commands[0]["unsettled"])
            self.assertEqual(commands[0]["error"], "KeyboardInterrupt: injected interruption")
            self.assertEqual(commands[0]["wait_error"], "RuntimeError: completion still unknown")

    def test_filemark_requires_nonempty_copy_and_exclusive_eof_at_expected_position(self):
        self.assertEqual(check_boundary(status(), 0, 1024), "FILEMARK")
        for text, size in ((status(), 0), (status("EOD ONLINE"), 1024), (status("EOF EOD ONLINE"), 1024),
                           (status("EOF EOT ONLINE"), 1024), (status("EOF DR_OPEN"), 1024),
                           (status(file=2), 1024), (status(block=-1), 1024), (status().replace("partition=0", "partition=1"), 1024)):
            with self.subTest(text=text, size=size), self.assertRaises(RuntimeError):
                check_boundary(text, 0, size)

    def test_zero_read_alone_does_not_establish_eod(self):
        eod = status("EOD ONLINE IM_REP_EN", file=3, block=-1)
        self.assertEqual(check_boundary(eod, 3, 0, end=True), "DRIVER_REPORTED_EOD")
        for text, size in ((status(file=3), 0), (eod, 1), (eod.replace("EOD", "EOD EOT"), 0),
                           (eod.replace("EOD", "EOF EOD"), 0), (eod.replace("number=3", "number=4"), 0)):
            with self.subTest(text=text, size=size), self.assertRaises(RuntimeError):
                check_boundary(text, 3, size, end=True)

    def test_eod_uses_the_proven_file_count_including_two_file_format(self):
        eod = status("EOD ONLINE IM_REP_EN", file=2, block=-1)
        self.assertEqual(check_boundary(eod, 2, 0, end=True), "DRIVER_REPORTED_EOD")
        with self.assertRaisesRegex(RuntimeError, "EOD was not independently established"):
            check_boundary(eod, 3, 0, end=True)

    def test_unknown_status_syntax_or_flags_remains_unknown(self):
        for text in ("", status() + "unexpected\n", status().replace("EOF", "UNKNOWN"),
                     status().replace("SCSI 2", "SCSI 3"), status().replace("block number=0", "block number=?")):
            with self.subTest(text=text), self.assertRaisesRegex(RuntimeError, "Unknown"):
                parse_status(text)

    def test_record_policy_restores_only_after_settled_failures(self):
        def completed(stdout="", code=0):
            process = mock.Mock(returncode=code)
            process.communicate.return_value = (stdout.encode(), b"")
            return process

        for failure in ("read", "check", "interrupted", "start"):
            with self.subTest(failure=failure), tempfile.TemporaryDirectory() as root:
                fixture = Path(root) / "fixture.json"
                fixture.write_text("{}")
                output = Path(root) / "reader"
                reader = PublicReader("/dev/nst0", "variable", 262144, fixture, output)
                processes = [completed("mt-st v. 1.8\n"), completed("dd (coreutils) 9.7\n"),
                             completed("tar (GNU tar) 1.35\n"), completed("Linux acceptance\n"),
                             completed(code=1), completed('{"filesystems": []}'), completed(status(size=1024)),
                             completed(), completed(status()), completed()]
                read = completed(code=1 if failure == "read" else 0)
                if failure == "interrupted":
                    read.communicate.side_effect = [KeyboardInterrupt("injected interruption"), (b"", b"")]
                processes.append(OSError("injected start failure") if failure == "start" else read)
                if failure == "check":
                    processes.append(completed(status(size=1024)))
                if failure in ("read", "check"):
                    processes.extend([completed(), completed(status(size=1024))])
                error = KeyboardInterrupt if failure == "interrupted" else OSError if failure == "start" else RuntimeError
                with mock.patch("physical_tar_recovery.device_aliases", return_value=("/dev/nst0", ["/dev/nst0", "/dev/st0", "/dev/sg0"])), \
                        mock.patch("physical_tar_recovery.subprocess.Popen", side_effect=processes) as run:
                    with self.assertRaises(error):
                        reader.read()
                commands = json.loads((output / "commands.json").read_text())
                read_command = next(row for row in commands if row["name"] == "read-0")
                if failure in ("interrupted", "start"):
                    self.assertTrue(reader.unsettled)
                    self.assertTrue(read_command["unsettled"])
                    self.assertIn("error", read_command)
                    self.assertNotIn("exit", read_command)
                    self.assertEqual(commands[-1], read_command)
                    self.assertEqual(len(run.call_args_list), 11)
                    if failure == "interrupted":
                        self.assertEqual(read.communicate.call_count, 2)
                        read.kill.assert_not_called()
                        read.terminate.assert_not_called()
                else:
                    self.assertFalse(reader.unsettled)
                    self.assertFalse(read_command["unsettled"])
                    self.assertEqual(read_command["exit"], 1 if failure == "read" else 0)
                    self.assertEqual([row["name"] for row in commands[-2:]], ["restore-record-policy", "restored-status"])
                    self.assertEqual(commands[-2]["argv"], ["mt", "-f", "/dev/nst0", "setblk", "1024"])
                self.assertFalse((output / "review.json").exists())

    def test_unknown_tool_or_ownership_stops_before_driver_policy_changes(self):
        for change in ({"version-mt": "mt-st v. 1.9\n"}, {"device-users": "1234"},
                       {"mounts": '{"filesystems": [{"source": "/dev/st0"}]}'}):
            with self.subTest(change=change), tempfile.TemporaryDirectory() as root:
                fixture = Path(root) / "fixture.json"
                fixture.write_text("{}")
                reader = PublicReader("/dev/nst0", "fixed", 262144, fixture, Path(root) / "reader")
                observations = {"version-mt": "mt-st v. 1.8\n", "version-dd": "dd (coreutils) 9.7\n",
                                "version-tar": "tar (GNU tar) 1.35\n", "version-kernel": "Linux acceptance\n",
                                "mounts": '{"filesystems": []}', **change}
                def command(name, argv, expected=0):
                    return SimpleNamespace(stdout=observations.get(name, "").encode(), stderr=b"", returncode=expected)
                with mock.patch("physical_tar_recovery.device_aliases", return_value=("/dev/nst0", ["/dev/nst0", "/dev/st0", "/dev/sg0"])), \
                        mock.patch.object(reader, "run", side_effect=command) as run:
                    with self.assertRaises(RuntimeError):
                        reader.read()
                self.assertFalse(any(call.args[0] in ("set-record-policy", "read-0") for call in run.call_args_list))


class TarFixtureTests(unittest.TestCase):
    def test_record_bounds_reject_before_allocation(self):
        with tempfile.TemporaryDirectory() as directory:
            for size in (None, 0, 513, MAX_RECORD_BYTES + 512, 2**32):
                with self.subTest(size=size), self.assertRaisesRegex(RuntimeError, "0xfffe00"):
                    generate(Path(directory) / "source", Path(directory) / "fixture.json", size)
            self.assertEqual(list(Path(directory).iterdir()), [])

    def test_nine_deterministic_boundary_files_metadata_and_false_positive_rejection(self):
        with tempfile.TemporaryDirectory() as directory, ExitStack() as stack:
            if not hasattr(os, "setxattr"):
                # The Linux helper uses real xattrs. macOS's Python omits these APIs;
                # substitute only that OS boundary so content/time/mode checks still run.
                attrs = {}
                stack.enter_context(mock.patch.object(os, "setxattr", create=True,
                    side_effect=lambda path, name, value: attrs.__setitem__((str(path), name), value)))
                stack.enter_context(mock.patch.object(os, "getxattr", create=True,
                    side_effect=lambda path, name: attrs[(str(path), name)]))
            root, manifest = Path(directory) / "source", Path(directory) / "fixture.json"
            values = generate(root, manifest, 262144)
            self.assertEqual([row["size"] for row in values.values()], [0, 1, 511, 512, 513, 262143, 262144, 262145, 1048613])
            self.assertEqual(values["format/empty"]["sha256"], hashlib.sha256(b"").hexdigest())
            self.assertEqual(values["format/one"]["sha256"], "68325720aabd7c82f30f554b313d0570c95accbb7dc4b5aae11204c08ffe732b")
            for index, (name, facts) in enumerate(values.items()):
                self.assertEqual(facts["mode"], (0o600, 0o640, 0o750)[index % 3])
                self.assertEqual(facts["mtime_ns"], str(1700000000123456789 + index * 10000003))
                self.assertEqual(base64.b64decode(facts["xattrs"]["user.yatm_empty"]), b"")
                self.assertEqual(base64.b64decode(facts["xattrs"]["user.yatm_binary"]), b"\x00\xff\n\x01" + name.encode())
            self.assertEqual(check(manifest, root, exact=True)["checked"], 9)
            with mock.patch("sys.stdout"):
                recovery_main(["fixture", "check", str(root), str(manifest), "--exact"])
            with self.assertRaisesRegex(RuntimeError, "must both be new"):
                generate(root, manifest, 262144)
            extracted = Path(directory) / "extracted"
            shutil.copytree(root / "format", extracted / "arbitrary-prefix" / "format", copy_function=shutil.copy2)
            for name, facts in values.items():
                if name.startswith("format/"):
                    for attr, value in facts["xattrs"].items():
                        os.setxattr(extracted / "arbitrary-prefix" / name, attr, base64.b64decode(value))
            self.assertEqual(check_extracted(manifest, extracted, "format")["checked"], 5)
            (extracted / "extra").write_bytes(b"")
            with self.assertRaisesRegex(RuntimeError, "unexpected"):
                check_extracted(manifest, extracted, "format")
            (extracted / "extra").unlink()
            (extracted / "arbitrary-prefix/format/one").write_bytes(b"x")
            with self.assertRaisesRegex(RuntimeError, "mismatch"):
                check_extracted(manifest, extracted, "format")



def attributes(*entries):
    """Raw READ ATTRIBUTE response from (identifier, format, value) entries."""
    body = b"".join(identifier.to_bytes(2, "big") + bytes([kind]) + len(value).to_bytes(2, "big") + value
                    for identifier, kind, value in entries)
    return len(body).to_bytes(4, "big") + body


def done(stdout=b"", returncode=0, stderr=b""):
    return SimpleNamespace(stdout=stdout, returncode=returncode, stderr=stderr)


class TarStageArgumentTests(TarArgumentsTests):
    confirmation = "CLEAR BARCODE /dev/nst0 /dev/sg3 TST001"

    def test_clear_barcode_requires_exact_device_sg_and_barcode_confirmation(self):
        stage = {"physical-stage": "tar-clear-barcode"}
        state = Path(self.options["state"])
        state.write_text("{}")
        self.assertEqual(self.arguments(**stage, **{"confirm-clear-barcode": self.confirmation}).confirm_clear_barcode,
                         self.confirmation)
        for change in ({}, {"confirm-clear-barcode": "CLEAR BARCODE /dev/nst0 /dev/sg4 TST001"},
                       {"confirm-clear-barcode": "CLEAR BARCODE /dev/nst0 TST001"}):
            with self.subTest(change=change), self.assertRaisesRegex(RuntimeError, "confirm-clear-barcode"):
                self.arguments(**stage, **change)
        state.unlink()
        with self.assertRaisesRegex(RuntimeError, "other stages reject"):
            self.arguments(**{"confirm-clear-barcode": self.confirmation})
        for change in ({"sg-device": None}, {"sg-device": "/dev/nst1"}):
            with self.subTest(change=change), self.assertRaisesRegex(RuntimeError, "sg-device"):
                self.arguments(**change)

    def test_fill_alone_accepts_the_capacity_payload_plan(self):
        Path(self.options["state"]).write_text("{}")
        fill = self.arguments(**{"physical-stage": "tar-fill", "capacity-file-mib": 65536,
                                 "capacity-source-reflink": True})
        self.assertEqual(fill.capacity_file_mib, [65536])
        self.assertTrue(fill.capacity_source_reflink)
        with self.assertRaisesRegex(RuntimeError, "payload plan"):
            self.arguments(**{"physical-stage": "tar-fill"})
        ew = self.arguments(**{"physical-stage": "tar-ew", "capacity-file-mib": 1024})
        self.assertEqual(ew.capacity_file_mib, [1024])
        with self.assertRaisesRegex(RuntimeError, "payload plan"):
            self.arguments(**{"physical-stage": "tar-ew"})
        with self.assertRaisesRegex(RuntimeError, "does not accept"):
            self.arguments(**{"physical-stage": "tar-read-order", "capacity-file-mib": 1024})
        Path(self.options["state"]).unlink()
        for change in ({"capacity-file-mib": 1024}, {"capacity-source-reflink": True}):
            with self.subTest(change=change), self.assertRaisesRegex(RuntimeError, "does not accept"):
                self.arguments(**change)
        Path(self.options["state"]).write_text("{}")
        with self.assertRaisesRegex(RuntimeError, "does not accept"):
            self.arguments(**{"physical-stage": "tar-fill", "capacity-file-mib": 1024, "capacity-observe": True})

    def test_tar_stages_follow_their_named_prerequisites_once(self):
        prepared = self.arguments()
        state = self.state(prepared)
        stages = (("tar-clear-barcode", ["prepare"], {"confirm-clear-barcode": self.confirmation}),
                  ("tar-fill", ["prepare", "baseline", "restore"], {"capacity-file-mib": 1024}),
                  ("tar-ew", ["prepare", "baseline", "restore", "tar-fill"], {"capacity-file-mib": 1024}),
                  ("tar-read-order", ["prepare", "baseline", "restore", "tar-fill", "tar-ew"], {}))
        for stage, completed, options in stages:
            Path(prepared.state).write_text("{}")
            args = self.arguments(**{"physical-stage": stage, **options})
            validate_state(args, {**state, "completed_stages": completed})
            with self.subTest(stage=stage), self.assertRaisesRegex(RuntimeError, "Complete"):
                validate_state(args, {**state, "completed_stages": completed[:-1]})
            marker = "tar_" + stage.removeprefix("tar-").replace("-", "_") + "_started"
            with self.subTest(stage=stage, marker=marker), self.assertRaisesRegex(RuntimeError, "already admitted"):
                validate_state(args, {**state, "completed_stages": completed, marker: True})
        # Baseline needs only prepare: clearing is optional when MAM 0806h is already empty.
        Path(prepared.state).write_text("{}")
        validate_state(self.arguments(**{"physical-stage": "baseline"}), state)


class TarMamTests(unittest.TestCase):
    def setUp(self):
        temporary = tempfile.TemporaryDirectory()
        self.addCleanup(temporary.cleanup)
        self.directory = Path(temporary.name)
        self.test = mock.Mock()
        self.test.args = SimpleNamespace(physical_device="/dev/nst0", physical_barcode="TST001", sg_device="/dev/sg3",
                                         physical_format="tar_pax_v1", record_mode="variable", record_bytes=262144,
                                         host="local", out=self.directory)
        self.test.root, self.test.install, self.test.url = "/owned", "/owned/install", "http://127.0.0.1:1"
        self.test.service = "yatm-acceptance-12345678.service"
        self.state = {"prepare_complete": True, "source_location": "1", "restored_location": "2", "mismatch_job_id": "3"}
        self.cases = TarCases(self.test, self.state, mock.Mock())
        self.cases.ensure_ready = mock.Mock()
        self.cases.check_device_released = mock.Mock()
        self.aliases = ["/dev/nst0", "/dev/sg3", "/dev/st0"]
        patcher = mock.patch("package_physical_tar.device_aliases", return_value=("/dev/nst0", self.aliases))
        patcher.start()
        self.addCleanup(patcher.stop)

    def test_mam_barcode_decoding_keeps_absent_empty_and_padded_values_distinct(self):
        padded = b"TST001".ljust(32)
        self.assertEqual(parse_mam_barcode(attributes()), {"present": False, "length": 0, "value": ""})
        self.assertEqual(parse_mam_barcode(attributes((0x0806, 1, b""))), {"present": True, "length": 0, "value": ""})
        self.assertEqual(parse_mam_barcode(attributes((0x0400, 1, b"HP      "), (0x0806, 1, padded), (0x0807, 1, b"x"))),
                         {"present": True, "length": 32, "value": "TST001"})
        self.assertEqual(parse_mam_barcode(attributes((0x0806, 1, b" " * 32)))["value"], "")
        for raw in (b"\x00\x00", attributes((0x0806, 1, padded))[:-1], attributes((0x0806, 0, padded)),
                    attributes((0x0806, 1, b"\xffBAD")), attributes((0x0806, 1, padded), (0x0806, 1, padded)),
                    b"\x00\x00\x00\x03\x08\x06\x01"):
            with self.subTest(raw=raw), self.assertRaises(RuntimeError):
                parse_mam_barcode(raw)

    def test_clear_barcode_sends_exact_write_attribute_and_requires_an_empty_readback(self):
        writes, order = {}, []
        responses = {"before": attributes((0x0806, 1, b"OLD001".ljust(32))), "after": attributes()}

        def remote(argv, **kwargs):
            order.append(argv[0])
            if argv[0] == "fuser":
                self.assertEqual(argv[1:], self.aliases)
                return done(returncode=1)
            if argv[0] == "sg_read_attr":
                self.assertEqual(argv, ["sg_read_attr", "--raw", "/dev/sg3"])
                return done(responses["after" if "sg_raw" in order else "before"])
            if argv[0] == "sg_raw":
                self.assertTrue(self.state["tar_clear_barcode_started"], "Admission marker must precede WRITE ATTRIBUTE")
                return done()
            raise AssertionError(argv)

        self.test.remote.side_effect = remote
        self.test.write.side_effect = lambda path, data: writes.__setitem__(path, data)
        self.test.one.side_effect = [{"media": []}, {"device": {"device": "/dev/nst0"}, "tape": {"medium_present": True}}]
        self.cases.clear_barcode()
        parameters = "/owned/tmp/clear-barcode-parameters.bin"
        self.assertEqual(writes, {parameters: bytes([0, 0, 0, 5, 8, 6, 1, 0, 0])})
        sg_raw = next(call.args[0] for call in self.test.remote.call_args_list if call.args[0][0] == "sg_raw")
        self.assertEqual(sg_raw, ["sg_raw", "-s", "9", "-i", parameters, "/dev/sg3",
                                  "8D", "00", "00", "00", "00", "00", "00", "00", "00", "00", "00", "00", "00", "09", "00", "00"])
        self.assertEqual(order, ["fuser", "sg_read_attr", "sg_raw", "sg_read_attr"])
        self.assertTrue(self.state["barcode_cleared"])
        self.assertEqual(self.test.one.call_args_list[-1].args, ("tape", "device", "inspect", "--device", "/dev/nst0"))
        receipt = json.loads((self.directory / "physical-evidence/mam/clear-write.json").read_text())
        self.assertEqual(receipt["parameters_hex"], "00 00 00 05 08 06 01 00 00")
        self.assertEqual(receipt["before"]["value"], "OLD001")

    def test_clear_barcode_refusal_or_remaining_barcode_stops_without_fallback(self):
        for failure in ("refused", "remaining", "reported", "registered", "owned"):
            with self.subTest(failure=failure):
                self.state.pop("tar_clear_barcode_started", None)
                self.state.pop("barcode_cleared", None)
                calls = []

                def remote(argv, **kwargs):
                    calls.append(argv[0])
                    if argv[0] == "fuser":
                        return done(b"/dev/sg3: 123" if failure == "owned" else b"", 0 if failure == "owned" else 1)
                    if argv[0] == "sg_raw":
                        return done(returncode=99, stderr=b"ILLEGAL REQUEST") if failure == "refused" else done()
                    remaining = failure == "remaining" and "sg_raw" in calls
                    return done(attributes((0x0806, 1, b"OLD001".ljust(32))) if remaining else attributes())

                self.test.remote.side_effect = remote
                inspect = {"tape": {"barcode": "OLD001"}} if failure == "reported" else {"tape": {}}
                media = {"media": [{"identity": "TST001"}]} if failure == "registered" else {"media": []}
                self.test.one.side_effect = [media, inspect]
                with self.assertRaises(RuntimeError):
                    self.cases.clear_barcode()
                self.assertLessEqual(calls.count("sg_raw"), 1)
                self.assertNotIn("barcode_cleared", self.state)
                if failure in ("registered", "owned"):
                    self.assertNotIn("sg_raw", calls)
                if failure == "refused":
                    self.assertEqual(calls[-1], "sg_raw")
                    receipt = json.loads((self.directory / "physical-evidence/mam/clear-write.json").read_text())
                    self.assertEqual((receipt["exit_code"], receipt["stderr"]), (99, "ILLEGAL REQUEST"))

    def test_clear_barcode_is_refused_after_format(self):
        self.state.update(tar_baseline_started=True)
        with self.assertRaisesRegex(RuntimeError, "before FORMAT"):
            self.cases.clear_barcode()
        self.test.remote.assert_not_called()

    def baseline(self, before, after):
        self.state.update(format_job_id="1", append_job_id="2")
        for name in ("inspect", "settled", "completed_archive", "export", "check_positions", "check_submitted_manifest",
                     "device_settings", "check_append_log", "refused_write"):
            setattr(self.cases, name, mock.Mock(return_value={}))
        self.cases.capture_job = mock.Mock(return_value=self.directory)
        self.cases.media = mock.Mock(return_value=("9", {"tar": {}}))
        self.cases.mam = mock.Mock(side_effect=[before, after])
        self.cases.baseline()

    def test_format_requires_empty_mam_before_and_requested_barcode_after(self):
        empty, written = {"value": "", "length": 0}, {"value": "TST001", "length": 32}
        with self.assertRaisesRegex(RuntimeError, "not empty before FORMAT"):
            self.baseline({"value": "OLD001", "length": 32}, written)
        self.test.cli.assert_not_called()
        self.assertNotIn("tar_baseline_started", self.state)
        for after in ({"value": "OTHER1", "length": 32}, {"value": "TST001", "length": 6}):
            self.state.pop("tar_baseline_started", None)
            with self.subTest(after=after), self.assertRaisesRegex(RuntimeError, "32-byte cartridge memory barcode"):
                self.baseline(empty, after)
        self.state.pop("tar_baseline_started", None)
        self.baseline(empty, written)
        self.assertTrue(self.state["baseline_complete"])

    def mismatch(self, cli_exit, settled=None, after="TST001", published=False):
        prepared = [{"status": "COPY_STATUS_PENDING"}]
        job = {"id": "3", "status": "JOB_STATUS_READY"}
        self.cases.media = mock.Mock(return_value=("9", {"tar": {}}))
        self.cases.export = mock.Mock(return_value={"archives/1": {}})
        self.cases.archive_items = mock.Mock(return_value=[{"status": "COPY_STATUS_SUBMITTED", "media_id": "9"}]
                                             if published else prepared)
        self.cases.wait_idle = mock.Mock(return_value=settled)
        self.cases.mam = mock.Mock(return_value={"value": after})
        self.test.one.return_value = {"job": job}
        self.test.remote.return_value = done(b"", cli_exit, b'{"code":"safety"}')
        return self.cases.refused_write("format", "BAD999")

    def test_mismatched_barcode_is_refused_before_data_by_cli_or_session(self):
        record = self.mismatch(2)
        self.assertEqual((record["refusal"], record["mam"]["value"]), ("cli", "TST001"))
        argv = self.test.remote.call_args.args[0]
        self.assertEqual(argv[argv.index("--barcode") + 1], "BAD999")
        self.assertEqual(argv[argv.index("--confirm-format") + 1], "BAD999")
        failed = {"id": "3", "status": "JOB_STATUS_READY", "error": "Cartridge memory barcode mismatch"}
        self.assertEqual(self.mismatch(0, settled=failed)["refusal"], "session")
        for case in ({"cli_exit": 0, "settled": {"id": "3", "status": "JOB_STATUS_READY"}},
                     {"cli_exit": 0, "settled": {"id": "3", "status": "JOB_STATUS_COMPLETED"}},
                     {"cli_exit": 2, "after": "BAD999"}, {"cli_exit": 2, "published": True}):
            with self.subTest(case=case), self.assertRaises(RuntimeError):
                self.mismatch(**case)

    def test_append_requires_direct_tail_proof_and_no_sequential_scan_log(self):
        (self.directory / "job.log").write_text('level=info msg="tar APPEND tail verified" next_archive=2\n')
        self.test.remote.return_value = done(b"service started\n")
        self.assertEqual(self.cases.check_append_log(self.directory, 1_700_000_000_123_456_789)["sequential_scan_lines"], 0)
        argv = self.test.remote.call_args.args[0]
        self.assertEqual(argv[:4], ["journalctl", "--unit", self.test.service, "--since"])
        self.assertEqual(argv[4], "@1700000000")
        self.test.remote.return_value = done(b"tar APPEND fallback: Sequential scan from BOT\n")
        with self.assertRaisesRegex(RuntimeError, "sequential scan"):
            self.cases.check_append_log(self.directory, 1)
        self.test.remote.return_value = done(b"")
        (self.directory / "job.log").write_text("tar APPEND started\n")
        with self.assertRaisesRegex(RuntimeError, "tail directly"):
            self.cases.check_append_log(self.directory, 1)


class TarLearningTests(unittest.TestCase):
    def setUp(self):
        temporary = tempfile.TemporaryDirectory()
        self.addCleanup(temporary.cleanup)
        self.directory = Path(temporary.name)
        self.test = mock.Mock()
        self.test.args = SimpleNamespace(physical_device="/dev/nst0", physical_barcode="TST001", host="local",
                                         record_mode="variable", record_bytes=262144, out=self.directory)
        self.test.root, self.test.install = "/owned", "/owned/install"
        self.state = {"media_id": "9"}
        self.cases = TarCases(self.test, self.state, mock.Mock())

    def test_read_observation_jsonl_needs_one_parseable_object(self):
        path = self.directory / "read-observations.jsonl"
        path.write_bytes(b'{"action":"READ","bytes":262144}\n\n{"action":"LOCATE"}\n{truncated\n')
        self.assertEqual(read_observations(path), {"records": 2, "unparseable_lines": 1})
        for data in (b"", b"\n\n", b"[1]\n", b"not json\n"):
            path.write_bytes(data)
            with self.subTest(data=data), self.assertRaisesRegex(RuntimeError, "no parseable read observation"):
                read_observations(path)

    def test_job_observations_wait_boundedly_then_copy_the_bundle_file(self):
        self.state["inventory_job_id"] = "5"
        path = "/owned/install/work/jobs/5/tapes/TST001/read-observations.jsonl"
        data = b'{"action":"READ"}\n'
        self.test.remote.side_effect = [done(returncode=1), done(), done(hashlib.sha256(data).hexdigest().encode() + b"  x\n"),
                                        done(data)]
        with mock.patch("package_physical_tar.time.sleep") as sleep:
            result = self.cases.job_observations("inventory")
        sleep.assert_called_once_with(5)
        self.assertEqual(result["records"], 1)
        self.assertEqual(self.test.remote.call_args_list[0].args[0], ["test", "-s", path])
        clock = iter([0, 0, 200])
        self.test.remote.side_effect = None
        self.test.remote.return_value = done(returncode=1)
        with mock.patch("package_physical_tar.time.monotonic", side_effect=lambda: next(clock)), \
                mock.patch("package_physical_tar.time.sleep"), \
                self.assertRaisesRegex(RuntimeError, "no read-observations.jsonl"):
            self.cases.job_observations("inventory")

    def settings(self, order="TAPE_READ_ORDER_LOGICAL", learning=True, models=()):
        return {"devices": [{"device": "/dev/other", "tape": {"read_order": "TAPE_READ_ORDER_AUTO"}},
                            {"device": "/dev/nst0", "tape": {"read_order": order, "passive_read_learning": learning,
                                                             "read_planning_seconds": 30}}],
                "learned_read_parameters": list(models)}

    def test_read_order_settings_keep_learning_on_and_other_devices_unchanged(self):
        written = {}
        self.test.write.side_effect = lambda path, data: written.__setitem__(path, json.loads(data))
        self.test.one.side_effect = [self.settings(), self.settings("TAPE_READ_ORDER_AUTO")]
        self.cases.set_read_order("AUTO")
        value = written["/owned/tmp/device-settings-auto.json"]
        self.assertNotIn("learned_read_parameters", value)
        self.assertEqual(value["devices"][0], {"device": "/dev/other", "tape": {"read_order": "TAPE_READ_ORDER_AUTO"}})
        self.assertEqual(value["devices"][1]["tape"], {"read_order": "TAPE_READ_ORDER_AUTO", "passive_read_learning": True,
                                                       "read_planning_seconds": 30})
        self.assertEqual(self.test.one.call_args_list[-1].args,
                         ("settings", "devices", "--device-json", "/owned/tmp/device-settings-auto.json"))
        for reply in (self.settings("TAPE_READ_ORDER_LOGICAL"), self.settings("TAPE_READ_ORDER_AUTO", learning=None)):
            self.test.one.side_effect = [self.settings(), reply]
            with self.subTest(reply=reply), self.assertRaises(RuntimeError):
                self.cases.set_read_order("AUTO")

    def test_learned_read_parameters_poll_is_bounded_and_later_changes_are_report_only(self):
        model = {"device": "/dev/nst0", "training_observations": 12, "updated_at_ns": "10", "parameters": {"a": 1}}
        other = {"device": "/dev/other", "training_observations": 1, "updated_at_ns": "99"}
        self.test.one.side_effect = [self.settings(models=[other]), self.settings(models=[other, model])]
        with mock.patch("package_physical_tar.time.sleep") as sleep:
            first = self.cases.learned_read_parameters()
        sleep.assert_called_once_with(10)
        self.assertEqual(first["models"], [model])
        clock = iter([0, 0, 700])
        self.test.one.side_effect = None
        self.test.one.return_value = self.settings(models=[model])
        with mock.patch("package_physical_tar.time.monotonic", side_effect=lambda: next(clock)), \
                mock.patch("package_physical_tar.time.sleep"):
            unchanged = self.cases.learned_read_parameters(first, required=False)
        self.assertEqual(unchanged["unchanged_after_seconds"], 600)
        self.assertFalse(model_change(first, unchanged)["updated"])
        clock = iter([0, 0, 700])
        self.test.one.return_value = self.settings(models=[other])
        with mock.patch("package_physical_tar.time.monotonic", side_effect=lambda: next(clock)), \
                mock.patch("package_physical_tar.time.sleep"), self.assertRaisesRegex(RuntimeError, "bounded wait"):
            self.cases.learned_read_parameters()
        newer = {**model, "updated_at_ns": "20", "training_observations": 30, "parameters": {"a": 2}}
        change = model_change(first, {"models": [newer]})
        self.assertEqual((change["updated"], change["parameters_changed"], change["training_observations"]),
                         (True, True, [[12], [30]]))

    def test_auto_and_logical_arms_record_duration_and_planner_reason(self):
        orders = []
        self.cases.set_read_order = mock.Mock(side_effect=orders.append)

        def restore(key, names, items=None):
            self.state[key + "_job_id"] = "11" if key.endswith("logical") else "12"
            self.state[key + "_seconds"] = 61.5 if key.endswith("logical") else 42.25
            self.cases.keep("jobs/" + self.state[key + "_job_id"] + "/job.log", (
                'msg="tar read order" requested_read_order=TAPE_READ_ORDER_' + key.rsplit("-", 1)[1].upper()
                + ' effective_read_order=TAPE_READ_ORDER_LOGICAL read_order_reason="Normal Tape unload timing is unavailable"\n'
                'msg="Restore read order prepared" planning_ns=12345\nmsg="copy finished"\n').encode())

        self.cases.restore_selection = mock.Mock(side_effect=restore)
        self.test.one.side_effect = [{"progress": {"stage": {"elapsed_ms": "61000"}}},
                                     {"prepared_read": {"job_id": "11", "effective_order": "LOGICAL"}},
                                     {"progress": {"elapsed_ms": "42000"}}, RuntimeError("no medium")]
        records = [self.cases.read_order_arm(order, ["format/one"]) for order in ("LOGICAL", "AUTO")]
        self.assertEqual(orders, ["LOGICAL", "AUTO"])
        self.assertEqual([(row["seconds"], row["progress_elapsed_ms"]) for row in records],
                         [(61.5, "61000"), (42.25, "42000")])
        self.assertEqual(len(records[0]["planner_log"]), 2)
        self.assertIn("read_order_reason", records[1]["planner_log"][0])
        self.assertEqual(records[1]["prepared_read"], {"error": "no medium"})
        saved = json.loads((self.directory / "physical-evidence/read-order/auto.json").read_text())
        self.assertEqual(saved["order"], "AUTO")
        self.cases.keep("jobs/12/job.log", b'msg="tar read order" requested_read_order=TAPE_READ_ORDER_LOGICAL\n')
        self.cases.restore_selection = mock.Mock()
        self.test.one.side_effect = None
        with self.assertRaisesRegex(RuntimeError, "requested AUTO"):
            self.cases.read_order_arm("AUTO", ["format/one"])

    def test_learning_checks_each_read_job_and_deletes_one_restore_bundle(self):
        self.state.update({"restore-selected_job_id": "6"})
        self.cases.job_observations = mock.Mock(side_effect=lambda key: {"key": key, "records": 1})
        first = {"models": [{"updated_at_ns": "1"}]}
        self.cases.learned_read_parameters = mock.Mock(side_effect=[first, first, {"models": [{"updated_at_ns": "2"}]}])
        self.cases.read_order_arm = mock.Mock(side_effect=lambda order, names: {"order": order})
        self.cases.set_read_order = mock.Mock()
        self.cases.learning(["format/one"])
        self.assertEqual([call.args[0] for call in self.cases.job_observations.call_args_list],
                         ["restore-full", "restore-selected", "inventory", "verify"])
        self.assertEqual(self.cases.learned_read_parameters.call_args_list[1:],
                         [mock.call(first, required=False), mock.call(first, required=False)])
        self.cases.set_read_order.assert_called_once_with("LOGICAL")
        self.test.cli.assert_called_once_with("job", "delete", "6")
        self.assertEqual(self.test.remote.call_args_list[-1].args[0], ["test", "!", "-e", "/owned/install/work/jobs/6"])
        self.assertEqual(self.state["deleted_job_ids"], ["6"])
        report = json.loads((self.directory / "physical-evidence/learning.json").read_text())
        self.assertEqual(report["deleted_restore_job"], "6")
        comparison = json.loads((self.directory / "physical-evidence/read-order-comparison.json").read_text())
        self.assertEqual(set(comparison["arms"]), {"LOGICAL", "AUTO"})


class TarFillTests(unittest.TestCase):
    def setUp(self):
        temporary = tempfile.TemporaryDirectory()
        self.addCleanup(temporary.cleanup)
        self.directory = Path(temporary.name)
        self.test = mock.Mock()
        self.test.args = SimpleNamespace(physical_device="/dev/nst0", physical_barcode="TST001", host="local",
                                         record_mode="variable", record_bytes=262144, out=self.directory,
                                         capacity_file_mib=[1, 1, 1], capacity_source_reflink=False)
        self.test.root, self.test.install = "/owned", "/owned/install"
        self.state = {"media_id": "9", "source_location": "1", "restore_complete": True, "tar_recovery_complete": True,
                      "format_items": {"format/one": {"file": {"media_path": "archives/0000000000000001/format/one"}}},
                      "append_items": {}}
        self.cases = TarCases(self.test, self.state, mock.Mock())
        for name in ("device_settings", "ensure_ready", "check_device_released", "settled", "check_positions",
                     "restore_selection", "scan", "public_recovery"):
            setattr(self.cases, name, mock.Mock(return_value={}))
        self.cases.available = mock.Mock(return_value=1 << 60)
        self.manifest = {f"capacity/chunk-{n:03d}.bin": {"size": 1 << 20, "sha256": f"{n:064x}"} for n in range(3)}
        self.cases.fill_sources = mock.Mock(return_value=self.manifest)
        self.cases.created = mock.Mock(return_value={"id": "20"})
        self.cases.capture_job = mock.Mock(return_value=self.directory)
        self.cases.check_submitted_manifest = mock.Mock(return_value={"pending": 1})

    def items(self, statuses):
        rows = []
        for number, status in enumerate(statuses):
            name = f"capacity/chunk-{number:03d}.bin"
            submitted = status == "COPY_STATUS_SUBMITTED"
            rows.append({"id": str(number + 1), "status": status, "size_bytes": str(1 << 20),
                         **({"media_id": "9"} if submitted else {}),
                         "file": {"source_path": "/owned/fixtures/source/" + name, "target_path": name,
                                  "media_path": "archives/0000000000000003/" + name if submitted else "",
                                  "expected": {"file_id": str(number + 10),
                                               "sha256": base64.b64encode(bytes.fromhex(f"{number:064x}")).decode()}}})
        return rows

    def run_fill(self, statuses, terminal, remaining="2097152"):
        pending = ["COPY_STATUS_PENDING"] * 3
        self.cases.archive_items = mock.Mock(side_effect=[self.items(pending), self.items(statuses)])
        earlier = {"archives/0000000000000001/format/one": {"hash": "x"}}
        later = {**earlier, **{"archives/0000000000000003/capacity/chunk-000.bin": {}}}
        self.cases.export = mock.Mock(side_effect=[earlier, later])
        self.cases.wait_idle = mock.Mock(return_value=terminal)
        self.test.one.return_value = {"tape": {"features": [{"feature": "DEVICE_FEATURE_KIND_CAPACITY",
                                                             "capacity": {"remaining_bytes": remaining}}]}}
        self.cases.fill()

    def test_fill_restore_samples_first_middle_and_last_members(self):
        names = {f"capacity/chunk-{number:03d}.bin": {} for number in range(7)}
        self.assertEqual(fill_restore_sample(names),
                         ["capacity/chunk-000.bin", "capacity/chunk-003.bin", "capacity/chunk-006.bin"])
        self.assertEqual(fill_restore_sample({"only.bin": {}}), ["only.bin"])

    def test_fill_requires_capacity_stop_with_published_members_and_pending_suffix(self):
        stopped = {"id": "20", "status": "JOB_STATUS_READY", "error": "tar capacity early warning: no space left"}
        published = ["COPY_STATUS_SUBMITTED", "COPY_STATUS_SUBMITTED", "COPY_STATUS_PENDING"]
        self.run_fill(published, stopped)
        self.assertTrue(self.state["tar_fill_complete"])
        self.assertEqual(sorted(self.state["fill_items"]), ["capacity/chunk-000.bin", "capacity/chunk-001.bin"])
        self.cases.wait_idle.assert_called_once_with({"id": "20"}, 3600 + 3 * 2**20 // 35_000_000)
        self.test.cli.assert_called_once_with("archive", "write", "tape", "append", "20", "--device", "/dev/nst0",
                                              "--barcode", "TST001")
        self.cases.check_submitted_manifest.assert_called_once()
        self.assertEqual(self.cases.check_submitted_manifest.call_args.kwargs, {"pending": 1})
        restore = self.cases.restore_selection.call_args
        self.assertEqual(restore.args[:2], ("fill-restore", ["capacity/chunk-000.bin", "capacity/chunk-001.bin"]))
        self.assertEqual(self.cases.scan.call_args.kwargs["key"], "fill-verify")
        self.assertEqual(set(self.cases.scan.call_args.kwargs["items"]),
                         {"format/one", "capacity/chunk-000.bin", "capacity/chunk-001.bin"})
        self.cases.public_recovery.assert_called_once_with(fill=self.state["fill_items"])
        result = json.loads((self.directory / "physical-evidence/fill/write-result.json").read_text())
        self.assertEqual(result["remaining_estimate_bytes"], 2097152)
        failures = ((["COPY_STATUS_SUBMITTED"] * 3, {"id": "20", "status": "JOB_STATUS_COMPLETED"}),
                    (published, {"id": "20", "status": "JOB_STATUS_READY"}),
                    (["COPY_STATUS_PENDING"] * 3, stopped),
                    (["COPY_STATUS_SUBMITTED", "COPY_STATUS_STAGED", "COPY_STATUS_PENDING"], stopped))
        for statuses, terminal in failures:
            self.state.pop("tar_fill_started")
            self.state.pop("tar_fill_complete", None)
            with self.subTest(statuses=statuses, terminal=terminal), self.assertRaises(RuntimeError):
                self.run_fill(statuses, terminal)
            self.assertNotIn("tar_fill_complete", self.state)

    def test_fill_payload_must_exceed_reported_capacity_and_fit_local_space(self):
        with self.assertRaisesRegex(RuntimeError, "exceed the reported remaining"):
            self.run_fill([], {}, remaining=str(3 << 20))
        self.assertNotIn("tar_fill_started", self.state)
        self.cases.available.return_value = 1
        with self.assertRaisesRegex(RuntimeError, "lacks the required space"):
            self.run_fill([], {})
        self.cases.fill_sources.assert_not_called()

    def test_fill_sources_reuse_capacity_generation_and_freeze_manifest(self):
        cases = TarCases(self.test, self.state, mock.Mock())
        with mock.patch("package_physical_capacity.generate_sources", return_value=self.manifest) as generate:
            self.assertEqual(cases.fill_sources([1 << 20] * 3, True), self.manifest)
        generate.assert_called_once_with(cases, "/owned/tmp/capacity/physical_performance_fixture.py", [1 << 20] * 3, True, "capacity")
        frozen = self.directory / "physical-evidence/fill/source-manifest.json"
        self.assertEqual(json.loads(frozen.read_text()), self.manifest)
        self.assertEqual(self.state["fill_manifest_sha256"], hashlib.sha256(frozen.read_bytes()).hexdigest())
        with mock.patch("package_physical_capacity.generate_sources", return_value=self.manifest), \
                self.assertRaises(FileExistsError):
            cases.fill_sources([1 << 20] * 3, True)

    def test_fill_cleanup_requires_completed_fill_readback(self):
        state = {"physical_format": "tar_pax_v1", "restore_complete": True, "tar_recovery_complete": True,
                 "completed_stages": ["prepare", "baseline", "restore"]}
        check_cleanup_state(state)
        for stage in ("fill", "ew", "read_order"):
            with self.subTest(stage=stage), self.assertRaisesRegex(RuntimeError, "Cleanup requires"):
                check_cleanup_state({**state, "tar_" + stage + "_started": True})
            check_cleanup_state({**state, "tar_" + stage + "_started": True, "tar_" + stage + "_complete": True})


class TarFillReaderTests(unittest.TestCase):
    members = {"archives/0000000000000003/capacity/chunk-000.bin": {"size": 3, "sha256": hashlib.sha256(b"abc").hexdigest()}}

    def record(self, name, data, size=None):
        return {"name": name, "size": len(data) if size is None else size, "received": len(data),
                "sha256": hashlib.sha256(data).hexdigest()}

    def test_fill_check_accepts_only_complete_published_members_and_an_unpublished_tail(self):
        name = "archives/0000000000000003/capacity/chunk-000.bin"
        tail = self.record("archives/0000000000000003/capacity/chunk-001.bin", b"ab", size=9)
        result = check_fill([self.record("./" + name, b"abc"), tail], self.members)
        self.assertEqual(result["published_checked"], 1)
        self.assertEqual(result["unpublished_tail"], [tail])
        for records in ([], [self.record(name, b"abd")], [self.record(name, b"ab", size=3)],
                        [self.record(name, b"abc")] * 2, [tail, self.record(name, b"abc")]):
            with self.subTest(records=records), self.assertRaises(RuntimeError):
                check_fill(records, self.members)
        self.assertEqual(check_fill_members(self.members), self.members)
        for members in ({}, {name: {"size": "3", "sha256": "0" * 64}}, {name: {"size": 3, "sha256": "x"}}):
            with self.subTest(members=members), self.assertRaises(RuntimeError):
                check_fill_members(members)

    def test_stream_fill_reads_to_the_filemark_even_after_tar_stops(self):
        with tempfile.TemporaryDirectory() as directory:
            output = Path(directory)
            reader = PublicReader("/dev/nst0", "variable", 262144, output / "fixture.json", output)
            reader.fill_members = self.members
            payload = b"x" * (3 << 20)
            started = []

            class Process:
                def __init__(self, argv, stdout=None, stdin=None, stderr=None, cwd=None):
                    started.append((argv, cwd))
                    self.argv, self.output = argv, stdout
                    if argv[0] == "dd":
                        self.stdout = io.BytesIO(payload)
                    else:
                        self.stdin = mock.Mock()
                        # tar exits after the first chunk, as on an unterminated EW tail.
                        self.stdin.write.side_effect = [None, BrokenPipeError()]

                def wait(self):
                    if self.argv[0] == "tar":
                        name = "archives/0000000000000003/capacity/chunk-000.bin"
                        self.output.write((json.dumps({"name": name, "size": 3, "received": 3,
                                                       "sha256": hashlib.sha256(b"abc").hexdigest()}) + "\n").encode())
                        return 2
                    return 0

            with mock.patch("physical_tar_recovery.subprocess.Popen", Process):
                result = reader.stream_fill(3)
            self.assertEqual((result["bytes"], result["sha256"]), (len(payload), hashlib.sha256(payload).hexdigest()))
            self.assertEqual((result["tar_exit"], result["input_closed_early"], result["published_checked"]), (2, True, 1))
            self.assertEqual(started[0][0], ["dd", "if=/dev/nst0", "bs=262144", "status=none"])
            self.assertEqual(started[1][0][:4], ["tar", "-x", "-f", "-"])
            self.assertTrue(started[1][0][4].startswith("--to-command=python3 -I "))
            self.assertEqual(started[1][1], output / "file-3")
            commands = json.loads((output / "commands.json").read_text())
            self.assertEqual([(row["name"], row["exit"], row["unsettled"]) for row in commands],
                             [("read-3", 0, False), ("hash-3", 2, False)])
            self.assertFalse(reader.unsettled)

    def test_four_file_read_requires_fill_filemark_before_eod(self):
        with tempfile.TemporaryDirectory() as directory:
            output = Path(directory) / "reader"
            output.mkdir()
            reader = PublicReader("/dev/nst0", "variable", 262144, Path(directory) / "fixture.json", output)
            reader.prepare = mock.Mock(return_value=({}, ["/dev/nst0"], 0))
            reader.fill_members = self.members
            reader.stream_fill = mock.Mock(return_value={"bytes": 4096, "sha256": "f" * 64, "published_checked": 1})

            def command(name, argv, expected=0):
                observed = ""
                if name.startswith("read-"):
                    filename = next(arg[3:] for arg in argv if arg.startswith("of="))
                    Path(filename).write_bytes(b"" if name == "read-tail" else b"x")
                elif name in ("selected-status", "restored-status"):
                    observed = status("ONLINE", file=0)
                elif name.startswith("post-close-"):
                    observed = status(file=int(name.rsplit("-", 1)[1]) + 1)
                elif name == "post-tail":
                    observed = status("EOD ONLINE", file=4, block=-1)
                elif name == "extract-0":
                    label = output / "file-0" / "extracted" / "YATM"
                    label.mkdir()
                    (label / "identity.json").write_text('{"format":"tar_pax_v1"}')
                    (label / "README.txt").write_text("label")
                return subprocess.CompletedProcess(argv, expected, observed.encode(), b"")

            with mock.patch.object(reader, "run", side_effect=command), mock.patch("physical_tar_recovery.check_extracted"):
                result = reader.read()
            reader.stream_fill.assert_called_once_with(3)
            self.assertEqual([row["boundary"] for row in result["files"]], ["FILEMARK"] * 4)
            self.assertEqual((result["end"], result["fill"]["published_checked"]), ("DRIVER_REPORTED_EOD", 1))


if __name__ == "__main__":
    unittest.main()
