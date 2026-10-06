"""Local argument and ownership regressions; no host or physical acceptance."""

from contextlib import redirect_stderr
import io
import json
from pathlib import Path
import shlex
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
        for stage in ("baseline", "restore", "full-write", "full-verify", "cleanup"):
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
        for stage in ("baseline", "restore", "full-write", "full-verify", "cleanup"):
            args.physical_stage = stage
            with self.subTest(stage=stage), self.assertRaisesRegex(RuntimeError, "Complete"):
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


if __name__ == "__main__":
    unittest.main()
