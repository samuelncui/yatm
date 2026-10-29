"""Local physical Tape cases using only the installed CLI and ordinary host tools.

The lifecycle controller owns service restart, device reservation, script configuration
and final host cleanup. Partition roles come from the captured mkltfs log.
"""

import base64
import hashlib
import json
import math
from pathlib import Path
import re

from package_acceptance import require, sha256
from package_ltfs_cases import TapeWorkflows
from package_physical_evidence import (check_positions, check_restore_database, check_signature,
                                       decode_bytes, format_partition_map, library_positions, parse_index, relative_path,
                                       seal_evidence, verify_evidence)


PNG = base64.b64decode("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mNk+A8AAQUBAScY42YAAAAASUVORK5CYII=")


def baseline_files():
    # Small ordinary files; the native helper supports the same PNG as package_media_cases.
    def content(seed, size):
        return hashlib.shake_256(seed.encode()).digest(size)
    return {"dataset/index-small.txt": content("index", 60 * 1024),
            "dataset/data-small.bin": content("small", 64 * 1024),
            "dataset/data-large.txt": content("large", 2254438),
            "dataset/empty.bin": b"", "dataset/nested/payload.png": PNG,
            "append/index-small.txt": content("append-index", 65 * 1024),
            "append/data.bin": content("append-data", 2453668)}


def check_cleanup_state(state):
    if state.get("performance"):
        required = ("performance-prepare", "performance-format", "capacity-verify")
        complete = (state.get("capacity_write_observed") and state.get("capacity_device_released")
                    and state.get("capacity_restore_complete") and state.get("capacity_verify_complete"))
    else:
        required = ("prepare", "baseline", "restore")
        # An admitted optional tar stage must finish its readback before cleanup.
        complete = (state.get("restore_complete") and
                    (state.get("physical_format", "ltfs_v1") != "tar_pax_v1" or state.get("tar_recovery_complete"))
                    and all(not state.get("tar_" + stage + "_started") or state.get("tar_" + stage + "_complete")
                            for stage in ("fill", "ew", "read_order")))
    require(complete and all(stage in state.get("completed_stages", []) for stage in required),
            "Cleanup requires completed baseline or capacity readback; retain incomplete evidence and owned resources.")


TAPE_KEY = re.compile(rb"v1:[0-9a-fA-F]{64}")


def redact_tape_keys(data):
    # Same-length replacement keeps SQLite pages and protobuf lengths valid for later evidence checks.
    return TAPE_KEY.sub(b"v1:" + b"x" * 64, data)

