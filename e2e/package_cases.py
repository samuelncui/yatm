"""Observable package workflows; every business action uses the shipped CLI."""

import base64
import hashlib
import io
import json
from pathlib import PurePosixPath
import tarfile

from package_acceptance import documents, require


def fixture_archives(directory):
    names = (" leading.txt", "trailing .txt", "back\\slash.txt", "line\nbreak.txt", "tab\tname.txt",
             "日本語.txt", ".hidden", "changed.txt", "pre-epoch.txt", "epoch.txt", "same-ms-a.txt", "same-ms-b.txt")
    files = {name: {"data": b"abc", "mtime_ns": 1_720_000_000_000_000_001} for name in names}
    files["pre-epoch.txt"]["mtime_ns"] = -123
    files["epoch.txt"]["mtime_ns"] = 0
    files["same-ms-b.txt"]["mtime_ns"] += 1
    regular = directory / "regular-fixtures.tar"
    with tarfile.open(regular, "w", format=tarfile.PAX_FORMAT) as archive:
        entries = {"source/" + name: value for name, value in files.items()}
        entries["second/second.txt"] = {"data": b"second", "mtime_ns": 1_720_000_000_000_000_002}
        for path, value in entries.items():
            member = tarfile.TarInfo(path)
            member.mode, member.size = 0o640, len(value["data"])
            ns = value["mtime_ns"]
            magnitude = abs(ns)
            member.pax_headers = {"mtime": ("-" if ns < 0 else "") + f"{magnitude // 10**9}.{magnitude % 10**9:09d}"}
            archive.addfile(member, io.BytesIO(value["data"]))
    # GNU headers preserve an invalid filename byte without requiring the local filesystem to create it.
    invalid = directory / "invalid-fixtures.tar"
    with tarfile.open(invalid, "w", format=tarfile.GNU_FORMAT, encoding="utf-8", errors="surrogateescape") as archive:
        for name in ("a-first.txt", "invalid-\udcff.txt", "z-after.txt"):
            member = tarfile.TarInfo(name)
            member.mode, member.size = 0o640, 3
            archive.addfile(member, io.BytesIO(b"abc"))
    return files, regular, invalid


def file_id(row):
    return row.get("associated_file_id") or row.get("reference", {}).get("file_id")


def check_no_recreation(test, kind, job):
    before = test.one("job", "list", "--limit", "100")
    original = test.one("job", "get", str(job["id"]))["job"]
    creation = test.one(kind, "creation", str(job["id"]))
    require(creation.get("request") and not creation.get("unavailable_reason"), "Creation inputs were not recoverable.")
    after = test.one("job", "list", "--limit", "100")
    require([row["id"] for row in before.get("jobs", [])] == [row["id"] for row in after.get("jobs", [])],
            "Reading creation inputs created another Job.")
    current = test.one("job", "get", str(job["id"]))["job"]
    for field in ("id", "kind", "status", "created_at_ns", "updated_at_ns"):
        require(current.get(field) == original.get(field), "Reading creation inputs changed the original Job.")
    return creation["request"]


def operation(test, *args):
    updates = test.cli(*args)
    summaries = [row["summary"] for row in updates if row.get("summary", {}).get("completed")]
    require(len(summaries) == 1, "File operation omitted its final summary.")
    summary = summaries[0]
    require(not any(int(summary.get(key, 0)) for key in ("failed_count", "publication_pending_count")),
            "File operation reported incomplete work.")
    return summary, [row["entry"] for row in updates if "entry" in row]


