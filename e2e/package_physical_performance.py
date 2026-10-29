"""Bounded Archive/finalization/Restore performance cases; no capacity filling."""

import hashlib
import json
import os
import signal
from pathlib import Path
import subprocess
import time

from package_acceptance import require, sha256
from package_physical_cases import PhysicalCases
from package_physical_evidence import check_positions, format_partition_map, relative_path
from physical_performance_fixture import MATRIX, dimensions
from physical_performance_verification import Verification
from physical_performance_metrics import PerformanceSampler, analyze_samples


PAGE_SIZE = 1000
VERIFY_BATCH = 64


def manifest_pages(test, kind, job, *selection):
    offset = 0
    while True:
        page = test.one(kind, "files", str(job["id"]), *selection, "--limit", str(PAGE_SIZE), "--offset", str(offset))
        batch = page.get("items", [])
        require(len(batch) <= PAGE_SIZE, "Performance manifest exceeded its requested page size.")
        offset += len(batch)
        require(offset <= 262144, "Performance manifest exceeds the largest fixture bound.")
        if batch:
            yield batch
        if not page.get("has_more"):
            return
        require(batch, "Performance manifest page did not advance.")


def sample_process(argv, timeout=20):
    process = subprocess.Popen(argv, stdout=subprocess.PIPE, stderr=subprocess.PIPE, start_new_session=True)
    timed_out = False
    try:
        try:
            stdout, stderr = process.communicate(timeout=timeout)
        except subprocess.TimeoutExpired:
            # Kill the isolated timeout/helper process group, not only its leader.
            timed_out = True
            os.killpg(process.pid, signal.SIGKILL)
            stdout, stderr = process.communicate(timeout=2)
        return {"exit_code": process.returncode, "timed_out": timed_out,
                "stdout": stdout.decode(errors="replace"), "stderr": stderr.decode(errors="replace")}
    finally:
        # Bound cleanup even if a killed helper remains blocked in a kernel xattr read.
        process.stdout.close()
        process.stderr.close()
        if process.poll() is None:
            os.killpg(process.pid, signal.SIGKILL)
        process.wait(timeout=2)