class PhysicalEvidence:
    """Owned Job receipts and sealed evidence shared by the two physical formats."""

    def __init__(self, test, state, save_state):
        self.test = test
        self.source = str(state.get("source_location", ""))
        self.restored = str(state.get("restored_location", ""))
        self.state, self.save_state = state, save_state
        self.barcode = test.args.physical_barcode
        self.evidence = Path(state.get("physical_evidence_dir", Path(test.args.out) / "physical-evidence"))

    def save(self, **values):
        self.state.update(values)
        self.save_state()

    def keep(self, name, data):
        path = self.evidence / relative_path(name)
        path.parent.mkdir(parents=True, exist_ok=True, mode=0o700)
        path.write_bytes(data)
        path.chmod(0o600)
        return path

    def keep_json(self, name, value):
        return self.keep(name, (json.dumps(value, indent=2) + "\n").encode())

    def job(self, key):
        value = str(self.state[key])
        require(re.fullmatch(r"[1-9][0-9]*", value), "Invalid saved Job ID.")
        return {"id": value}

    def created(self, key, *args):
        require(key not in self.state, "Job already exists; do not submit it again.")
        job = self.test.one(*args)["job"]
        self.save(**{key: str(job["id"])})
        return job

    def settled(self, job, status="JOB_STATUS_COMPLETED", timeout=7200):
        result = self.test.wait_job(job, status, timeout=timeout)
        require(result.get("phase", "JOB_PHASE_UNSPECIFIED") in ("JOB_PHASE_UNSPECIFIED", "JOB_PHASE_COMPLETED"),
                "Job has not released its Media attempt.")
        require(not result.get("error"), "Physical operation settled with an error.")
        return result

    def saved_media(self, media_id, identity):
        """One Media record from a private Library export; API replies omit its saved Tape key."""
        records = self.test.private_cli("library", "export", "--output", "-", timeout=600)
        require(records and records[0].get("type") == "header" and records[-1].get("type") == "end",
                "Library export is incomplete.")
        rows = [record["data"] for record in records
                if record.get("type") == "media" and str(record.get("data", {}).get("id")) == str(media_id)]
        require(len(rows) == 1 and rows[0].get("identity") == identity, "Library export lacks the saved Media.")
        require(rows[0].get("profile", {}).get("tape", {}).get("encryption"), "Tape profile did not retain encryption.")
        return rows[0]

    def export(self, name):
        records = self.test.cli("library", "export", "--output", "-")
        data = b"".join(json.dumps(record).encode() + b"\n" for record in records)
        self.keep(name, data)
        return library_positions(data, self.state["media_id"])

    def capture_job(self, job):
        identifier = str(job["id"])
        require(re.fullmatch(r"[1-9][0-9]*", identifier), "Invalid Job evidence ID.")
        root = self.test.install + "/work/jobs/" + identifier
        catalog = self.test.one("job", "get", identifier)["job"]
        require(catalog.get("phase", "JOB_PHASE_UNSPECIFIED") in ("JOB_PHASE_UNSPECIFIED", "JOB_PHASE_COMPLETED")
                and catalog["status"] in ("JOB_STATUS_READY", "JOB_STATUS_COMPLETED", "JOB_STATUS_FAILED"),
                "Cannot capture or delete an active Job.")
        self.keep_json("jobs/" + identifier + "/catalog.json", catalog)
        # SQLite's backup API gives a consistent snapshot even if the settled runner retains a handle.
        snapshot = self.test.root + "/tmp/physical-job-" + identifier + ".db"
        self.test.remote(["sqlite3", "-readonly", root + "/state.db", ".backup '" + snapshot.replace("'", "''") + "'"])
        self.copy_evidence(snapshot, "jobs/" + identifier + "/state.db")
        self.copy_evidence(root + "/job.json", "jobs/" + identifier + "/job.json")
        # Preserve exact bytes; CLI JSON text can replace UTF-8 split across byte pages.
        self.copy_evidence(root + "/job.log", "jobs/" + identifier + "/job.log")
        self.tape_evidence(identifier)
        return self.evidence / "jobs" / identifier

    def copy_evidence(self, source, relative):
        expected = self.test.remote(["sha256sum", "--", source]).stdout.decode().split()[0]
        data = self.test.remote(["cat", "--", source]).stdout
        require(hashlib.sha256(data).hexdigest() == expected, "Transferred evidence checksum mismatch; retain Jobs.")
        # Job profiles and state retain Media keys; evidence keeps same-length placeholders so files stay readable.
        return self.keep(relative, redact_tape_keys(data))

    def check_fixture(self, items, group):
        expected = {name: value for name, value in self.state["fixture_manifest"].items() if name.startswith(group + "/")}
        found = set()
        for item in items:
            path = item["file"]["target_path"]
            name = next((name for name in expected if path == name or path.endswith("/" + name)), None)
            require(name is not None and name not in found, "Archive changed its physical fixture selection.")
            found.add(name)
            require(int(item.get("size_bytes", 0)) == expected[name]["size"]
                    and decode_bytes(item["file"]["expected"]["sha256"]).hex() == expected[name]["sha256"],
                    "Archive manifest differs from generated fixture bytes.")
        require(found == set(expected), "Archive omitted fixture files.")

    def check_prepared(self, job, group):
        items = self.archive_items(job)
        self.check_fixture(items, group)
        root = self.test.root + "/fixtures/source/"
        expected_paths = {root + name for name in self.state["fixture_manifest"] if name.startswith(group + "/")}
        require({item["file"]["source_path"] for item in items} == expected_paths,
                "Prepared Archive source paths differ from the owned fixture.")
        ids = [int(item["file"]["expected"].get("file_id", 0)) for item in items]
        require(len(set(ids)) == len(items) and all(value > 0 for value in ids),
                "Prepared Archive has missing or duplicate File IDs.")
        require(all(item["status"] == "COPY_STATUS_PENDING" and not item.get("media_id")
                    and not item["file"].get("media_path")
                    and str(item["file"]["expected"].get("original_location_id")) == self.source for item in items),
                "Prepared Archive is not pending on the selected source Location.")

    def cleanup_evidence(self):
        # Fixed existing artifacts, not a second acceptance record or reconstructed timings.
        manifests = {}
        for name in ("packages.sha256", "controller.sha256"):
            path = self.evidence / "source" / name
            require(path.is_file() and not path.is_symlink(), "Preserve source/" + name + " before cleanup.")
            rows = [line.split("  ", 1) for line in path.read_text().splitlines()]
            require(rows and all(len(row) == 2 and re.fullmatch(r"[0-9a-f]{64}", row[0]) and row[1] for row in rows),
                    "Source checksum manifest is incomplete: " + name)
            manifests[name] = {Path(filename).name: digest for digest, filename in rows}
        require("physical_package_acceptance.py" in manifests["controller.sha256"],
                "Source manifest lacks the executed physical controller identity.")
        stages = (("performance-prepare", "performance-format", "capacity-write", "capacity-verify")
                  if self.state.get("performance") else ("prepare", "baseline", "restore") + tuple(
                      stage for stage in ("tar-clear-barcode", "tar-fill", "tar-ew", "tar-read-order")
                      if stage in self.state.get("completed_stages", [])))
        for stage in stages:
            path = self.evidence / "stages" / stage / "report.json"
            require(path.is_file() and not path.is_symlink(), "Preserve the original stage report before cleanup: " + stage)
            report = json.loads(path.read_text())
            require(all(report.get(key) == self.state[key] for key in ("commit", "version", "remote_root")),
                    "Stage report belongs to a different source or installation: " + stage)
            duration = report.get("duration_seconds")
            require(type(duration) in (int, float) and math.isfinite(duration) and duration >= 0
                    and report.get("status") in (("passed", "failed") if stage == "capacity-write" else ("passed",))
                    and any(case.get("name") == "physical-" + stage and case.get("status") == report["status"]
                            for case in report.get("cases", [])), "Stage timing/result is incomplete: " + stage)
            if stage == stages[0]:
                archive = report.get("archive", {})
                require(archive.get("sha256") and manifests["packages.sha256"].get(Path(archive.get("path", "")).name)
                        == archive["sha256"], "Package source manifest differs from the preparation report.")
        if self.test.args.require_sg_trace:
            require(self.state.get("performance"), "Required SG evidence applies to capacity Archive and Restore.")
            require("physical_sg_trace.c" in manifests["controller.sha256"]
                    and "physical_sg_trace.so" in manifests["controller.sha256"],
                    "Source manifest lacks the executed SG observer source/binary identities.")
            for key in ("capacity_archive_job_id", "capacity_restore_job_id"):
                path = self.evidence / "jobs" / self.job(key)["id"] / "tapes" / self.barcode / "sg-trace.jsonl"
                require(path.is_file() and not path.is_symlink() and path.stat().st_size > 0,
                        "Required capacity SG trace is missing or empty: " + key)

    def syscall_evidence(self):
        paths = self.test.args.syscall_trace or []
        require(paths or self.test.args.no_syscall_trace, "Declare syscall evidence before cleanup.")
        names = [Path(path).name for path in paths]
        require(len(set(names)) == len(names), "Supplied syscall traces have duplicate filenames.")
        for source, name in zip(paths, names):
            relative_path(name)
            self.test.remote(["test", "-f", source])
            self.test.remote(["test", "!", "-L", source])
            users = self.test.remote(["fuser", "--", source], expected=None)
            require(users.returncode == 1 and not users.stdout.strip() and not users.stderr.strip(),
                    "Syscall evidence is active or ownership is uncertain; stop the observer before cleanup.")
            destination = "observers/syscall/" + name
            if self.state.get("evidence_complete"):
                preserved = self.evidence / destination
                require(preserved.is_file() and sha256(preserved) ==
                        self.test.remote(["sha256sum", "--", source]).stdout.decode().split()[0],
                        "Syscall evidence differs from its sealed copy; retain owned resources.")
            else:
                self.copy_evidence(source, destination)

    def cleanup(self):
        check_cleanup_state(self.state)
        jobs, offset = [], 0
        while True:
            page = self.test.one("job", "list", "--limit", "100", "--offset", str(offset))
            batch = page.get("jobs", [])
            jobs.extend(batch)
            if not page.get("has_more"):
                break
            require(batch, "Job listing did not advance.")
            offset += len(batch)
        require(all(job.get("status") in ("JOB_STATUS_READY", "JOB_STATUS_COMPLETED", "JOB_STATUS_FAILED")
                    and job.get("phase", "JOB_PHASE_UNSPECIFIED") in ("JOB_PHASE_UNSPECIFIED", "JOB_PHASE_COMPLETED")
                    for job in jobs), "Cannot clean active or uncertain Jobs.")
        self.check_device_released()
        self.test.check_cleanup_paths()
        # Capture the entire isolated catalog, including companion Jobs, before any deletion.
        if not self.state.get("evidence_complete"):
            require(jobs, "No physical Jobs remain to preserve.")
            self.export("library-final.jsonl")
            for job in jobs:
                self.capture_job(job)
        self.cleanup_evidence()
        self.syscall_evidence()
        if not self.state.get("evidence_complete"):
            digest = seal_evidence(self.evidence)
            self.save(evidence_complete=True, evidence_sha256=digest, cleanup_job_ids=[str(j["id"]) for j in jobs])
        verify_evidence(self.evidence)
        require(sha256(self.evidence / "SHA256SUMS") == self.state["evidence_sha256"],
                "Saved evidence manifest changed; retain all Jobs.")
        remaining = set(self.state["cleanup_job_ids"]) - set(self.state.get("deleted_job_ids", []))
        require({str(job["id"]) for job in jobs} == remaining, "Job catalog changed after sealing; retain owned resources.")
        for job in jobs:
            saved = json.loads((self.evidence / "jobs" / str(job["id"]) / "catalog.json").read_text())
            require(self.test.one("job", "get", str(job["id"]))["job"] == saved,
                    "Job changed after evidence capture; retain owned resources.")
            self.tape_evidence(str(job["id"]), verify=True)
        for identifier in self.state["cleanup_job_ids"]:
            if identifier in self.state.get("deleted_job_ids", []):
                continue
            self.test.cli("job", "delete", identifier)
            self.test.remote(["test", "!", "-e", self.test.install + "/work/jobs/" + identifier])
            self.save(deleted_job_ids=self.state.get("deleted_job_ids", []) + [identifier])
        self.save(cleanup_complete=True)



