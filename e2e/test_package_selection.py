"""Focused controller selection, timing and batching regressions; no host connections."""

from contextlib import ExitStack, redirect_stdout
import io
import json
from pathlib import Path
import shlex
import subprocess
import tarfile
import tempfile
import threading
from types import SimpleNamespace
import unittest
from unittest import mock

from package_acceptance import Acceptance, CASE_DEPENDENCIES, PROGRAMS, case_scope, check_arguments, main, sha256
from package_legacy_cases import run_legacy


class SelectionTests(unittest.TestCase):
    def setUp(self):
        temporary = tempfile.TemporaryDirectory()
        self.addCleanup(temporary.cleanup)
        self.root = Path(temporary.name)
        self.args = SimpleNamespace(host="isolated-test", test_parent="/srv/acceptance",
                                    archive=Path("yatm-linux-amd64-v1.0.0-alpha.2.tar.gz"),
                                    preview_archive=None, version="v1.0.0-alpha.2", commit="1" * 40,
                                    out=self.root / "evidence", keep_root=False, ltfs=False,
                                    legacy_package=None, legacy_fixture=None, case=None)

    def test_exact_requests_add_only_dependencies_once_in_stable_order(self):
        self.args.case = ["volume-inventory-import-delete-and-register", "recorded-library-and-live-location-search",
                          "volume-inventory-import-delete-and-register"]
        scope = case_scope(self.args)
        self.assertEqual(scope["requested"], self.args.case[:2])
        self.assertEqual(scope["selected"], ["fresh-systemd-installation", "scan-overlap-multiple-locations-and-recreate",
                                             "recorded-library-and-live-location-search", "volume-archive-restore-and-media-scan",
                                             "volume-copy-damage-missing-and-health", "volume-inventory-import-delete-and-register"])
        self.assertEqual(scope["prerequisite"], ["fresh-systemd-installation", "scan-overlap-multiple-locations-and-recreate",
                                                 "volume-archive-restore-and-media-scan", "volume-copy-damage-missing-and-health"])
        self.assertTrue(scope["subset"])
        self.assertEqual(scope["executed"], [])
        self.assertIn({"name": "native-preview-assets-and-generation-policies", "reason": "No Preview archive was selected."},
                      scope["skipped"])

    def test_invalid_profiles_and_unavailable_cases_fail_before_any_setup(self):
        for name in ("literal*", "literal-filenames", "legacy-readonly-installer-review",
                     "native-preview-assets-and-generation-policies", "ltfs-format-append-restore-verify"):
            with self.subTest(name=name):
                self.args.case = [name]
                with self.assertRaisesRegex(RuntimeError, "Unknown|unavailable"):
                    check_arguments(self.args)
                self.assertFalse(self.args.out.exists())
        self.args.legacy_package = Path("yatm-linux-amd64-v0.1.8.tar.gz")
        self.args.legacy_fixture = self.root / "approved-copy.tar.gz"
        self.args.case = ["fresh-systemd-installation"]
        with self.assertRaisesRegex(RuntimeError, "Unknown legacy case"):
            check_arguments(self.args)

    def test_case_cli_option_is_repeatable(self):
        argv = ["package_acceptance.py", "--host", self.args.host, "--test-parent", self.args.test_parent,
                "--archive", str(self.args.archive), "--version", self.args.version, "--commit", self.args.commit,
                "--out", str(self.args.out), "--case", "literal-filenames-and-nanoseconds",
                "--case", "invalid-byte-row-and-cli-failure"]
        with mock.patch("package_acceptance.sys.argv", argv), mock.patch("package_acceptance.os.umask"), \
                mock.patch("package_acceptance.Acceptance") as controller:
            self.assertEqual(main(), 0)
        self.assertEqual(controller.call_args.args[0].case,
                         ["literal-filenames-and-nanoseconds", "invalid-byte-row-and-cli-failure"])
        controller.return_value.execute.assert_called_once()

    def test_every_dependency_precedes_its_case_and_defaults_keep_complete_profiles(self):
        for profile, dependencies in CASE_DEPENDENCIES.items():
            names = list(dependencies)
            for name, prerequisites in dependencies.items():
                self.assertTrue(all(names.index(prerequisite) < names.index(name) for prerequisite in prerequisites))
        self.args.preview_archive = Path("preview.tar.gz")
        self.args.ltfs = True
        scope = case_scope(self.args)
        self.assertEqual(scope["selected"], list(CASE_DEPENDENCIES["normal"]))
        self.assertEqual(scope["prerequisite"], [])
        self.assertEqual(scope["skipped"], [])
        self.assertFalse(scope["subset"])
        self.args.legacy_package = Path("legacy.tar.gz")
        self.args.case = ["legacy-complete-jsonl-roundtrip", "legacy-repeated-cleanup-and-historical-repair"]
        scope = case_scope(self.args)
        self.assertEqual(scope["selected"], ["legacy-exact-package-installer-upgrade",
                                             "legacy-repeated-cleanup-and-historical-repair", "legacy-complete-jsonl-roundtrip"])
        self.assertEqual(scope["prerequisite"], ["legacy-exact-package-installer-upgrade"])
        self.args.case = None
        self.assertEqual(case_scope(self.args)["selected"], list(CASE_DEPENDENCIES["legacy"]))

    def execute_normal(self, controller):
        with ExitStack() as stack:
            for method in ("verify_inputs", "prepare_host", "stage", "install_fresh", "installation_checks", "cleanup"):
                stack.enter_context(mock.patch.object(controller, method))
            workflows = stack.enter_context(mock.patch("package_cases.Workflows"))
            workflows.return_value.locations = {"source": "1", "restored": "2"}
            stack.enter_context(mock.patch("package_install_cases.sqlite_wal_restart"))
            stack.enter_context(mock.patch("package_install_cases.installation_replacement"))
            stack.enter_context(mock.patch("package_ltfs_cases.TapeWorkflows"))
            stack.enter_context(redirect_stdout(io.StringIO()))
            controller.execute()
            return workflows

    def test_normal_execution_automatically_runs_prerequisites_and_never_claims_release(self):
        self.args.case = ["recorded-library-and-live-location-search", "lossless-jsonl-export-import"]
        controller = Acceptance(self.args)
        workflows = self.execute_normal(controller)
        self.assertEqual(controller.report["case_scope"]["executed"], ["fresh-systemd-installation",
                         "scan-overlap-multiple-locations-and-recreate", "recorded-library-and-live-location-search",
                         "lossless-jsonl-export-import"])
        workflows.return_value.scan.assert_called_once()
        workflows.return_value.search.assert_called_once()
        workflows.return_value.backup.assert_called_once()
        workflows.return_value.listing.assert_not_called()
        workflows.return_value.preparation_barrier.assert_not_called()
        self.assertEqual(controller.report["status"], "passed")
        self.assertTrue(controller.report["case_scope"]["subset"])
        self.assertFalse(controller.report["release_accepted"])
        self.assertTrue(all(item["duration_seconds"] >= 0 for item in controller.report["cases"] + controller.report["phases"]))
        self.assertEqual(controller.report["phases"][-1]["name"], "cleanup")

    def test_default_normal_execution_keeps_all_existing_cases_in_order(self):
        self.args.preview_archive = Path("preview.tar.gz")
        self.args.ltfs = True
        controller = Acceptance(self.args)
        self.execute_normal(controller)
        self.assertEqual([item["name"] for item in controller.report["cases"]], list(CASE_DEPENDENCIES["normal"]))
        self.assertFalse(controller.report["case_scope"]["subset"])

    def test_installer_only_selection_does_not_create_workflow_fixtures(self):
        self.args.case = ["readonly-checks-and-unchanged-rerun"]
        controller = Acceptance(self.args)
        workflows = self.execute_normal(controller)
        workflows.assert_not_called()
        self.assertEqual(controller.report["case_scope"]["executed"], ["fresh-systemd-installation", "readonly-checks-and-unchanged-rerun"])

    def test_monotonic_timing_retains_failure_timeout_cleanup_and_unreached_scope(self):
        self.args.case = ["recorded-library-and-live-location-search"]
        controller = Acceptance(self.args)
        timeout = subprocess.TimeoutExpired(["stub"], 2, output=b"partial", stderr=b"waiting")
        with ExitStack() as stack:
            for method in ("verify_inputs", "prepare_host", "stage"):
                stack.enter_context(mock.patch.object(controller, method))
            stack.enter_context(mock.patch.object(controller, "install_fresh", side_effect=lambda: controller.run(["stub"], timeout=2)))
            stack.enter_context(mock.patch.object(controller, "cleanup", side_effect=lambda: controller.run(["cleanup-stub"])))
            stack.enter_context(mock.patch("package_acceptance.subprocess.run", side_effect=[timeout, subprocess.CompletedProcess([], 0, b"", b"")]))
            stack.enter_context(mock.patch("package_acceptance.time.time", side_effect=AssertionError("wall clock used")))
            with self.assertRaisesRegex(RuntimeError, "timed out"):
                controller.execute()
        report = json.loads((self.args.out / "report.json").read_text())
        self.assertEqual(report["status"], "failed")
        self.assertEqual(report["cases"][0]["status"], "failed")
        self.assertEqual(report["commands"][0]["case"], "fresh-systemd-installation")
        self.assertEqual(report["commands"][0]["phase"], "cases")
        self.assertNotIn("exit_code", report["commands"][0])
        self.assertEqual(report["commands"][0]["timed_out_seconds"], 2)
        self.assertEqual(report["commands"][1]["phase"], "cleanup")
        self.assertEqual(report["phases"][-1]["status"], "passed")
        self.assertTrue(all(item["duration_seconds"] >= 0 for item in report["commands"] + report["cases"] + report["phases"]))
        self.assertIn({"name": "recorded-library-and-live-location-search", "reason": "Not reached after failure."}, report["case_scope"]["skipped"])

    def test_cleanup_failure_is_reported_and_timed(self):
        self.args.case = ["fresh-systemd-installation"]
        controller = Acceptance(self.args)
        with ExitStack() as stack:
            for method in ("verify_inputs", "prepare_host", "stage", "install_fresh"):
                stack.enter_context(mock.patch.object(controller, method))
            stack.enter_context(mock.patch.object(controller, "cleanup", side_effect=RuntimeError("ownership unknown")))
            stack.enter_context(redirect_stdout(io.StringIO()))
            with self.assertRaisesRegex(RuntimeError, "ownership unknown"):
                controller.execute()
        self.assertEqual(controller.report["status"], "failed")
        self.assertEqual(controller.report["cleanup_error"], "ownership unknown")
        self.assertEqual(controller.report["phases"][-1]["status"], "failed")
        self.assertGreaterEqual(controller.report["phases"][-1]["duration_seconds"], 0)

    def test_readonly_batch_is_bounded_quoted_and_retains_every_individual_outcome(self):
        controller = Acceptance(self.args)
        names = ["$(literal)", "line\nbreak", "back\\slash", "日本語", "failed", "timeout"]
        barrier = threading.Barrier(4, timeout=5)

        def invoke(argv, **kwargs):
            command = shlex.split(argv[-1])
            self.assertEqual(command[:4], ["timeout", "--kill-after=10s", "3s", "read-only-stub"])
            self.assertEqual(kwargs["timeout"], 23)
            name = command[-1]
            if name in names[:4]:
                barrier.wait()
            if name == "timeout":
                raise subprocess.TimeoutExpired(argv, 23, output=b"partial", stderr=b"waiting")
            return subprocess.CompletedProcess(argv, 7 if name == "failed" else 0, name.encode(), b"individual stderr")

        with mock.patch("package_acceptance.subprocess.run", side_effect=invoke):
            with self.assertRaisesRegex(RuntimeError, "exited 7"):
                controller.remote_batch([["read-only-stub", name] for name in names], timeout=3)
        commands = controller.report["commands"]
        self.assertEqual(len(commands), len(names))
        self.assertEqual([shlex.split(item["argv"][-1])[-1] for item in commands], names)
        self.assertEqual([item.get("exit_code") for item in commands], [0, 0, 0, 0, 7, None])
        for name, item in zip(names, commands):
            self.assertGreaterEqual(item["duration_seconds"], 0)
            self.assertEqual((self.args.out / item["stdout"]).read_bytes(), b"partial" if name == "timeout" else name.encode())

    def test_missing_tool_records_all_prerequisites_and_never_allocates_a_root(self):
        controller = Acceptance(self.args)

        def invoke(argv, **kwargs):
            command = shlex.split(argv[-1])[3:]
            output, code = b"", 0
            if command[0] == "uname":
                output = b"Linux x86_64\n"
            elif command[0] == "id":
                output = b"0\n"
            elif command[0] == "df":
                output = b"Filesystem 1024-blocks Used Available Capacity Mounted\n/dev/test 99999999 0 99999999 0% /srv\n"
            elif command[-1] == "jq":
                code = 1
            else:
                output = ("/usr/bin/" + command[-1]).encode()
            return subprocess.CompletedProcess(argv, code, output, b"")

        with mock.patch("package_acceptance.subprocess.run", side_effect=invoke):
            with self.assertRaisesRegex(RuntimeError, "exited 1"):
                controller.prepare_host()
        self.assertIsNone(controller.root)
        self.assertEqual(len(controller.report["commands"]), 12)
        self.assertEqual(sum(item["exit_code"] != 0 for item in controller.report["commands"]), 1)
        self.assertFalse(any("mktemp" in shlex.split(item["argv"][-1]) for item in controller.report["commands"]))

    def test_offline_identity_batch_keeps_each_result_before_rejecting_a_mismatch(self):
        controller = Acceptance(self.args)

        def invoke(argv, **kwargs):
            command = shlex.split(argv[-1])[3:]
            self.assertEqual(command[-1], "--version")
            program = command[0].rsplit("/", 1)[-1]
            value = {"program": program, "version": self.args.version, "commit": self.args.commit}
            if program == "yatm-migrate":
                value["commit"] = "2" * 40
            return subprocess.CompletedProcess(argv, 0, json.dumps(value).encode(), b"")

        with mock.patch("package_acceptance.subprocess.run", side_effect=invoke):
            with self.assertRaisesRegex(RuntimeError, "Offline identity differs: yatm-migrate"):
                controller.verify_programs("/owned/package")
        self.assertEqual(len(controller.report["commands"]), len(PROGRAMS))
        self.assertTrue(all(item["exit_code"] == 0 for item in controller.report["commands"]))

    def test_each_invocation_allocates_a_new_owned_root_and_refuses_report_reuse(self):
        roots = []
        for index in range(2):
            args = SimpleNamespace(**{**vars(self.args), "out": self.root / f"evidence-{index}"})
            controller = Acceptance(args)
            root = f"/srv/acceptance/yatm-package-acceptance.{index:08d}"

            def remote(argv, **kwargs):
                output = root.encode() if argv[0] == "mktemp" else b"owned-fixture" if argv[0] == "getfattr" else b""
                return subprocess.CompletedProcess(argv, 0, output, b"")

            checks = [subprocess.CompletedProcess([], 0, output, b"") for output in
                      (b"Linux x86_64", b"0", b"Filesystem 1024-blocks Used Available Capacity Mounted\n/dev/test 99999999 0 99999999 0% /srv\n")]
            with mock.patch.object(controller, "remote_batch", return_value=checks), mock.patch.object(controller, "remote", side_effect=remote) as calls:
                controller.prepare_host()
            self.assertEqual(calls.call_args_list[0].args[0][:2], ["mktemp", "-d"])
            roots.append(controller.root)
            with self.assertRaises(FileExistsError):
                Acceptance(args)
        self.assertNotEqual(roots[0], roots[1])

    def test_legacy_review_and_abort_selection_never_runs_upgrade_or_v1_checks(self):
        for name in ("legacy-readonly-installer-review", "legacy-prepare-decline-and-abort"):
            with self.subTest(name=name):
                args = SimpleNamespace(**{**vars(self.args), "out": self.root / name, "case": [name],
                                         "legacy_package": self.root / "yatm-linux-amd64-v0.1.8.tar.gz",
                                         "legacy_fixture": self.root / "metadata.tar.gz"})
                for path, member, contents in ((args.legacy_package, "VERSION", b"v0.1.8\n"),
                                               (args.legacy_fixture, "tapes.db", b"synthetic legacy data")):
                    with tarfile.open(path, "w:gz") as archive:
                        entry = tarfile.TarInfo(member)
                        entry.size = len(contents)
                        archive.addfile(entry, io.BytesIO(contents))
                controller = Acceptance(args)
                controller.root = "/srv/acceptance/yatm-package-acceptance.12345678"
                controller.install = controller.root + "/install"
                controller.service = "yatm-acceptance-12345678.service"
                controller.url = "http://127.0.0.1:23456"

                def remote(argv, **kwargs):
                    output, code = b"", 0
                    if argv[0] == "sha256sum":
                        local = args.legacy_package if argv[1].endswith(args.legacy_package.name) else args.out / "legacy-metadata.tar.gz"
                        output = sha256(local).encode()
                    if argv[:2] == ["systemctl", "is-active"]:
                        code = 3
                    if "-phase" in argv and argv[argv.index("-phase") + 1] == "schema":
                        output = b"legacy\n"
                    return subprocess.CompletedProcess(argv, code, output, b"")

                with mock.patch.object(controller, "upload"), mock.patch.object(controller, "write"), \
                        mock.patch.object(controller, "remote", side_effect=remote), \
                        mock.patch.object(controller, "installer", return_value=SimpleNamespace(stdout=b"Prepared migration aborted\n")) as installer, \
                        mock.patch.object(controller, "verify_programs") as verify, \
                        mock.patch.object(controller, "one") as one, redirect_stdout(io.StringIO()):
                    run_legacy(controller)
                self.assertEqual(controller.report["case_scope"]["executed"], [name])
                installer.assert_called_once()
                verify.assert_not_called()
                one.assert_not_called()
                self.assertEqual(controller.report["cases"][0]["status"], "passed")


if __name__ == "__main__":
    unittest.main()
