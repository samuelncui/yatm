"""Installed-package tar acceptance through the service and CLI; no LTFS mount, Index or Preview.

Stages: cartridge memory barcode preparation, FORMAT/APPEND identity, restarted reads with passive read learning and
AUTO/LOGICAL records, an EW fill through ordinary Archive, and independent public-tool recovery.
"""

from contextlib import closing
import json
from pathlib import Path
import re
import sqlite3
import time

from package_acceptance import require, sha256
from package_physical_cases import PhysicalCases, PhysicalEvidence
from package_physical_evidence import decode_bytes, library_positions, relative_path
from physical_package_acceptance import public_tape_data, tar_marker
from physical_tar_recovery import device_aliases, parse_status


MAM_BARCODE = 0x0806
# WRITE ATTRIBUTE, parameter list length 9: one ASCII attribute 0806h whose zero length clears it.
CLEAR_BARCODE_CDB = ("8D", "00", "00", "00", "00", "00", "00", "00", "00", "00", "00", "00", "00", "09", "00", "00")
CLEAR_BARCODE_PARAMETERS = bytes.fromhex("00 00 00 05 08 06 01 00 00")
SEQUENTIAL_SCAN = re.compile(r"sequential[ -]scan", re.I)
# The learned read parameters refit is asynchronous after drive release; waits are bounded and report-only after the first.
LEARNING_WAIT_SECONDS = 600
LEARNING_POLL_SECONDS = 10
OBSERVATION_WAIT_SECONDS = 120
# Explicit fill budget: a quarter of nominal LTO-5 native rate plus one hour, for each Job wait.
FILL_BUDGET_BYTES_PER_SECOND = 35 * 1000**2
FILL_RESTORE_SAMPLES = 3


def parse_mam_barcode(data):
    """Decode raw READ ATTRIBUTE (ATTRIBUTE VALUES) output; an absent or blank barcode is empty."""
    require(len(data) >= 4, "READ ATTRIBUTE response is truncated")
    end = 4 + int.from_bytes(data[:4], "big")
    require(len(data) >= end, "READ ATTRIBUTE response is incomplete; cartridge memory barcode is unknown")
    found, offset = [], 4
    while offset < end:
        require(end - offset >= 5, "READ ATTRIBUTE entry header is truncated")
        identifier = int.from_bytes(data[offset:offset + 2], "big")
        flags, length = data[offset + 2], int.from_bytes(data[offset + 3:offset + 5], "big")
        value = data[offset + 5:offset + 5 + length]
        require(len(value) == length and offset + 5 + length <= end, "READ ATTRIBUTE entry value is truncated")
        if identifier == MAM_BARCODE:
            found.append((flags, value))
        offset += 5 + length
    require(len(found) <= 1, "READ ATTRIBUTE reported duplicate barcodes")
    if not found:
        return {"present": False, "length": 0, "value": ""}
    flags, value = found[0]
    require(not value or (flags & 0x03 == 0x01 and value.isascii()), "Cartridge memory barcode is not ASCII")
    return {"present": True, "length": len(value), "value": value.decode("ascii").rstrip(" \x00")}


def barcode_fields(value):
    """Return reported non-empty barcode fields; device inspection defines none."""
    if isinstance(value, list):
        return [field for item in value for field in barcode_fields(item)]
    if isinstance(value, dict):
        return [field for key, item in value.items()
                for field in ([key] if "barcode" in key.lower() and item else barcode_fields(item))]
    return []


def capacity_remaining(reply):
    """Remaining native bytes from passive inspection when reported; absent stays unknown."""
    for feature in reply.get("tape", {}).get("features", []):
        remaining = (feature.get("capacity") or {}).get("remaining_bytes")
        if remaining is not None:
            return int(remaining)
    return None


def idle(job):
    return (job.get("status") in ("JOB_STATUS_READY", "JOB_STATUS_FAILED", "JOB_STATUS_COMPLETED")
            and job.get("phase", "JOB_PHASE_UNSPECIFIED") in ("JOB_PHASE_UNSPECIFIED", "JOB_PHASE_COMPLETED"))


def read_observations(path):
    """Count parseable JSON object lines; at least one is required."""
    parsed = invalid = 0
    for line in path.read_bytes().splitlines():
        if not line.strip():
            continue
        try:
            value = json.loads(line)
        except ValueError:
            invalid += 1
            continue
        if isinstance(value, dict):
            parsed += 1
        else:
            invalid += 1
    require(parsed > 0, "Job bundle has no parseable read observation")
    return {"records": parsed, "unparseable_lines": invalid}


def model_change(before, after):
    """Report-only differences between two learned read parameters snapshots."""
    def parameters(snapshot):
        return {json.dumps(model.get("parameters", {}), sort_keys=True): model for model in snapshot.get("models", [])}
    old, new = parameters(before), parameters(after)
    return {"updated": max((int(m.get("updated_at_ns", 0)) for m in after.get("models", [])), default=0)
            > max((int(m.get("updated_at_ns", 0)) for m in before.get("models", [])), default=0),
            "parameters_changed": set(old) != set(new),
            "training_observations": [[int(m.get("training_observations", 0)) for m in snapshot.get("models", [])]
                                      for snapshot in (before, after)],
            "mae_seconds": [[m.get("mae_seconds") for m in snapshot.get("models", [])] for snapshot in (before, after)]}


def find_value(value, key):
    if isinstance(value, dict):
        if key in value:
            return value[key]
        value = list(value.values())
    if isinstance(value, list):
        for item in value:
            found = find_value(item, key)
            if found is not None:
                return found
    return None


