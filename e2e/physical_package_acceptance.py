"""Run explicitly authorized scratch-Tape stages through shipped YATM programs."""

import argparse

import json
import os
from pathlib import Path, PurePosixPath
import re
import shlex
import sys
import time

from package_acceptance import Acceptance, argument_parser, check_arguments, documents, require


STAGES = ("prepare", "baseline", "restore", "cleanup")
PERFORMANCE_STAGES = ("performance-prepare", "performance-format", "performance-run")
CAPACITY_STAGES = ("capacity-write", "capacity-verify")
TAPE_SCRIPTS = {"read_info": "readinfo", "encrypt": "encrypt", "mkfs": "mkfs",
                "mount": "mount.openltfs", "umount": "umount"}


def physical_arguments(argv=None):
    parser = argument_parser()
    parser.description = __doc__
    parser.add_argument("--physical-device", required=True)
    parser.add_argument("--physical-barcode", required=True, help="Explicitly assigned, erasable six-character barcode.")
    parser.add_argument("--physical-stage", choices=STAGES + PERFORMANCE_STAGES + CAPACITY_STAGES, required=True)
    parser.add_argument("--state", type=Path, required=True, help="Private local state shared by the separate stages.")
    parser.add_argument("--mkltfs", default="mkltfs", help="Host executable used by the isolated packaged format script.")
    parser.add_argument("--ltfs-binary", default="ltfs", help="Host executable used by the isolated packaged mount script.")
    parser.add_argument("--performance-case", action="append", choices=("small-random", "small-compressible", "large-random", "large-compressible"), help="Affected cases in fixed matrix order; default all four.")
    parser.add_argument("--performance-source-read-mode", choices=("buffered", "mapped"), default="buffered",
                        help="Fixed original Location read mode for a performance comparison.")
    parser.add_argument("--performance-size-gib", type=int, choices=(4, 32, 64), default=4,
                        help="Logical fixture size per selected performance case.")
    parser.add_argument("--performance-driver-sampling", action=argparse.BooleanOptionalAction, default=True,
                        help="Read owned LTFS metric xattrs; disable for collector calibration.")
    parser.add_argument("--capacity-observe", action="store_true",
                        help="Install passive capacity hooks before FORMAT; repeat on subsequent stages.")
    parser.add_argument("--capacity-source-reflink", action="store_true",
                        help="Require reflink capacity sources to reduce fixture disk use; repeat on subsequent stages.")
    parser.add_argument("--capacity-files", type=int, choices=range(1, 101), default=24,
                        help="Number of ordinary 4 GiB Archive source files; default 24 (96 GiB).")
    parser.add_argument("--capacity-file-mib", action="append", type=int,
                        help="Explicit ordered file sizes in MiB (repeat, up to 100); replaces --capacity-files.")
    parser.add_argument("--capacity-preflight", type=Path,
                        help="Saved passive sample from this installation's active FORMAT mount; required before capacity-write.")
    observers = parser.add_mutually_exclusive_group()
    observers.add_argument("--syscall-trace", action="append",
                           help="Cleanup: exact absolute host path of a finalized syscall trace; repeat for each file.")
    observers.add_argument("--no-syscall-trace", action="store_true",
                           help="Cleanup: explicitly confirm no syscall observer was used.")
    parser.add_argument("--require-sg-trace", action="store_true",
                        help="Cleanup: require SG traces for the capacity Archive and Restore Jobs.")
    args = parser.parse_args(argv)
    check_physical_arguments(args)
    return args


