"""Local controller regressions; these tests never connect to a host."""

import json
from pathlib import Path
import shlex
import shutil
import subprocess
import tarfile
import tempfile
from types import SimpleNamespace
import unittest
from unittest import mock

from package_acceptance import Acceptance, check_arguments, documents
from package_cases import Workflows, fixture_archives
from package_ltfs_cases import prepare_virtual_tapes


class ControllerTests(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory()
        self.addCleanup(self.temporary.cleanup)
        self.directory = Path(self.temporary.name)
        self.args = SimpleNamespace(host="isolated-test", test_parent="/srv/acceptance",
                                    archive=Path("yatm-linux-amd64-v1.0.0-alpha.2.tar.gz"),
                                    preview_archive=None, version="v1.0.0-alpha.2", commit="1" * 40,
                                    out=self.directory / "evidence", keep_root=False, ltfs=False)

    def controller(self):
        result = Acceptance(self.args)
        result.root = "/srv/acceptance/yatm-package-acceptance.12345678"
        result.install = result.root + "/install"
        result.service = "yatm-acceptance-12345678.service"
        return result

    def test_arguments_reject_ssh_options_and_unsafe_root(self):
        check_arguments(self.args)
        for host in ("-oProxyCommand=touch file", "valid;touch file", "name\nnext"):
            with self.subTest(host=host):
                with self.assertRaises(RuntimeError):
                    check_arguments(SimpleNamespace(**{**vars(self.args), "host": host}))
        for parent in ("/", "relative", "/safe/../other", "/safe;other"):
            with self.subTest(parent=parent):
                with self.assertRaises(RuntimeError):
                    check_arguments(SimpleNamespace(**{**vars(self.args), "test_parent": parent}))

    def test_remote_preserves_literal_filename_arguments(self):
        controller = self.controller()
        names = ["back\\slash", "line\nbreak", "$(not-a-command)", " leading ", "日本語"]
        with mock.patch.object(controller, "run") as run:
            controller.remote(["cat", "--", *names])
        command = shlex.split(run.call_args.args[0][-1])
        self.assertEqual(command, ["timeout", "--kill-after=10s", "180s", "cat", "--", *names])
        self.assertEqual(run.call_args.args[0][-2], self.args.host)

    def test_local_preserves_literal_arguments_and_timeout(self):
        self.args.host = "local"
        check_arguments(self.args)
        controller = self.controller()
        names = ["back\\slash", "line\nbreak", "$(not-a-command)", " leading ", "日本語", "; echo unsafe", "'quoted'"]
        with mock.patch.object(controller, "run") as run:
            controller.remote(["cat", "--", *names], body=b"input", expected=1, timeout=42)
        run.assert_called_once_with(
            ["timeout", "--kill-after=10s", "42s", "cat", "--", *names], b"input", 1, 62)
        self.assertEqual(controller.report["scope"], "Exact Linux package executed locally on the test host.")

    def test_local_upload_copies_literal_paths_and_records_command(self):
        self.args.host = "local"
        controller = self.controller()
        root = self.directory / "owned 日本語 $(literal)"
        root.mkdir()
        controller.root = str(root)
        files = [self.directory / "one space.txt", self.directory / "日本語; '$(literal)'.txt"]
        for index, path in enumerate(files):
            path.write_bytes(f"fixture {index}".encode())
        for destination in (root, root / "nested space"):
            with self.subTest(destination=destination):
                destination.mkdir(exist_ok=True)
                controller.upload(files, str(destination))
                for path in files:
                    self.assertEqual((destination / path.name).read_bytes(), path.read_bytes())
                record = controller.report["commands"][-1]
                self.assertEqual(record["argv"], ["cp", "--", *map(str, files), str(destination) + "/"])
                self.assertEqual(record["exit_code"], 0)

    def test_local_upload_rejects_external_destination_before_effects(self):
        self.args.host = "local"
        controller = self.controller()
        for destination in ("/elsewhere", controller.root + "-other"):
            with self.subTest(destination=destination), mock.patch.object(controller, "run") as run:
                with self.assertRaisesRegex(RuntimeError, "outside the owned root"):
                    controller.upload([self.directory / "fixture"], destination)
                run.assert_not_called()
        self.assertEqual(controller.report["commands"], [])

    def test_ssh_upload_keeps_destination_quoted(self):
        controller = self.controller()
        destination = controller.root + "/日本語 space '$(literal)'"
        files = [self.directory / "source space;literal"]
        with mock.patch.object(controller, "run") as run:
            controller.upload(files, destination)
        run.assert_called_once_with(["scp", "-q", "-o", "BatchMode=yes", "--", str(files[0]),
                                     self.args.host + ":" + shlex.quote(destination + "/")])
        self.assertEqual(shlex.split(run.call_args.args[0][-1].split(":", 1)[1]), [destination + "/"])

    def test_cli_stream_keeps_escaped_newline_and_precise_ns(self):
        raw = b'{"entries":[{"name":"line\\nbreak","mtime_ns":"1720000000000000001"}]}\n{"total_entry_count":"1"}\n'
        values = documents(raw)
        self.assertEqual(values[0]["entries"][0], {"name": "line\nbreak", "mtime_ns": "1720000000000000001"})
        self.assertEqual(values[1]["total_entry_count"], "1")
        with self.assertRaises(json.JSONDecodeError):
            documents(raw + b"not JSON\n")

    def test_failed_cli_exit_is_never_hidden_by_valid_json(self):
        controller = self.controller()
        response = subprocess.CompletedProcess(["fixture-cli"], 1, b'{"summary":{"completed":true}}\n', b"failed")
        with mock.patch("package_acceptance.subprocess.run", return_value=response):
            with self.assertRaisesRegex(RuntimeError, "exited 1, expected 0"):
                controller.run(["fixture-cli"])
        self.assertEqual(controller.report["commands"][0]["exit_code"], 1)
        self.assertEqual((self.args.out / "0001.stdout").read_bytes(), response.stdout)

    def test_cli_deadline_tracks_the_selected_workload_timeout(self):
        controller = self.controller()
        controller.url = "http://127.0.0.1:23456"
        with mock.patch.object(controller, "remote", return_value=subprocess.CompletedProcess([], 0, b'{}\n', b"")) as remote:
            self.assertEqual(controller.cli("library", "export", "--output", "-", timeout=600), [{}])
        self.assertIn("580s", remote.call_args.args[0])
        self.assertEqual(remote.call_args.kwargs["timeout"], 600)

    def test_ltfs_stage_keeps_fresh_installation_absent_and_unconfigured(self):
        controller = self.controller()
        controller.args.ltfs = True
        template = SimpleNamespace(stdout=b"database:\npaths:\n")
        listeners = SimpleNamespace(stdout=b"")

        with mock.patch.object(controller, "upload"), mock.patch.object(controller, "remote") as remote, \
                mock.patch.object(controller, "verify_programs"), \
                mock.patch.object(controller, "remote_batch", return_value=[template, listeners]), \
                mock.patch.object(controller, "write") as write, \
                mock.patch("package_ltfs_cases.prepare_virtual_tapes") as prepare:
            controller.stage()
        prepare.assert_not_called()
        config = json.loads(write.call_args.args[1])
        self.assertEqual(config["tape_devices"], [])
        self.assertNotIn("scripts", config)
        self.assertFalse(any(call.args[0][0] in ("mkdir", "cp") for call in remote.call_args_list))

    def test_fresh_ltfs_setup_waits_for_installation_and_restarts_with_installed_config(self):
        controller = self.controller()
        controller.args.ltfs = True
        controller.url = "http://127.0.0.1:23456"
        installed = False
        configured = False
        config = {"tape_devices": [], "paths": {"work": controller.install + "/work"}}

        def installer(**kwargs):
            nonlocal installed
            self.assertFalse(installed)
            if not kwargs.get("check"):
                installed = True

        def remote(argv, **kwargs):
            if argv[:2] == ["test", "-e"]:
                self.assertFalse(installed)
                return SimpleNamespace(returncode=1)
            self.assertTrue(installed)
            if argv[:2] == ["systemctl", "show"]:
                return SimpleNamespace(stdout=b"inactive\n")
            if argv[0] == "cat":
                self.assertEqual(argv[1], controller.install + "/config.yaml")
                return SimpleNamespace(stdout=json.dumps(config).encode())
            return SimpleNamespace(stdout=b"<html></html>")

        def prepare(test, value):
            self.assertTrue(installed)
            self.assertEqual(value, config)
            value["scripts"] = {"encrypt": test.install + "/scripts/virtual-tape/encrypt"}

        def write(path, data):
            nonlocal configured
            self.assertEqual(path, controller.install + "/config.yaml")
            self.assertIn("scripts", json.loads(data))
            configured = True

        def ready(test):
            self.assertTrue(configured)
            self.assertIn(mock.call(["systemctl", "start", test.service]), remote_mock.call_args_list)

        with mock.patch.object(controller, "installer", side_effect=installer), \
                mock.patch.object(controller, "remote", side_effect=remote) as remote_mock, \
                mock.patch.object(controller, "verify_programs"), mock.patch.object(controller, "one"), \
                mock.patch.object(controller, "write", side_effect=write), \
                mock.patch("package_ltfs_cases.prepare_virtual_tapes", side_effect=prepare), \
                mock.patch("package_install_cases.wait_for_service", side_effect=ready) as wait:
            controller.install_fresh()
        wait.assert_called_once_with(controller)
        self.assertIn(mock.call(["systemctl", "stop", controller.service]), remote_mock.call_args_list)

    def test_virtual_tape_adapters_are_installed_and_cartridges_remain_outside(self):
        controller = self.controller()
        controller.root = str(self.directory / "owned")
        controller.install = controller.root + "/install"
        scripts = Path(controller.install) / "scripts"
        scripts.mkdir(parents=True)
        template = Path(controller.root) / "package/templates/testing/ltfs-file-backend"
        shutil.copytree(Path(__file__).parent / "ltfs-file-backend", template)

        def remote(argv, **kwargs):
            if argv[0] == "sh":
                return SimpleNamespace(stdout=("/opt/ltfs/bin/" + argv[-1] + "\n").encode())
            return subprocess.run(argv, capture_output=True, check=True)

        with mock.patch.dict("os.environ", {"YATM_E2E_LTFS_CAPTURE_INDEX": "bare"}), \
                mock.patch.object(controller, "remote", side_effect=remote), \
                mock.patch.object(controller, "write", side_effect=lambda path, data: Path(path).write_bytes(data)):
            config = {}
            prepare_virtual_tapes(controller, config)
        for path in config["scripts"].values():
            self.assertTrue(Path(path).is_file())
            self.assertEqual(Path(path).parent, scripts / "virtual-tape")
        self.assertIn("\nexport YATM_E2E_LTFS_CAPTURE_INDEX=bare\n", Path(config["scripts"]["mount"]).read_text())
        for device in config["tape_devices"]:
            self.assertEqual(Path(device).parent, Path(controller.root) / "virtual-tapes")
            self.assertFalse(Path(device).is_relative_to(controller.install))

    def test_fixtures_retain_invalid_bytes_and_exact_signed_time(self):
        files, regular, invalid = fixture_archives(self.directory)
        with tarfile.open(regular) as archive:
            self.assertEqual(archive.getmember("source/pre-epoch.txt").pax_headers["mtime"], "-0.000000123")
            self.assertEqual(archive.getmember("source/epoch.txt").pax_headers["mtime"], "0.000000000")
            self.assertNotEqual(archive.getmember("source/same-ms-a.txt").pax_headers["mtime"],
                                archive.getmember("source/same-ms-b.txt").pax_headers["mtime"])
            for name, value in files.items():
                self.assertEqual(archive.extractfile("source/" + name).read(), value["data"])
        with tarfile.open(invalid, encoding="utf-8", errors="surrogateescape") as archive:
            self.assertIn(b"invalid-\xff.txt", [entry.name.encode("utf-8", "surrogateescape") for entry in archive])

    def test_backup_exercises_both_rejected_timestamp_shapes(self):
        controller = self.controller()
        original = {"entry": {"associated_file_id": "1"}, "organization": {"note": "original"}}
        edited = {"entries": [{"entry": original["entry"], "organization": {"note": "changed after export"}}]}
        records = [{"type": "file", "data": {"id": str(index), "note": "original",
                                             "created_at_ns": "1720000000000000001"}}
                   for index in (1, 2)]
        encoded = b"".join(json.dumps(record).encode() + b"\n" for record in records)
        workflow = SimpleNamespace(test=controller, locations={"source": "1"})
        with mock.patch.object(controller, "one", side_effect=[original, edited, original]), \
                mock.patch.object(controller, "remote", return_value=SimpleNamespace(stdout=encoded)), \
                mock.patch.object(controller, "cli", side_effect=[[], [], records, [], records, [], records]) as cli, \
                mock.patch.object(controller, "write") as write:
            Workflows.backup(workflow)

        self.assertEqual(write.call_count, 2)
        obsolete, numeric = [documents(call.args[1]) for call in write.call_args_list]
        self.assertEqual(obsolete[-1]["data"]["created_at_ms"], "1720000000000000001")
        self.assertNotIn("created_at_ns", obsolete[-1]["data"])
        self.assertEqual(numeric[-1]["data"]["created_at_ns"], 1720000000000000001)
        self.assertEqual([call.kwargs for call in cli.call_args_list if call.args[:2] == ("library", "import")],
                         [{}, {"expected": 1}, {"expected": 1}])
        self.assertEqual(records[0]["data"]["note"], "original")
        self.assertEqual(records[-1]["data"]["created_at_ns"], "1720000000000000001")

    def test_timeout_retains_partial_transcripts_without_claiming_an_exit(self):
        controller = self.controller()
        failure = subprocess.TimeoutExpired(["fixture-cli"], 30, output=b"started\n", stderr=b"still waiting\n")
        with mock.patch("package_acceptance.subprocess.run", side_effect=failure):
            with self.assertRaisesRegex(RuntimeError, "timed out"):
                controller.run(["fixture-cli"], timeout=30)
        self.assertEqual((self.args.out / "0001.stdout").read_bytes(), b"started\n")
        self.assertEqual((self.args.out / "0001.stderr").read_bytes(), b"still waiting\n")
        self.assertEqual(controller.report["commands"][0]["timed_out_seconds"], 30)
        self.assertNotIn("exit_code", controller.report["commands"][0])

    def test_cleanup_does_not_treat_network_failure_as_missing_service(self):
        controller = self.controller()
        with mock.patch.object(controller, "remote", return_value=subprocess.CompletedProcess([], 255, b"", b"connection lost")) as remote:
            with self.assertRaisesRegex(RuntimeError, "Cannot establish service ownership"):
                controller.cleanup()
        self.assertEqual(remote.call_count, 1)
        self.assertNotIn("cleanup", controller.report)

    def test_cleanup_refuses_foreign_service_and_retains_failed_root(self):
        controller = self.controller()
        responses = [subprocess.CompletedProcess([], 0, b"LoadState=loaded\nFragmentPath=/etc/systemd/system/foreign.service\n", b""),
                     subprocess.CompletedProcess([], 0, b"/elsewhere/foreign.service\n", b"")]
        with mock.patch.object(controller, "remote", side_effect=responses) as remote:
            with self.assertRaisesRegex(RuntimeError, "owned by another path"):
                controller.cleanup()
        self.assertEqual(remote.call_count, 2)
        controller.report["status"] = "failed"
        with mock.patch.object(controller, "remote", return_value=subprocess.CompletedProcess([], 1, b"LoadState=not-found\nFragmentPath=\n", b"")) as remote:
            controller.cleanup()
        self.assertEqual(remote.call_count, 1)
        self.assertTrue(controller.report["cleanup"]["root_retained"])

    def test_cleanup_unmounts_only_owned_fuse_paths(self):
        controller = self.controller()
        controller.args.ltfs = True
        controller.report["status"] = "failed"
        owned = controller.root + "/install/work/mount/tape"
        mounts = {"filesystems": [{"target": "/", "fstype": "ext4"},
                                   {"target": "/another-attempt/tape", "fstype": "fuse.ltfs"},
                                   {"target": owned, "fstype": "fuse.ltfs"}]}
        responses = [subprocess.CompletedProcess([], 0, b"LoadState=not-found\nFragmentPath=\n", b""),
                     subprocess.CompletedProcess([], 0, json.dumps(mounts).encode(), b""),
                     subprocess.CompletedProcess([], 0, b"", b"")]
        with mock.patch.object(controller, "remote", side_effect=responses) as remote:
            controller.cleanup()
        self.assertEqual(remote.call_args_list[-1].args[0], ["fusermount", "-u", owned])
        self.assertEqual(remote.call_count, 3)
        self.assertTrue(controller.report["cleanup"]["root_retained"])

    def test_cleanup_retains_root_with_unexpected_mount(self):
        controller = self.controller()
        controller.args.ltfs = True
        controller.report["status"] = "passed"
        mounts = {"filesystems": [{"target": controller.root + "/other", "fstype": "ext4"}]}
        responses = [subprocess.CompletedProcess([], 0, b"LoadState=not-found\nFragmentPath=\n", b""),
                     subprocess.CompletedProcess([], 0, json.dumps(mounts).encode(), b"")]
        with mock.patch.object(controller, "remote", side_effect=responses) as remote:
            with self.assertRaisesRegex(RuntimeError, "Unexpected filesystem"):
                controller.cleanup()
        self.assertEqual(remote.call_count, 2)


if __name__ == "__main__":
    unittest.main()
