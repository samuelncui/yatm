"""Controller for shipped YATM programs on an isolated Linux test host, locally or over SSH."""

import argparse
from concurrent.futures import ThreadPoolExecutor
from contextlib import contextmanager
import hashlib
import json
import os
from pathlib import Path, PurePosixPath
import re
import secrets
import shlex
import subprocess
import sys
import tarfile
import time


ROOT = Path(__file__).resolve().parents[1]
PROGRAMS = ("yatm-httpd", "yatm-cli", "yatm-migrate", "yatm-export-library", "yatm-lto-info")

# Dependencies own required state, not the preceding case's position in the full run.
CASE_DEPENDENCIES = {
    "normal": {
        "fresh-systemd-installation": (),
        "readonly-checks-and-unchanged-rerun": ("fresh-systemd-installation",),
        "literal-filenames-and-nanoseconds": ("fresh-systemd-installation",),
        "invalid-byte-row-and-cli-failure": ("fresh-systemd-installation",),
        "scan-all-source-preparation-barrier": ("fresh-systemd-installation",),
        "scan-overlap-multiple-locations-and-recreate": ("fresh-systemd-installation",),
        "recorded-library-and-live-location-search": ("scan-overlap-multiple-locations-and-recreate",),
        "dryrun-delete-and-recover-literal-path": ("fresh-systemd-installation",),
        "volume-archive-restore-and-media-scan": ("fresh-systemd-installation",),
        "volume-copy-damage-missing-and-health": ("volume-archive-restore-and-media-scan",),
        "volume-inventory-import-delete-and-register": ("volume-copy-damage-missing-and-health",),
        "lossless-jsonl-export-import": ("scan-overlap-multiple-locations-and-recreate",),
        "native-preview-assets-and-generation-policies": ("fresh-systemd-installation",),
        "ltfs-format-append-restore-verify": ("fresh-systemd-installation",),
        "ltfs-full-prefix-second-media-restore-verify": ("fresh-systemd-installation",),
        "sqlite-wal-enable-persist-and-disable": ("scan-overlap-multiple-locations-and-recreate",),
        # Preserve actual Catalog and historical Jobs rather than comparing empty inventories.
        "same-version-replacement-and-complete-backup-recovery": ("scan-overlap-multiple-locations-and-recreate",),
    },
    "legacy": {
        "legacy-readonly-installer-review": (),
        "legacy-prepare-decline-and-abort": (),
        "legacy-exact-package-installer-upgrade": (),
        "legacy-repeated-cleanup-and-historical-repair": ("legacy-exact-package-installer-upgrade",),
        "legacy-complete-jsonl-roundtrip": ("legacy-exact-package-installer-upgrade",),
    },
}


def case_scope(args):
    profile = "legacy" if getattr(args, "legacy_package", None) else "normal"
    dependencies = CASE_DEPENDENCIES[profile]
    unavailable = {}
    if profile == "normal":
        if not args.preview_archive:
            unavailable["native-preview-assets-and-generation-policies"] = "No Preview archive was selected."
        if not args.ltfs:
            for name in ("ltfs-format-append-restore-verify", "ltfs-full-prefix-second-media-restore-verify"):
                unavailable[name] = "--ltfs was not selected."
    available = [name for name in dependencies if name not in unavailable]
    requested = list(dict.fromkeys(getattr(args, "case", None) or available))
    selected = set()

    def include(name):
        require(name in dependencies, f"Unknown {profile} case: {name}.")
        require(name not in unavailable, f"Case {name} is unavailable: {unavailable.get(name)}")
        if name in selected:
            return
        for prerequisite in dependencies[name]:
            include(prerequisite)
        selected.add(name)

    for name in requested:
        include(name)
    return {"profile": profile, "requested": requested,
            "prerequisite": [name for name in dependencies if name in selected and name not in requested],
            "selected": [name for name in dependencies if name in selected], "executed": [],
            "skipped": [{"name": name, "reason": unavailable.get(name, "Not selected by --case.")}
                        for name in dependencies if name not in selected],
            "subset": selected != set(available)}


def require(condition, message):
    if not condition:
        raise RuntimeError(message)


def sha256(path):
    digest = hashlib.sha256()
    with Path(path).open("rb") as stream:
        for block in iter(lambda: stream.read(1 << 20), b""):
            digest.update(block)
    return digest.hexdigest()