class Workflows:
    def __init__(self, test):
        self.test = test
        self.files, regular, invalid = fixture_archives(test.args.out)
        test.upload([regular, invalid], test.root)
        test.remote(["tar", "--no-same-owner", "-xf", test.root + "/" + regular.name, "-C", test.root + "/fixtures"])
        test.remote(["mkdir", test.root + "/fixtures/invalid", test.root + "/fixtures/restored", test.root + "/volumes/disk"])
        test.remote(["tar", "--no-same-owner", "-xf", test.root + "/" + invalid.name, "-C", test.root + "/fixtures/invalid"])
        self.locations = {}
        for name in ("source", "second", "invalid", "restored"):
            args = ["location", "create", "--name", "Acceptance " + name, "--root", test.root + "/fixtures/" + name]
            if name == "restored":
                args += ["--restore-target"]
            if name == "second":
                args += ["--use-mmap"]
            self.locations[name] = str(test.one(*args)["location"]["id"])

    def listing(self):
        test, location = self.test, self.locations["source"]
        pages = test.cli("ls", "--location-id", location, "--long", "--include", "operations")
        rows = [row for page in pages for row in page.get("entries", [])]
        require(len(rows) == len(self.files) and {row["name"] for row in rows} == set(self.files),
                "Literal UTF-8 names were changed, duplicated or omitted.")
        require(any(int(page.get("total_entry_count", -1)) == len(self.files) for page in pages), "Directory total is incomplete.")
        for row in rows:
            require(not row.get("error") and row.get("reference"), "A legal filename became unavailable.")
            require(row["mtime_ns"] == str(self.files[row["name"]]["mtime_ns"]), "Nanoseconds lost precision or sign.")
            require(not file_id(row), "Reading a directory admitted a File.")

    def invalid_listing(self):
        test, location = self.test, self.locations["invalid"]
        pages = test.cli("ls", "--location-id", location, "--long", expected=1)
        rows = [row for page in pages for row in page.get("entries", [])]
        require(len(rows) == 3 and any(int(page.get("total_entry_count", -1)) == 3 for page in pages),
                "Incomplete directory reads did not drain/count every row.")
        ordinary = {row["name"] for row in rows if not row.get("error")}
        require(ordinary == {"a-first.txt", "z-after.txt"}, "An invalid child suppressed a later valid sibling.")
        failed = [row for row in rows if row.get("error")]
        require(len(failed) == 1 and "\\xff" in failed[0]["name"].lower(), "Invalid bytes are not explicitly escaped.")
        require(not any(field in failed[0] for field in ("reference", "size_bytes", "mtime_ns")),
                "An unavailable row acquired executable identity or invented attributes.")
        test.cli("ls", "--location-id", location, "--query", "type:file", expected=1)
        test.cli("archive", "estimate", "--location", location + ":", expected=1)

    def preparation_barrier(self):
        test = self.test
        job = test.one("scan", "create", "--location", self.locations["source"] + ":",
                       "--location", self.locations["invalid"] + ":", "--signature", "force-read", "--result", "originals")["job"]
        test.wait_job(job, "JOB_STATUS_FAILED")
        rows = test.rows("ls", "--location-id", self.locations["source"], "--long", "--include", "operations")
        require(len(rows) == len(self.files) and all(not file_id(row) for row in rows),
                "Failed all-source preparation changed the source listing or partially admitted originals.")
        check_no_recreation(test, "scan", job)

    def scan(self):
        test = self.test
        args = ["scan", "create", "--location", self.locations["source"] + ":",
                "--location", self.locations["source"] + ":back\\slash.txt",
                "--location", self.locations["second"] + ":", "--priority", "2",
                "--signature", "force-read", "--result", "originals"]
        job = test.one(*args)["job"]
        test.wait_job(job)
        entries = test.one("scan", "results", str(job["id"]), "--limit", "100").get("entries", [])
        expected = {(self.locations["source"], name) for name in self.files} | {(self.locations["second"], "second.txt")}
        require(len(entries) == len(expected) and {(row["location_id"], row["path"]) for row in entries} == expected,
                "Scan lost a Location or duplicated an overlapping selection.")
        for row in entries:
            data = self.files[row["path"]]["data"] if row["location_id"] == self.locations["source"] else b"second"
            require(base64.b64decode(row["sha256"]) == hashlib.sha256(data).digest(), "Scan did not read the expected bytes.")
            mtime = self.files[row["path"]]["mtime_ns"] if row["location_id"] == self.locations["source"] else 1_720_000_000_000_000_002
            require(row.get("mtime_ns", "0") == str(mtime), "Scan did not retain the observed nanoseconds.")
        request = check_no_recreation(test, "scan", job)
        require(request["priority"] == "2" and len(request["spec"]["selections"]) == 3,
                "Recreate did not retain Scan options and explicit selections.")
        again = test.one(*args)["job"]
        require(again["id"] != job["id"], "Explicit submission reused the old Job.")
        test.wait_job(again)

    def search(self):
        test, location = self.test, self.locations["source"]
        detail = test.one("files", "get", "--location-id", location, "--path", "changed.txt")
        changed_id = file_id(detail["entry"])
        data = b"changed live contents"
        test.write(test.root + "/fixtures/source/changed.txt", data)
        self.files["changed.txt"]["data"] = data
        query = "name:changed.txt AND size:3"
        logical = test.rows("ls", "--file-id", "0", "--scope", "all", "--recursive", "--query", query, "--long")
        require([file_id(row) for row in logical] == [changed_id], "Library Search used live facts instead of recorded size.")
        require(not test.rows("ls", "--location-id", location, "--query", query), "Location Search used stale recorded size.")
        live = test.rows("ls", "--location-id", location, "--query", "name:changed.txt AND size:" + str(len(data)), "--long")
        require(len(live) == 1, "Location Search did not read current size.")
        self.files["changed.txt"]["mtime_ns"] = int(live[0]["mtime_ns"])
        test.remote(["mv", "--", test.root + "/fixtures/source/changed.txt", test.root + "/fixtures/temporarily-absent"])
        try:
            require(len(test.rows("ls", "--file-id", "0", "--scope", "all", "--recursive", "--query", query)) == 1,
                    "A missing original removed a recorded Library match.")
        finally:
            test.remote(["mv", "--", test.root + "/fixtures/temporarily-absent", test.root + "/fixtures/source/changed.txt"])
        ids, cursor = [], None
        while True:
            args = ["ls", "--file-id", "0", "--scope", "all", "--recursive", "--limit", "1",
                    "--query", "type:file AND NOT (size:99 OR size:100)"]
            if cursor:
                args += ["--cursor", cursor]
            page = test.one(*args)
            ids += [file_id(row) for row in page.get("entries", [])]
            cursor = page.get("next_cursor")
            if not cursor:
                break
            require(len(ids) <= len(self.files) + 1, "Search cursor failed to advance.")
        require(len(ids) == len(self.files) + 1 and len(set(ids)) == len(ids), "Paged grouped NOT changed the result set.")

    def file_operations(self):
        test, location = self.test, self.locations["source"]
        source = "back\\slash.txt"
        summary, proposed = operation(test, "rm", "--location", location, "--source", source, "--dryrun")
        require(summary.get("dryrun") and len(proposed) == 1, "Dry run did not return its complete proposal.")
        require(test.remote(["cat", "--", test.root + "/fixtures/source/" + source]).stdout == b"abc", "Dry run changed bytes.")
        _, removed = operation(test, "rm", "--location", location, "--source", source)
        require(len(removed) == 1 and removed[0]["target_path"].startswith(".trash/"), "Delete did not move into Trash.")
        require(test.remote(["cat", "--", test.root + "/fixtures/source/" + removed[0]["target_path"]]).stdout == b"abc",
                "Delete lost the original content.")
        operation(test, "mv", "--location", location, "--source", removed[0]["target_path"], "--destination", ".", "--name", source)
        require(test.remote(["cat", "--", test.root + "/fixtures/source/" + source]).stdout == self.files[source]["data"],
                "Move from Trash did not recover the literal path and content.")

    def archive_restore(self):
        test, location = self.test, self.locations["source"]
        volume = test.one("volume", "initialize", test.root + "/volumes/disk", "--name", "Acceptance Volume", "--type", "hdd")["media"]
        job = test.one("archive", "create", "--location", location + ":", "--priority", "3")["job"]
        test.wait_job(job, "JOB_STATUS_READY")
        creation = check_no_recreation(test, "archive", job)
        require(creation.get("priority") == "3", "Archive Recreate lost the explicit priority.")
        test.cli("archive", "write", "volume", str(job["id"]), "--uuid", volume["identity"])
        test.wait_job(job)
        versions = {}
        for name, source in self.files.items():
            detail = test.one("files", "get", "--location-id", location, "--path", name)
            identity = file_id(detail["entry"])
            version = test.one("files", "versions", str(identity))["versions"][0]
            require(isinstance(version.get("last_archived_at_ns"), str), "Archive time is not a lossless decimal string.")
            mode = detail["entry"]["reference"]["location"]["facts"]["mode"]
            require(version["file_id"] == identity and version.get("mtime_ns", "0") == str(source["mtime_ns"])
                    and version.get("mode") == mode
                    and base64.b64decode(version["sha256"]) == hashlib.sha256(source["data"]).digest(),
                    "Archive did not retain the selected File's content and restore metadata.")
            versions[version["id"]] = {**source, "mode": mode, "path": detail["organization"]["path"]}
        # Explicit epoch and pre-epoch cutoffs must not be confused with an absent cutoff.
        for before in ("1969-12-31T23:59:59.999999999Z", "1970-01-01T00:00:00Z"):
            estimate = test.one("restore", "estimate", "--file-id", str(identity), "--before", before)
            require(int(estimate.get("unmatched_version_count", 0)) == 1 and not int(estimate.get("file_count", 0)),
                    "Signed/zero Restore cutoff was dropped or lost precision.")
        args = ["restore", "create", "--target-location", self.locations["restored"], "--directory", "results"]
        args += list(versions)
        restore = test.one(*args)["job"]
        test.wait_job(restore, "JOB_STATUS_READY")
        check_no_recreation(test, "restore", restore)
        manifest = test.one("restore", "files", str(restore["id"]), "--media-id", str(volume["id"]), "--limit", "100")
        require(not manifest.get("has_more") and len(manifest.get("items", [])) == len(versions)
                and {item["file"]["file_version_id"] for item in manifest["items"]} == set(versions),
                "Restore manifest lost or duplicated a selected version.")
        test.cli("restore", "run", "volume", str(restore["id"]), "--uuid", volume["identity"])
        test.wait_job(restore)
        for item in manifest["items"]:
            expected = versions[item["file"]["file_version_id"]]
            path = PurePosixPath(item["file"]["target_path"])
            require(not path.is_absolute() and ".." not in path.parts and item["file"]["target_path"] == expected["path"],
                    "Restore manifest changed the selected logical path or escaped its Location.")
            actual = test.remote(["cat", "--", test.root + "/fixtures/restored/results/" + str(path)]).stdout
            require(actual == expected["data"], "Restored bytes differ from the selected version.")
            restored = test.one("files", "get", "--location-id", self.locations["restored"], "--path", "results/" + str(path))["entry"]
            require(restored["mtime_ns"] == str(expected["mtime_ns"])
                    and restored["reference"]["location"]["facts"]["mode"] == expected["mode"],
                    "Restore did not apply the selected version's nanoseconds and permissions.")
        scan = test.one("scan", "media", str(volume["id"]))["job"]
        test.wait_job(scan)
        self.volume = volume

    def verify(self):
        from package_media_cases import verify_volume
        self.verified_copies, self.position_count = verify_volume(self.test, self.volume, self.locations["source"])

    def inventory(self):
        from package_media_cases import volume_inventory
        volume_inventory(self.test, self.volume, self.verified_copies, self.position_count)

    def preview(self):
        from package_media_cases import preview_assets
        preview_assets(self.test, self.locations["source"])

    def backup(self):
        test = self.test
        location = self.locations["source"]
        original = test.one("files", "get", "--location-id", location, "--path", "changed.txt")
        snapshot = test.root + "/snapshot.jsonl"
        test.cli("library", "export", "--output", snapshot)
        data = test.remote(["cat", snapshot]).stdout
        records = documents(data)
        require(records, "Metadata export was empty.")
        def check(value):
            if isinstance(value, dict):
                for key, item in value.items():
                    if key.endswith("_ns") and item is not None:
                        require(isinstance(item, str) and -(2**63) <= int(item) < 2**63, "JSONL timestamp is not signed lossless ns.")
                    check(item)
            elif isinstance(value, list):
                for item in value:
                    check(item)
        check(records)
        updated = test.one("files", "metadata", "--location", location + ":changed.txt", "--note", "changed after export")["entries"]
        require(len(updated) == 1 and file_id(updated[0]["entry"]) == file_id(original["entry"])
                and updated[0]["organization"]["note"] == "changed after export", "Metadata edit did not change the exported File.")
        test.cli("library", "import", "--input", snapshot)
        detail = test.one("files", "get", "--location-id", location, "--path", "changed.txt")
        require(file_id(detail["entry"]) == file_id(original["entry"]) and detail["organization"] == original["organization"],
                "Import did not restore the exported File and its metadata.")

        # Reject incompatible timestamp shapes after valid records without replacing any data.
        before = test.cli("library", "export", "--output", "-")
        files = [index for index, record in enumerate(records) if record.get("type") == "file"]
        require(len(files) >= 2, "The import rejection fixture needs two existing Files.")
        for shape in ("old-key", "number"):
            broken = json.loads(json.dumps(records))
            broken[files[0]]["data"]["note"] = "Must not survive rejected import"
            later = broken[files[-1]]["data"]
            require(isinstance(later.get("created_at_ns"), str), "Export omitted a required File instant.")
            if shape == "old-key":
                later["created_at_ms"] = later.pop("created_at_ns")
            else:
                later["created_at_ns"] = int(later["created_at_ns"])
            rejected = test.root + "/incompatible-snapshot.jsonl"
            test.write(rejected, b"".join(json.dumps(record).encode() + b"\n" for record in broken))
            test.cli("library", "import", "--input", rejected, expected=1)
            require(test.cli("library", "export", "--output", "-") == before,
                    "Rejected timestamp shape changed existing Library metadata.")
        test.report["snapshot_sha256"] = hashlib.sha256(data).hexdigest()
        test.save()


