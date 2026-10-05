"""Run explicitly authorized scratch-Tape stages through shipped YATM programs."""

import json
import os
from pathlib import Path, PurePosixPath
import re
import shlex
import sys

from package_acceptance import Acceptance, argument_parser, check_arguments, require


STAGES = ("baseline", "restore", "full-write", "full-verify", "cleanup")
TAPE_SCRIPTS = {"read_info": "readinfo", "encrypt": "encrypt", "mkfs": "mkfs",
                "mount": "mount.openltfs", "umount": "umount"}


def physical_arguments(argv=None):
    parser = argument_parser()
    parser.description = __doc__
    parser.add_argument("--physical-device", required=True)
    parser.add_argument("--physical-barcode", required=True, help="Explicitly assigned, erasable six-character barcode.")
    parser.add_argument("--physical-stage", choices=STAGES, required=True)
    parser.add_argument("--state", type=Path, required=True, help="Private local state shared by the separate stages.")
    parser.add_argument("--mkltfs", default="mkltfs", help="Host executable used by the isolated packaged format script.")
    parser.add_argument("--ltfs-binary", default="ltfs", help="Host executable used by the isolated packaged mount script.")
    args = parser.parse_args(argv)
    check_physical_arguments(args)
    return args


def check_physical_arguments(args):
    require(not args.ltfs and not args.legacy_package and not args.legacy_fixture and not args.case,
            "Physical stages cannot select virtual Tape, legacy migration or ordinary package cases.")
    require(args.preview_archive is not None, "Use the matching packaged native Preview helper.")
    require(re.fullmatch(r"/dev/(?:n?st\d+[alm]?|tape/by-id/[A-Za-z0-9_.:-]+)", args.physical_device),
            "Choose an explicit physical Tape device or its by-id path.")
    require(re.fullmatch(r"[A-Z0-9]{6}", args.physical_barcode), "Choose the exact six-character scratch barcode.")
    for executable in (args.mkltfs, args.ltfs_binary):
        require(re.fullmatch(r"(?:/[A-Za-z0-9_./-]+|[A-Za-z0-9_.-]+)", executable)
                and ".." not in PurePosixPath(executable).parts, "Choose a host LTFS executable without shell syntax.")
    check_arguments(args)
    require(args.state.exists() != (args.physical_stage == "baseline"),
            "Baseline needs new state; later stages require the preserved local state.")


def validate_state(args, state):
    expected = {"host": args.host, "commit": args.commit, "version": args.version,
                "device": args.physical_device, "barcode": args.physical_barcode,
                "mkltfs": args.mkltfs, "ltfs_binary": args.ltfs_binary}
    require(all(state.get(key) == value for key, value in expected.items()),
            "Stage inputs differ from the owned physical acceptance state.")
    root = state.get("remote_root", "")
    require(re.fullmatch(re.escape(args.test_parent.rstrip("/")) + r"/yatm-package-acceptance\.[A-Za-z0-9]{8}", root),
            "State does not identify an owned root below the selected test parent.")
    require(state.get("install") == root + "/install"
            and state.get("service") == "yatm-acceptance-" + root.rsplit(".", 1)[1] + ".service"
            and re.fullmatch(r"http://127\.0\.0\.1:\d+", state.get("url", "")),
            "State contains an unexpected installation, service or listener.")
    require(not state.get("cleaned"), "This physical acceptance root has already been cleaned.")
    previous = STAGES[STAGES.index(args.physical_stage) - 1]
    require(previous in state.get("completed_stages", []), f"Complete {previous} before {args.physical_stage}.")


def adapt_script(source, command, executable, options=""):
    marker = "\n" + command + " "
    require(source.count(marker) == 1, f"Packaged {command} script changed; review its adaptation.")
    return source.replace(marker, "\n" + shlex.quote(executable) + options + " ", 1)


