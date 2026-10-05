"""Official LTFS acceptance with disposable file-backend cartridges, never device nodes."""

import base64
import json
from pathlib import PurePosixPath
import shlex

from package_acceptance import require


def prepare_virtual_tapes(test, config):
    directory = test.root + "/virtual-tapes"
    devices = [directory + "/" + barcode for barcode in ("APP001", "FUL001", "FUL002")]
    test.remote(["mkdir", directory, *devices])
    # The official file backend accepts this cartridge fixture before its first format.
    cartridge = b'''<?xml version="1.0" encoding="UTF-8"?>
<filedebug_cartridge_config>
  <dummy_io>false</dummy_io><emulate_readonly>false</emulate_readonly>
  <capacity_mb>3072</capacity_mb><cart_type>L5</cart_type><density_code>58</density_code>
  <delay_mode>None</delay_mode><wraps>40</wraps><eot_to_bot_sec>12</eot_to_bot_sec>
  <change_direction_us>2000000</change_direction_us><change_track_us>10000</change_track_us>
  <threading_sec>0</threading_sec>
</filedebug_cartridge_config>
'''
    for device in devices:
        test.write(device + "/filedebug_tc_conf.xml", cartridge)
    adapters = test.install + "/templates/testing/ltfs-file-backend/"
    config["tape_devices"] = devices
    config["scripts"] = {"encrypt": adapters + "encrypt", "mkfs": adapters + "mkfs", "mount": adapters + "mount",
                         "umount": adapters + "umount", "read_info": adapters + "readinfo"}
    test.report["virtual_tape_devices"] = devices
    test.save()