def check_physical_arguments(args):
    if args.physical_stage == "cleanup":
        require(args.syscall_trace or args.no_syscall_trace,
                "Cleanup requires explicit --syscall-trace paths or --no-syscall-trace.")
    else:
        require(not (args.syscall_trace or args.no_syscall_trace or args.require_sg_trace),
                "Observer evidence options apply only to cleanup.")
    for path in args.syscall_trace or []:
        require(PurePosixPath(path).is_absolute() and ".." not in PurePosixPath(path).parts
                and "\x00" not in path, "Supply exact absolute syscall trace paths without traversal.")
    if args.capacity_file_mib is not None:
        require(1 <= len(args.capacity_file_mib) <= 100 and all(1 <= size <= 65536 for size in args.capacity_file_mib),
                "Choose up to 100 positive file sizes, each at most 64 GiB.")
        args.capacity_files = len(args.capacity_file_mib)
    require(not args.ltfs and not args.legacy_package and not args.legacy_fixture and not args.case,
            "Physical stages cannot select virtual Tape, legacy migration or ordinary package cases.")
    require(args.physical_stage in PERFORMANCE_STAGES + CAPACITY_STAGES + ("cleanup",) or args.preview_archive is not None,
            "Use the matching packaged native Preview helper.")
    require(re.fullmatch(r"/dev/(?:n?st\d+[alm]?|tape/by-id/[A-Za-z0-9_.:-]+)", args.physical_device),
            "Choose an explicit physical Tape device or its by-id path.")
    require(re.fullmatch(r"[A-Z0-9]{6}", args.physical_barcode), "Choose the exact six-character scratch barcode.")
    for executable in (args.mkltfs, args.ltfs_binary):
        require(re.fullmatch(r"(?:/[A-Za-z0-9_./-]+|[A-Za-z0-9_.-]+)", executable)
                and ".." not in PurePosixPath(executable).parts, "Choose a host LTFS executable without shell syntax.")
    check_arguments(args)
    require(args.state.exists() != (args.physical_stage in ("prepare", "performance-prepare")),
            "Prepare needs new state; later stages require the preserved local state.")