class PhysicalAcceptance(Acceptance):
    def __init__(self, args):
        super().__init__(args)
        self.state = {}
        name = "physical-" + args.physical_stage
        self.selected_cases = {name}
        self.report["case_scope"] = {"profile": "physical", "requested": [name], "prerequisite": [],
                                     "selected": [name], "executed": [], "skipped": [], "subset": True}
        self.report["physical_device"] = args.physical_device
        self.report["physical_barcode"] = args.physical_barcode
        self.report["limits"].append("Runner-level wrong-barcode fault injection reuses local/CI coverage; hardware checks the public CLI refusal.")
        self.save()

    def save_state(self):
        self.state.update(host=self.args.host, commit=self.args.commit, version=self.args.version,
                          device=self.args.physical_device, barcode=self.args.physical_barcode,
                          mkltfs=self.args.mkltfs, ltfs_binary=self.args.ltfs_binary,
                          remote_root=self.root, install=self.install, service=self.service, url=self.url)
        self.args.state.parent.mkdir(parents=True, exist_ok=True)
        temporary = self.args.state.with_suffix(self.args.state.suffix + ".tmp")
        temporary.write_text(json.dumps(self.state, indent=2) + "\n")
        temporary.replace(self.args.state)

    def wait_job(self, job, status="JOB_STATUS_COMPLETED", timeout=180):
        response = self.one("job", "wait", str(job["id"]), "--wait-timeout", f"{timeout - 20}s", "--poll-interval", "2s",
                            expected=0 if status == "JOB_STATUS_COMPLETED" else 1, timeout=timeout)
        require(response["job"]["status"] == status, "Physical Job reached an unexpected durable state.")
        return response["job"]

    def configure_tape(self):
        config = json.loads(self.remote(["cat", self.root + "/config.yaml"]).stdout)
        adapters = self.root + "/tape-scripts"
        self.remote(["mkdir", adapters])
        names = ("get_device", "readinfo", "encrypt", "umount", "mkfs", "mount.openltfs")
        source = self.root + "/package/templates/scripts/"
        for name in names:
            data = self.remote(["cat", source + name]).stdout.decode()
            if name == "mkfs":
                data = adapt_script(data, "mkltfs", self.args.mkltfs, " -r 'size=1M/name=*.txt'")
            elif name == "mount.openltfs":
                data = adapt_script(data, "ltfs", self.args.ltfs_binary)
            self.write(adapters + "/" + name, data.encode())
        self.remote(["chmod", "700", *[adapters + "/" + name for name in names]])
        config["tape_devices"] = [self.args.physical_device]
        config["database"]["sqlite_wal"] = True
        config["scripts"] = {key: adapters + "/" + name for key, name in TAPE_SCRIPTS.items()}
        self.write(self.root + "/config.yaml", json.dumps(config, indent=2).encode() + b"\n")

    def verify_owner(self):
        fragment, config = self.remote_batch([
            ["systemctl", "show", self.service, "--property", "FragmentPath", "--value"],
            ["cat", self.install + "/config.yaml"],
        ])
        filename = fragment.stdout.decode().strip()
        require(filename, "Owned service has no source file.")
        resolved = self.remote(["readlink", "-f", "--", filename]).stdout.decode().strip()
        require(resolved == self.install + "/" + self.service, "Refusing a service outside the owned installation.")
        value = json.loads(config.stdout)
        require(value["tape_devices"] == [self.args.physical_device]
                and value["database"] == {"dialect": "sqlite", "dsn": "./catalog.db", "sqlite_wal": True}
                and value["scripts"] == {key: self.root + "/tape-scripts/" + name
                                         for key, name in TAPE_SCRIPTS.items()}
                and value["paths"]["work"] == self.install + "/work"
                and value["paths"]["access"] == [{"root": self.root + "/fixtures"}]
                and value["domain"] == self.url, "Owned physical configuration changed.")

    def start_owned(self):
        self.remote(["systemctl", "start", self.service])
        from package_install_cases import wait_for_service
        wait_for_service(self)

    def stop_owned(self):
        self.remote(["systemctl", "stop", self.service])
        state = self.remote(["systemctl", "show", self.service, "--property", "ActiveState", "--value"]).stdout.strip()
        require(state == b"inactive", "Owned physical acceptance service did not stop.")

    def execute(self):
        try:
            if self.args.physical_stage == "baseline":
                with self.phase("verify-inputs"):
                    self.verify_inputs()
                with self.phase("prepare-host"):
                    for tool in ("sg_map", "mt", "stenc", "fuser", "umount", "openssl", "sqlite3", "findmnt",
                                 self.args.mkltfs, self.args.ltfs_binary):
                        self.remote(["sh", "-c", 'command -v "$1"', "check-tool", tool])
                    self.prepare_host()
                    self.save_state()
                with self.phase("stage"):
                    self.stage()
                    self.configure_tape()
                    self.install_fresh()
                    self.save_state()
                commands = (("media", "inspect", "tape"), ("archive", "create"),
                            ("archive", "write", "tape", "format"), ("archive", "write", "tape", "append"),
                            ("restore", "create"), ("restore", "run", "tape"), ("job", "wait"),
                            ("library", "export"), ("job", "delete"), ("preview", "get"))
                self.remote_batch([[self.install + "/yatm-cli", *parts, "--help"] for parts in commands])
            else:
                self.state = json.loads(self.args.state.read_text())
                validate_state(self.args, self.state)
                self.root, self.install, self.service, self.url = (self.state[key] for key in
                                                               ("remote_root", "install", "service", "url"))
                self.report.update(remote_root=self.root, install=self.install, service=self.service, url=self.url)
                self.verify_owner()
                self.start_owned()
            self.report.update(remote_root=self.root, install=self.install, service=self.service, url=self.url)
            from package_physical_cases import run_stage
            self.case("physical-" + self.args.physical_stage, lambda: run_stage(self, self.state, self.save_state))
            self.state.setdefault("completed_stages", [])
            if self.args.physical_stage not in self.state["completed_stages"]:
                self.state["completed_stages"].append(self.args.physical_stage)
            self.report["status"] = "passed"
            if self.args.physical_stage == "cleanup":
                mounts = json.loads(self.remote(["findmnt", "--list", "--json", "--output", "TARGET"]).stdout)
                require(not any(row["target"] == self.root or row["target"].startswith(self.root + "/")
                                for row in mounts.get("filesystems", [])), "An owned mount remains; refusing cleanup.")
                self.args.keep_root = False
                self.cleanup()
                self.state["cleaned"] = True
            else:
                self.stop_owned()
                self.report["cleanup"] = {"service_stopped": True, "root_retained": True}
            self.save_state()
        except Exception as error:
            self.report.update(status="failed", error=str(error),
                               retention="Owned resources and any active physical operation retained for inspection.")
            raise
        finally:
            self.save()


def main(argv=None):
    args = physical_arguments(argv)
    os.umask(0o077)
    PhysicalAcceptance(args).execute()
    return 0


if __name__ == "__main__":
    sys.exit(main())