class TarCases(PhysicalEvidence):
    # Reuse the LTFS controller's payload stream and free-space helpers unchanged.
    stream_fixture = PhysicalCases.stream_fixture
    available = PhysicalCases.available

    def check_device_released(self):
        devices = self.test.one("tape", "device", "list").get("devices", [])
        selected = [row for row in devices if row.get("device") == self.test.args.physical_device]
        require(len(selected) == 1 and not selected[0].get("busy", False), "Selected tar device is not known idle")

    def ensure_ready(self):
        # One explicit load handles normal unload; a failed/unknown load is never retried.
        self.check_device_released()
        command = ["mt", "-f", self.test.args.physical_device]
        observed = parse_status(self.test.remote([*command, "status"]).stdout.decode())
        if "DR_OPEN" in observed["flags"]:
            require(self.test.args.host == "local", "Tar load must be owned by the local controller")
            self.test.run([*command, "load"], timeout=None)
            observed = parse_status(self.test.remote([*command, "status"]).stdout.decode())
        require("ONLINE" in observed["flags"] and "DR_OPEN" not in observed["flags"],
                "Tar device did not become ready; retain the operation without retry")

    def wait_idle(self, job, timeout):
        # Poll the catalog only; the explicit budget never cancels or repeats the Media operation.
        deadline = time.monotonic() + timeout
        while True:
            current = self.test.one("job", "get", str(job["id"]), timeout=60)["job"]
            if idle(current):
                return current
            require(time.monotonic() < deadline, "Job is still active after its explicit budget; owner retained")
            time.sleep(5)

    def page(self, domain, job):
        items = []
        for offset in range(0, 3000, 1000):
            result = self.test.one(domain, "files" if domain in ("archive", "restore") else "entries",
                                   str(job["id"]), "--limit", "1000", "--offset", str(offset))
            page = result.get("items", result.get("entries", []))
            require(len(page) <= 1000 and (page or not result.get("has_more")), "Tar manifest page did not advance")
            items.extend(page)
            if not result.get("has_more"):
                return items
        raise RuntimeError("Tar manifest exceeds the fixed fixture row envelope")

    def archive_items(self, job):
        return self.page("archive", job)

    def tape_evidence(self, identifier, verify=False):
        # Tar keeps no recovery descriptor or key artifact; only passive read observations are collected.
        path = self.observations_path(identifier)
        destination = "jobs/" + identifier + "/tapes/" + self.barcode + "/read-observations.jsonl"
        if self.test.remote(["test", "-f", path], expected=None).returncode != 0:
            require(not (verify and (self.evidence / destination).exists()), "Sealed read observations disappeared")
            return
        self.test.remote(["test", "!", "-L", path])
        if verify:
            preserved = self.evidence / destination
            require(preserved.is_file() and sha256(preserved) ==
                    self.test.remote(["sha256sum", "--", path]).stdout.decode().split()[0],
                    "Live read observations differ from their sealed copy; retain all Jobs.")
        else:
            self.copy_evidence(path, destination)

    def observations_path(self, identifier):
        return self.test.install + "/work/jobs/" + identifier + "/tapes/" + self.barcode + "/read-observations.jsonl"

    def export(self, name):
        records = public_tape_data(self.test.cli("library", "export", "--output", "-"))
        data = b"".join(json.dumps(record).encode() + b"\n" for record in records)
        self.keep(name, data)
        return library_positions(data, self.state["media_id"])

    def copy_evidence(self, source, relative):
        if not relative.endswith("/job.json"):
            return super().copy_evidence(source, relative)
        # Normal Job profiles retain keys; the acceptance report keeps only public fields.
        value = json.loads(self.test.remote(["cat", "--", source]).stdout)
        return self.keep_json(relative, public_tape_data(value))

    def sg(self):
        # The confirmed SG path must be the generic device sysfs resolves for the selected Tape alias.
        _, aliases = device_aliases(self.test.args.physical_device)
        require(self.test.args.sg_device in aliases, "Selected SG path does not resolve to the selected Tape device")
        return self.test.args.sg_device, aliases

    def mam(self, name):
        sg, _ = self.sg()
        argv = ["sg_read_attr", "--raw", sg]
        result = self.test.remote(argv, expected=None)
        self.keep("mam/" + name + ".bin", result.stdout)
        receipt = {"argv": argv, "exit_code": result.returncode, "stderr": result.stderr.decode(errors="replace")}
        self.keep_json("mam/" + name + ".json", receipt)
        require(result.returncode == 0, "READ ATTRIBUTE failed; cartridge memory barcode is unknown")
        receipt["barcode"] = parse_mam_barcode(result.stdout)
        self.keep_json("mam/" + name + ".json", receipt)
        return receipt["barcode"]

    def inspect_identity(self, name):
        reply = self.test.one("tape", "device", "inspect", "--device", self.test.args.physical_device)
        self.keep_json("mam/" + name + "-device-inspect.json", reply)
        require(not barcode_fields(reply), "Device inspection reported a barcode")
        return reply

    def device_settings(self, settings=None, order=None):
        settings = settings if settings is not None else self.test.one("settings", "devices")
        entries = [row for row in settings.get("devices", []) if row.get("device") == self.test.args.physical_device]
        require(len(entries) == 1 and entries[0].get("tape", {}).get("passive_read_learning") is True,
                "Passive read learning is not explicitly enabled for the selected device")
        require(order is None or entries[0]["tape"].get("read_order") == "TAPE_READ_ORDER_" + order,
                "Device read order differs from the selected arm")
        return settings

    def set_read_order(self, order):
        return self.set_tape(order.lower(), {"read_order": "TAPE_READ_ORDER_" + order}, order)

    def set_tape(self, name, changes, order=None):
        # Replace only this device's settings; server-owned learned read parameters are ignored on update.
        # A None value removes that preference so the service default applies again.
        current = self.test.one("settings", "devices")
        device = self.test.args.physical_device
        others = [row for row in current.get("devices", []) if row.get("device") != device]
        entry = next((row for row in current.get("devices", []) if row.get("device") == device), {})
        tape = {**entry.get("tape", {}), **changes, "passive_read_learning": True}
        tape = {key: value for key, value in tape.items() if value is not None}
        path = self.test.root + "/tmp/device-settings-" + name + ".json"
        self.test.write(path, json.dumps({"devices": others + [{"device": device, "tape": tape}]}).encode())
        return self.device_settings(self.test.one("settings", "devices", "--device-json", path), order)

    def learned_read_parameters(self, previous=None, required=True):
        """Poll Device settings after release until newer learned read parameters appear, within a bound."""
        baseline = max((int(m.get("updated_at_ns", 0)) for m in (previous or {}).get("models", [])), default=0)
        deadline = time.monotonic() + LEARNING_WAIT_SECONDS
        while True:
            settings = self.device_settings()
            models = [model for model in settings.get("learned_read_parameters", [])
                      if model.get("device") == self.test.args.physical_device]
            if models and max(int(m.get("updated_at_ns", 0)) for m in models) > baseline:
                require(all(int(m.get("training_observations", 0)) > 0 for m in models),
                        "Learned read parameters have no training observations")
                return {"models": models, "observed_at_ns": time.time_ns()}
            if time.monotonic() >= deadline:
                require(not required, "No learned read parameters appeared in Device settings within the bounded wait")
                return {**(previous or {}), "unchanged_after_seconds": LEARNING_WAIT_SECONDS}
            time.sleep(LEARNING_POLL_SECONDS)

    def job_observations(self, key):
        job = self.job(key + "_job_id")
        path = self.observations_path(job["id"])
        deadline = time.monotonic() + OBSERVATION_WAIT_SECONDS
        while self.test.remote(["test", "-s", path], expected=None).returncode != 0:
            require(time.monotonic() < deadline, "Job bundle has no read-observations.jsonl: " + key)
            time.sleep(5)
        destination = "jobs/" + job["id"] + "/tapes/" + self.barcode + "/read-observations.jsonl"
        return {"job_id": job["id"], **read_observations(self.copy_evidence(path, destination))}

    def prepare(self):
        jobs = self.test.one("job", "list", "--limit", "1")
        require(not jobs.get("jobs") and not jobs.get("has_more"), "Prepare requires an empty isolated Job catalog")
        self.save(physical_evidence_dir=str(self.evidence.resolve()))
        owner = Path(__file__).resolve().parent
        sources = ("physical_package_acceptance.py", "package_acceptance.py", "package_install_cases.py",
                   "package_physical_cases.py", "package_ltfs_cases.py", "package_physical_evidence.py",
                   "package_physical_tar.py", "package_physical_capacity.py", "physical_performance_fixture.py",
                   "physical_tar_recovery.py")
        manifest = []
        for name in sources:
            path = self.keep("source/controller/" + name, (owner / name).read_bytes())
            manifest.append(sha256(path) + "  " + name + "\n")
        self.keep("source/controller.sha256", "".join(manifest).encode())
        self.keep("source/packages.sha256", (sha256(self.test.args.archive) + "  " + self.test.args.archive.name + "\n").encode())
        self.test.upload([owner / "physical_tar_recovery.py"], self.test.root + "/tmp")
        self.test.remote(["mkdir", self.test.root + "/fixtures/restored"])
        fixture = self.test.root + "/tmp/fixture.json"
        self.test.remote(["python3", "-B", self.test.root + "/tmp/physical_tar_recovery.py", "fixture", "generate",
                          self.test.root + "/fixtures/source", fixture, "--record-bytes", str(self.test.args.record_bytes)])
        values = json.loads(self.test.remote(["cat", fixture]).stdout)
        require(len(values) == 9, "Tar file count differs from its fixed fixture")
        self.save(fixture_manifest=values)
        self.keep_json("source-manifest.json", values)
        for name in ("source", "restored"):
            result = self.test.one("location", "create", "--name", "Tar baseline " + name,
                                   "--root", self.test.root + "/fixtures/" + name,
                                   *(["--restore-target"] if name == "restored" else []))
            self.save(**{name + "_location": str(result["location"]["id"])})
        self.source, self.restored = self.state["source_location"], self.state["restored_location"]
        # Every later stage runs with explicit passive read learning; LOGICAL is the baseline read order.
        self.keep_json("device-settings.json", self.set_read_order("LOGICAL"))
        # The mismatch Job reuses the APPEND selection and must stay unwritten.
        for key, group in (("format", "format"), ("append", "append"), ("mismatch", "append")):
            job = self.created(key + "_job_id", "archive", "create", "--location", self.source + ":" + group,
                               "--preview-policy", "none")
            self.settled(job, "JOB_STATUS_READY")
            self.check_prepared(job, group)
        self.check_device_released()
        self.save(prepare_complete=True)

    def inspect(self):
        self.ensure_ready()
        reply = self.test.one("tape", "device", "inspect", "--device", self.test.args.physical_device)
        device, tape = reply.get("device", {}), reply.get("tape", {})
        require(device.get("device") == self.test.args.physical_device and not device.get("busy", False)
                and reply.get("reason") in (None, "DEVICE_REASON_UNSPECIFIED") and tape.get("medium_present") is True,
                "Passive inspection did not establish the selected idle device and present medium")
        writes = [feature for feature in tape.get("features", [])
                  if feature.get("feature") == "DEVICE_FEATURE_KIND_SEQUENTIAL_WRITE"]
        require(len(writes) == 1 and isinstance(writes[0].get("records"), dict), "Write record bounds are unknown")
        bounds = writes[0]["records"]
        minimum, maximum, alignment = (bounds.get(key) for key in ("minimum_bytes", "maximum_bytes", "alignment_bytes"))
        size = self.test.args.record_bytes
        require(type(minimum) is int and minimum >= 0 and type(alignment) is int and alignment > 0
                and (maximum is None or type(maximum) is int and maximum >= minimum), "Write record bounds are malformed")
        require(size >= minimum and (maximum is None or size <= maximum) and (size - minimum) % alignment == 0,
                "Selected record size violates reported bounds")
        if self.test.args.record_mode == "variable":
            require(minimum <= 512 and 512 % alignment == 0 and (512 - minimum) % alignment == 0,
                    "Variable final-short records cannot satisfy reported bounds")
        self.keep_json("passive-inspection.json", reply)

    def media(self):
        reply = self.test.one("media", "list", "--kind", "tape", "--query", self.barcode, "--limit", "100")
        rows = [row for row in reply.get("media", []) if row.get("identity") == self.barcode]
        require(not reply.get("has_more") and len(rows) == 1, "Expected one exact registered cartridge")
        media = rows[0]
        profile = media.get("profile", {}).get("tape", {})
        tar = profile.get("tar", {})
        require(profile.get("format") == "tar_pax_v1" and tar.get("record_mode") ==
                "TAPE_RECORD_MODE_" + self.test.args.record_mode.upper()
                and int(tar.get("record_bytes", 0)) == self.test.args.record_bytes
                and not tar.get("hardware_compression", False) and media.get("encryption_key_saved")
                and tar.get("initialization_id"), "Frozen tar profile/options differ")
        public = {key: profile[key] for key in ("serial_number", "format") if key in profile}
        public["tar"] = {key: tar[key] for key in ("initialization_id", "record_mode", "record_bytes",
                        "hardware_compression", "encryption_algorithm") if key in tar}
        return str(media["id"]), public

    def completed_archive(self, group):
        items = self.archive_items(self.job(group + "_job_id"))
        self.check_fixture(items, group)
        root = self.test.root + "/fixtures/source/"
        found = {}
        for item in items:
            source = item["file"]["source_path"]
            require(source.startswith(root), "Archive source escaped the fixture")
            name = str(relative_path(source[len(root):]))
            require(name not in found and item["status"] == "COPY_STATUS_SUBMITTED"
                    and str(item.get("media_id")) == self.state["media_id"]
                    and int(item["file"]["expected"].get("file_id", 0)) > 0, "Archive did not publish its frozen copy")
            found[name] = item
        return found

    def check_positions(self, positions, items):
        by_path = positions
        require(len(by_path) == len(items)
                and set(by_path) == {item["file"]["media_path"] for item in items.values()}, "Published member set differs")
        anchors = 0
        for item in items.values():
            row = by_path[item["file"]["media_path"]]
            locator = row.get("storage_metadata", {}).get("tar")
            require(isinstance(locator, dict) and row.get("hash") == item["file"]["expected"]["sha256"]
                    and int(row.get("size", 0)) == int(item.get("size_bytes", 0)), "Position locator/content differs")
            require(bool(locator.get("header")) == bool(locator.get("end")), "Unpaired physical member anchors")
            anchors += bool(locator.get("header"))
        return {"members": len(positions), "paired_anchors": anchors,
                "scope": "Persisted locator coverage only; reload/seek qualification is separate."}

    def check_submitted_manifest(self, captured, items, pending=0):
        # Item.Result.Storage is provisional; CommitMedia publishes its locator then clears Result.
        database = captured / "state.db"
        require(database.is_file() and not database.is_symlink(), "Captured Archive Item database is missing")
        with closing(sqlite3.connect(database.resolve().as_uri() + "?mode=ro", uri=True)) as db:
            rows = db.execute("SELECT id,status,size,media_id,target_path,media_path,result FROM items ORDER BY id").fetchall()
        expected = {int(item["id"]): item for item in items.values()}
        submitted = [row for row in rows if row[0] in expected]
        require(len(submitted) == len(expected) and len(rows) == len(expected) + pending,
                "Captured Archive manifest membership differs from the settled CLI result")
        for identifier, status, size, media_id, target, path, result in rows:
            if identifier not in expected:
                # A capacity-stopped suffix returns to PENDING without staged storage or Media publication.
                require(status == 1 and not media_id and not path and result is None,
                        "Captured pending Item retains staged or published storage")
                continue
            item = expected[identifier]
            require(status == 4 and item["status"] == "COPY_STATUS_SUBMITTED"
                    and size == int(item.get("size_bytes", 0)) and media_id == int(item["media_id"])
                    and target == item["file"]["target_path"] and path == item["file"]["media_path"] and result is None,
                    "Captured submitted Item differs or retains provisional storage after publication")
        return {"members": len(submitted), "pending": pending, "submitted_results_cleared": True}

    def clear_barcode(self):
        # Test preparation only: clear MAM 0806h before the first FORMAT, never after Media exists.
        require(self.state.get("prepare_complete") and not self.state.get("tar_baseline_started")
                and "media_id" not in self.state, "Clear the cartridge memory barcode only after prepare and before FORMAT")
        registered = self.test.one("media", "list", "--kind", "tape", "--query", self.barcode, "--limit", "100")
        require(not registered.get("has_more") and not any(row.get("identity") == self.barcode
                                                           for row in registered.get("media", [])),
                "Barcode is registered in the Library; clearing would hide a Media identity")
        self.ensure_ready()
        sg, aliases = self.sg()
        users = self.test.remote(["fuser", *aliases], expected=None)
        require(users.returncode == 1 and not users.stdout.strip() and not users.stderr.strip(),
                "Tape device ownership is uncertain; refusing WRITE ATTRIBUTE")
        before = self.mam("clear-before")
        parameters = self.test.root + "/tmp/clear-barcode-parameters.bin"
        self.test.write(parameters, CLEAR_BARCODE_PARAMETERS)
        argv = ["sg_raw", "-s", str(len(CLEAR_BARCODE_PARAMETERS)), "-i", parameters, sg, *CLEAR_BARCODE_CDB]
        self.save(**{tar_marker("tar-clear-barcode"): True})
        result = self.test.remote(argv, expected=None)
        self.keep_json("mam/clear-write.json", {
            "argv": argv, "parameters_hex": CLEAR_BARCODE_PARAMETERS.hex(" "), "before": before,
            "exit_code": result.returncode, "stdout": result.stdout.decode(errors="replace"),
            "stderr": result.stderr.decode(errors="replace")})
        require(result.returncode == 0, "Drive refused WRITE ATTRIBUTE; recorded without retry or fallback")
        after = self.mam("clear-after")
        require(after["value"] == "", "Cartridge memory barcode remains after WRITE ATTRIBUTE")
        self.inspect_identity("clear-after")
        self.save(barcode_cleared=True)

    def refused_write(self, mode, wrong):
        """Submit one mismatched write and require refusal before any data or MAM change."""
        job = self.job("mismatch_job_id")
        before = self.test.one("job", "get", job["id"])["job"]
        require(idle(before) and before["status"] == "JOB_STATUS_READY", "Mismatch probe requires an idle prepared Job")
        media = self.media()
        positions = self.export("library-before-" + mode + "-mismatch.jsonl")
        self.ensure_ready()
        args = ["archive", "write", "tape", mode, job["id"], "--device", self.test.args.physical_device, "--barcode", wrong]
        if mode == "format":
            args += ["--name", "Tar mismatch " + wrong, "--confirm-format", wrong, "--format", "tar_pax_v1",
                     "--hardware-compression", "false", "--record-mode", self.test.args.record_mode,
                     "--record-bytes", str(self.test.args.record_bytes)]
        result = self.test.remote([self.test.install + "/yatm-cli", "--server", self.test.url, "--timeout", "160s", *args],
                                  expected=None)
        record = {"mode": mode, "requested": wrong, "exit_code": result.returncode,
                  "stdout": result.stdout.decode(errors="replace"), "stderr": result.stderr.decode(errors="replace")}
        if result.returncode == 0:
            # An admitted attempt must fail under the lease and return to pre-Media READY.
            settled = self.wait_idle(job, 3600)
            record.update(refusal="session", job=settled)
            require(settled["status"] == "JOB_STATUS_READY" and settled.get("error"),
                    "Mismatched " + mode + " was not refused")
        else:
            record["refusal"] = "cli"
            require(self.test.one("job", "get", job["id"])["job"] == before, "CLI refusal changed the Job")
        self.check_device_released()
        require(all(item["status"] == "COPY_STATUS_PENDING" and not item.get("media_id")
                    for item in self.archive_items(job)), "Mismatched " + mode + " published content")
        require(self.media() == media and self.export("library-after-" + mode + "-mismatch.jsonl") == positions,
                "Mismatched " + mode + " changed Media or Positions")
        self.ensure_ready()
        record["mam"] = self.mam(mode + "-mismatch-after")
        require(record["mam"]["value"] == self.barcode, "Mismatched " + mode + " changed the cartridge memory barcode")
        return record

    def check_append_log(self, captured, since_ns):
        log = (captured / "job.log").read_text(errors="replace")
        journal = self.test.remote(["journalctl", "--unit", self.test.service, "--since", "@" + str(since_ns // 10**9),
                                    "--output", "cat", "--no-pager"]).stdout
        self.keep("append-service.log", journal)
        hits = [line for line in log.splitlines() + journal.decode(errors="replace").splitlines()
                if SEQUENTIAL_SCAN.search(line)]
        require("tar APPEND tail verified" in log and not hits,
                "APPEND did not prove its tail directly or logged a sequential scan")
        return {"tail_verified": True, "sequential_scan_lines": 0}

    def baseline(self):
        require(self.state.get("prepare_complete") and not self.state.get("tar_baseline_started"),
                "Baseline requires prepared, unsubmitted tar state")
        self.device_settings()
        self.inspect()
        require(self.mam("format-before")["value"] == "",
                "Cartridge memory barcode is not empty before FORMAT; run tar-clear-barcode first")
        self.save(tar_baseline_started=True)
        all_items = {}
        for group in ("format", "append"):
            job = self.job(group + "_job_id")
            started = time.time_ns()
            if group == "append":
                # Reload after the mismatch probes; APPEND proves its tail with SPACE to EOD.
                self.ensure_ready()
            args = ["archive", "write", "tape", group, job["id"], "--device", self.test.args.physical_device,
                    "--barcode", self.barcode]
            if group == "format":
                args += ["--name", "Tar baseline " + self.barcode, "--confirm-format", self.barcode,
                         "--format", "tar_pax_v1", "--hardware-compression", "false",
                         "--record-mode", self.test.args.record_mode, "--record-bytes", str(self.test.args.record_bytes)]
            self.test.cli(*args)
            self.settled(job)
            self.check_device_released()
            media_id, profile = self.media()
            if group == "format":
                self.save(media_id=media_id, tar_profile=profile)
                self.ensure_ready()
                written = self.mam("format-after")
                require(written["value"] == self.barcode and written["length"] == 32,
                        "FORMAT did not write the requested 32-byte cartridge memory barcode")
            else:
                require(media_id == self.state["media_id"] and profile == self.state["tar_profile"],
                        "APPEND changed immutable Media/profile")
            items = self.completed_archive(group)
            all_items.update(items)
            self.save(**{group + "_items": items})
            positions = self.export("library-" + group + ".jsonl")
            self.keep_json(group + "-anchor-coverage.json", self.check_positions(positions, all_items))
            if group == "format":
                self.save(format_positions=positions)
            else:
                require(all(positions[path] == row for path, row in self.state["format_positions"].items()),
                        "APPEND changed earlier Positions")
            captured = self.capture_job(job)
            self.keep_json(group + "-item-publication.json", self.check_submitted_manifest(captured, items))
            if group == "format":
                wrong = "BAD999" if self.barcode != "BAD999" else "BAD998"
                self.keep_json("barcode-mismatch.json", [self.refused_write(mode, wrong) for mode in ("format", "append")])
            else:
                self.keep_json("append-tail.json", self.check_append_log(captured, started))
        self.save(baseline_complete=True)

    def restore_selection(self, key, names, items=None, timeout=7200):
        # Every Restore identifies the Tape by its cartridge memory barcode; no --media-id hint is supplied.
        fixture = items is None
        items = items or {**self.state["format_items"], **self.state["append_items"]}
        args = ["restore", "create", "--target-location", self.restored, "--directory", key]
        for name in names:
            args += ["--file-id", str(items[name]["file"]["expected"]["file_id"])]
        job = self.created(key + "_job_id", *args)
        self.settled(job, "JOB_STATUS_READY")
        self.ensure_ready()
        started = time.monotonic()
        self.test.cli("restore", "run", "tape", job["id"], "--device", self.test.args.physical_device)
        self.settled(job, timeout=timeout)
        self.save(**{key + "_seconds": time.monotonic() - started})
        self.check_device_released()
        restored = self.page("restore", job)
        by_file = {str(row["file"]["file_id"]): row for row in restored}
        require(len(restored) == len(names) and set(by_file) ==
                {str(items[name]["file"]["expected"]["file_id"]) for name in names}, "Restore changed its frozen selection")
        mapping = {}
        for name in names:
            archived = items[name]
            row = by_file[str(archived["file"]["expected"]["file_id"])]
            require(row["status"] == "COPY_STATUS_COMPLETED" and not row.get("damaged")
                    and str(row["candidate"]["media_id"]) == self.state["media_id"]
                    and row["candidate"]["media_path"] == archived["file"]["media_path"]
                    and row.get("actual_sha256") == archived["file"]["expected"]["sha256"]
                    and int(row.get("actual_size_bytes", 0)) == int(archived.get("size_bytes", 0))
                    and int(row["file"].get("file_version_id", 0)) > 0, "Restore did not verify its selected physical copy")
            mapping[name] = str(relative_path(row["file"]["target_path"]))
        if fixture:
            target = self.test.root + "/tmp/" + key + "-map.json"
            self.test.write(target, json.dumps(mapping).encode())
            self.test.remote(["python3", "-B", self.test.root + "/tmp/physical_tar_recovery.py", "fixture", "check",
                              self.test.root + "/fixtures/restored/" + key, self.test.root + "/tmp/fixture.json",
                              "--mapping", target, "--exact"])
        else:
            for name, relative in mapping.items():
                path = self.test.root + "/fixtures/restored/" + key + "/" + relative
                digest = self.test.remote(["sha256sum", "--", path], timeout=timeout).stdout.decode().split()[0]
                size = int(self.test.remote(["stat", "-c", "%s", "--", path]).stdout)
                require(digest == decode_bytes(items[name]["file"]["expected"]["sha256"]).hex()
                        and size == int(items[name].get("size_bytes", 0)), "Actual restored bytes differ from source")
        self.keep_json(key + "-selection.json", {"identification": "automatic cartridge memory barcode", "files": mapping,
                                                 "seconds": self.state[key + "_seconds"]})
        self.capture_job(job)

    def scan(self, verify, key=None, items=None, timeout=7200):
        key = key or ("verify" if verify else "inventory")
        args = (["verify", "create", self.state["media_id"]] if verify else
                ["scan", "media", self.state["media_id"], "--signature", "known-only", "--result", "inventory",
                 "--preview-policy", "none"])
        job = self.created(key + "_job_id", *args)
        self.settled(job, "JOB_STATUS_READY")
        self.ensure_ready()
        self.test.cli("verify" if verify else "scan", "run", job["id"], "--device", self.test.args.physical_device,
                      "--media-id", self.state["media_id"])
        self.settled(job, timeout=timeout)
        self.check_device_released()
        rows = self.page("verify", job)
        items = items or {**self.state["format_items"], **self.state["append_items"]}
        expected = {item["file"]["media_path"] for item in items.values()}
        require(len(rows) == len(expected) and {row["path"] for row in rows} == expected, "Scan/Verify member set differs")
        if verify:
            require(all(row.get("finding") == "SCAN_FINDING_MATCH" and int(row.get("checked_at_ns", 0)) > 0
                        for row in rows), "Verify has unmatched or unread findings")
        else:
            require(all(row.get("finding", "SCAN_FINDING_NOT_CHECKED") == "SCAN_FINDING_NOT_CHECKED"
                        and not int(row.get("checked_at_ns", 0)) for row in rows), "Inventory claimed content verification")
        self.check_positions(self.export("library-" + key + ".jsonl"), items)
        self.capture_job(job)

    def read_order_arm(self, order, names, prefix="restore-", items=None):
        """Restore one fixed selection under a frozen read order; durations and reasons are report-only."""
        self.set_read_order(order)
        key = prefix + order.lower()
        self.restore_selection(key, names, items=items)
        job = self.job(key + "_job_id")
        log = (self.evidence / "jobs" / job["id"] / "job.log").read_text(errors="replace")
        decisions = [line for line in log.splitlines() if "read order" in line]
        require(any("TAPE_READ_ORDER_" + order in line for line in decisions if "requested_read_order" in line),
                "Job log lacks the requested " + order + " read-order decision")
        progress = self.test.one("job", "progress", job["id"])
        try:
            optimization = self.test.one("tape", "device", "inspect", "--device",
                                         self.test.args.physical_device).get("prepared_read")
        except Exception as error:  # Optional diagnostics never replace the Job log record.
            optimization = {"error": str(error)}
        record = {"order": order, "job_id": job["id"], "seconds": self.state[key + "_seconds"],
                  "progress_elapsed_ms": find_value(progress, "elapsed_ms"), "planner_log": decisions,
                  "prepared_read": optimization}
        self.keep_json("read-order/" + key.removeprefix("restore-") + ".json", record)
        return record

    def learning(self, names):
        observed = {key: self.job_observations(key) for key in ("restore-full", "restore-selected", "inventory", "verify")}
        first = self.learned_read_parameters()
        arms = {}
        models = [first]
        for order in ("LOGICAL", "AUTO"):
            arms[order] = self.read_order_arm(order, names)
            models.append(self.learned_read_parameters(models[-1], required=False))
        self.set_read_order("LOGICAL")
        # Job deletion is the only observation retention control; capture already preserved this bundle.
        deleted = self.job("restore-selected_job_id")
        bundle = self.test.install + "/work/jobs/" + deleted["id"]
        self.test.remote(["test", "-d", bundle])
        self.test.cli("job", "delete", deleted["id"])
        self.test.remote(["test", "!", "-e", bundle])
        self.save(deleted_job_ids=self.state.get("deleted_job_ids", []) + [deleted["id"]])
        self.keep_json("learning.json", {
            "observations": observed, "first_model": first, "after_logical": models[1], "after_auto": models[2],
            "second_restore_change": model_change(models[0], models[1]),
            "deleted_restore_job": deleted["id"], "bundle_removed": True,
            "scope": "Model changes are report-only observations of this run."})
        self.keep_json("read-order-comparison.json", {
            "selection": names, "arms": arms,
            "scope": "Report only; no duration threshold. AUTO is effective only when the Job log reports it."})

    def public_recovery(self, fill=None):
        self.ensure_ready()
        # Obtain the existing successful Media key only for this explicitly invoked recovery.
        media = self.saved_media(self.state["media_id"], self.barcode)
        encoded = media["profile"]["tape"]["encryption"]
        require(re.fullmatch(r"v1:[0-9a-fA-F]{64}", encoded), "Recovery requires the saved v1 Tape key")
        name = "fill-public-recovery" if fill else "public-recovery"
        members = None
        if fill:
            members = self.test.root + "/tmp/fill-members.json"
            self.test.write(members, json.dumps({
                item["file"]["media_path"]: {"size": int(item.get("size_bytes", 0)),
                                             "sha256": decode_bytes(item["file"]["expected"]["sha256"]).hex()}
                for item in fill.values()}).encode())
        self.test.stop_owned()
        output = self.test.root + "/tmp/" + name
        # Public tools receive one temporary key file, never a Job recovery artifact or locator.
        command = ["python3", "-B", self.test.root + "/tmp/physical_tar_recovery.py",
                   "--device", self.test.args.physical_device, "--record-mode", self.test.args.record_mode,
                   "--record-bytes", str(self.test.args.record_bytes), "--fixture", self.test.root + "/tmp/fixture.json",
                   "--out", output]
        temporary = self.test.remote(["mktemp", "-d", self.test.root + "/tmp/public-key.XXXXXXXX"]).stdout.decode().strip()
        require(re.fullmatch(re.escape(self.test.root) + r"/tmp/public-key\.[A-Za-z0-9]{8}", temporary),
                "Recovery key directory is outside the execution host's owned root")
        key_file = temporary + "/key"
        try:
            # Shell redirection consumes private stdin; tee would echo the key into command transcripts.
            self.test.remote(["sh", "-c", 'umask 077; cat > "$1" && chmod 600 "$1"',
                              "yatm-public-key", key_file], body=encoded[3:].encode())
            command += ["--encrypt-script", self.test.root + "/tape-scripts/encrypt",
                        "--key-file", key_file, "--barcode", self.barcode,
                        "--tape-name", media.get("name", ""), "--script-directory", self.test.install]
            if members:
                command += ["--fill-members", members]
            # Tar stages run locally; issued public-reader commands must settle without a watchdog.
            self.test.run(command, timeout=None)
        finally:
            self.test.remote(["rm", "-rf", "--", temporary])
        # Payload archives and extracted files stay on the host; only bounded receipts cross stdout.
        listing = self.test.remote(["find", output, "-maxdepth", "1", "-type", "f", "-print0"]).stdout
        remaining = 16 << 20
        for raw in listing.split(b"\x00"):
            if raw:
                path = raw.decode()
                require(path.startswith(output + "/"), "Public reader output escaped its owned directory")
                relative = str(relative_path(path[len(output) + 1:]))
                if relative not in ("review.json", "commands.json") and not re.fullmatch(r"[a-z][a-z0-9-]*\.(out|err)", relative):
                    continue
                size = int(self.test.remote(["stat", "-c", "%s", "--", path]).stdout)
                require(0 <= size <= remaining, "Public recovery receipts exceed the bounded evidence allowance")
                remaining -= size
                self.copy_evidence(path, name + "/" + relative)
        self.keep_json(name + "/payload-retention.json", {
            "host_directory": output, "payloads_retained_on_host": True,
            "scope": "Archive and extracted content remain for review and explicit owned-root cleanup; receipt hashes do not copy payloads.",
        })
        review = json.loads((self.evidence / name / "review.json").read_text())
        identity = review.get("identity", {})
        require(identity.get("format") == "tar_pax_v1" and identity.get("barcode") == self.barcode
                and identity.get("profile") == self.state["tar_profile"]["tar"]
                and review.get("record_setting_restored") is True and review.get("end") == "DRIVER_REPORTED_EOD"
                and [row.get("boundary") for row in review.get("files", [])] == ["FILEMARK"] * (4 if fill else 3)
                and (not fill or review.get("fill", {}).get("published_checked") == len(fill)),
                "Independent label/profile, FILEMARK, fill member or normal EOD acceptance is incomplete")
        self.save(**{("tar_fill_recovery_complete" if fill else "tar_recovery_complete"): True})
        self.test.start_owned()

    def restore(self):
        require(self.state.get("baseline_complete") and not self.state.get("tar_restore_started"),
                "Restore requires completed baseline and unsubmitted readback state")
        self.save(tar_restore_started=True)
        self.device_settings(order="LOGICAL")
        # The stage controller has restarted the owned service from persisted Catalog/Job data.
        names = sorted({**self.state["format_items"], **self.state["append_items"]})
        self.restore_selection("restore-full", names)
        self.restore_selection("restore-selected", [names[0], names[-1]])
        self.scan(verify=False)
        self.scan(verify=True)
        self.learning(names)
        self.public_recovery()
        self.save(restore_complete=True)

    def fill_sources(self, sizes, reflink, key="fill", directory="capacity"):
        from package_physical_capacity import generate_sources

        helper = self.test.root + "/tmp/capacity/physical_performance_fixture.py"
        self.test.remote(["mkdir", "-p", "--", self.test.root + "/tmp/capacity"])
        self.test.write(helper, Path(__file__).with_name("physical_performance_fixture.py").read_bytes())
        manifest = generate_sources(self, helper, sizes, reflink, directory)
        path = self.evidence / key / "source-manifest.json"
        path.parent.mkdir(parents=True, exist_ok=True, mode=0o700)
        with path.open("x") as output:
            output.write(json.dumps(manifest, indent=2) + "\n")
        path.chmod(0o400)
        self.save(**{key + "_manifest_sha256": sha256(path)})
        return manifest

    def fill(self):
        from package_physical_capacity import selected_items

        require(self.state.get("restore_complete") and self.state.get("tar_recovery_complete")
                and not self.state.get("tar_fill_started"), "tar-fill requires completed restore and recovery, once")
        sizes = [size * 1024**2 for size in self.test.args.capacity_file_mib]
        reflink = self.test.args.capacity_source_reflink
        payload = sum(sizes)
        # Explicit per-wait budget; issued device work still settles without a watchdog.
        budget = 3600 + payload // FILL_BUDGET_BYTES_PER_SECOND
        # Only sampled members are restored to disk; Verify and public recovery hash full copies in flight.
        required = (max(sizes) if reflink else payload) + FILL_RESTORE_SAMPLES * max(sizes) + 8 * 1024**3
        require(self.available(self.test.root) > required,
                "Fill source/Restore filesystem lacks the required space: " + str(required) + " bytes")
        self.device_settings(order="LOGICAL")
        self.ensure_ready()
        inspection = self.test.one("tape", "device", "inspect", "--device", self.test.args.physical_device)
        remaining = capacity_remaining(inspection)
        self.keep_json("fill/device-inspect.json", inspection)
        require(remaining is None or payload > remaining, "Fill payload does not exceed the reported remaining capacity")
        self.save(tar_fill_started=True, fill_file_sizes=sizes, fill_budget_seconds=budget)
        manifest = self.fill_sources(sizes, reflink)
        sources = self.test.root + "/fixtures/source"
        job = self.created("fill_job_id", "archive", "create", "--location", self.source + ":capacity",
                           "--preview-policy", "none")
        self.settled(job, "JOB_STATUS_READY", timeout=budget)
        prepared = self.archive_items(job)
        self.keep_json("fill/prepared-items.json", prepared)
        selected_items(prepared, manifest, sources, self.state["media_id"])
        require(all(item["status"] == "COPY_STATUS_PENDING" for item in prepared), "Fill Job is not pending")
        before = self.export("fill/library-before.jsonl")
        self.ensure_ready()
        started = time.monotonic()
        self.test.cli("archive", "write", "tape", "append", job["id"], "--device", self.test.args.physical_device,
                      "--barcode", self.barcode)
        terminal = self.wait_idle(job, budget)
        items = self.archive_items(job)
        result = {"job": terminal, "seconds": time.monotonic() - started, "payload_bytes": payload,
                  "remaining_estimate_bytes": remaining, "budget_seconds": budget,
                  "statuses": [item["status"] for item in items]}
        self.keep_json("fill/write-result.json", result)
        published = selected_items(items, manifest, sources, self.state["media_id"])
        require(terminal["status"] == "JOB_STATUS_READY" and terminal.get("error")
                and published and len(published) < len(items),
                "Fill did not settle at a capacity stop with published members and a pending suffix")
        self.check_device_released()
        fill_items = {str(relative_path(item["file"]["source_path"][len(sources) + 1:])): item for item in published}
        everything = {**self.state["format_items"], **self.state["append_items"], **fill_items}
        positions = self.export("fill/library.jsonl")
        self.keep_json("fill/anchor-coverage.json", self.check_positions(positions, everything))
        require(all(positions.get(path) == row for path, row in before.items()), "Fill changed earlier Positions")
        captured = self.capture_job(job)
        self.keep_json("fill/item-publication.json",
                       self.check_submitted_manifest(captured, fill_items, pending=len(items) - len(published)))
        self.save(fill_items=fill_items)
        self.restore_selection("fill-restore", fill_restore_sample(fill_items), items=fill_items, timeout=budget)
        self.scan(verify=True, key="fill-verify", items=everything, timeout=budget)
        self.public_recovery(fill=fill_items)
        self.save(tar_fill_complete=True)


    def ew(self):
        """Write small members with capacity polling Off until the drive itself reports EW."""
        from package_physical_capacity import selected_items

        require(self.state.get("tar_fill_complete") and not self.state.get("tar_ew_started"),
                "tar-ew requires a completed tar-fill, once")
        sizes = [size * 1024**2 for size in self.test.args.capacity_file_mib]
        reflink = self.test.args.capacity_source_reflink
        payload = sum(sizes)
        budget = 3600 + payload // FILL_BUDGET_BYTES_PER_SECOND
        # Every published member is restored to disk.
        required = (max(sizes) if reflink else payload) + payload + 8 * 1024**3
        require(self.available(self.test.root) > required,
                "EW source/Restore filesystem lacks the required space: " + str(required) + " bytes")
        self.ensure_ready()
        inspection = self.test.one("tape", "device", "inspect", "--device", self.test.args.physical_device)
        remaining = capacity_remaining(inspection)
        self.keep_json("ew/device-inspect.json", inspection)
        require(remaining is None or payload > remaining, "EW payload does not exceed the reported remaining capacity")
        self.save(tar_ew_started=True, ew_file_sizes=sizes, ew_budget_seconds=budget)
        # Polling Off leaves admission with only the preparation sample, so the drive's EW can stop the
        # write before admission does. The service default is restored before any readback.
        self.keep_json("ew/device-settings.json", self.set_tape("ew", {"capacity_poll_interval_seconds": 0}, "LOGICAL"))
        manifest = self.fill_sources(sizes, reflink, key="ew", directory="ew")
        sources = self.test.root + "/fixtures/source"
        job = self.created("ew_job_id", "archive", "create", "--location", self.source + ":ew", "--preview-policy", "none")
        self.settled(job, "JOB_STATUS_READY", timeout=budget)
        prepared = self.archive_items(job)
        selected_items(prepared, manifest, sources, self.state["media_id"])
        before = self.export("ew/library-before.jsonl")
        self.ensure_ready()
        started = time.monotonic()
        self.test.cli("archive", "write", "tape", "append", job["id"], "--device", self.test.args.physical_device,
                      "--barcode", self.barcode)
        terminal = self.wait_idle(job, budget)
        items = self.archive_items(job)
        drive_ew = "Tape early warning" in terminal.get("error", "")
        result = {"job": terminal, "seconds": time.monotonic() - started, "payload_bytes": payload,
                  "remaining_estimate_bytes": remaining, "drive_reported_ew": drive_ew,
                  "statuses": [item["status"] for item in items]}
        self.keep_json("ew/write-result.json", result)
        self.keep_json("ew/device-settings-restored.json",
                       self.set_tape("ew-restored", {"capacity_poll_interval_seconds": None}, "LOGICAL"))
        published = selected_items(items, manifest, sources, self.state["media_id"])
        require(terminal["status"] == "JOB_STATUS_READY" and terminal.get("error")
                and published and len(published) < len(items),
                "EW write did not settle at a capacity stop with published members and a pending suffix")
        self.check_device_released()
        ew_items = {str(relative_path(item["file"]["source_path"][len(sources) + 1:])): item for item in published}
        everything = {**self.state["format_items"], **self.state["append_items"], **self.state["fill_items"], **ew_items}
        positions = self.export("ew/library.jsonl")
        self.keep_json("ew/anchor-coverage.json", self.check_positions(positions, everything))
        require(all(positions.get(path) == row for path, row in before.items()), "EW write changed earlier Positions")
        captured = self.capture_job(job)
        self.keep_json("ew/item-publication.json",
                       self.check_submitted_manifest(captured, ew_items, pending=len(items) - len(published)))
        self.save(ew_items=ew_items)
        self.restore_selection("ew-restore", sorted(ew_items), items=ew_items, timeout=budget)
        self.save(tar_ew_complete=True)
        require(drive_ew, "Capacity admission stopped before the drive reported EW: " + terminal["error"])

    def read_order(self):
        """One AUTO and one LOGICAL Restore of the same selection on the filled Media; report only."""
        require(self.state.get("tar_ew_complete") and not self.state.get("tar_read_order_started"),
                "tar-read-order requires a completed tar-ew, once")
        self.save(tar_read_order_started=True)
        # BOT-side fixture members plus every other EW-tail member; the 64 GiB fill members would
        # make both arms transfer-dominated.
        fixture = {**self.state["format_items"], **self.state["append_items"]}
        tail = sorted(self.state["ew_items"])[::2]
        items = {**fixture, **{name: self.state["ew_items"][name] for name in tail}}
        names = sorted(items)
        arms = {order: self.read_order_arm(order, names, prefix="full-restore-", items=items)
                for order in ("LOGICAL", "AUTO")}
        self.set_read_order("LOGICAL")
        self.keep_json("read-order-full-comparison.json", {
            "selection": names, "arms": arms,
            "scope": "Single run each on the filled Media; report only, no duration threshold."})
        self.save(tar_read_order_complete=True)


def fill_restore_sample(items):
    # The first, middle and last published members cover BOT-adjacent, mid-tape and near-EW reads.
    names = list(items)
    picked = {names[0], names[len(names) // 2], names[-1]} if names else set()
    return sorted(picked)[:FILL_RESTORE_SAMPLES]


def run_stage(test, state, save_state):
    stages = {"prepare": "prepare", "tar-clear-barcode": "clear_barcode", "baseline": "baseline",
              "restore": "restore", "tar-fill": "fill", "tar-ew": "ew", "tar-read-order": "read_order",
              "cleanup": "cleanup"}
    require(test.args.physical_format == "tar_pax_v1" and test.args.physical_stage in stages,
            "Unknown tar physical stage")
    for key, value in (("physical_device", test.args.physical_device), ("physical_barcode", test.args.physical_barcode),
                       ("physical_root", test.root)):
        require(key not in state or state[key] == value, "Physical stage identity differs from saved state")
        require(test.args.physical_stage == "prepare" or key in state, "Physical stage has no saved preparation identity")
        state[key] = value
    save_state()
    getattr(TarCases(test, state, save_state), stages[test.args.physical_stage])()