def documents(data):
    """CLI streams contain one complete ProtoJSON document per line."""
    return [json.loads(line) for line in data.splitlines() if line.strip()]


def check_arguments(args):
    require(re.fullmatch(r"[A-Za-z0-9][A-Za-z0-9_.@-]*", args.host), "Choose 'local' or an explicit SSH test-host alias.")
    parent = PurePosixPath(args.test_parent)
    require(parent.is_absolute() and parent != PurePosixPath("/") and ".." not in parent.parts
            and re.fullmatch(r"/[A-Za-z0-9_./-]+", args.test_parent), "Choose an absolute test parent without shell syntax.")
    require(re.fullmatch(r"v\d+\.\d+\.\d+(?:-[A-Za-z0-9.-]+)?", args.version), "An explicit release version is required.")
    require(re.fullmatch(r"[0-9a-f]{40}", args.commit), "An exact full commit is required.")
    require(args.archive.name == f"yatm-linux-amd64-{args.version}.tar.gz", "Select the Linux amd64 main archive.")
    require(not args.out.exists(), "Use a new private evidence directory.")
    legacy_package = getattr(args, "legacy_package", None)
    legacy_fixture = getattr(args, "legacy_fixture", None)
    require(bool(legacy_package) == bool(legacy_fixture), "Select both a legacy package and an approved metadata fixture.")
    if legacy_package:
        require(not args.ltfs and not args.preview_archive, "Run legacy migration separately from LTFS/Preview acceptance.")
        require(re.fullmatch(r"yatm-linux-amd64-v0\.1\.\d+\.tar\.gz", legacy_package.name),
                "Select a published Linux amd64 v0.1.x package.")
    case_scope(args)