class PerformanceCases(PhysicalCases):
    def helper(self, command, *arguments, body=None):
        return self.test.remote(["python3", self.state["performance_helper"], command, *arguments],
                                body=body, timeout=7200)

    def performance_prepare(self):
        self.save(performance=True, performance_source_read_mode=self.test.args.performance_source_read_mode,
                  performance_size_gib=self.test.args.performance_size_gib,
                  performance_driver_sampling=self.test.args.performance_driver_sampling)
        require(not self.test.one("job", "list", "--limit", "1").get("jobs"),
                "Performance preparation requires an empty isolated Job catalog.")
        self.save(physical_evidence_dir=str(self.evidence.resolve()))
        for name in ("source", "restored"):
            root = self.test.root + "/fixtures/" + name
            self.test.remote(["mkdir", "-p", "--", root])
            args = ["location", "create", "--name", "Performance " + name, "--root", root]
            if name == "restored":
                args += ["--restore-target"]
            elif self.test.args.performance_source_read_mode == "mapped":
                args += ["--use-mmap"]
            location = self.test.one(*args)["location"]
            self.save(**{name + "_location": str(location["id"])})
            registered = self.test.one("location", "get", str(location["id"]))
            require(registered["location"]["root_path"] == root
                    and bool(registered["location"].get("config", {}).get("use_mmap")) ==
                    (name == "source" and self.test.args.performance_source_read_mode == "mapped")
                    and registered.get("accessibility", {}).get("accessible"),
                    "Performance Location differs from requested root/read mode.")
            self.keep_json("location-" + name + ".json", registered)
        self.source, self.restored = self.state["source_location"], self.state["restored_location"]
        data = hashlib.shake_256(b"yatm-performance-setup-v1").digest(64 * 1024)
        self.test.remote(["mkdir", "--", self.test.root + "/fixtures/source/dataset"])
        self.test.write(self.test.root + "/fixtures/source/dataset/setup.bin", data)
        self.save(fixture_manifest={"dataset/setup.bin": {"size": len(data), "sha256": hashlib.sha256(data).hexdigest()}})
        self.keep_json("setup-source-manifest.json", self.state["fixture_manifest"])
        job = self.created("format_job_id", "archive", "create", "--location", self.source + ":dataset")
        self.settled(job, "JOB_STATUS_READY", timeout=14400)
        self.check_prepared(job, "dataset")
        self.save(prepare_complete=True)
        helper = self.test.root + "/tmp/physical-performance-fixture.py"
        self.test.write(helper, Path(__file__).with_name("physical_performance_fixture.py").read_bytes())
        selected = self.test.args.performance_case or list(MATRIX)
        metrics_helper = self.test.root + "/tmp/physical-performance-metrics.py"
        self.test.write(metrics_helper, Path(__file__).with_name("physical_performance_metrics.py").read_bytes())
        self.save(performance_helper=helper, performance_metrics_helper=metrics_helper, performance_cases=[case for case in MATRIX if case in selected])
        self.keep_json("performance-matrix.json", {case: dimensions(case, self.state["performance_size_gib"]) for case in self.state["performance_cases"]})

    def performance_format(self):
        require(not self.state.get("performance_format_started"), "FORMAT was already admitted; inspect preserved state instead of replaying.")
        require(self.inspect(allow_unassigned=True)["identity"] == self.barcode, "Setup cartridge identity differs.")
        job = self.job("format_job_id")
        self.save(performance_format_started=True)
        self.format(job, self.barcode)
        self.settled(job, timeout=14400)
        self.ejected()
        media = self.media(self.barcode)
        encryption = self.saved_media(media["id"], self.barcode)["profile"]["tape"]["encryption"]
        self.save(media_id=str(media["id"]), encryption_sha256=hashlib.sha256(encryption.encode()).hexdigest())
        items = self.archive_items(job)
        self.check_fixture(items, "dataset")
        positions = self.export("performance-library-setup.jsonl")
        index = self.index(job)
        check_positions(items, positions, index, self.state["media_id"])
        log = self.evidence / "jobs" / str(job["id"]) / "tapes" / self.barcode / "ltfs.log"
        self.save(performance_partition_map=format_partition_map(log.read_text()), performance_format_complete=True)

    def timed_media(self, case, kind, job, command):
        tape_dir = self.test.install + "/work/jobs/" + str(job["id"]) + "/tapes/" + self.barcode
        raw = self.evidence / case / (kind + "-samples.jsonl")
        started_wall, started = time.time_ns(), time.monotonic()
        driver = self.state.get("performance_driver_sampling", True)
        with PerformanceSampler(self.test, job, self.state["performance_helper"], tape_dir, raw,
                                driver=driver, resource_helper_path=self.state["performance_metrics_helper"]) as sampler:
            sampler.mark("media-admission-start")
            self.test.cli(*command, timeout=300)
            sampler.mark("media-admission-returned")
            settled = self.settled(job, timeout=14400)
            elapsed = time.monotonic() - started
            sampler.mark("job-settled", {"status": settled["status"]})
        self.ejected()
        progress = self.test.one("job", "progress", str(job["id"]))
        self.keep_json(case + "/" + kind + "-progress.json", progress)
        logical_bytes = self.state.get("performance_size_gib", 4) * 1024**3
        self.keep_json(case + "/" + kind + "-timing.json", {
            "start_ns": started_wall, "elapsed_seconds": elapsed, "job": settled,
            "logical_bytes": logical_bytes, "logical_bytes_per_second": logical_bytes / elapsed,
            "app_interval_seconds": 2, "driver_interval_seconds": 5, "driver_sampling": driver,
            "scope": "Media admission through settled Job; independent ACP handoff, drive proxies and resources; "
                     "normal finalization/publication included, sampler shutdown excluded",
            "repositions": "page34 rate proxy, not cumulative events or proof of physical commitment"})
        analyze_samples(raw, self.evidence / case / kind)
        return elapsed

    def copy_evidence(self, source, relative):
        # CLI files and Index bundles never pass through an in-memory stdout buffer.
        expected = self.test.remote(["sha256sum", "--", source]).stdout.decode().split()[0]
        path = self.evidence / relative_path(relative)
        path.parent.mkdir(parents=True, exist_ok=True, mode=0o700)
        argv = self.test.remote_arguments(["cat", "--", source], 3600)
        record = self.test.command_record(argv)
        record["streamed_stdout"] = str(path)
        self.test.save()
        started = time.monotonic()
        try:
            with path.open("wb") as output:
                result = subprocess.run(argv, stdout=output, stderr=subprocess.PIPE, timeout=3620, check=False)
        except Exception as error:
            result = error
        self.test.command_result(record, (result, time.monotonic() - started), 0, 3620)
        path.chmod(0o600)
        require(sha256(path) == expected,
                "Transferred evidence checksum mismatch; retain Job bundles.")
        record["streamed_sha256"] = expected
        self.test.save()
        return path

    def export_file(self, name):
        remote = self.test.root + "/tmp/" + name.replace("/", "-")
        self.test.cli("library", "export", "--output", remote, timeout=3600)
        return self.copy_evidence(remote, name)

    def manifest_file(self, case, kind, job, verification, source_directory=None):
        path = self.evidence / case / (kind + "-manifest.jsonl")
        with path.open("x") as output:
            for rows in manifest_pages(self.test, "archive", job):
                verification.archive_page(rows, source_directory, submitted=kind == "archive")
                for row in rows:
                    output.write(json.dumps(row) + "\n")
        verification.archive_complete(submitted=kind == "archive")

    def restore_case(self, case, restored, verification):
        self.timed_media(case, "restore", restored, ["restore", "run", "tape", restored["id"],
                         "--device", self.device(self.barcode)])
        target = self.test.root + "/fixtures/restored/" + case
        path = self.evidence / case / "restore-manifest.jsonl"
        with path.open("x") as output:
            for rows in manifest_pages(self.test, "restore", restored, "--media-id", self.state["media_id"]):
                checks = verification.restore_page(rows, target)
                for start in range(0, len(checks), VERIFY_BATCH):
                    batch = checks[start:start + VERIFY_BATCH]
                    actual = json.loads(self.helper("verify", body=json.dumps(batch).encode()).stdout)
                    require(actual == batch, "Actual restored bytes differ from deterministic source.")
                for row in rows:
                    output.write(json.dumps(row) + "\n")
        root = self.capture_job(restored)
        verification.restore_complete(root / "state.db", self.evidence / case / "restore-storage-order.jsonl")

    def performance_run(self):
        require(self.state.get("performance_format_complete"), "Performance cases require normal-placement setup FORMAT.")
        size_gib = self.state.get("performance_size_gib", 4)
        for case in self.state["performance_cases"]:
            if case in self.state.get("performance_completed", []):
                continue
            prefix = "performance_" + case + "_"
            require(prefix + "archive" not in self.state, "Case was already created; inspect preserved state before another Media attempt.")
            directory = self.test.root + "/fixtures/source/" + case
            logical_bytes = size_gib * 1024**3
            require(self.available(self.test.root) > 2 * logical_bytes + 8 * 1024**3,
                    "Owned fixture filesystem lacks twice the scene size plus eight GiB evidence headroom.")
            manifest_remote = self.test.root + "/tmp/" + case + "-source-manifest.jsonl"
            generated = json.loads(self.helper("generate-jsonl", directory, case, str(size_gib), manifest_remote).stdout)
            count, file_size, _ = dimensions(case, size_gib)
            require(generated["count"] == count and generated["logical_bytes"] == logical_bytes,
                    "Generated fixture differs from selected dimensions.")
            manifest = self.copy_evidence(manifest_remote, case + "/source-manifest.jsonl")
            require(sha256(manifest) == generated["sha256"], "Fixture manifest identity changed during transfer.")
            verification = Verification(self.evidence / case / "verification.db", self.state["media_id"])
            try:
                verification.fixtures(manifest, count, logical_bytes)
                verification.library(self.export_file(case + "/library-before.jsonl"), previous=True)
                job = self.created(prefix + "archive", "archive", "create", "--location", self.source + ":" + case)
                self.settled(job, "JOB_STATUS_READY", timeout=14400)
                self.manifest_file(case, "prepared", job, verification, directory)
                self.timed_media(case, "archive", job, ["archive", "write", "tape", "append", job["id"],
                                 "--device", self.device(self.barcode), "--barcode", self.barcode])
                self.manifest_file(case, "archive", job, verification, directory)
                verification.library(self.export_file(case + "/library.jsonl"))
                root = self.capture_job(job)
                verification.index(root / "tapes" / self.barcode / (self.barcode + ".schema"))
                verification.validate_positions(self.state["performance_partition_map"]["data"])
                restored = self.created(prefix + "restore", "restore", "create", "--location", self.source + ":" + case,
                                        "--target-location", self.restored, "--directory", case)
                self.settled(restored, "JOB_STATUS_READY", timeout=14400)
                self.restore_case(case, restored, verification)
            finally:
                verification.close()
            self.save(performance_completed=self.state.get("performance_completed", []) + [case])
            # Only successfully verified owned fixture directories are removed.
            self.test.remote(["rm", "-rf", "--", directory, self.test.root + "/fixtures/restored/" + case])
        self.save(performance_complete=True)


def run_stage(test, state, save_state):
    stage = test.args.physical_stage
    require(stage in ("performance-prepare", "performance-format", "performance-run"), "Unknown performance stage.")
    for key, value in (("physical_device", test.args.physical_device),
                       ("physical_barcode", test.args.physical_barcode), ("physical_root", test.root)):
        require(key not in state or state[key] == value, "Performance stage identity differs from saved state.")
        require(stage == "performance-prepare" or key in state, "Performance stage has no saved preparation identity.")
        state[key] = value
    save_state()
    getattr(PerformanceCases(test, state, save_state), test.args.physical_stage.replace("-", "_"))()
