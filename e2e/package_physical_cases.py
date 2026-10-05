"""Local physical Tape cases using only the installed CLI and ordinary host tools.

The lifecycle controller owns service restart, device reservation, script configuration
and final host cleanup. Partition roles come from the captured mkltfs log.
"""

import base64
import hashlib
import json
from pathlib import Path
import re

from package_acceptance import require, sha256
from package_ltfs_cases import TapeWorkflows
from package_physical_evidence import (check_positions, check_restore_database, check_signature,
                                       check_checkpoint, decode_bytes, format_partition_map, library_positions, parse_index, relative_path,
                                       seal_evidence, verify_evidence)


PNG = base64.b64decode("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mNk+A8AAQUBAScY42YAAAAASUVORK5CYII=")
BOUNDARY_CHUNK_BYTES = 4 * 1024**3


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


class PhysicalCases(TapeWorkflows):
    def __init__(self, test, state, save_state):
        super().__init__(test, str(state.get("source_location", "")), str(state.get("restored_location", "")))
        self.state, self.save_state = state, save_state
        self.barcode = test.args.physical_barcode
        self.evidence = Path(state.get("physical_evidence_dir", Path(test.args.out) / "physical-evidence"))

    def device(self, barcode):
        require(barcode == self.barcode, "Tape operation requested a different barcode.")
        return self.test.args.physical_device

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

    def inspect(self):
        reply = self.test.one("media", "inspect", "tape", "--device", self.device(self.barcode))
        require(reply["identity"] == self.barcode, "Device inspection returned a different barcode.")
        return reply

    def settled(self, job, status="JOB_STATUS_COMPLETED", timeout=7200):
        result = self.test.wait_job(job, status, timeout=timeout)
        require(result.get("phase", "JOB_PHASE_UNSPECIFIED") in ("JOB_PHASE_UNSPECIFIED", "JOB_PHASE_COMPLETED"),
                "Job has not released its Media attempt.")
        require(not result.get("error"), "Physical operation settled with an error.")
        return result

    def ejected(self):
        status = self.test.remote(["mt", "-f", self.device(self.barcode), "status"]).stdout.decode()
        require(re.search(r"\bDR_OPEN\b", status), "Physical operation completed before cartridge eject.")
        devices = self.test.one("tape", "device", "list").get("devices", [])
        require(self.device(self.barcode) in devices, "Device remains unavailable after settlement.")

    def archive_items(self, job):
        # Shared assertions handle the deliberately small baseline; no EOM-size assumption here.
        return self.items(job)

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
        offset, chunks = 0, []
        while True:
            page = self.test.one("job", "log", identifier, "--offset", str(offset))
            data = decode_bytes(page.get("logs"))
            if not data:
                break
            require(int(page["offset"]) > offset, "Job log cursor did not advance.")
            chunks.append(data)
            offset = int(page["offset"])
        self.keep("jobs/" + identifier + "/job.log", b"".join(chunks))
        listing = self.test.remote(["find", root, "-type", "f", "-print0"]).stdout
        for raw in listing.split(b"\x00"):
            if not raw:
                continue
            path = raw.decode()
            require(path.startswith(root + "/"), "Job artifact escaped its bundle.")
            relative = str(relative_path(path[len(root) + 1:]))
            if relative.startswith("tapes/") and (relative.endswith(".schema") or relative.rsplit("/", 1)[-1] in
                                                 ("ltfs.log", "yatm-report.json")):
                self.copy_evidence(path, "jobs/" + identifier + "/" + relative)
        return self.evidence / "jobs" / identifier

    def copy_evidence(self, source, relative):
        expected = self.test.remote(["sha256sum", "--", source]).stdout.decode().split()[0]
        path = self.keep(relative, self.test.remote(["cat", "--", source]).stdout)
        require(sha256(path) == expected, "Transferred evidence checksum mismatch; retain Jobs.")
        return path

    def index(self, job):
        root = self.capture_job(job)
        directory = root / "tapes" / self.barcode
        require((directory / "ltfs.log").is_file() and (directory / "yatm-report.json").is_file(),
                "Archive Tape artifacts are incomplete.")
        return parse_index((directory / (self.barcode + ".schema")).read_bytes())

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

    def baseline(self):
        require(not any(key in self.state for key in ("baseline_started", "format_job_id", "append_job_id", "media_id")),
                "Baseline has existing state; never format on a resumed baseline.")
        require(not self.inspect().get("media"), "Loaded Tape already exists in the isolated Library.")
        jobs = self.test.one("job", "list", "--limit", "1")
        require(not jobs.get("jobs") and not jobs.get("has_more"), "Baseline requires an empty isolated Job catalog.")
        self.save(baseline_started=True, physical_evidence_dir=str(self.evidence.resolve()))
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
        self.format(job, self.barcode)
        self.settled(job)
        self.ejected()
        media = self.media(self.barcode)
        encryption = media["profile"]["tape"].get("encryption")
        require(encryption, "Tape profile did not retain encryption.")
        self.save(media_id=str(media["id"]), encryption_sha256=hashlib.sha256(encryption.encode()).hexdigest())
        initial = self.archive_items(job)
        self.check_fixture(initial, "dataset")
        positions = self.export("library-format.jsonl")
        require(len(positions) == len(initial), "FORMAT published an unexpected Position set.")
        index = self.index(job)
        format_log = self.evidence / "jobs" / str(job["id"]) / "tapes" / self.barcode / "ltfs.log"
        mapping = format_partition_map(format_log.read_text())
        self.save(partition_map=mapping)
        check_positions(initial, positions, index, self.state["media_id"], mapping)
        assets = self.test.one("preview", "get", "--location-id", self.source, "--path", "dataset/nested/payload.png")
        require(assets.get("availability") == "PREVIEW_AVAILABILITY_READY" and assets.get("assets"), "PNG Preview is missing.")
        for asset in assets["assets"]:
            require(asset["url"].startswith("/files/preview?"), "Unexpected Preview URL.")
            data = self.test.remote(["curl", "--fail", "--silent", self.test.url + asset["url"]]).stdout
            require(data, "Preview HTTP asset is empty.")
            self.keep("preview-" + str(relative_path(asset["role"])), data)
        inspected = self.inspect()
        require(str(inspected["media"]["id"]) == self.state["media_id"]
                and int(inspected.get("file_count", 0)) == len(initial), "Reload changed durable Tape identity/count.")
        append = self.created("append_job_id", "archive", "create", "--location", self.source + ":append")
        self.settled(append, "JOB_STATUS_READY")
        self.check_prepared(append, "append")
        self.barcode_refusal(append)
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
        result = self.test.remote([self.test.install + "/yatm-cli", "--server", self.test.url,
                                   "--timeout", "160s", "archive", "write", "tape", "append", job["id"],
                                   "--device", self.device(self.barcode), "--barcode", wrong], expected=2)
        error = json.loads(result.stderr)
        message = error.get("error", "")
        require(error.get("code") == "safety" and "does not match the inspected identity" in message
                and f'requested="{wrong}" inspected="{self.barcode}"' in message,
                "Wrong-barcode CLI test did not observe its explicit identity refusal.")
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
            media["profile"]["tape"]["encryption"].encode()).hexdigest() == self.state["encryption_sha256"],
            "APPEND/restart changed the Tape identity or encryption profile.")

    def restore(self):
        require(self.state.get("baseline_complete"), "Restore requires a completed baseline and lifecycle restart.")
        self.restore_selection("restore_job_id", self.state["baseline_items"], "baseline", "restore_complete")

    def restore_selection(self, key, items, directory, completion):
        if self.state.get(completion):
            return
        self.check_media()
        if key not in self.state:
            inspected = self.inspect()
            require(str(inspected["media"]["id"]) == self.state["media_id"], "Restore inspected a different Tape.")
            args = ["restore", "create", "--target-location", self.restored, "--directory", directory]
            for item in items:
                args += ["--file-id", str(item["file"]["expected"]["file_id"])]
            job = self.created(key, *args)
            self.settled(job, "JOB_STATUS_READY")
            self.save(**{key + "_submitted": True})
            self.test.cli("restore", "run", "tape", job["id"], "--device", self.device(self.barcode))
        else:
            job = self.job(key)
            # Do not turn an interrupted or failed invocation into an implicit second Media attempt.
            require(self.state.get(key + "_submitted"), "Saved Restore was not submitted; explicit operator review is required.")
        self.settled(job, timeout=14400)
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

    def tape_script(self, name, directory, **environment):
        require(name in ("readinfo", "encrypt", "mount.openltfs", "umount"), "Unexpected physical Tape script.")
        values = {"DEVICE": self.device(self.barcode), "TAPE_BARCODE": self.barcode,
                  "TAPE_NAME": "Physical acceptance", "TAPE_DIR": directory, **environment}
        # readinfo invokes the shipped lto-info by relative path; run from the verified installation.
        return self.test.remote(["bash", "-c", 'cd "$1"; shift; exec "$@"', "physical-script", self.test.install,
                                 "env", *[key + "=" + value for key, value in values.items()],
                                 self.test.root + "/tape-scripts/" + name], timeout=900)

    def available(self, path):
        values = self.test.remote(["stat", "-f", "-c", "%a %S", "--", path]).stdout.split()
        require(len(values) == 2, "Filesystem available-space result is incomplete.")
        return int(values[0]) * int(values[1])

    def stream_fixture(self, path, size, stream, hash_content=True):
        require(size > 0 and size % (1024**2) == 0, "Fixture size must be a positive whole MiB.")
        require(path.startswith(self.test.root + "/"), "Fixture destination is outside the owned root.")
        relative_path(path[len(self.test.root) + 1:])
        # One sequential write and one inline digest; no source-sized temporary file or reread.
        seed = hashlib.sha256(b"YATM physical acceptance deterministic fixture").hexdigest()
        command = ('set -euo pipefail; test ! -e "$1"; '
                   'dd if=/dev/zero bs=1M count="$2" status=none | '
                   'openssl enc -aes-256-ctr -K "$3" -iv "$4" -nosalt')
        command += ' | tee -- "$1" | sha256sum' if hash_content else ' > "$1"'
        result = self.test.remote(["bash", "-c", command, "physical-fixture", path,
                                   str(size // 1024**2), seed, f"{stream:032x}"], timeout=43200)
        actual_size = int(self.test.remote(["stat", "-c", "%s", "--", path]).stdout)
        require(actual_size == size, "Fixture stream did not write its complete bounded content.")
        manifest = {"size": size, "stream": stream}
        if hash_content:
            digest = result.stdout.decode().split()[0]
            require(re.fullmatch(r"[0-9a-f]{64}", digest), "Fixture stream did not complete its SHA-256.")
            manifest["sha256"] = digest
        return manifest

    def prefill(self, chunk):
        require(not self.state.get("prefill_started"), "Prefill already started; retain partial results for diagnosis.")
        self.check_media()
        inspected = self.inspect()
        require(str(inspected["media"]["id"]) == self.state["media_id"], "Prefill inspected a different Tape.")
        encryption = inspected["media"]["profile"]["tape"]["encryption"]
        require(re.fullmatch(r"v1:[0-9a-f]{64}", encryption), "Unsupported scratch Tape key encoding.")
        directory, mount = self.test.root + "/prefill", self.test.root + "/prefill-mount"
        key = directory + "/scratch.key"
        self.save(prefill_started=True, boundary_chunk_bytes=chunk)
        self.test.stop_owned()
        self.test.remote(["mkdir", "--", directory, mount])
        info = directory + "/identity.json"
        self.tape_script("readinfo", directory, OUT=info)
        require(json.loads(self.test.remote(["cat", "--", info]).stdout)["barcode"] == self.barcode,
                "Cartridge changed before stopped-service prefill.")
        # Do not put the encryption key in arguments, stdout, state or error messages.
        self.test.remote(["bash", "-c", 'umask 077; set -C; cat > "$1"', "physical-key", key],
                         body=encryption[3:].encode())
        try:
            self.tape_script("encrypt", directory, KEY_FILE=key)
        finally:
            self.test.remote(["rm", "--", key])
        self.tape_script("mount.openltfs", directory, MOUNT_POINT=mount)
        mounted = self.test.remote(["findmnt", "--json", "--mountpoint", mount, "--output", "TARGET,FSTYPE"]).stdout
        mounts = json.loads(mounted).get("filesystems", [])
        require(len(mounts) == 1 and mounts[0]["target"] == mount and mounts[0]["fstype"].startswith("fuse"),
                "Prefill destination is not the owned LTFS mount.")
        available = self.available(mount)
        fill_size = ((available - 2 * chunk) // 1024**2) * 1024**2
        require(fill_size > 0, "Tape lacks the planned prefill headroom; no filler was written.")
        filler = "physical-acceptance-filler.bin"
        manifest = self.stream_fixture(mount + "/" + filler, fill_size, 1000000, hash_content=False)
        remaining = self.available(mount)
        require(chunk < remaining < 3 * chunk, "Post-prefill free space cannot support the bounded boundary fixture.")
        self.save(prefill_manifest={"path": filler, **manifest}, prefill_available_bytes=remaining)
        self.tape_script("umount", directory, MOUNT_POINT=mount)
        index_path = self.copy_evidence(directory + "/" + self.barcode + ".schema", "prefill/" + self.barcode + ".schema")
        self.copy_evidence(directory + "/ltfs.log", "prefill/ltfs.log")
        index = parse_index(index_path.read_bytes())
        require(filler in index and index[filler]["size"] == fill_size, "Prefill final Index did not retain the complete filler.")
        from package_physical_evidence import extent_order
        extent_order(index[filler]["extents"], fill_size)
        require(all(e["partition"] == self.state["partition_map"]["data"] for e in index[filler]["extents"]),
                "Prefill did not consume the data partition.")
        status = self.test.remote(["mt", "-f", self.device(self.barcode), "status"]).stdout.decode()
        require(re.search(r"\bDR_OPEN\b", status), "Prefill did not eject normally.")
        self.keep_json("prefill/manifest.json", self.state["prefill_manifest"])
        self.save(prefill_complete=True)
        self.test.start_owned()

    def full_write(self):
        require(self.state.get("restore_complete"), "Full boundary requires baseline Restore acceptance.")
        if "full_archive_job_id" in self.state:
            self.full_checkpoint()
            return
        chunk = BOUNDARY_CHUNK_BYTES
        require(self.available(self.test.root) > 5 * chunk + 1024**3,
                "Owned source/Restore filesystem lacks space for three sources and two restored chunks.")
        if not self.state.get("prefill_complete"):
            self.prefill(chunk)
        require(self.state["boundary_chunk_bytes"] == chunk, "Boundary chunk size changed after prefill.")
        require(not self.state.get("boundary_fixture_started"), "Boundary source generation was interrupted; retain it for diagnosis.")
        self.save(boundary_fixture_started=True)
        directory = self.test.root + "/fixtures/source/eom"
        self.test.remote(["mkdir", "--", directory])
        manifest = {}
        for number in range(3):
            name = f"chunk-{number:03d}.bin"
            manifest[name] = self.stream_fixture(directory + "/" + name, chunk, number)
        self.keep_json("boundary-source-manifest.json", manifest)
        self.save(boundary_manifest=manifest)
        job = self.created("full_archive_job_id", "archive", "create", "--location", self.source + ":eom")
        self.settled(job, "JOB_STATUS_READY")
        self.save(full_write_submitted=True)
        self.test.cli("archive", "write", "tape", "append", job["id"], "--device", self.device(self.barcode),
                      "--barcode", self.barcode)
        self.test.wait_job(job, "JOB_STATUS_READY", timeout=28800)
        self.ejected()
        self.full_checkpoint()

    def full_checkpoint(self):
        job = self.job("full_archive_job_id")
        current = self.test.one("job", "get", job["id"])["job"]
        items = self.archive_items(job)
        manifest = self.state["boundary_manifest"]
        require(len(items) == len(manifest) == 3, "Full Archive changed the boundary fixture selection.")
        names = [relative_path(item["file"]["target_path"]).name for item in items]
        require(names == sorted(manifest), "Full Archive changed the deterministic boundary file order.")
        for item in items:
            name = relative_path(item["file"]["target_path"]).name
            require(name in manifest and int(item["size_bytes"]) == manifest[name]["size"], "Boundary source size differs.")
            if item["status"] == "COPY_STATUS_SUBMITTED":
                require(decode_bytes(item["file"]["expected"]["sha256"]).hex() == manifest[name]["sha256"],
                        "Submitted boundary hash differs from generation manifest.")
        root = self.capture_job(job)
        report = json.loads((root / "tapes" / self.barcode / "yatm-report.json").read_bytes())
        progress = self.test.one("job", "progress", job["id"])["progress"]
        prefix = check_checkpoint((root / "job.log").read_bytes(), report, current, items, self.state["media_id"], progress)
        positions = self.export("library-full.jsonl")
        index = parse_index((root / "tapes" / self.barcode / (self.barcode + ".schema")).read_bytes())
        check_positions(prefix, positions, index, self.state["media_id"], self.state["partition_map"])
        baseline = self.state["baseline_items"]
        expected_paths = {i["file"]["media_path"] for i in baseline + prefix}
        require(set(positions) == expected_paths, "Filler or unfinalized suffix leaked into Library Positions.")
        filler = self.state["prefill_manifest"]
        require(filler["path"] in index and index[filler["path"]]["size"] == filler["size"],
                "Final Index lost or truncated the physical prefill file.")
        # The later Index must retain both earlier Archives as well as this attempt's prefix.
        for key in ("format_job_id", "append_job_id"):
            earlier = self.archive_items(self.job(key))
            check_positions(earlier, positions, index, self.state["media_id"], self.state["partition_map"])
        self.check_media()
        self.save(full_write_complete=True, full_submitted_items=prefix)
        return prefix

    def full_verify(self):
        if self.state.get("full_verify_complete"):
            return
        prefix = self.full_checkpoint()
        selected = [prefix[0]] if len(prefix) == 1 else [prefix[0], prefix[-1]]
        self.restore_selection("boundary_restore_job_id", selected, "boundary", "full_verify_complete")

    def cleanup(self):
        require(self.state.get("full_verify_complete"), "Cleanup requires completed physical full-boundary acceptance.")
        # Capture the entire isolated catalog, including companion Jobs, before any deletion.
        if not self.state.get("evidence_complete"):
            jobs, offset = [], 0
            while True:
                page = self.test.one("job", "list", "--limit", "100", "--offset", str(offset))
                batch = page.get("jobs", [])
                jobs.extend(batch)
                if not page.get("has_more"):
                    break
                require(batch, "Job listing did not advance.")
                offset += len(batch)
            require(jobs, "No physical Jobs remain to preserve.")
            self.export("library-final.jsonl")
            for job in jobs:
                self.capture_job(job)
            digest = seal_evidence(self.evidence)
            self.save(evidence_complete=True, evidence_sha256=digest, cleanup_job_ids=[str(j["id"]) for j in jobs])
        verify_evidence(self.evidence)
        require(sha256(self.evidence / "SHA256SUMS") == self.state["evidence_sha256"],
                "Saved evidence manifest changed; retain all Jobs.")
        for identifier in self.state["cleanup_job_ids"]:
            if identifier in self.state.get("deleted_job_ids", []):
                continue
            self.test.cli("job", "delete", identifier)
            self.test.remote(["test", "!", "-e", self.test.install + "/work/jobs/" + identifier])
            self.save(deleted_job_ids=self.state.get("deleted_job_ids", []) + [identifier])
        self.ejected()
        self.save(cleanup_complete=True)


def run_stage(test, state, save_state):
    stage = test.args.physical_stage
    require(stage in ("baseline", "restore", "full-write", "full-verify", "cleanup"), "Unknown physical stage.")
    device, barcode = test.args.physical_device, test.args.physical_barcode
    require(isinstance(device, str) and device.startswith("/dev/") and ".." not in Path(device).parts,
            "Choose the explicit physical Tape device.")
    require(re.fullmatch(r"[A-Z0-9]{6}", barcode), "Expected a six-character physical Tape barcode.")
    for key, value in (("physical_device", device), ("physical_barcode", barcode), ("physical_root", test.root)):
        require(key not in state or state[key] == value, "Physical stage identity differs from saved state.")
        require(stage == "baseline" or key in state, "Physical stage has no saved baseline identity.")
        state[key] = value
    save_state()
    getattr(PhysicalCases(test, state, save_state), stage.replace("-", "_"))()
