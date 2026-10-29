"""One real Archive capacity attempt and explicit readback on preformatted scratch Tape.

Device loading, capacity changes and FORMAT belong to the external lifecycle owner.
These stages retain all sources, results and uncertain ownership; they never replay Media.
"""

import json
from pathlib import Path
import re
import time

from package_acceptance import require, sha256
from package_physical_evidence import check_positions, decode_bytes, parse_index, relative_path
from package_physical_performance import PerformanceCases
from physical_performance_metrics import PerformanceSampler


BOUNDARY_CHUNK_BYTES = 4 * 1024**3


# Copy this small entrypoint beside its dependencies, retaining their module names.
CAPACITY_HELPER = '''import json
from pathlib import Path
import sys
from physical_capacity_boundary import files_snapshot
from physical_capacity_probe import snapshot
from physical_performance_fixture import sample

command, work_directory = sys.argv[1:]
if command != "sample":
    raise SystemExit("Only passive sample is supported")
observation = sample(work_directory, pages=())
if observation.get("status") == "mounted":
    observation = snapshot(work_directory, Path(observation["mount"]).resolve(),
                           pages=("34", "0C", "31", "02", "17", "2E"))
    try:
        observation["target_listing"] = files_snapshot(Path(observation["mount"]))
    except OSError as error:
        observation["target_listing"] = {"error": str(error), "errno": error.errno}
print(json.dumps(observation))
'''


def check_preflight(path, state):
    require(path is not None, "Capacity write requires a saved FORMAT-mount sampler preflight.")
    record = json.loads(Path(path).read_text())
    reading = record.get("snapshot", record)
    directory = (state["install"] + "/work/jobs/" + str(state["format_job_id"])
                 + "/tapes/" + state["physical_barcode"])
    require(reading.get("status") == "mounted" and "work_directory=" + directory in reading.get("argv", [])
            and reading.get("timestamp_ns", 0) > 0, "Sampler preflight does not identify this installation's FORMAT mount.")
    for page, parameters in (("31", {"1", "2", "3", "4"}), ("0C", {"0", "1", "2", "3", "4", "5", "7"})):
        value = reading.get("pages", {}).get(page, {})
        require(value.get("raw") and parameters <= value.get("parameters", {}).keys()
                and not value.get("error") and not value.get("decode_error"),
                "Sampler preflight has no usable page " + page + "; resolve sampling before the capacity write.")
    # This saved FORMAT reading establishes the starting capacity. The live
    # before-copy hook requires all three capacity pages before payload writes.
    space = reading.get("statvfs", {})
    require(not space.get("error") and isinstance(space.get("available_bytes"), int)
            and space.get("fragment_bytes", 0) > 0, "Sampler preflight has no usable statvfs reading.")
    return record


def generate_sources(cases, fixture_helper, sizes, reflink, subdirectory="capacity"):
    """Write ordered incompressible capacity sources and return their immutable size/hash manifest."""
    directory = cases.test.root + "/fixtures/source/" + subdirectory
    cases.test.remote(["mkdir", "--", directory])
    manifest = {}
    if reflink:
        reply = cases.test.remote(["python3", fixture_helper, "capacity-reflink", directory, json.dumps(sizes)], timeout=43200)
        generated = json.loads(reply.stdout)
        names = [f"chunk-{number:03d}.bin" for number in range(len(sizes))]
        require(isinstance(generated, dict) and set(generated) == set(names), "Reflink fixture changed selected filenames.")
        for name, size in zip(names, sizes):
            entry = generated[name]
            require(isinstance(entry, dict) and entry.get("size") == size
                    and isinstance(entry.get("sha256"), str) and re.fullmatch(r"[0-9a-f]{64}", entry["sha256"]),
                    "Reflink fixture returned an invalid size/hash manifest.")
            manifest[subdirectory + "/" + name] = {"size": size, "sha256": entry["sha256"]}
        return manifest
    stream = 1 << 64
    for number, size in enumerate(sizes):
        name = f"chunk-{number:03d}.bin"
        # Offset beyond setup/other fixture streams, with disjoint AES counter ranges.
        manifest[subdirectory + "/" + name] = cases.stream_fixture(directory + "/" + name, size, stream)
        stream += size // 16
    return manifest


def idle(job):
    return (job.get("status") in ("JOB_STATUS_READY", "JOB_STATUS_FAILED", "JOB_STATUS_COMPLETED")
            and job.get("phase", "JOB_PHASE_UNSPECIFIED") in ("JOB_PHASE_UNSPECIFIED", "JOB_PHASE_COMPLETED"))