class Acceptance:
    def __init__(self, args):
        self.args = args
        self.root = None
        self.service = None
        self.install = None
        self.url = None
        self.sequence = 0
        self.started = time.monotonic()
        self.current_case = None
        self.current_phase = None
        scope = case_scope(args)
        self.selected_cases = set(scope["selected"])
        args.out.mkdir(parents=True, mode=0o700)
        self.report = {"status": "running", "version": args.version, "commit": args.commit,
                       "host": args.host, "cases": [], "commands": [], "phases": [], "limits": [],
                       "case_scope": scope,
                       "release_accepted": False,
                       "scope": ("Exact Linux package executed locally on the test host."
                                 if args.host == "local" else
                                 "Exact Linux package over SSH; controller and assertions remain local.")}
        self.save()

    def save(self):
        self.report["duration_seconds"] = time.monotonic() - self.started
        (self.args.out / "report.json").write_text(json.dumps(self.report, indent=2) + "\n")

    def command_record(self, argv):
        self.sequence += 1
        stem = f"{self.sequence:04d}"
        record = {"argv": [str(value) for value in argv], "stdout": stem + ".stdout", "stderr": stem + ".stderr",
                  "case": self.current_case, "phase": self.current_phase}
        self.report["commands"].append(record)
        return record

    @staticmethod
    def invoke(argv, body, timeout):
        start = time.monotonic()
        try:
            result = subprocess.run(argv, input=body, capture_output=True, timeout=timeout)
        except Exception as error:
            result = error
        return result, time.monotonic() - start

    def command_result(self, record, outcome, expected, timeout):
        result, record["duration_seconds"] = outcome
        (self.args.out / record["stdout"]).write_bytes(getattr(result, "stdout", None) or b"")
        (self.args.out / record["stderr"]).write_bytes(getattr(result, "stderr", None) or b"")
        stem = record["stdout"].removesuffix(".stdout")
        if isinstance(result, subprocess.TimeoutExpired):
            record["timed_out_seconds"] = timeout
            self.save()
            raise RuntimeError(f"Command {stem} timed out; inspect its private transcript.") from result
        if isinstance(result, Exception):
            record["error"] = str(result)
            self.save()
            raise result
        record["exit_code"] = result.returncode
        self.save()
        if expected is not None:
            require(result.returncode == expected,
                    f"Command {stem} exited {result.returncode}, expected {expected}; inspect its private transcript.")
        return result

    def run(self, argv, body=None, expected=0, timeout=180):
        record = self.command_record(argv)
        self.save()
        return self.command_result(record, self.invoke(record["argv"], body, timeout), expected, timeout)

    def remote_arguments(self, argv, timeout):
        # Quote each argument once; no fixture filename or CLI selection becomes shell source.
        command = ["timeout", "--kill-after=10s", f"{timeout}s", *map(str, argv)]
        if self.args.host == "local":
            return command
        return ["ssh", "-o", "BatchMode=yes", "-o", "ConnectTimeout=10", self.args.host, shlex.join(command)]

    def remote(self, argv, body=None, expected=0, timeout=180):
        return self.run(self.remote_arguments(argv, timeout), body, expected, timeout + 20)

    def remote_batch(self, commands, expected=0, timeout=180):
        """Batch independent read-only calls; retain every result even when one fails."""
        if not commands:
            return []
        records = [self.command_record(self.remote_arguments(argv, timeout)) for argv in commands]
        self.save()
        results, failures = [], []
        with ThreadPoolExecutor(max_workers=min(4, len(records))) as pool:
            futures = [pool.submit(self.invoke, record["argv"], None, timeout + 20) for record in records]
            for record, future in zip(records, futures):
                try:
                    results.append(self.command_result(record, future.result(), expected, timeout + 20))
                except Exception as error:
                    failures.append(error)
        if failures:
            raise failures[0]
        return results

    def upload(self, files, directory):
        require(directory == self.root or directory.startswith(self.root + "/"), "Upload is outside the owned root.")
        if self.args.host == "local":
            self.run(["cp", "--", *map(str, files), directory + "/"])
            return
        self.run(["scp", "-q", "-o", "BatchMode=yes", "--", *map(str, files),
                  self.args.host + ":" + shlex.quote(directory + "/")])

    def write(self, path, data):
        require(path.startswith(self.root + "/"), "Fixture write is outside the owned root.")
        self.remote(["tee", path], body=data)

    def cli(self, *args, expected=0, timeout=180):
        result = self.remote([self.install + "/yatm-cli", "--server", self.url, "--timeout", f"{timeout - 20}s", *args],
                             expected=expected, timeout=timeout)
        return documents(result.stdout)

    def one(self, *args, expected=0, timeout=180):
        result = self.cli(*args, expected=expected, timeout=timeout)
        require(len(result) == 1, "Expected one CLI response document.")
        return result[0]

    def rows(self, *args, expected=0):
        return [entry for page in self.cli(*args, expected=expected) for entry in page.get("entries", [])]

    def wait_job(self, job, status="JOB_STATUS_COMPLETED", timeout=180):
        response = self.one("job", "wait", str(job["id"]), "--wait-timeout", f"{timeout - 20}s", "--poll-interval", "100ms",
                            expected=0 if status == "JOB_STATUS_COMPLETED" else 1, timeout=timeout)
        require(response["job"]["status"] == status, "Job reached an unexpected durable state.")
        return response["job"]

    def wants_case(self, name):
        return name in self.selected_cases

    @contextmanager
    def phase(self, name):
        item = {"name": name, "status": "running"}
        self.report["phases"].append(item)
        previous, self.current_phase = self.current_phase, name
        self.save()
        start = time.monotonic()
        try:
            yield
        except Exception:
            item["status"] = "failed"
            raise
        else:
            item["status"] = "passed"
        finally:
            item["duration_seconds"] = time.monotonic() - start
            self.current_phase = previous
            self.save()

    def case(self, name, operation):
        if not self.wants_case(name):
            return
        item = {"name": name, "status": "running"}
        self.report["cases"].append(item)
        self.report["case_scope"]["executed"].append(name)
        previous, self.current_case = self.current_case, name
        self.save()
        start = time.monotonic()
        try:
            operation()
        except Exception:
            item["status"] = "failed"
            raise
        else:
            item["status"] = "passed"
            print(name + " passed", flush=True)
        finally:
            item["duration_seconds"] = time.monotonic() - start
            self.current_case = previous
            self.save()

    def verify_inputs(self):
        # Existing package validators own member allowlists, binary identity and corresponding source.
        archive = self.args.archive.resolve()
        companion = Path(str(archive) + ".sha256")
        digest = sha256(archive)
        require(companion.read_text().strip() == f"{digest}  {archive.name}", "Main checksum does not match.")
        self.run(["node", ROOT / "build/release/check-release.mjs", archive], timeout=300)
        with tarfile.open(archive, "r:gz") as package:
            for name, expected in (("VERSION", self.args.version), ("COMMIT", self.args.commit)):
                member = next((entry for entry in package if entry.name.removeprefix("./") == name), None)
                require(member is not None and member.isfile(), f"Missing main {name}.")
                require(package.extractfile(member).read().decode().strip() == expected, f"Main {name} differs.")
        self.report["archive"] = {"path": str(archive), "sha256": digest}
        if self.args.preview_archive:
            preview = self.args.preview_archive.resolve()
            require(preview.name == f"yatm-preview-linux-amd64-{self.args.version}.tar.gz", "Unexpected Preview target.")
            validator = (ROOT / "build/release/check-candidate-set.mjs").as_uri()
            self.run(["node", "--input-type=module", "--eval",
                      f"import {{checkPreviewPackage}} from {json.dumps(validator)}; "
                      "checkPreviewPackage(process.argv[1], process.argv[2], process.argv[3]);",
                      preview, self.args.version, self.args.commit], timeout=300)
            self.report["preview_archive"] = {"path": str(preview), "sha256": sha256(preview)}
        self.save()

    def prepare_host(self):
        # No service or existing installation is touched before host and storage checks succeed.
        required_tools = ["bash", "tar", "sha256sum", "systemctl", "curl", "jq", "ss", "setfattr", "getfattr"]
        if self.args.ltfs:
            required_tools += ["mkltfs", "ltfs", "fusermount", "mountpoint", "realpath", "findmnt", "truncate"]
        checks = self.remote_batch([["uname", "-sm"], ["id", "-u"], ["df", "-Pk", self.args.test_parent],
                                    *[["sh", "-c", 'command -v "$1"', "check-tool", tool] for tool in required_tools]])
        require(checks[0].stdout.strip() == b"Linux x86_64", "Linux x86_64 is required.")
        require(checks[1].stdout.strip() == b"0", "Systemd acceptance needs an authorized root test session.")
        space = checks[2].stdout.decode()
        required_gib = 16 if self.args.ltfs else 4
        require(int(space.splitlines()[-1].split()[3]) >= required_gib * 1024 * 1024,
                f"At least {required_gib} GiB of test space is required.")
        root = self.remote(["mktemp", "-d", self.args.test_parent.rstrip("/") + "/yatm-package-acceptance.XXXXXXXX"]).stdout.decode().strip()
        require(re.fullmatch(re.escape(self.args.test_parent.rstrip("/")) + r"/yatm-package-acceptance\.[A-Za-z0-9]{8}", root),
                "Remote allocation returned an unexpected root.")
        self.root = root
        self.install = root + "/install"
        self.service = "yatm-acceptance-" + root.rsplit(".", 1)[1] + ".service"
        self.report.update(remote_root=root, service=self.service, install=self.install)
        self.save()
        self.remote(["mkdir", root + "/package", root + "/tmp", root + "/fixtures", root + "/volumes"])
        probe = root + "/fixtures/xattr-probe"
        self.write(probe, b"fixture\n")
        self.remote(["setfattr", "-n", "user.yatm_acceptance", "-v", "owned-fixture", probe])
        require(self.remote(["getfattr", "--only-values", "-n", "user.yatm_acceptance", probe]).stdout == b"owned-fixture",
                "Test filesystem did not retain its user attribute.")
        self.remote(["rm", "--", probe])

    def installer(self, *, fresh=False, check=False, checksum=None, body=None, expected=0):
        args = ["env", "TMPDIR=" + self.root + "/tmp", "bash", self.root + "/package/install-release.sh",
                "--version", self.args.version, "--archive", self.root + "/" + self.args.archive.name,
                "--checksum", checksum or self.root + "/" + self.args.archive.name + ".sha256",
                "--install-dir", self.install, "--service", self.service]
        if fresh:
            args += ["--config", self.root + "/config.yaml"]
        if check:
            args += ["--check"]
        if self.args.preview_archive:
            args += ["--preview-archive", self.root + "/" + self.args.preview_archive.name,
                     "--preview-checksum", self.root + "/" + self.args.preview_archive.name + ".sha256"]
        else:
            args += ["--without-preview"]
        return self.remote(args, body=body, expected=expected, timeout=600)

    def stage(self):
        files = [self.args.archive, Path(str(self.args.archive) + ".sha256")]
        if self.args.preview_archive:
            files += [self.args.preview_archive, Path(str(self.args.preview_archive) + ".sha256")]
        self.upload(files, self.root)
        for archive in files[::2]:
            self.remote(["sh", "-c", 'cd "$1" && sha256sum -c "$2"', "check-package", self.root, archive.name + ".sha256"])
        self.remote(["tar", "--no-same-owner", "--no-same-permissions", "-xzf", self.root + "/" + self.args.archive.name,
                     "-C", self.root + "/package"])
        self.verify_programs(self.root + "/package")
        # Supply an explicit Volume-only configuration using the published template's field names.
        template_result, listeners = self.remote_batch([["cat", self.root + "/package/templates/config.example.yaml"],
                                                       ["ss", "-H", "-lnt"]])
        template = template_result.stdout
        require(b"database:" in template and b"paths:" in template, "Packaged configuration template is missing.")
        used = listeners.stdout.decode()
        ports = []
        while len(ports) < 2:
            port = 20000 + secrets.randbelow(30000)
            if port not in ports and not re.search(r":" + str(port) + r"\s", used):
                ports.append(port)
        self.url = f"http://127.0.0.1:{ports[0]}"
        config = {"domain": self.url, "listen": f"127.0.0.1:{ports[0]}", "debug_listen": f"127.0.0.1:{ports[1]}",
                  "database": {"dialect": "sqlite", "dsn": "./catalog.db", "sqlite_wal": False},
                  "tape_devices": [], "paths": {"work": self.install + "/work", "volumes": [self.root + "/volumes"],
                                                "access": [{"root": self.root + "/fixtures"}]},
                  "preview": {"root": self.install + "/work/previews"}}
        if self.args.ltfs:
            from package_ltfs_cases import prepare_virtual_tapes
            prepare_virtual_tapes(self, config)
        self.write(self.root + "/config.yaml", json.dumps(config, indent=2).encode() + b"\n")
        self.report["url"] = self.url
        self.save()

    def verify_programs(self, directory):
        results = self.remote_batch([[directory + "/" + program, "--version"] for program in PROGRAMS])
        for program, result in zip(PROGRAMS, results):
            value = json.loads(result.stdout)
            require(value.get("program") == program and value.get("version") == self.args.version
                    and value.get("commit") == self.args.commit, f"Offline identity differs: {program}.")

    def install_fresh(self):
        self.installer(fresh=True, check=True)
        require(self.remote(["test", "-e", self.install], expected=None).returncode == 1,
                "Read-only review created an installation.")
        self.installer(fresh=True, body=b"y\n")
        self.verify_programs(self.install)
        self.remote(["systemctl", "is-active", "--quiet", self.service])
        self.one("status")
        page = self.remote(["curl", "--fail", "--silent", self.url + "/"]).stdout
        require(b"<html" in page.lower(), "Installed frontend was not served.")

    def installation_checks(self):
        pid = self.remote(["systemctl", "show", self.service, "--property", "MainPID", "--value"]).stdout
        require(int(pid.strip()) > 0, "Owned service has no active process.")
        files = [self.install + "/" + name for name in (*PROGRAMS, "config.yaml", self.service)]
        before = self.remote(["sha256sum", "--", *files]).stdout
        self.installer(check=True)
        bad = self.root + "/wrong.sha256"
        self.write(bad, ("0" * 64 + "  " + self.args.archive.name + "\n").encode())
        require(self.installer(checksum=bad, body=b"y\n", expected=None).returncode != 0,
                "Installer accepted an altered checksum.")
        self.installer(body=b"")
        require(self.remote(["systemctl", "show", self.service, "--property", "MainPID", "--value"]).stdout == pid,
                "Read-only rejection or unchanged rerun restarted the service.")
        require(self.remote(["sha256sum", "--", *files]).stdout == before, "Read-only rejection changed active files.")
        self.one("status")

    def cleanup(self):
        if not self.root:
            return
        # Stop only the uniquely named unit whose source file belongs to this attempt.
        state = self.remote(["systemctl", "show", self.service, "--property", "FragmentPath,LoadState"], expected=None)
        properties = dict(line.split("=", 1) for line in state.stdout.decode().splitlines() if "=" in line)
        require(state.returncode in (0, 1) and properties.get("LoadState") in ("loaded", "not-found"),
                "Cannot establish service ownership; retain the attempt for inspection.")
        fragment = properties.get("FragmentPath", "")
        require(fragment or properties["LoadState"] == "not-found", "Loaded service has no verifiable source file.")
        if fragment:
            fragment = self.remote(["readlink", "-f", "--", fragment]).stdout.decode().strip()
            require(fragment == self.install + "/" + self.service, "Refusing to clean a service owned by another path.")
            self.remote(["journalctl", "--no-pager", "-u", self.service], expected=None)
            self.remote(["systemctl", "stop", self.service])
            require(self.remote(["systemctl", "show", self.service, "--property", "ActiveState", "--value"]).stdout.strip() == b"inactive",
                    "Owned service did not stop.")
            self.remote(["systemctl", "disable", self.service])
            self.remote(["systemctl", "daemon-reload"])
        if self.args.ltfs:
            mounts = json.loads(self.remote(["findmnt", "--list", "--json", "--output", "TARGET,FSTYPE"]).stdout)
            for mount in mounts.get("filesystems", []):
                if not mount["target"].startswith(self.root + "/"):
                    continue
                require(mount["fstype"].startswith("fuse"), "Unexpected filesystem in the owned root; retain it for inspection.")
                self.remote(["fusermount", "-u", mount["target"]])
        self.report["cleanup"] = {"service_stopped": True, "root_retained": True}
        if self.report["status"] == "passed" and not self.args.keep_root:
            self.remote(["rm", "-rf", "--", self.root])
            self.report["cleanup"]["root_retained"] = False
        self.save()

    def execute(self):
        try:
            with self.phase("verify-inputs"):
                self.verify_inputs()
            with self.phase("prepare-host"):
                self.prepare_host()
            with self.phase("stage"):
                self.stage()
            with self.phase("cases"):
                if getattr(self.args, "legacy_package", None):
                    from package_legacy_cases import run_legacy
                    run_legacy(self)
                else:
                    self.case("fresh-systemd-installation", self.install_fresh)
                    self.case("readonly-checks-and-unchanged-rerun", self.installation_checks)
                    from package_cases import run_cases
                    run_cases(self)
                    from package_install_cases import installation_replacement
                    self.case("same-version-replacement-and-complete-backup-recovery", lambda: installation_replacement(self))
                require(self.report["case_scope"]["executed"] == self.report["case_scope"]["selected"],
                        "Selected cases did not execute in their stable order.")
            self.report["status"] = "passed"
        except Exception as error:
            self.report.update(status="failed", error=str(error))
            raise
        finally:
            try:
                with self.phase("cleanup"):
                    self.cleanup()
            except Exception as error:
                self.report.update(status="failed", cleanup_error=str(error))
                raise
            finally:
                scope = self.report["case_scope"]
                scope["skipped"] += [{"name": name, "reason": "Not reached after failure."}
                                     for name in scope["selected"] if name not in scope["executed"]]
                self.save()


def argument_parser():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--host", required=True, help="SSH test-host alias, or 'local' to execute directly on the test host.")
    parser.add_argument("--test-parent", required=True)
    parser.add_argument("--archive", type=Path, required=True)
    parser.add_argument("--preview-archive", type=Path)
    parser.add_argument("--ltfs", action="store_true", help="Exercise official LTFS using newly allocated virtual cartridges only.")
    parser.add_argument("--version", required=True)
    parser.add_argument("--commit", required=True)
    parser.add_argument("--out", type=Path, required=True)
    parser.add_argument("--keep-root", action="store_true", help="Retain fixture data after stopping the owned service.")
    parser.add_argument("--legacy-package", type=Path, help="Published v0.1.x Linux package for isolated upgrade acceptance.")
    parser.add_argument("--legacy-fixture", type=Path, help="Approved copied metadata archive; never a live installation.")
    parser.add_argument("--case", action="append", help="Select an exact existing case name; repeat to select more. Required cases run automatically.")
    return parser


def main():
    args = argument_parser().parse_args()
    check_arguments(args)
    os.umask(0o077)
    acceptance = Acceptance(args)
    acceptance.execute()
    return 0


if __name__ == "__main__":
    sys.exit(main())