class TapeWorkflows:
    def __init__(self, test, source, restored):
        self.test, self.source, self.restored = test, source, restored

    def device(self, barcode):
        path = self.test.root + "/virtual-tapes/" + barcode
        require(path in self.test.report["virtual_tape_devices"], "Tape selection is not this attempt's virtual cartridge.")
        return path

    def archive(self, relative):
        job = self.test.one("archive", "create", "--location", self.source + ":" + relative)["job"]
        self.test.wait_job(job, "JOB_STATUS_READY", timeout=600)
        return job

    def format(self, job, barcode):
        self.test.cli("archive", "write", "tape", "format", job["id"], "--device", self.device(barcode),
                      "--barcode", barcode, "--name", "Acceptance " + barcode, "--confirm-format", barcode)

    def items(self, job):
        page = self.test.one("archive", "files", job["id"], "--limit", "100")
        require(not page.get("has_more"), "Small Tape fixture exceeded one manifest page.")
        return page.get("items", [])

    def media(self, barcode):
        page = self.test.one("media", "list", "--kind", "tape", "--query", barcode)
        rows = page.get("media", [])
        require(not page.get("has_more") and len(rows) == 1 and rows[0]["identity"] == barcode,
                "Virtual cartridge was not published with its exact barcode.")
        require(rows[0]["profile"]["tape"]["format"] == "ltfs_v1", "Virtual cartridge has no appendable LTFS profile.")
        return rows[0]

    def positions(self, barcode, items):
        test, media = self.test, self.media(barcode)
        inspected = test.one("media", "inspect", "tape", "--device", self.device(barcode))
        require(inspected["identity"] == barcode and inspected["media"]["id"] == media["id"]
                and int(inspected.get("file_count", 0)) == len(items),
                "Tape inventory does not contain exactly its finalized files.")
        positions = {}
        for item in items:
            expected = item["file"]["expected"]
            signature = base64.b64decode(expected["signature"], validate=True).hex()
            page = test.one("files", "copies", "--signature", signature)
            require(not page.get("has_more"), "Small Tape fixture exceeded one copy page.")
            copies = [position for position in page.get("positions", []) if position["media_id"] == media["id"]]
            require(len(copies) == 1, "A finalized file has no unique Position on its Tape.")
            position = copies[0]
            require(position["path"] == item["file"]["media_path"] and position["size_bytes"] == item["size_bytes"]
                    and position["sha256"] == expected["sha256"], "Tape Position differs from its finalized file.")
            positions[position["path"]] = position
        require(len(positions) == len(items), "Finalized Tape files share an unexpected physical path.")
        return positions

    def restore(self, items, barcodes, directory, expected_hashes):
        test = self.test
        args = ["restore", "create", "--target-location", self.restored, "--directory", directory]
        for item in items:
            args += ["--file-id", item["file"]["expected"]["file_id"]]
        job = test.one(*args)["job"]
        test.wait_job(job, "JOB_STATUS_READY")
        manifests = []
        for index, barcode in enumerate(barcodes):
            media = self.media(barcode)
            test.cli("restore", "run", "tape", job["id"], "--device", self.device(barcode))
            status = "JOB_STATUS_COMPLETED" if index == len(barcodes) - 1 else "JOB_STATUS_READY"
            settled = test.wait_job(job, status, timeout=900)
            require(not settled.get("error"), "Tape Restore returned to Media selection with an error.")
            page = test.one("restore", "files", job["id"], "--media-id", media["id"], "--limit", "100")
            require(not page.get("has_more"), "Small Tape Restore fixture exceeded one manifest page.")
            restored_items = page.get("items", [])
            expected = {item["file"]["expected"]["file_id"]: item for item in items if item["media_id"] == media["id"]}
            require(len(restored_items) == len(expected)
                    and {item["file"]["file_id"] for item in restored_items} == set(expected),
                    "Tape Restore changed its selected File set.")
            for item in restored_items:
                archived = expected[item["file"]["file_id"]]
                require(item["status"] == "COPY_STATUS_COMPLETED" and not item.get("damaged")
                        and item["candidate"]["media_id"] == media["id"]
                        and item["candidate"]["media_path"] == archived["file"]["media_path"]
                        and item["actual_sha256"] == archived["file"]["expected"]["sha256"]
                        and item["actual_size_bytes"] == archived["size_bytes"],
                        "Tape Restore did not verify the selected physical copy.")
            manifests.extend(restored_items)
        require(len(manifests) == len(items)
                and len({item["file"]["file_id"] for item in manifests}) == len(items),
                "Cross-Media Restore duplicated or lost a selected File.")
        summary = test.one("job", "progress", job["id"])["summary"]
        require(int(summary.get("verified_files", 0)) == len(items)
                and not int(summary.get("damaged_files", 0)) and not int(summary.get("pending_files", 0)),
                "Cross-Media Restore did not verify its entire selection.")
        for item in manifests:
            path = PurePosixPath(item["file"]["target_path"])
            require(not path.is_absolute() and ".." not in path.parts and path.name in expected_hashes,
                    "Unexpected Tape Restore destination.")
            restored = test.root + "/fixtures/restored/" + directory + "/" + str(path)
            actual = test.remote(["sha256sum", "--", restored], timeout=180).stdout.decode().split()[0]
            require(actual == expected_hashes[path.name], "Restored Tape bytes differ from their source.")

    def verify(self, barcode, items):
        test, media = self.test, self.media(barcode)
        job = test.one("verify", "create", media["id"])["job"]
        test.wait_job(job, "JOB_STATUS_READY")
        test.cli("verify", "run", job["id"], "--device", self.device(barcode))
        test.wait_job(job, timeout=900)
        progress = test.one("job", "progress", job["id"])
        require(int(progress.get("matched_count", 0)) == len(items)
                and all(not int(progress.get(field, 0)) for field in
                        ("damaged_count", "missing_count", "unreadable_count", "unverifiable_count")),
                "Virtual Tape integrity findings do not match its finalized manifest.")
        page = test.one("verify", "entries", job["id"], "--limit", "100")
        entries = page.get("entries", [])
        positions = self.positions(barcode, items)
        require(not page.get("has_more") and len(entries) == len(positions)
                and {entry["path"] for entry in entries} == set(positions),
                "Tape Verify did not examine exactly its finalized Position set.")
        for entry in entries:
            position = positions[entry["path"]]
            require(entry["finding"] == "SCAN_FINDING_MATCH" and entry["position_id"] == position["id"]
                    and entry["sha256"] == entry["actual_hash"] == position["sha256"]
                    and entry["size_bytes"] == entry["actual_size_bytes"] == position["size_bytes"]
                    and int(entry.get("checked_at_ns", 0)) > 0
                    and position["health"] == "POSITION_HEALTH_HEALTHY" and position["health_job_id"] == job["id"],
                    "Tape Verify did not publish the actual read result for its Position.")

    def format_append_restore(self):
        test = self.test
        directory = test.root + "/fixtures/source/ltfs-small"
        test.remote(["mkdir", directory])
        names = ("format.txt", "append.txt")
        hashes, items = {}, []
        for index, name in enumerate(names):
            test.write(directory + "/" + name, (name + "\n").encode() * 4096)
            hashes[name] = test.remote(["sha256sum", "--", directory + "/" + name]).stdout.decode().split()[0]
            job = self.archive("ltfs-small/" + name)
            if index == 0:
                self.format(job, "APP001")
            else:
                test.cli("archive", "write", "tape", "append", job["id"], "--device", self.device("APP001"), "--barcode", "APP001")
            test.wait_job(job, timeout=600)
            current = self.items(job)
            require(len(current) == 1 and current[0]["status"] == "COPY_STATUS_SUBMITTED",
                    "Format or append did not publish its completed item.")
            if index:
                require(current[0]["file"]["media_path"].endswith("/" + current[0]["file"]["target_path"]),
                        "Append did not retain its separate physical path prefix.")
            items.extend(current)
        media = self.media("APP001")
        require(all(item["media_id"] == media["id"] for item in items), "Append unexpectedly created a different Media.")
        self.restore(items, ["APP001"], "ltfs-small", hashes)
        self.verify("APP001", items)

    def capacity_and_second_media(self):
        test = self.test
        space = test.remote(["df", "-Pk", test.root]).stdout.decode()
        require(int(space.splitlines()[-1].split()[3]) >= 12 * 1024 * 1024,
                "The LTFS capacity fixture needs at least 12 GiB free for source, cartridges and Restore output.")
        directory = test.root + "/fixtures/source/ltfs-large"
        test.remote(["mkdir", directory])
        hashes = {}
        for name in ("a.bin", "b.bin", "c.bin"):
            filename = directory + "/" + name
            test.write(filename, name.encode())
            test.remote(["truncate", "-s", str(1200 * 1024 * 1024), filename])
            hashes[name] = test.remote(["sha256sum", "--", filename], timeout=180).stdout.decode().split()[0]
        job = self.archive("ltfs-large")
        self.format(job, "FUL001")
        stopped = test.wait_job(job, "JOB_STATUS_READY", timeout=900)
        require(stopped.get("error") and stopped.get("phase", "JOB_PHASE_UNSPECIFIED") == "JOB_PHASE_UNSPECIFIED",
                "A full Tape did not return to idle Media selection with a reason.")
        items = self.items(job)
        require(len(items) == 3, "Capacity fixture did not retain every selected item.")
        prefix, ended = [], False
        first_media = self.media("FUL001")
        for item in items:
            if item["status"] == "COPY_STATUS_SUBMITTED":
                require(not ended and item["media_id"] == first_media["id"], "Finalized items are not a continuous prefix on the first Tape.")
                prefix.append(item)
            else:
                ended = True
                require(item["status"] == "COPY_STATUS_PENDING" and not item.get("media_id")
                        and not item["file"].get("media_path"), "An unfinished suffix was published or lost its pending state.")
        require(0 < len(prefix) < len(items), "Capacity fixture did not exercise a partial physical success.")
        self.positions("FUL001", prefix)
        progress = test.one("job", "progress", job["id"])["progress"]
        completed_bytes = sum(int(item["size_bytes"]) for item in prefix)
        require(progress.get("total_known") and int(progress["total_file_count"]) == len(items)
                and int(progress["total_bytes"]) == 3 * 1200 * 1024 * 1024
                and int(progress["copied_file_count"]) == len(prefix) and int(progress["copied_bytes"]) == completed_bytes,
                "Archive progress includes unfinalized Tape content.")
        path = test.install + "/work/jobs/" + job["id"] + "/tapes/FUL001/yatm-report.json"
        report = json.loads(test.remote(["cat", path]).stdout)
        require(int(report["media_id"]) == int(first_media["id"]) and int(report["file_count"]) == len(prefix)
                and int(report["bytes"]) == completed_bytes and report.get("termination_error"),
                "Tape report does not identify its durable prefix and stopping reason.")
        log = test.one("job", "log-lines", job["id"], "--query", "archive_media_checkpoint")
        checkpoints = []
        for line in log.get("lines", []):
            fields = dict(field.split("=", 1) for field in shlex.split(line["text"]) if "=" in field)
            if fields.get("event") == "archive_media_checkpoint":
                checkpoints.append(fields)
        require(checkpoints and all(checkpoints[-1].get(key) == expected for key, expected in {
            "reason": "no_space", "media_id": first_media["id"], "files": str(len(prefix)), "bytes": str(completed_bytes)
        }.items()), "Archive log did not explain the finalized prefix at full capacity.")

        self.format(job, "FUL002")
        test.wait_job(job, timeout=900)
        completed = self.items(job)
        second_media = self.media("FUL002")
        require(len(completed) == 3 and all(item["status"] == "COPY_STATUS_SUBMITTED" for item in completed)
                and {item["media_id"] for item in completed} == {first_media["id"], second_media["id"]},
                "Explicit second-Media selection did not finish exactly the pending suffix.")
        require(completed[:len(prefix)] == prefix
                and [item["id"] for item in completed] == [item["id"] for item in items]
                and all(item["media_id"] == second_media["id"] for item in completed[len(prefix):]),
                "Second-Media work changed the finalized prefix or the pending suffix.")
        progress = test.one("job", "progress", job["id"])["progress"]
        require(progress.get("total_known") and int(progress["copied_file_count"]) == len(completed)
                and int(progress["copied_bytes"]) == sum(int(item["size_bytes"]) for item in completed),
                "Completed Archive progress does not cover both Tape checkpoints.")
        self.restore(completed, ["FUL001", "FUL002"], "ltfs-large", hashes)
        self.verify("FUL001", prefix)
        self.verify("FUL002", completed[len(prefix):])


def run_ltfs_cases(test, source, restored):
    workflows = TapeWorkflows(test, source, restored)
    test.case("ltfs-format-append-restore-verify", workflows.format_append_restore)
    test.case("ltfs-full-prefix-second-media-restore-verify", workflows.capacity_and_second_media)