def validate_state(args, state):
    expected = {"host": args.host, "commit": args.commit, "version": args.version,
                "device": args.physical_device, "barcode": args.physical_barcode,
                "mkltfs": args.mkltfs, "ltfs_binary": args.ltfs_binary}
    require(all(state.get(key) == value for key, value in expected.items()),
            "Stage inputs differ from the owned physical acceptance state.")
    require(state.get("capacity_observe", False) == args.capacity_observe,
            "Capacity observation differs from saved preparation.")
    require(state.get("capacity_source_reflink", False) == args.capacity_source_reflink,
            "Capacity source reflink differs from saved preparation.")
    root = state.get("remote_root", "")
    require(re.fullmatch(re.escape(args.test_parent.rstrip("/")) + r"/yatm-package-acceptance\.[A-Za-z0-9]{8}", root),
            "State does not identify an owned root below the selected test parent.")
    require(state.get("install") == root + "/install"
            and state.get("service") == "yatm-acceptance-" + root.rsplit(".", 1)[1] + ".service"
            and re.fullmatch(r"http://127\.0\.0\.1:\d+", state.get("url", "")),
            "State contains an unexpected installation, service or listener.")
    require(not state.get("cleaned"), "This physical acceptance root has already been cleaned.")
    if args.physical_stage == "cleanup":
        from package_physical_cases import check_cleanup_state
        check_cleanup_state(state)
        return
    require(args.physical_stage not in state.get("completed_stages", []),
            "Physical stage already completed; retain its original evidence and timing.")
    if args.physical_stage == "performance-format":
        require(not state.get("performance_format_started"),
                "FORMAT was already admitted; inspect preserved state instead of replaying.")
    stages = PERFORMANCE_STAGES if args.physical_stage in PERFORMANCE_STAGES + CAPACITY_STAGES else STAGES
    require(state.get("performance", False) == (stages == PERFORMANCE_STAGES), "Cannot mix physical acceptance and performance state.")
    if stages == PERFORMANCE_STAGES:
        require(state.get("performance_driver_sampling", True) == getattr(args, "performance_driver_sampling", True),
                "Performance driver sampling differs from saved preparation.")
        require(state.get("performance_size_gib", 4) == args.performance_size_gib,
                "Performance fixture size differs from saved preparation.")
        require(state.get("performance_source_read_mode") == args.performance_source_read_mode,
                "Performance source read mode differs from saved preparation.")
    if args.physical_stage in CAPACITY_STAGES:
        require(state.get("performance_format_complete"), "Capacity stages require completed performance-format.")
        require(not state.get("performance_completed") and not any(
            key.startswith("performance_") and key.endswith("_archive") for key in state),
            "Capacity stages require a fresh formatted installation without performance writes.")
        require(state.get("capacity_files", args.capacity_files) == args.capacity_files,
                "Capacity fixture count differs from saved admission.")
        sizes = getattr(args, "capacity_file_mib", None)
        if "capacity_file_sizes" in state:
            requested = [size * 1024**2 for size in sizes] if sizes else [4 * 1024**3] * args.capacity_files
            require(state["capacity_file_sizes"] == requested, "Capacity fixture sizes differ from saved admission.")
        marker = "capacity_write_started" if args.physical_stage == "capacity-write" else "capacity_verify_started"
        require(not state.get(marker), "Capacity stage already admitted; never replay its Media operation.")
        if args.physical_stage == "capacity-write":
            from package_physical_capacity import check_preflight
            check_preflight(args.capacity_preflight, state)
        if args.physical_stage == "capacity-verify":
            # A failed research attempt still has evidence and may have published files.
            require(state.get("capacity_write_observed"), "Observe capacity-write before capacity-verify.")
            require(state.get("capacity_device_released"), "Capacity device ownership is uncertain; preserve the owner.")
            return
        previous = "performance-format"
    else:
        previous = stages[stages.index(args.physical_stage) - 1]
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
        self.report["limits"].append("Hardware checks CLI barcode refusal; backend Session identity and failed-Media lifecycle checks reuse local/CI evidence, not a physical RPC mismatch probe.")
        self.save()

    def save_state(self):
        self.state.update(host=self.args.host, commit=self.args.commit, version=self.args.version,
                          device=self.args.physical_device, barcode=self.args.physical_barcode,
                          mkltfs=self.args.mkltfs, ltfs_binary=self.args.ltfs_binary,
                          capacity_observe=self.args.capacity_observe,
                          capacity_source_reflink=self.args.capacity_source_reflink,
                          remote_root=self.root, install=self.install, service=self.service, url=self.url)
        self.args.state.parent.mkdir(parents=True, exist_ok=True)
        temporary = self.args.state.with_suffix(self.args.state.suffix + ".tmp")
        temporary.write_text(json.dumps(self.state, indent=2) + "\n")
        temporary.replace(self.args.state)

    def wait_job(self, job, status="JOB_STATUS_COMPLETED", timeout=180):
        # Retry only transient observations, never an operation or a durable Job failure.
        deadline, retries, backoff = time.monotonic() + timeout, 0, 2
        while True:
            remaining = int(deadline - time.monotonic())
            require(remaining > 20, "Physical Job observation exceeded its original deadline.")
            result = self.remote([self.install + "/yatm-cli", "--server", self.url,
                                  "--timeout", f"{remaining - 20}s", "job", "wait", str(job["id"]),
                                  "--wait-timeout", f"{remaining - 20}s", "--poll-interval", "2s"],
                                 expected=None, timeout=remaining)
            rows = documents(result.stdout)
            response = rows[0].get("job", {}) if len(rows) == 1 else {}
            try:
                error = json.loads(result.stderr)
            except ValueError:
                error = {}
            transient = (result.returncode == 1 and error.get("code") == "unknown"
                         and 'method="/yatm.v1.JobService/Get"' in error.get("error", "")
                         and "database is locked" in error.get("error", "")
                         and response.get("status") != "JOB_STATUS_FAILED")
            if transient:
                retries += 1
                remaining = deadline - time.monotonic()
                delay = min(backoff, max(0, remaining - 20))
                self.report.setdefault("observation_retries", []).append({
                    "job_id": str(job["id"]), "attempt": retries, "error": error,
                    "remaining_seconds": remaining, "delay_seconds": delay,
                    "last_observed_status": response.get("status"), "last_observed_phase": response.get("phase")})
                self.save()
                require(delay > 0, "Physical Job observation exceeded its original deadline.")
                time.sleep(delay)
                backoff = min(backoff * 2, 30)
                continue
            expected = 0 if status == "JOB_STATUS_COMPLETED" else 1
            require(result.returncode == expected and
                    ((expected == 0 and not error) or (expected == 1 and error.get("code") == "action_required")),
                    "Physical Job observation failed: " + str(error.get("error", result.stderr.decode(errors="replace"))))
            require(response.get("status") == status, "Physical Job reached an unexpected durable state.")
            return response

    def configure_tape(self):
        config = json.loads(self.remote(["cat", self.root + "/config.yaml"]).stdout)
        adapters = self.root + "/tape-scripts"
        self.remote(["mkdir", adapters])
        if self.args.capacity_observe:
            from package_physical_capacity import CapacityCases
            helper = CapacityCases(self, self.state, self.save_state).install_helper()
            boundary = str(PurePosixPath(helper).with_name("physical_capacity_boundary.py"))
            observation = ("python3 -B " + shlex.quote(boundary)
                           + ' --work-directory "${TAPE_DIR}" --mount "${MOUNT_POINT}"')
        names = ("get_device", "readinfo", "encrypt", "umount", "mkfs", "mount.openltfs")
        source = self.root + "/package/templates/scripts/"
        for name in names:
            data = self.remote(["cat", source + name]).stdout.decode()
            if name == "mkfs":
                data = adapt_script(data, "mkltfs", self.args.mkltfs,
                                    "" if self.args.physical_stage in PERFORMANCE_STAGES else " -r 'size=1M'")
            elif name == "mount.openltfs":
                data = adapt_script(data, "ltfs", self.args.ltfs_binary)
            if self.args.capacity_observe and name == "mount.openltfs":
                data += ('\n' + observation + ' --phase before-copy --output "${TAPE_DIR}/capacity-before-copy.json"\n')
            if self.args.capacity_observe and name == "umount":
                marker = "\nmain() {\n"
                require(data.count(marker) == 1, "Packaged unmount script changed; review its capacity hook.")
                hook = ("    " + observation + ' --phase before-unmount --output "${TAPE_DIR}/capacity-before-unmount.json"'
                        + ' || echo "Capacity final observation failed; continuing normal unmount." >&2\n')
                data = data.replace(marker, marker + hook, 1)
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

    def check_cleanup_paths(self):
        if self.args.host == "local":
            root = Path(self.root).resolve()
            for path in (self.args.state, self.args.out, Path(self.state["physical_evidence_dir"])):
                require(not path.resolve().is_relative_to(root),
                        "Cleanup would delete preserved state or evidence; retain the owned root.")
        mounts = json.loads(self.remote(["findmnt", "--list", "--json", "--output", "TARGET"]).stdout)
        require("filesystems" in mounts and not any(
            row["target"] == self.root or row["target"].startswith(self.root + "/")
            for row in mounts["filesystems"]), "An owned mount remains or mount state is unknown; refusing cleanup.")

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
            if self.args.physical_stage in ("prepare", "performance-prepare"):
                with self.phase("verify-inputs"):
                    self.verify_inputs()
                with self.phase("prepare-host"):
                    for tool in ("mt", "stenc", "fuser", "umount", "openssl", "sqlite3", "findmnt",
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
                if self.args.physical_stage == "cleanup":
                    self.check_cleanup_paths()
                self.start_owned()
            self.report.update(remote_root=self.root, install=self.install, service=self.service, url=self.url)
            if self.args.physical_stage in CAPACITY_STAGES:
                from package_physical_capacity import run_stage
            elif self.args.physical_stage in PERFORMANCE_STAGES:
                from package_physical_performance import run_stage
            else:
                from package_physical_cases import run_stage
            self.case("physical-" + self.args.physical_stage, lambda: run_stage(self, self.state, self.save_state))
            self.state.setdefault("completed_stages", [])
            if self.args.physical_stage not in self.state["completed_stages"]:
                self.state["completed_stages"].append(self.args.physical_stage)
            self.report["status"] = "passed"
            if self.args.physical_stage == "cleanup":
                self.check_cleanup_paths()
                self.args.keep_root = False
                self.cleanup()
                self.state["cleaned"] = True
                self.save_state()
            else:
                # Finishing service teardown must not make completed device work replayable.
                self.save_state()
                self.stop_owned()
                self.report["cleanup"] = {"service_stopped": True, "root_retained": True}
        except Exception as error:
            self.report.update(status="failed", error=str(error),
                               retention="Owned resources and any active physical operation retained for inspection.")
            raise
        finally:
            self.save()
            if (self.args.physical_stage != "cleanup" and self.state.get("physical_evidence_dir")
                    and self.report["case_scope"]["executed"]):
                from package_physical_cases import PhysicalCases
                PhysicalCases(self, self.state, self.save_state).keep(
                    "stages/" + self.args.physical_stage + "/report.json",
                    (self.args.out / "report.json").read_bytes())


def main(argv=None):
    args = physical_arguments(argv)
    os.umask(0o077)
    PhysicalAcceptance(args).execute()
    return 0


if __name__ == "__main__":
    sys.exit(main())