def run_cases(test):
    workflows = Workflows(test)
    for name, method in (("literal-filenames-and-nanoseconds", workflows.listing),
                         ("invalid-byte-row-and-cli-failure", workflows.invalid_listing),
                         ("scan-all-source-preparation-barrier", workflows.preparation_barrier),
                         ("scan-overlap-multiple-locations-and-recreate", workflows.scan),
                         ("recorded-library-and-live-location-search", workflows.search),
                         ("dryrun-delete-and-recover-literal-path", workflows.file_operations),
                         ("volume-archive-restore-and-media-scan", workflows.archive_restore),
                         ("volume-copy-damage-missing-and-health", workflows.verify),
                         ("volume-inventory-import-delete-and-register", workflows.inventory),
                         ("lossless-jsonl-export-import", workflows.backup)):
        test.case(name, method)
    if test.args.preview_archive:
        test.case("native-preview-assets-and-generation-policies", workflows.preview)
    else:
        test.report["limits"].append("Native Preview integration was not run: no Preview archive was selected.")
        test.save()
    if test.args.ltfs:
        from package_ltfs_cases import run_ltfs_cases
        run_ltfs_cases(test, workflows.locations["source"], workflows.locations["restored"])
    else:
        test.report["limits"].append("Official LTFS file-backend acceptance was not run: --ltfs was not selected.")
        test.save()
    from package_install_cases import sqlite_wal_restart
    test.case("sqlite-wal-enable-persist-and-disable", lambda: sqlite_wal_restart(test, workflows.locations["source"]))
