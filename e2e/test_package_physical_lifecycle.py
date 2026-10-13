"""Local argument and ownership regressions; no host or physical acceptance."""

from contextlib import redirect_stderr
import io
import json
import os
from pathlib import Path
import shlex
import subprocess
import tempfile
from types import SimpleNamespace
import unittest
from unittest import mock

from physical_package_acceptance import (PhysicalAcceptance, adapt_script,
                                         physical_arguments, validate_state)


class PhysicalControllerTests(unittest.TestCase):
    def setUp(self):
        temporary = tempfile.TemporaryDirectory()
        self.addCleanup(temporary.cleanup)
        self.directory = Path(temporary.name)
        self.options = {
            "host": "isolated-test", "test-parent": "/srv/acceptance",
            "archive": "yatm-linux-amd64-v1.0.0-alpha.2.tar.gz",
            "preview-archive": "yatm-preview-linux-amd64-v1.0.0-alpha.2.tar.gz",
            "version": "v1.0.0-alpha.2", "commit": "1" * 40,
            "out": str(self.directory / "evidence"), "state": str(self.directory / "state.json"),
            "physical-device": "/dev/nst0", "physical-barcode": "TST001", "physical-stage": "prepare",
        }
        # A missed stub must fail locally rather than reaching SSH or a device.
        blocker = mock.patch("package_acceptance.subprocess.run", side_effect=AssertionError("Unexpected process execution"))
        blocker.start()
        self.addCleanup(blocker.stop)

    def arguments(self, **changes):
        options = {**self.options, **changes}
        if options.get("physical-stage") == "cleanup" and "syscall-trace" not in options and "no-syscall-trace" not in options:
            options["no-syscall-trace"] = True
        argv = []
        for key, value in options.items():
            if value is not None:
                argv.append("--" + key if value is True else "--" + key + "=" + str(value))
        return physical_arguments(argv)

    def owned_state(self):
        return {
            "host": "isolated-test", "commit": "1" * 40, "version": "v1.0.0-alpha.2",
            "device": "/dev/nst0", "barcode": "TST001", "mkltfs": "mkltfs", "ltfs_binary": "ltfs",
            "remote_root": "/srv/acceptance/yatm-package-acceptance.12345678",
            "install": "/srv/acceptance/yatm-package-acceptance.12345678/install",
            "service": "yatm-acceptance-12345678.service", "url": "http://127.0.0.1:23456",
            "completed_stages": ["prepare", "baseline"],
        }

    def resumed_arguments(self):
        Path(self.options["state"]).write_text(json.dumps(self.owned_state()))
        return self.arguments(**{"physical-stage": "restore"})

    def controller(self):
        controller = PhysicalAcceptance(self.resumed_arguments())
        for field, key in (("root", "remote_root"), ("install", "install"), ("service", "service"), ("url", "url")):
            setattr(controller, field, self.owned_state()[key])
        return controller

    def wait_result(self, status=None, phase=None, code=None, error=None, exit_code=1):
        stdout = json.dumps({"job": {"id": "3", "status": status, "phase": phase}}).encode() if status else b""
        stderr = json.dumps({"code": code, "error": error}).encode() if code else b""
        return SimpleNamespace(stdout=stdout, stderr=stderr, returncode=exit_code)

    def lock_result(self):
        return self.wait_result("JOB_STATUS_PREPARING", "JOB_PHASE_INDEXING", "unknown",
                                'call RPC failed, method="/yatm.v1.JobService/Get", open sqlite database failed, database is locked')

    def test_wait_retries_only_read_lock_and_preserves_failure_evidence(self):
        controller = self.controller()
        ready = self.wait_result("JOB_STATUS_READY", "JOB_PHASE_UNSPECIFIED", "action_required", "ready")
        with mock.patch.object(controller, "remote", side_effect=[self.lock_result(), ready]) as remote, \
                mock.patch("physical_package_acceptance.time.sleep") as sleep:
            result = controller.wait_job({"id": "3"}, "JOB_STATUS_READY")
        self.assertEqual(result["status"], "JOB_STATUS_READY")
        self.assertEqual(remote.call_count, 2)
        self.assertTrue(all(call.args[0][-6:] == ["wait", "3", "--wait-timeout", call.args[0][-3], "--poll-interval", "2s"]
                            for call in remote.call_args_list))
        self.assertTrue(all(call.kwargs["expected"] is None for call in remote.call_args_list))
        self.assertEqual(controller.report["observation_retries"][0]["last_observed_phase"], "JOB_PHASE_INDEXING")
        self.assertIn("database is locked", controller.report["observation_retries"][0]["error"]["error"])
        sleep.assert_called_once_with(2)

    def test_wait_long_lock_streak_uses_capped_backoff_and_original_deadline(self):
        controller = self.controller()
        clock = [0]
        ready = self.wait_result("JOB_STATUS_READY", "JOB_PHASE_UNSPECIFIED", "action_required", "ready")
        def advance(delay):
            clock[0] += delay
        with mock.patch.object(controller, "remote", side_effect=[self.lock_result()] * 8 + [ready]) as remote, \
                mock.patch.object(controller, "save"), \
                mock.patch("physical_package_acceptance.time.sleep", side_effect=advance) as sleep, \
                mock.patch("physical_package_acceptance.time.monotonic", side_effect=lambda: clock[0]):
            result = controller.wait_job({"id": "3"}, "JOB_STATUS_READY", timeout=300)
        self.assertEqual(result["status"], "JOB_STATUS_READY")
        self.assertEqual(remote.call_count, 9)
        self.assertEqual([call.args[0] for call in sleep.call_args_list], [2, 4, 8, 16, 30, 30, 30, 30])
        self.assertEqual([call.kwargs["timeout"] for call in remote.call_args_list],
                         [300, 298, 294, 286, 270, 240, 210, 180, 150])
        self.assertEqual([row["attempt"] for row in controller.report["observation_retries"]], list(range(1, 9)))

    def test_wait_lock_backoff_stops_at_original_deadline_without_mutation(self):
        controller = self.controller()
        clock = [0]
        def locked(*args, **kwargs):
            clock[0] += 5
            return self.lock_result()
        def advance(delay):
            clock[0] += delay
        with mock.patch.object(controller, "remote", side_effect=locked) as remote, \
                mock.patch.object(controller, "save"), \
                mock.patch("physical_package_acceptance.time.sleep", side_effect=advance) as sleep, \
                mock.patch("physical_package_acceptance.time.monotonic", side_effect=lambda: clock[0]):
            with self.assertRaisesRegex(RuntimeError, "original deadline"):
                controller.wait_job({"id": "3"}, "JOB_STATUS_READY", timeout=50)
        self.assertEqual(remote.call_count, 4)
        self.assertEqual([call.args[0] for call in sleep.call_args_list], [2, 4, 8])
        self.assertEqual(controller.report["observation_retries"][-1]["delay_seconds"], 0)
        self.assertTrue(all(call.args[0][5:8] == ["job", "wait", "3"] for call in remote.call_args_list))

    def test_wait_does_not_retry_real_failure_or_unrelated_rpc_error(self):
        controller = self.controller()
        for result in (self.wait_result("JOB_STATUS_FAILED", "JOB_PHASE_UNSPECIFIED", "action_required", "indexing failed"),
                       self.wait_result("JOB_STATUS_PREPARING", "JOB_PHASE_INDEXING", "unknown", "permission denied"),
                       self.wait_result("JOB_STATUS_READY", "JOB_PHASE_UNSPECIFIED", "deadline_exceeded", "timed out")):
            with mock.patch.object(controller, "remote", return_value=result) as remote, \
                    mock.patch("physical_package_acceptance.time.sleep") as sleep:
                with self.assertRaises(RuntimeError):
                    controller.wait_job({"id": "3"}, "JOB_STATUS_READY")
            self.assertEqual(remote.call_count, 1)
            sleep.assert_not_called()

    def test_wait_rejects_durable_mismatch_and_expired_deadline(self):
        controller = self.controller()
        result = self.wait_result("JOB_STATUS_FAILED", "JOB_PHASE_UNSPECIFIED", exit_code=0)
        with mock.patch.object(controller, "remote", return_value=result) as remote:
            with self.assertRaisesRegex(RuntimeError, "unexpected durable state"):
                controller.wait_job({"id": "3"})
            self.assertEqual(remote.call_count, 1)
        with mock.patch.object(controller, "remote") as remote, \
                mock.patch("physical_package_acceptance.time.monotonic", side_effect=[0, 160]):
            with self.assertRaisesRegex(RuntimeError, "original deadline"):
                controller.wait_job({"id": "3"}, timeout=180)
            remote.assert_not_called()

    def test_prepare_accepts_explicit_device_and_typed_local_paths(self):
        for device in ("/dev/nst0", "/dev/st1", "/dev/tape/by-id/scsi-test-nst"):
            with self.subTest(device=device):
                args = self.arguments(**{"physical-device": device})
                self.assertEqual(args.physical_device, device)
                self.assertEqual(args.state, Path(self.options["state"]))
                self.assertEqual(args.archive, Path(self.options["archive"]))

    def test_requires_scratch_device_barcode_state_and_stage(self):
        for field in ("physical-device", "physical-barcode", "state", "physical-stage"):
            with self.subTest(field=field), redirect_stderr(io.StringIO()), self.assertRaises(SystemExit) as error:
                self.arguments(**{field: None})
            self.assertEqual(error.exception.code, 2)

    def test_rejects_virtual_legacy_and_ordinary_case_modes(self):
        for field, value in (("ltfs", True), ("legacy-package", "legacy.tar.gz"),
                             ("legacy-fixture", "fixture.tar.gz"), ("case", "fresh-systemd-installation")):
            with self.subTest(field=field), self.assertRaisesRegex(RuntimeError, "Physical stages cannot select"):
                self.arguments(**{field: value})
        with self.assertRaisesRegex(RuntimeError, "Preview"):
            self.arguments(**{"preview-archive": None})

    def test_rejects_invalid_identity_and_shell_input(self):
        invalid = {
            "host": ("-oProxyCommand=command", "test;command"),
            "physical-device": ("", "/dev/sda", "/dev/nst0;command", "/dev/tape/by-id/../../sda"),
            "physical-barcode": ("", "abc123", "TST001L5", "TST00;"),
            "test-parent": ("/", "relative", "/srv/../opt", "/srv/test;command"),
            "mkltfs": ("../mkltfs", "/usr/bin/../mkltfs", "mkltfs;command", "$(command)"),
            "ltfs-binary": ("ltfs --option", "/usr/bin/ltfs\ncommand"),
            "commit": ("1234567",), "version": ("latest",),
        }
        for field, values in invalid.items():
            for value in values:
                with self.subTest(field=field, value=value), self.assertRaises(RuntimeError):
                    self.arguments(**{field: value})

    def test_prepare_never_overwrites_existing_state(self):
        path = Path(self.options["state"])
        path.write_text("preserved state\n")
        with self.assertRaisesRegex(RuntimeError, "Prepare needs new state"):
            self.arguments()
        self.assertEqual(path.read_text(), "preserved state\n")
        self.assertFalse(Path(self.options["out"]).exists())

    def test_resumed_stages_require_state_and_new_evidence(self):
        for stage in ("baseline", "restore", "cleanup"):
            with self.subTest(stage=stage), self.assertRaisesRegex(RuntimeError, "preserved local state"):
                self.arguments(**{"physical-stage": stage})
        self.resumed_arguments()
        Path(self.options["out"]).mkdir()
        with self.assertRaisesRegex(RuntimeError, "new private evidence directory"):
            self.arguments(**{"physical-stage": "restore"})

    def test_resumed_inputs_must_match_saved_owner_and_identity(self):
        args, state = self.resumed_arguments(), self.owned_state()
        validate_state(args, state)
        changes = {"host": "another-test", "commit": "2" * 40, "version": "v1.0.0-alpha.3",
                   "device": "/dev/nst1", "barcode": "TST002", "mkltfs": "/usr/bin/mkltfs",
                   "ltfs_binary": "/usr/bin/ltfs"}
        for key, value in changes.items():
            with self.subTest(key=key), self.assertRaisesRegex(RuntimeError, "Stage inputs differ"):
                validate_state(args, {**state, key: value})

    def test_rejects_foreign_root_install_service_and_listener(self):
        args, state = self.resumed_arguments(), self.owned_state()
        changes = {
            "remote_root": ("/opt/yatm", "/srv/other/yatm-package-acceptance.12345678", state["remote_root"] + "/../other"),
            "install": ("/opt/yatm", state["remote_root"] + "/../install"),
            "service": ("yatm.service", "yatm-acceptance-87654321.service"),
            "url": ("http://example.invalid:23456", "http://0.0.0.0:23456"),
        }
        for key, values in changes.items():
            for value in values:
                with self.subTest(key=key, value=value), self.assertRaises(RuntimeError):
                    validate_state(args, {**state, key: value})

    def test_rejects_cleaned_state_or_missing_prerequisite(self):
        args, state = self.resumed_arguments(), self.owned_state()
        with self.assertRaisesRegex(RuntimeError, "already been cleaned"):
            validate_state(args, {**state, "cleaned": True})
        for stage in ("baseline", "restore", "cleanup"):
            args.physical_stage = stage
            with self.subTest(stage=stage), self.assertRaisesRegex(RuntimeError, "Complete|Cleanup requires"):
                validate_state(args, {**state, "completed_stages": []})

    def test_invalid_saved_state_fails_before_any_host_command(self):
        args = self.resumed_arguments()
        Path(args.state).write_text(json.dumps({**self.owned_state(), "service": "yatm.service"}))
        controller = PhysicalAcceptance(args)
        with mock.patch.object(controller, "remote", side_effect=AssertionError("Host command before ownership check")) as remote, \
                mock.patch.object(controller, "remote_batch", side_effect=AssertionError("Host batch before ownership check")) as batch:
            with self.assertRaisesRegex(RuntimeError, "unexpected installation, service or listener"):
                controller.execute()
        remote.assert_not_called()
        batch.assert_not_called()
        self.assertEqual(json.loads((args.out / "report.json").read_text())["status"], "failed")

    def owned_config(self, controller):
        return {"tape_devices": ["/dev/nst0"], "domain": controller.url,
                "database": {"dialect": "sqlite", "dsn": "./catalog.db", "sqlite_wal": True},
                "paths": {"work": controller.install + "/work", "access": [{"root": controller.root + "/fixtures"}]},
                "scripts": {key: controller.root + "/tape-scripts/" + name for key, name in
                            (("read_info", "readinfo"), ("encrypt", "encrypt"), ("mkfs", "mkfs"),
                             ("mount", "mount.openltfs"), ("umount", "umount"))}}

    def verify_observed_owner(self, controller, config, fragment):
        # Only read-only host observations are stubbed; no physical stage runs.
        with mock.patch.object(controller, "remote_batch", return_value=[
                SimpleNamespace(stdout=(fragment + "\n").encode()),
                SimpleNamespace(stdout=json.dumps(config).encode())]), \
                mock.patch.object(controller, "remote", return_value=SimpleNamespace(stdout=(fragment + "\n").encode())):
            controller.verify_owner()

    def test_observed_service_and_config_must_belong_to_installation(self):
        controller = self.controller()
        config = self.owned_config(controller)
        fragment = controller.install + "/" + controller.service
        self.verify_observed_owner(controller, config, fragment)
        with self.assertRaisesRegex(RuntimeError, "outside the owned installation"):
            self.verify_observed_owner(controller, config, "/opt/yatm/yatm.service")
        for key, value in (("tape_devices", ["/dev/nst1"]), ("domain", "http://127.0.0.1:23457"),
                           ("paths", {"work": "/opt/yatm/work", "access": config["paths"]["access"]}),
                           ("paths", {"work": config["paths"]["work"], "access": [{"root": "/opt/yatm"}]})):
            with self.subTest(key=key, value=value), self.assertRaisesRegex(RuntimeError, "configuration changed"):
                self.verify_observed_owner(controller, {**config, key: value}, fragment)

    def test_observed_config_rejects_production_database_and_scripts(self):
        controller = self.controller()
        for section, key, value in (("database", "dsn", "/opt/yatm/catalog.db"),
                                    ("scripts", "mkfs", "/opt/yatm/scripts/mkfs"),
                                    ("scripts", "mount", "/opt/yatm/scripts/mount.openltfs")):
            config = self.owned_config(controller)
            config[section][key] = value
            with self.subTest(section=section, key=key), self.assertRaises(RuntimeError):
                self.verify_observed_owner(controller, config, controller.install + "/" + controller.service)

    def test_remote_arguments_preserve_literal_typed_paths(self):
        controller = self.controller()
        paths = [Path("/tmp/source with spaces"), Path("/tmp/$(command);literal"), Path("/tmp/line\nbreak")]
        argv = controller.remote_arguments(["cat", "--", *paths], 30)
        self.assertEqual(shlex.split(argv[-1])[-5:], ["cat", "--", *map(str, paths)])
        self.assertEqual(argv[-2], "isolated-test")

    def test_script_adaptation_preserves_arguments_and_requires_one_command(self):
        source = '#!/bin/sh\nmkltfs -f -d "$SG_DEVICE"\n'
        result = adapt_script(source, "mkltfs", "/usr/local/bin/mkltfs", " -r 'size=1M'")
        self.assertEqual(shlex.split(result.splitlines()[1]),
                         ["/usr/local/bin/mkltfs", "-r", "size=1M", "-f", "-d", "$SG_DEVICE"])
        for changed in ("#!/bin/sh\nother-command\n", source + 'mkltfs -f -d "$SG_DEVICE"\n'):
            with self.subTest(source=changed), self.assertRaisesRegex(RuntimeError, "script changed"):
                adapt_script(changed, "mkltfs", "/usr/local/bin/mkltfs")

    def test_capacity_options_are_saved_and_cannot_change_between_stages(self):
        controller = self.controller()
        for field in ("capacity_observe", "capacity_source_reflink"):
            self.assertFalse(getattr(controller.args, field))
            setattr(controller.args, field, True)
        controller.save_state()
        state = json.loads(controller.args.state.read_text())
        state["completed_stages"] = ["prepare", "baseline"]
        validate_state(controller.args, state)
        for field in ("capacity_observe", "capacity_source_reflink"):
            self.assertTrue(state[field])
            with self.subTest(field=field), self.assertRaisesRegex(RuntimeError, "differs from saved preparation"):
                validate_state(controller.args, {**state, field: False})
            setattr(controller.args, field, False)
            with self.assertRaisesRegex(RuntimeError, "differs from saved preparation"):
                validate_state(controller.args, state)
            setattr(controller.args, field, True)

    def test_cleanup_accepts_capacity_state_without_preview_or_obsolete_stages(self):
        self.resumed_arguments()
        args = self.arguments(**{"physical-stage": "cleanup", "preview-archive": None})
        state = self.owned_state()
        state.update(performance=True, capacity_write_observed=True, capacity_device_released=True,
                     capacity_restore_complete=True, capacity_verify_complete=True,
                     completed_stages=["performance-prepare", "performance-format", "capacity-verify"])
        validate_state(args, state)
        for key in ("capacity_verify_complete", "capacity_device_released"):
            with self.subTest(key=key), self.assertRaisesRegex(RuntimeError, "Cleanup requires"):
                validate_state(args, {**state, key: False})

    def test_cleanup_paths_reject_mounts_missing_observation_and_owned_evidence(self):
        controller = self.controller()
        controller.args.host = "local"
        controller.state["physical_evidence_dir"] = str(self.directory / "preserved")
        for mounts in ({"filesystems": []}, {"filesystems": [{"target": "/unrelated"}]}):
            with mock.patch.object(controller, "remote", return_value=SimpleNamespace(stdout=json.dumps(mounts).encode())):
                controller.check_cleanup_paths()
        for mounts in ({}, {"filesystems": [{"target": controller.root}]},
                       {"filesystems": [{"target": controller.root + "/work/mount"}]}):
            with mock.patch.object(controller, "remote", return_value=SimpleNamespace(stdout=json.dumps(mounts).encode())), \
                    self.assertRaisesRegex(RuntimeError, "owned mount remains or mount state is unknown"):
                controller.check_cleanup_paths()
        for name in ("state", "out", "physical_evidence_dir"):
            original = (controller.state[name] if name == "physical_evidence_dir" else getattr(controller.args, name))
            if name == "physical_evidence_dir":
                controller.state[name] = controller.root + "/evidence"
            else:
                setattr(controller.args, name, Path(controller.root) / "evidence")
            with mock.patch.object(controller, "remote") as remote, self.assertRaisesRegex(RuntimeError, "delete preserved"):
                controller.check_cleanup_paths()
            remote.assert_not_called()
            if name == "physical_evidence_dir":
                controller.state[name] = original
            else:
                setattr(controller.args, name, original)

    def test_cleanup_observer_options_require_explicit_declaration_and_exact_paths(self):
        self.resumed_arguments()
        with self.assertRaisesRegex(RuntimeError, "explicit --syscall-trace"):
            self.arguments(**{"physical-stage": "cleanup", "no-syscall-trace": None})
        args = self.arguments(**{"physical-stage": "cleanup", "syscall-trace": "/owned/trace.123", "require-sg-trace": True})
        self.assertEqual(args.syscall_trace, ["/owned/trace.123"])
        self.assertTrue(args.require_sg_trace)
        for path in ("relative", "/owned/../trace"):
            with self.subTest(path=path), self.assertRaisesRegex(RuntimeError, "exact absolute"):
                self.arguments(**{"physical-stage": "cleanup", "syscall-trace": path})
        with self.assertRaisesRegex(RuntimeError, "only to cleanup"):
            self.arguments(**{"physical-stage": "restore", "no-syscall-trace": True})

    def test_final_non_cleanup_report_is_preserved_after_final_save(self):
        controller = self.controller()
        evidence = self.directory / "common-evidence"
        state = {**self.owned_state(), "physical_evidence_dir": str(evidence)}
        controller.args.state.write_text(json.dumps(state))
        with mock.patch.object(controller, "verify_owner"), mock.patch.object(controller, "start_owned"), \
                mock.patch.object(controller, "stop_owned"), mock.patch("package_physical_cases.run_stage"):
            controller.execute()
        original = (controller.args.out / "report.json").read_bytes()
        self.assertEqual((evidence / "stages/restore/report.json").read_bytes(), original)
        report = json.loads(original)
        self.assertEqual(report["status"], "passed")
        self.assertEqual(report["cleanup"], {"service_stopped": True, "root_retained": True})
        self.assertGreaterEqual(report["duration_seconds"], report["cases"][0]["duration_seconds"])

    def test_failed_capacity_write_preserves_final_report_and_rejected_replay_cannot_replace_it(self):
        args = self.resumed_arguments()
        args.physical_stage = "capacity-write"
        evidence = self.directory / "common-evidence"
        state = {**self.owned_state(), "physical_evidence_dir": str(evidence), "performance": True,
                 "performance_source_read_mode": "buffered", "performance_format_complete": True,
                 "completed_stages": ["performance-prepare", "performance-format"]}
        args.state.write_text(json.dumps(state))
        controller = PhysicalAcceptance(args)
        def fail(test, state, save_state):
            state["capacity_write_started"] = True
            save_state()
            test.report["capacity_result"] = {"outcome": "operation-failure", "errors": {"execution": "no space"}}
            raise RuntimeError("capacity operation failed")
        with mock.patch.object(controller, "verify_owner"), mock.patch.object(controller, "start_owned"), \
                mock.patch.object(controller, "stop_owned") as stop, \
                mock.patch("package_physical_capacity.check_preflight"), \
                mock.patch("package_physical_capacity.run_stage", side_effect=fail):
            with self.assertRaisesRegex(RuntimeError, "capacity operation failed"):
                controller.execute()
        stop.assert_not_called()
        original = (args.out / "report.json").read_bytes()
        preserved = evidence / "stages/capacity-write/report.json"
        self.assertEqual(preserved.read_bytes(), original)
        report = json.loads(original)
        self.assertEqual(report["status"], "failed")
        self.assertEqual(report["error"], "capacity operation failed")
        self.assertIn("retained", report["retention"])
        self.assertEqual(report["cases"][0]["status"], "failed")
        self.assertGreaterEqual(report["duration_seconds"], report["cases"][0]["duration_seconds"])
        args.out = self.directory / "rejected-replay"
        with self.assertRaisesRegex(RuntimeError, "never replay"):
            PhysicalAcceptance(args).execute()
        self.assertEqual(preserved.read_bytes(), original)

    def test_service_stop_failure_keeps_completed_stage_and_original_report(self):
        controller = self.controller()
        evidence = self.directory / "common-evidence"
        controller.args.state.write_text(json.dumps({**self.owned_state(), "physical_evidence_dir": str(evidence)}))
        with mock.patch.object(controller, "verify_owner"), mock.patch.object(controller, "start_owned"), \
                mock.patch.object(controller, "stop_owned", side_effect=RuntimeError("service did not stop")), \
                mock.patch("package_physical_cases.run_stage"):
            with self.assertRaisesRegex(RuntimeError, "service did not stop"):
                controller.execute()
        preserved = evidence / "stages/restore/report.json"
        original = preserved.read_bytes()
        report = json.loads(original)
        self.assertEqual(report["status"], "failed")
        self.assertEqual(report["cases"][0]["status"], "passed")
        controller.args.out = self.directory / "rejected-after-stop-failure"
        replay = PhysicalAcceptance(controller.args)
        with mock.patch.object(replay, "verify_owner") as owner, \
                mock.patch.object(replay, "start_owned") as start, \
                mock.patch.object(replay, "stop_owned"), mock.patch.object(replay, "case") as case:
            with self.assertRaisesRegex(RuntimeError, "already completed"):
                replay.execute()
        owner.assert_not_called()
        start.assert_not_called()
        case.assert_not_called()
        self.assertEqual(preserved.read_bytes(), original)

    def test_completed_stage_rejection_preserves_original_report(self):
        for stage in ("baseline", "performance-format", "restore"):
            with self.subTest(stage=stage):
                args = self.resumed_arguments()
                args.physical_stage = stage
                args.out = self.directory / (stage + "-rejected")
                evidence = self.directory / (stage + "-evidence")
                state = {**self.owned_state(), "physical_evidence_dir": str(evidence)}
                if stage == "performance-format":
                    state.update(performance=True, performance_source_read_mode="buffered",
                                 performance_format_started=True, performance_format_complete=True,
                                 completed_stages=["performance-prepare", stage])
                elif stage == "restore":
                    state["completed_stages"].append(stage)
                    state["restore_complete"] = True
                else:
                    state["baseline_complete"] = True
                args.state.write_text(json.dumps(state))
                preserved = evidence / "stages" / stage / "report.json"
                preserved.parent.mkdir(parents=True)
                original = b'{"status":"passed","duration_seconds":123.456}\n'
                preserved.write_bytes(original)
                controller = PhysicalAcceptance(args)
                with mock.patch.object(controller, "verify_owner") as owner, \
                        mock.patch.object(controller, "start_owned") as start, \
                        mock.patch.object(controller, "case") as case:
                    with self.assertRaisesRegex(RuntimeError, "already completed"):
                        controller.execute()
                owner.assert_not_called()
                start.assert_not_called()
                case.assert_not_called()
                self.assertEqual(controller.report["case_scope"]["executed"], [])
                self.assertEqual(preserved.read_bytes(), original)

    def test_admitted_format_rejection_preserves_failed_or_canceled_report(self):
        for reason in ("FORMAT failed", "FORMAT canceled"):
            with self.subTest(reason=reason):
                args = self.resumed_arguments()
                args.physical_stage = "performance-format"
                args.out = self.directory / reason / "first"
                evidence = self.directory / reason / "common-evidence"
                state = {**self.owned_state(), "physical_evidence_dir": str(evidence),
                         "performance": True, "performance_source_read_mode": "buffered",
                         "completed_stages": ["performance-prepare"]}
                args.state.write_text(json.dumps(state))
                controller = PhysicalAcceptance(args)
                def fail(test, state, save_state):
                    state["performance_format_started"] = True
                    save_state()
                    raise RuntimeError(reason)
                with mock.patch.object(controller, "verify_owner"), \
                        mock.patch.object(controller, "start_owned"), \
                        mock.patch("package_physical_performance.run_stage", side_effect=fail):
                    with self.assertRaisesRegex(RuntimeError, reason):
                        controller.execute()
                original = (args.out / "report.json").read_bytes()
                preserved = evidence / "stages/performance-format/report.json"
                self.assertEqual(preserved.read_bytes(), original)
                args.out = self.directory / reason / "rejected"
                controller = PhysicalAcceptance(args)
                with mock.patch.object(controller, "verify_owner") as owner, \
                        mock.patch.object(controller, "start_owned") as start, \
                        mock.patch.object(controller, "case") as case:
                    with self.assertRaisesRegex(RuntimeError, "FORMAT was already admitted"):
                        controller.execute()
                owner.assert_not_called()
                start.assert_not_called()
                case.assert_not_called()
                self.assertEqual(controller.report["case_scope"]["executed"], [])
                self.assertEqual(preserved.read_bytes(), original)

    def configured_scripts(self, observe):
        controller = self.controller()
        controller.args.physical_stage = "performance-prepare"
        controller.args.capacity_observe = observe
        controller.root = str(self.directory / "isolated")
        controller.state = {}
        sources = Path(__file__).resolve().parents[1] / "scripts"
        written = {}
        def read(argv):
            if argv == ["cat", controller.root + "/config.yaml"]:
                return SimpleNamespace(stdout=b'{"database":{}}')
            if argv[0] == "cat":
                return SimpleNamespace(stdout=(sources / Path(argv[1]).name).read_bytes())
            return SimpleNamespace(stdout=b"")
        with mock.patch.object(controller, "remote", side_effect=read), \
                mock.patch.object(controller, "write", side_effect=lambda path, data: written.update({path: data})), \
                mock.patch.object(controller, "save_state"):
            controller.configure_tape()
        return controller, written

    def test_capacity_hooks_are_opt_in_and_installed_during_preparation(self):
        controller, written = self.configured_scripts(False)
        sources = Path(__file__).resolve().parents[1] / "scripts"
        for name in ("mount.openltfs", "umount", "mkfs"):
            self.assertEqual(written[controller.root + "/tape-scripts/" + name], (sources / name).read_bytes())
        self.assertNotIn("capacity_helper", controller.state)

    def test_capacity_write_reuses_helper_from_configure_after_state_reload(self):
        from package_physical_capacity import CapacityCases

        controller, written = self.configured_scripts(True)
        helper = controller.root + "/tmp/capacity/sample.py"
        self.assertIn(helper, written)
        controller.save_state()
        controller.state = json.loads(controller.args.state.read_text())
        self.assertEqual(controller.state["capacity_helper"], helper)
        # FORMAT facts stand in for hardware; helper installation and write preparation are real.
        controller.state.update(source_location="1", restored_location="2", media_id="3", format_job_id="4",
                                performance_format_complete=True, physical_barcode="TST001",
                                physical_evidence_dir=str(self.directory / "capacity-evidence"))
        controller.args.capacity_files = 1
        controller.args.physical_stage = "capacity-write"
        controller.args.capacity_preflight = self.directory / "preflight.json"
        controller.args.capacity_preflight.write_text(json.dumps({
            "status": "mounted", "timestamp_ns": 1,
            "argv": ["work_directory=" + controller.install + "/work/jobs/4/tapes/TST001"],
            "pages": {"31": {"raw": "31", "parameters": {str(n): 0 for n in range(1, 5)}},
                      "0C": {"raw": "0c", "parameters": {str(n): 0 for n in (0, 1, 2, 3, 4, 5, 7)}}},
            "statvfs": {"available_bytes": 0, "fragment_bytes": 4096}}))
        cases = CapacityCases(controller, controller.state, controller.save_state)
        with mock.patch.object(controller, "remote") as remote, mock.patch.object(controller, "write") as write, \
                mock.patch.object(cases, "available", return_value=1024**4), \
                mock.patch.object(cases, "check_media"), \
                mock.patch.object(cases, "inspect", return_value={"media": {"id": "3"}}), \
                mock.patch.object(cases, "stream_fixture", return_value={"size": 4 * 1024**3, "sha256": "a" * 64}) as generate, \
                mock.patch.object(cases, "created", side_effect=RuntimeError("stop before Archive creation")) as created:
            with self.assertRaisesRegex(RuntimeError, "recorded a failure"):
                cases.capacity_write()
            with self.assertRaisesRegex(RuntimeError, "never replay"):
                cases.capacity_write()
        remote.assert_called_once_with(["mkdir", "--", controller.root + "/fixtures/source/capacity"])
        write.assert_not_called()
        generate.assert_called_once()
        created.assert_called_once()
        self.assertEqual(len(cases.source_manifest()), 1)
        result = json.loads((cases.evidence / "capacity/write-result.json").read_text())
        self.assertEqual(result["errors"]["execution"], "stop before Archive creation")

    def test_capacity_hook_failure_blocks_mount_return_but_not_normal_unmount(self):
        controller, written = self.configured_scripts(True)
        helper = controller.root + "/tmp/capacity/sample.py"
        self.assertEqual(controller.state["capacity_helper"], helper)
        for name in ("sample.py", "physical_performance_fixture.py", "physical_capacity_probe.py", "physical_capacity_boundary.py"):
            self.assertIn(str(Path(helper).with_name(name)), written)
        scripts = self.directory / "adapters"
        scripts.mkdir()
        (scripts / "get_device").write_text("#!/bin/sh\nprintf '%s\\n' /unused-test-device\n")
        (scripts / "get_device").chmod(0o700)
        for name in ("mount.openltfs", "umount"):
            (scripts / name).write_bytes(written[controller.root + "/tape-scripts/" + name])
        tape = self.directory / "tape"
        tape.mkdir()
        events = self.directory / "events"
        # Every hardware-facing command is a local stub; execute only adapter control flow.
        stubs = '''
ltfs() { echo mount >> "$EVENTS"; return "$MOUNT_EXIT"; }
sleep() { :; }
df() { printf 'Mounted on\\n%s\\n' "$MOUNT_POINT"; }
python3() {
    local phase output
    while (($#)); do
        case "$1" in --phase) phase="$2"; shift;; --output) output="$2"; shift;; esac
        shift
    done
    echo "$phase" >> "$EVENTS"
    printf '{"retained":true}\\n' > "$output"
    return "$OBSERVATION_EXIT"
}
umount() { echo unmount >> "$EVENTS"; return "$UNMOUNT_EXIT"; }
fuser() { return 1; }
mt() { echo DR_OPEN; }
source "$0"
'''
        for name, observation, mount_exit, unmount_exit, expected, order in (
                ("mount.openltfs", 0, 0, 0, 0, ["mount", "before-copy"]),
                ("mount.openltfs", 7, 0, 0, 7, ["mount", "before-copy"]),
                ("mount.openltfs", 0, 6, 0, 6, ["mount"]),
                ("umount", 7, 0, 0, 0, ["before-unmount", "unmount"]),
                ("umount", 7, 0, 9, 9, ["before-unmount", "unmount"])):
            events.write_text("")
            env = {**os.environ, "TAPE_DIR": str(tape), "MOUNT_POINT": str(self.directory / "mount"),
                   "DEVICE": "/unused-test-device", "EVENTS": str(events), "OBSERVATION_EXIT": str(observation),
                   "MOUNT_EXIT": str(mount_exit), "UNMOUNT_EXIT": str(unmount_exit)}
            with self.subTest(name=name, observation=observation, mount_exit=mount_exit, unmount_exit=unmount_exit):
                with subprocess.Popen(["bash", "-c", stubs, str(scripts / name)], env=env,
                                      stdout=subprocess.PIPE, stderr=subprocess.PIPE) as process:
                    stdout, stderr = process.communicate(timeout=10)
                self.assertEqual(process.returncode, expected, (stdout, stderr))
                self.assertEqual(events.read_text().splitlines(), order)
        self.assertIn("Capacity final observation failed", (tape / "ltfs.log").read_text())
        self.assertEqual(json.loads((tape / "capacity-before-unmount.json").read_text()), {"retained": True})


if __name__ == "__main__":
    unittest.main()