class PhysicalCases(PhysicalEvidence, TapeWorkflows):
    def check_device_released(self):
        self.ejected()
    def device(self, barcode):
        require(barcode == self.barcode, "Tape operation requested a different barcode.")
        return self.test.args.physical_device

    def inspect(self, allow_unassigned=False):
        reply = self.test.one("media", "inspect", "tape", "--device", self.device(self.barcode))
        if allow_unassigned and reply.get("identity", "") == "":
            # Resolve registration for the explicitly assigned FORMAT identity, not a guessed label.
            reply = self.test.one("media", "inspect", "tape", "--device", self.device(self.barcode),
                                  "--identity", self.barcode)
        require(reply["identity"] == self.barcode, "Device inspection returned a different barcode.")
        return reply

    def ejected(self):
        devices = self.test.one("tape", "device", "list").get("devices", [])
        device = self.device(self.barcode)
        summary = next((summary for summary in devices if summary.get("device") == device), None)
        require(summary is not None, "Device owner remains uncertain; no physical status query attempted.")
        require(not summary.get("busy", False), "Device remains busy; no physical status query attempted.")
        status = self.test.remote(["mt", "-f", device, "status"]).stdout.decode()
        require(re.search(r"\bDR_OPEN\b", status), "Physical operation completed before cartridge eject.")

    def archive_items(self, job):
        # Shared assertions handle the deliberately small baseline; no EOM-size assumption here.
        return self.items(job)

    def tape_evidence(self, identifier, verify=False):
        root = self.test.install + "/work/jobs/" + identifier
        listing = self.test.remote(["find", root, "(", "-type", "f", "-o", "-type", "l", ")", "-print0"]).stdout
        for raw in listing.split(b"\x00"):
            if not raw:
                continue
            path = raw.decode()
            require(path.startswith(root + "/"), "Job artifact escaped its bundle.")
            relative = str(relative_path(path[len(root) + 1:]))
            if relative.startswith("tapes/") and (relative.endswith(".schema") or relative.rsplit("/", 1)[-1] in
                                                 ("ltfs.log", "yatm-report.json", "capacity-before-copy.json",
                                                  "capacity-before-unmount.json", "sg-trace.jsonl", "sg-trace.jsonl.error")):
                self.test.remote(["test", "!", "-L", path])
                destination = "jobs/" + identifier + "/" + relative
                if verify:
                    preserved = self.evidence / destination
                    require(preserved.is_file() and sha256(preserved) ==
                            self.test.remote(["sha256sum", "--", path]).stdout.decode().split()[0],
                            "Live Tape evidence is missing or differs from its sealed copy; retain all Jobs.")
                else:
                    self.copy_evidence(path, destination)

    def index(self, job):
        root = self.capture_job(job)
        directory = root / "tapes" / self.barcode
        require((directory / "ltfs.log").is_file() and (directory / "yatm-report.json").is_file(),
                "Archive Tape artifacts are incomplete.")
        return parse_index((directory / (self.barcode + ".schema")).read_bytes())

    def configure_preview(self):
        # settings preview prints PreviewSettings directly, matching package_media_cases.preview_assets.
        settings = self.test.one("settings", "preview")
        require(isinstance(settings.get("generators"), list), "CLI did not return direct PreviewSettings.")
        settings.update(enabled=True, command=self.test.install + "/yatm-preview")
        path = self.test.root + "/tmp/physical-preview.json"
        self.test.write(path, json.dumps(settings).encode())
        updated = self.test.one("settings", "preview", "--preview-json", path)
        require(updated.get("enabled") and updated.get("command") == settings["command"],
                "Native Preview settings did not retain the installed command.")
        capabilities = self.test.one("preview", "capabilities")
        require(capabilities.get("available") and "image" in capabilities.get("kinds", []),
                "Installed native Preview does not support the PNG fixture.")

    def prepare(self):
        jobs = self.test.one("job", "list", "--limit", "1")
        require(not jobs.get("jobs") and not jobs.get("has_more"), "Prepare requires an empty isolated Job catalog.")
        self.save(physical_evidence_dir=str(self.evidence.resolve()))
        for name in ("source", "restored"):
            key = name + "_location"
            root = self.test.root + "/fixtures/" + name
            self.test.remote(["mkdir", "-p", "--", root])
            if key not in self.state:
                args = ["location", "create", "--name", "Physical " + name, "--root", root]
                if name == "restored":
                    args += ["--restore-target"]
                location = self.test.one(*args)["location"]
                self.save(**{key: str(location["id"])})
            registered = self.test.one("location", "get", str(self.state[key]))
            require(str(registered["location"]["id"]) == str(self.state[key])
                    and registered["location"]["root_path"] == root
                    and registered.get("accessibility", {}).get("accessible"),
                    "Physical Location does not resolve to its owned fixture directory.")
        self.source, self.restored = str(self.state["source_location"]), str(self.state["restored_location"])
        files = baseline_files()
        self.test.remote(["mkdir", "--", self.test.root + "/fixtures/source/dataset",
                          self.test.root + "/fixtures/source/dataset/nested", self.test.root + "/fixtures/source/append"])
        manifest = {}
        for name, data in files.items():
            self.test.write(self.test.root + "/fixtures/source/" + name, data)
            manifest[name] = {"size": len(data), "sha256": hashlib.sha256(data).hexdigest()}
        self.save(fixture_manifest=manifest)
        self.keep_json("source-manifest.json", manifest)
        self.configure_preview()
        job = self.created("format_job_id", "archive", "create", "--location", self.source + ":dataset",
                           "--preview-policy", "missing-only")
        self.settled(job, "JOB_STATUS_READY")
        progress = self.test.one("job", "progress", job["id"])
        require(not progress.get("preview_error") and int(progress.get("preview_job_id", 0)) > 0,
                "Archive did not create its Preview Scan.")
        self.save(preview_job_id=str(progress["preview_job_id"]))
        self.settled(self.job("preview_job_id"))
        self.check_prepared(job, "dataset")
        self.capture_job(self.job("preview_job_id"))
        self.save(prepare_complete=True)

    def baseline(self):
        require(not self.state.get("append_write_started") and not self.state.get("baseline_complete"),
                "Baseline APPEND already started; retain its state without replaying writes.")
        require(self.state.get("prepare_complete"), "Baseline requires completed nonphysical preparation.")
        job = self.job("format_job_id")
        if "baseline_started" not in self.state:
            require("media_id" not in self.state, "Baseline has Media without its FORMAT attempt state.")
            require(not self.inspect(allow_unassigned=True).get("media"), "Loaded Tape already exists in the isolated Library.")
            self.save(baseline_started=True)
            self.format(job, self.barcode)
        # A failed evidence check may reuse a completed FORMAT, never repeat it.
        self.settled(job)
        self.ejected()
        media = self.media(self.barcode)
        encryption = self.saved_media(media["id"], self.barcode)["profile"]["tape"]["encryption"]
        encryption_sha256 = hashlib.sha256(encryption.encode()).hexdigest()
        if "media_id" in self.state:
            require(str(media["id"]) == self.state["media_id"]
                    and encryption_sha256 == self.state["encryption_sha256"],
                    "FORMAT evidence belongs to a different Media or encryption profile.")
        else:
            self.save(media_id=str(media["id"]), encryption_sha256=encryption_sha256)
        initial = self.archive_items(job)
        self.check_fixture(initial, "dataset")
        positions = self.export("library-format.jsonl")
        require(len(positions) == len(initial), "FORMAT published an unexpected Position set.")
        index = self.index(job)
        format_log = self.evidence / "jobs" / str(job["id"]) / "tapes" / self.barcode / "ltfs.log"
        mapping = format_partition_map(format_log.read_text())
        self.save(partition_map=mapping)
        check_positions(initial, positions, index, self.state["media_id"], mapping)
        png = next(item for item in initial if item["file"]["target_path"].endswith("/nested/payload.png"))
        signature = decode_bytes(png["file"]["expected"]["signature"]).hex()
        assets = self.test.one("preview", "get", "--signature", signature)
        require(assets.get("availability") == "PREVIEW_AVAILABILITY_READY" and assets.get("assets"), "PNG Preview is missing.")
        for asset in assets["assets"]:
            require(asset["url"].startswith("/files/preview?"), "Unexpected Preview URL.")
            data = self.test.remote(["curl", "--fail", "--silent", self.test.url + asset["url"]]).stdout
            require(data, "Preview HTTP asset is empty.")
            self.keep("preview-" + str(relative_path(asset["role"])), data)
        inspected = self.inspect()
        require(str(inspected["media"]["id"]) == self.state["media_id"]
                and int(inspected.get("file_count", 0)) == len(initial), "Reload changed durable Tape identity/count.")
        # A prepared APPEND Job is reused until its Media write is admitted.
        append = (self.job("append_job_id") if "append_job_id" in self.state else
                  self.created("append_job_id", "archive", "create", "--location", self.source + ":append"))
        self.settled(append, "JOB_STATUS_READY")
        self.check_prepared(append, "append")
        self.barcode_refusal(append)
        self.save(append_write_started=True)
        self.test.cli("archive", "write", "tape", "append", append["id"], "--device", self.device(self.barcode),
                      "--barcode", self.barcode)
        self.settled(append)
        self.ejected()
        added = self.archive_items(append)
        self.check_fixture(added, "append")
        current = self.export("library-baseline.jsonl")
        require(len(current) == len(initial) + len(added) and all(current.get(k) == v for k, v in positions.items()),
                "APPEND changed existing Positions or published extra files.")
        require(all(i["file"]["media_path"] != i["file"]["target_path"] for i in added), "APPEND omitted its path prefix.")
        final_index = self.index(append)
        check_positions(initial, current, final_index, self.state["media_id"], mapping)
        check_positions(added, current, final_index, self.state["media_id"], mapping)
        self.check_media()
        self.test.report.setdefault("limits", []).append(
            "PT-04 runner barcode bypass remains local/CI coverage; baseline does not claim that physical fault probe.")
        self.test.save()
        self.save(baseline_items=initial + added, baseline_complete=True)

    def barcode_refusal(self, job):
        # This is explicitly a CLI preflight test, never evidence of the runner's independent check.
        wrong = "BAD999" if self.barcode != "BAD999" else "BAD998"
        before = self.test.one("job", "get", job["id"])
        require(before["job"]["status"] == "JOB_STATUS_READY"
                and before["job"].get("phase", "JOB_PHASE_UNSPECIFIED") == "JOB_PHASE_UNSPECIFIED"
                and not before["job"].get("error"), "Barcode preflight requires an idle prepared Job.")
        prepared = self.archive_items(job)
        media = self.media(self.barcode)
        cli = [self.test.install + "/yatm-cli", "--server", self.test.url, "--timeout", "160s",
               "archive", "write", "tape"]
        device = ["--device", self.device(self.barcode), "--barcode", wrong]
        # FORMAT compares the request with the loaded cartridge's identity.
        result = self.test.remote(cli + ["format", job["id"], *device, "--name", "Mismatch " + wrong,
                                         "--confirm-format", wrong], expected=2)
        error = json.loads(result.stderr)
        message = error.get("error", "")
        require(error.get("code") == "safety" and "does not match the inspected identity" in message
                and f'requested="{wrong}" inspected="{self.barcode}"' in message,
                "Wrong-barcode CLI test did not observe its explicit identity refusal.")
        # APPEND resolves the requested barcode's registered Media before inspecting the device.
        result = self.test.remote(cli + ["append", job["id"], *device], expected=1)
        error = json.loads(result.stderr)
        require(error.get("code") == "not_found" and f'barcode="{wrong}"' in error.get("error", ""),
                "Wrong-barcode APPEND did not observe its unregistered Media refusal.")
        require(self.test.one("job", "get", job["id"]) == before, "CLI preflight refusal admitted a Media attempt.")
        require(self.archive_items(job) == prepared and self.media(self.barcode) == media,
                "CLI preflight refusal changed the prepared manifest or durable Media.")
        self.test.remote(["test", "!", "-e", self.test.install + "/work/jobs/" + job["id"] + "/tapes/" + wrong])
        inspected = self.inspect()
        require(inspected["identity"] == self.barcode and str(inspected["media"]["id"]) == str(media["id"]),
                "CLI refusal changed the loaded identity or Media.")

    def check_media(self):
        media = self.media(self.barcode)
        require(str(media["id"]) == self.state["media_id"] and hashlib.sha256(
            self.saved_media(media["id"], self.barcode)["profile"]["tape"]["encryption"].encode()).hexdigest()
            == self.state["encryption_sha256"],
            "APPEND/restart changed the Tape identity or encryption profile.")

    def restore(self):
        require(self.state.get("baseline_complete"), "Restore requires a completed baseline and lifecycle restart.")
        self.restore_selection("restore_job_id", self.state["baseline_items"], "baseline", "restore_complete")

    def restore_selection(self, key, items, directory, completion, *, observe=None, timeout=14400):
        from contextlib import nullcontext

        if self.state.get(completion):
            return
        self.check_media()
        submit = key not in self.state
        if submit:
            inspected = self.inspect()
            require(str(inspected["media"]["id"]) == self.state["media_id"], "Restore inspected a different Tape.")
            args = ["restore", "create", "--target-location", self.restored, "--directory", directory]
            for item in items:
                args += ["--file-id", str(item["file"]["expected"]["file_id"])]
            job = self.created(key, *args)
            self.settled(job, "JOB_STATUS_READY")
        else:
            job = self.job(key)
            # Do not turn an interrupted or failed invocation into an implicit second Media attempt.
            require(self.state.get(key + "_submitted"), "Saved Restore was not submitted; explicit operator review is required.")
        with observe(job) if observe else nullcontext():
            if submit:
                self.save(**{key + "_submitted": True})
                self.test.cli("restore", "run", "tape", job["id"], "--device", self.device(self.barcode))
            self.settled(job, timeout=timeout)
        self.ejected()
        page = self.test.one("restore", "files", job["id"], "--media-id", self.state["media_id"], "--limit", "100")
        restored = page.get("items", [])
        expected = {str(i["file"]["expected"]["file_id"]): i for i in items}
        require(not page.get("has_more") and len(restored) == len(expected)
                and {str(i["file"]["file_id"]) for i in restored} == set(expected), "Restore changed its selected file set.")
        hashes = {}
        for item in restored:
            archived = expected[str(item["file"]["file_id"])]
            digest = decode_bytes(archived["file"]["expected"]["sha256"]).hex()
            size = int(archived.get("size_bytes", 0))
            require(item["status"] == "COPY_STATUS_COMPLETED" and not item.get("damaged")
                    and str(item["candidate"]["media_id"]) == self.state["media_id"]
                    and item["candidate"]["media_path"] == archived["file"]["media_path"]
                    and decode_bytes(item["actual_sha256"]).hex() == digest
                    and int(item.get("actual_size_bytes", 0)) == size, "Restore did not verify its selected physical copy.")
            relative = str(relative_path(item["file"]["target_path"]))
            path = self.test.root + "/fixtures/restored/" + directory + "/" + relative
            actual = self.test.remote(["sha256sum", "--", path], timeout=3600).stdout.decode().split()[0]
            actual_size = int(self.test.remote(["stat", "-c", "%s", "--", path]).stdout)
            require(actual == digest and actual_size == size, "Actual restored bytes differ from source.")
            signature = self.test.remote(["getfattr", "--only-values", "-n", "user.acp.signature", "--", path]).stdout
            check_signature(signature, size, digest)
            hashes[relative] = {"size": size, "sha256": digest}
        positions = self.export("library-restored-" + directory + ".jsonl")
        root = self.capture_job(job)
        require((root / "tapes" / self.barcode / "ltfs.log").is_file(), "Restore LTFS log is missing.")
        order = check_restore_database(root / "state.db", positions,
                                       [i["file"]["media_path"] for i in items], self.state["media_id"])
        self.keep_json(directory + "-restored-manifest.json", hashes)
        self.keep_json(directory + "-restore-storage-order.json", order)
        self.save(**{completion: True})

    def available(self, path):
        values = self.test.remote(["stat", "-f", "-c", "%a %S", "--", path]).stdout.split()
        require(len(values) == 2, "Filesystem available-space result is incomplete.")
        return int(values[0]) * int(values[1])

    def stream_fixture(self, path, size, stream):
        require(size > 0 and size % (1024**2) == 0, "Fixture size must be a positive whole MiB.")
        require(path.startswith(self.test.root + "/"), "Fixture destination is outside the owned root.")
        relative_path(path[len(self.test.root) + 1:])
        # One sequential write and one inline digest; no source-sized temporary file or reread.
        seed = hashlib.sha256(b"YATM physical acceptance deterministic fixture").hexdigest()
        command = ('set -euo pipefail; test ! -e "$1"; '
                   'dd if=/dev/zero bs=1M count="$2" status=none | '
                   'openssl enc -aes-256-ctr -K "$3" -iv "$4" -nosalt')
        command += ' | tee -- "$1" | sha256sum'
        result = self.test.remote(["bash", "-c", command, "physical-fixture", path,
                                   str(size // 1024**2), seed, f"{stream:032x}"], timeout=43200)
        actual_size = int(self.test.remote(["stat", "-c", "%s", "--", path]).stdout)
        require(actual_size == size, "Fixture stream did not write its complete bounded content.")
        digest = result.stdout.decode().split()[0]
        require(re.fullmatch(r"[0-9a-f]{64}", digest), "Fixture stream did not complete its SHA-256.")
        return {"size": size, "stream": stream, "sha256": digest}


def run_stage(test, state, save_state):
    stage = test.args.physical_stage
    require(stage in ("prepare", "baseline", "restore", "cleanup"), "Unknown physical stage.")
    device, barcode = test.args.physical_device, test.args.physical_barcode
    require(isinstance(device, str) and device.startswith("/dev/") and ".." not in Path(device).parts,
            "Choose the explicit physical Tape device.")
    require(re.fullmatch(r"[A-Z0-9]{6}", barcode), "Expected a six-character physical Tape barcode.")
    for key, value in (("physical_device", device), ("physical_barcode", barcode), ("physical_root", test.root)):
        require(key not in state or state[key] == value, "Physical stage identity differs from saved state.")
        require(stage == "prepare" or key in state, "Physical stage has no saved preparation identity.")
        state[key] = value
    save_state()
    getattr(PhysicalCases(test, state, save_state), stage.replace("-", "_"))()