def selected_items(items, manifest, directory, media_id):
    """Validate exact order/content and retain all submitted rows, even a broken prefix."""
    names = [str(relative_path(item["file"]["source_path"].removeprefix(directory + "/"))) for item in items]
    require(names == sorted(manifest), "Capacity Archive changed selected file order or membership.")
    for name, item in zip(names, items):
        relative_path(item["file"]["target_path"])
        expected = manifest[name]
        require(item["file"]["source_path"] == directory + "/" + name
                and int(item.get("size_bytes", 0)) == expected["size"]
                and decode_bytes(item["file"]["expected"]["sha256"]).hex() == expected["sha256"],
                "Capacity Archive differs from immutable source size/hash/path.")
        if item["status"] == "COPY_STATUS_SUBMITTED":
            require(str(item.get("media_id")) == str(media_id) and item["file"].get("media_path"),
                    "Capacity Archive published to a different Media.")
        else:
            require(item["status"] == "COPY_STATUS_PENDING" and not item.get("media_id")
                    and not item["file"].get("media_path"),
                    "Capacity Archive retains staged or published suffix content.")
    return [item for item in items if item["status"] == "COPY_STATUS_SUBMITTED"]


class CapacityCases(PerformanceCases):
    def observation(self, result, name, action):
        """Attempt independent evidence reads even after another read or validation fails."""
        try:
            return action()
        except Exception as error:
            result["errors"][name] = str(error)
            return None

    def observe_terminal(self, job, timeout=28800):
        deadline = time.monotonic() + timeout
        path = self.evidence / "capacity" / "archive-jobs.jsonl"
        with path.open("x") as output:
            while time.monotonic() < deadline:
                try:
                    current = self.test.one("job", "get", str(job["id"]), timeout=30)["job"]
                    row = {"job": current}
                except Exception as error:
                    current, row = {}, {"observation_error": str(error)}
                output.write(json.dumps({"timestamp_ns": time.time_ns(), **row}) + "\n")
                output.flush()
                if idle(current):
                    return current
                time.sleep(2)
        raise RuntimeError("Capacity Job observation timed out; active owner and artifacts retained.")

    def install_helper(self):
        directory = self.test.root + "/tmp/capacity"
        helper = directory + "/sample.py"
        if "capacity_helper" in self.state:
            require(self.state["capacity_helper"] == helper, "Saved capacity helper is outside the owned installation.")
            return helper
        self.test.remote(["mkdir", "--", directory])
        for name in ("physical_performance_fixture.py", "physical_capacity_probe.py", "physical_capacity_boundary.py"):
            self.test.write(directory + "/" + name, Path(__file__).with_name(name).read_bytes())
        self.test.write(helper, CAPACITY_HELPER.encode())
        self.save(capacity_helper=helper)
        return helper

    def source_manifest(self):
        path = self.evidence / "capacity" / "source-manifest.json"
        require(sha256(path) == self.state["capacity_manifest_sha256"], "Immutable capacity manifest changed.")
        return json.loads(path.read_text())

    def capture_result(self, job, result):
        current = self.observation(result, "job", lambda: self.test.one("job", "get", str(job["id"]))["job"])
        result["job"] = current
        for name, action in (
                ("items", lambda: self.archive_items(job)),
                ("progress", lambda: self.test.one("job", "progress", str(job["id"]))),
                ("library", lambda: self.export("capacity/library.jsonl")),
                ("bundle", lambda: self.capture_job(job))):
            value = self.observation(result, name, action)
            if name == "bundle":
                result[name] = str(value) if value else None
            else:
                result[name] = value
        # Never issue even an mt status query while a Media operation may still own the device.
        if current and idle(current):
            self.observation(result, "device", self.ejected)
        else:
            result["errors"]["device"] = "Job is not observed idle; no device query attempted."
        result["device_released_ejected"] = "device" not in result["errors"]
        # A terminal catalog row alone does not establish successful finalization or release.
        result["owner"] = "released/ejected" if result["device_released_ejected"] else "uncertain; preserve service and device"
        directory = self.evidence / "jobs" / str(job["id"]) / "tapes" / self.barcode
        for name, path, reader in (
                ("finalIndex", directory / (self.barcode + ".schema"), lambda p: parse_index(p.read_bytes())),
                ("report", directory / "yatm-report.json", lambda p: json.loads(p.read_bytes()))):
            result[name] = self.observation(result, name, lambda p=path, read=reader: read(p))
        items = result.get("items")
        if items is not None:
            published = [item for item in items if item.get("status") == "COPY_STATUS_SUBMITTED"]
            prefix = []
            for item in items:
                if item.get("status") != "COPY_STATUS_SUBMITTED":
                    break
                prefix.append(item["file"]["target_path"])
            result.update(published_items=published, completed_prefix=prefix,
                          published_is_prefix=len(prefix) == len(published),
                          selected_order=[item["file"]["target_path"] for item in items])
            def validate():
                selected_items(items, self.source_manifest(), self.test.root + "/fixtures/source", self.state["media_id"])
                prepared = json.loads((self.evidence / "capacity/prepared-items.json").read_text())
                require([item["file"]["target_path"] for item in prepared] == result["selected_order"],
                        "Capacity Archive changed its prepared target order.")
                require(result["published_is_prefix"], "Published capacity files are not a continuous prefix.")
                require(result["library"] is not None and result["finalIndex"] is not None,
                        "Library or final Index unavailable; publication is unverified.")
                check_positions(published, result["library"], result["finalIndex"], self.state["media_id"])
                setup = self.archive_items(self.job("format_job_id"))
                check_positions(setup, result["library"], result["finalIndex"], self.state["media_id"])
                require(set(result["library"]) == {item["file"]["media_path"] for item in setup + published},
                        "Library contains unsubmitted capacity files or lost setup Positions.")
                report = result["report"]
                require(report is not None and str(report.get("media_id")) == self.state["media_id"]
                        and int(report.get("file_count", 0)) == len(published)
                        and int(report.get("bytes", 0)) == sum(int(item.get("size_bytes", 0)) for item in published),
                        "Archive report differs from published capacity content.")
            self.observation(result, "publication", validate)
        result["outcome"] = ("operation-failure" if result["errors"] or not current or current.get("error")
                             or current.get("status") == "JOB_STATUS_FAILED" else "completed-without-boundary")
        self.keep_json("capacity/write-result.json", result)
        self.save(capacity_write_observed=bool(current and idle(current) and items is not None),
                  capacity_device_released=result["device_released_ejected"],
                  capacity_published_items=result.get("published_items", []))
        self.test.report["capacity_result"] = result

    def capacity_write(self):
        require(not self.state.get("capacity_write_started"), "Capacity write already admitted; never replay it.")
        require(self.state.get("performance_format_complete"), "Capacity write requires performance-format.")
        count = self.test.args.capacity_files
        require(1 <= count <= 100, "Capacity fixture is bounded to 100 files.")
        requested = getattr(self.test.args, "capacity_file_mib", None)
        sizes = [size * 1024**2 for size in requested] if requested else [BOUNDARY_CHUNK_BYTES] * count
        reading = check_preflight(self.test.args.capacity_preflight, self.state)
        self.keep_json("capacity/preflight.json", reading)
        # Persist before source generation, Job creation, or any Media admission.
        self.save(capacity_write_started=True, capacity_files=count, capacity_file_sizes=sizes)
        result = {"errors": {}, "started_ns": time.time_ns(), "file_count": count,
                  "file_sizes": sizes, "logical_bytes": sum(sizes)}
        job = None
        try:
            reflink = self.test.args.capacity_source_reflink
            required = sum(sizes) + (max(sizes) if reflink else sum(sizes)) + 8 * 1024**3
            require(self.available(self.test.root) > required,
                    "Capacity source/Restore filesystem lacks the required fixture space: " + str(required) + " bytes.")
            self.check_media()
            inspected = self.inspect()
            require(str(inspected["media"]["id"]) == self.state["media_id"], "Capacity inspected a different Media.")
            helper = self.install_helper()
            manifest = generate_sources(self, str(Path(helper).with_name("physical_performance_fixture.py")), sizes, reflink)
            path = self.evidence / "capacity" / "source-manifest.json"
            path.parent.mkdir(parents=True, exist_ok=True)
            with path.open("x") as output:
                output.write(json.dumps(manifest, indent=2) + "\n")
            path.chmod(0o400)
            self.save(capacity_manifest_sha256=sha256(path))
            job = self.created("capacity_archive_job_id", "archive", "create", "--location", self.source + ":capacity")
            self.settled(job, "JOB_STATUS_READY", timeout=14400)
            prepared = self.archive_items(job)
            self.keep_json("capacity/prepared-items.json", prepared)
            selected_items(prepared, manifest, self.test.root + "/fixtures/source", self.state["media_id"])
            require(all(item["status"] == "COPY_STATUS_PENDING" for item in prepared), "Capacity Job is not pending.")
            self.export("capacity/library-before.jsonl")
            tape_dir = self.test.install + "/work/jobs/" + str(job["id"]) + "/tapes/" + self.barcode
            with PerformanceSampler(self.test, job, helper, tape_dir, self.evidence / "capacity/archive-samples.jsonl",
                                    driver=True, resource_helper_path=self.state["performance_metrics_helper"],
                                    app_period=1, driver_period=1) as sampler:
                self.save(capacity_media_admitted=True)
                sampler.mark("media-admission-start")
                # Record even nonzero CLI results; observation never repeats the Media operation.
                reply = self.observation(result, "admission", lambda: self.test.remote([self.test.install + "/yatm-cli", "--server", self.test.url,
                                          "--timeout", "280s", "archive", "write", "tape", "append", str(job["id"]),
                                          "--device", self.device(self.barcode), "--barcode", self.barcode],
                                         expected=None, timeout=300))
                result["admission"] = ({"exit_code": reply.returncode, "stdout": reply.stdout.decode(errors="replace"),
                                        "stderr": reply.stderr.decode(errors="replace")} if reply else {})
                sampler.mark("media-admission-returned", result["admission"])
                result["terminal"] = self.observe_terminal(job)
                sampler.mark("job-settled", result["terminal"])
        except Exception as error:
            result["errors"]["execution"] = str(error)
        finally:
            result["finished_ns"] = time.time_ns()
            if job:
                self.capture_result(job, result)
            else:
                self.keep_json("capacity/write-result.json", result)
                self.test.report["capacity_result"] = result
        # READY/error is evidence, never an assertion that a pre-write refusal was safe.
        terminal = result.get("job") or {}
        failed = (result["errors"] or terminal.get("error") or terminal.get("status") == "JOB_STATUS_FAILED"
                  or result.get("admission", {}).get("exit_code", 1) != 0)
        require(not failed, "Capacity attempt recorded a failure; inspect write-result.json. No Media retry or cleanup was performed.")

    def restore_sampler(self, job):
        tape_dir = self.test.install + "/work/jobs/" + str(job["id"]) + "/tapes/" + self.barcode
        return PerformanceSampler(self.test, job, self.state["capacity_helper"], tape_dir,
                                  self.evidence / "capacity/restore-samples.jsonl", driver=True,
                                  resource_helper_path=self.state["performance_metrics_helper"],
                                  app_period=1, driver_period=1)

    def capacity_verify(self):
        require(self.state.get("capacity_write_observed"), "Observe the capacity Archive before Restore.")
        require(self.state.get("capacity_device_released"), "Capacity device ownership is uncertain; lifecycle owner must resolve it.")
        require(not self.state.get("capacity_verify_started"), "Capacity verification already admitted; never replay Restore.")
        manifest = self.source_manifest()
        items = self.state["capacity_published_items"]
        require(len(items) <= 100, "Capacity Restore exceeds its bounded selection.")
        # Validate selected published content against the immutable manifest again before admission.
        for item in items:
            source_name = item["file"]["source_path"].removeprefix(self.test.root + "/fixtures/source/")
            expected = manifest[source_name]
            require(int(item.get("size_bytes", 0)) == expected["size"]
                    and decode_bytes(item["file"]["expected"]["sha256"]).hex() == expected["sha256"],
                    "Published Restore selection differs from source manifest.")
        self.save(capacity_verify_started=True)
        result = {"errors": {}, "selected_files": len(items), "published_items": items}
        try:
            require(items, "No published files; no Restore created. This is not a successful boundary readback.")
            self.restore_selection("capacity_restore_job_id", items, "capacity", "capacity_restore_complete",
                                   observe=self.restore_sampler, timeout=28800)
        except Exception as error:
            result["errors"]["restore"] = str(error)
        finally:
            if "capacity_restore_job_id" in self.state:
                job = self.job("capacity_restore_job_id")
                self.observation(result, "bundle", lambda: self.capture_job(job))
                for name, args in (("job", ("job", "get", job["id"])),
                                   ("progress", ("job", "progress", job["id"])),
                                   ("items", ("restore", "files", job["id"], "--media-id", self.state["media_id"], "--limit", "100"))):
                    result[name] = self.observation(result, name, lambda args=args: self.test.one(*args))
                current = (result.get("job") or {}).get("job", {})
                if idle(current):
                    self.observation(result, "device", self.ejected)
                else:
                    result["errors"]["device"] = "Restore is not observed idle; no device query attempted."
            result["owner"] = "uncertain; preserve service and device" if result["errors"] else "released/ejected"
            self.keep_json("capacity/verify-result.json", result)
            self.test.report["capacity_verification"] = result
        require(not result["errors"], "Capacity Restore failed; preserved verify-result.json and all owned resources.")
        self.save(capacity_verify_complete=True)


def run_stage(test, state, save_state):
    require(test.args.physical_stage in ("capacity-write", "capacity-verify"), "Unknown capacity stage.")
    for key, value in (("physical_device", test.args.physical_device),
                       ("physical_barcode", test.args.physical_barcode), ("physical_root", test.root)):
        require(state.get(key) == value, "Capacity stage identity differs from saved preparation.")
    getattr(CapacityCases(test, state, save_state), test.args.physical_stage.replace("-", "_"))()
