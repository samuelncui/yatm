"""Media acceptance through the installed server, CLI and native Preview helper."""

import base64
import hashlib
import json
from pathlib import PurePosixPath
import re
import struct

from package_acceptance import ROOT, require, sha256


def verify_volume(test, volume, location):
    # Three distinct signatures make damaged and missing physical copies independent.
    contents = {"good.txt": b"unchanged verification content", "damaged.txt": b"before",
                "missing.txt": b"missing verification content"}
    test.remote(["mkdir", test.root + "/fixtures/source/integrity"])
    for name, content in contents.items():
        test.write(test.root + "/fixtures/source/integrity/" + name, content)
    archive = test.one("archive", "create", "--location", location + ":integrity")["job"]
    test.wait_job(archive, "JOB_STATUS_READY")
    test.cli("archive", "write", "volume", archive["id"], "--uuid", volume["identity"])
    test.wait_job(archive)
    copies = {}
    for name in contents:
        entry = test.one("files", "get", "--location-id", location, "--path", "integrity/" + name)["entry"]
        version = test.one("files", "versions", entry["associated_file_id"])["versions"][0]
        signature = base64.b64decode(version["signature"]).hex()
        positions = test.one("files", "copies", "--signature", signature)["positions"]
        require(len(positions) == 1, "Integrity fixture did not produce one independent physical copy.")
        position = positions[0]
        path = PurePosixPath(position["path"])
        require(position["media_id"] == volume["id"] and path.parts and not path.is_absolute() and ".." not in path.parts,
                "Physical copy does not belong to the test Volume.")
        copies[name] = {"position": position, "signature": signature,
                        "path": test.root + "/volumes/disk/" + str(path)}

    def verify():
        job = test.one("verify", "create", volume["id"])["job"]
        test.wait_job(job)
        progress = test.one("job", "progress", job["id"])
        require(progress["progress"]["total_file_count"] == progress["progress"]["copied_file_count"],
                "Verification did not finish its entire manifest.")
        return job, progress

    _, healthy = verify()
    total = int(healthy["progress"]["total_file_count"])
    require(total >= 3 and int(healthy.get("matched_count", 0)) == total,
            "Newly archived Volume did not verify as healthy.")
    test.write(copies["damaged.txt"]["path"], b"damage")
    test.remote(["rm", "--", copies["missing.txt"]["path"]])
    job, damaged = verify()
    require(int(damaged.get("matched_count", 0)) == total - 2
            and int(damaged.get("damaged_count", 0)) == 1 and int(damaged.get("missing_count", 0)) == 1,
            "Verification did not distinguish healthy, damaged and missing copies.")

    # Page every result and compare reverse/offset navigation against that same result set.
    rows, cursor = [], ""
    while True:
        page = test.one("verify", "entries", job["id"], "--limit", "1", "--cursor", cursor, "--include-total")
        require(int(page.get("total_entry_count", 0)) == total, "Verification result total changed between pages.")
        entries = page.get("entries", [])
        require(len(entries) == 1, "Verification cursor returned an empty or unbounded page.")
        rows.extend(entries)
        require(len(rows) <= total, "Verification cursor did not advance.")
        cursor = entries[-1]["id"]
        if not page.get("has_more"):
            break
    require(len(rows) == total and len({row["id"] for row in rows}) == total, "Verification paging duplicated or lost results.")
    reverse = test.one("verify", "entries", job["id"], "--limit", "1", "--cursor", rows[-1]["id"], "--order", "desc")
    require(reverse["entries"][0]["id"] == rows[-2]["id"], "Reverse verification cursor skipped a result.")
    anchored = test.one("verify", "entries", job["id"], "--limit", "1", "--offset", str(total - 1))
    require(anchored["entries"][0]["id"] == rows[-1]["id"], "Verification offset selected a different result.")
    by_position = {row["position_id"]: row for row in rows}
    for name, finding in (("damaged.txt", "SCAN_FINDING_MISMATCH"), ("missing.txt", "SCAN_FINDING_MISSING")):
        copy = copies[name]
        row = by_position[copy["position"]["id"]]
        require(row["finding"] == finding and row["sha256"] == copy["position"]["sha256"],
                "Verification overwrote the expected copy identity or lost its finding.")
    require(base64.b64decode(by_position[copies["damaged.txt"]["position"]["id"]]["actual_hash"])
            == hashlib.sha256(b"damage").digest(), "Verification did not report the bytes it actually read.")
    current = test.one("files", "copies", "--signature", copies["damaged.txt"]["signature"])["positions"][0]
    require(current["health"] == "POSITION_HEALTH_DAMAGED" and current["health_job_id"] == job["id"],
            "Damaged copy health is not associated with its verification Job.")

    # Only explicit repair of owned fixture bytes followed by another Verify may clear bad health.
    for name in ("damaged.txt", "missing.txt"):
        test.write(copies[name]["path"], contents[name])
        # Keep the following inventory case's baseline unchanged by this repair.
        source = test.root + "/fixtures/source/integrity/" + name
        test.remote(["chmod", "--reference", source, "--", copies[name]["path"]])
        test.remote(["touch", "-m", "--reference", source, "--", copies[name]["path"]])
    _, repaired = verify()
    require(int(repaired.get("matched_count", 0)) == total and not int(repaired.get("damaged_count", 0))
            and not int(repaired.get("missing_count", 0)), "Explicitly repaired fixture did not verify as healthy.")
    for name in ("damaged.txt", "missing.txt"):
        current = test.one("files", "copies", "--signature", copies[name]["signature"])["positions"][0]
        require(current["health"] == "POSITION_HEALTH_HEALTHY"
                and current["sha256"] == copies[name]["position"]["sha256"], "Verification did not clear prior bad health.")
    return copies, total


def volume_inventory(test, volume, copies, total):
    mount = test.root + "/volumes/disk"
    marker = test.remote(["cat", "--", mount + "/.yatm.json"]).stdout
    candidates = test.one("volume", "candidates")
    candidate = next(item for item in candidates["candidates"] if item["mount_point"] == mount)
    require(candidate["state"] == "VOLUME_CANDIDATE_STATE_REGISTERED" and candidate["media"]["id"] == volume["id"],
            "Registered Volume candidate did not retain its identity.")
    test.write(copies["damaged.txt"]["path"], b"changed inventory content")
    test.remote(["rm", "--", copies["missing.txt"]["path"]])
    test.remote(["mkdir", mount + "/manual"])
    added = mount + "/manual/added.txt"
    test.write(added, b"added")
    test.remote(["chmod", "640", added])
    test.remote(["touch", "-m", "-d", "@100.000000001", added])

    def scan(identity, force=False):
        args = ["scan", "media", identity]
        if force:
            args += ["--signature", "force-read"]
        job = test.one(*args)["job"]
        test.wait_job(job)
        page = test.one("scan", "results", job["id"], "--limit", "100", "--include-total")
        require(not page.get("has_more"), "Small inventory fixture unexpectedly exceeded one result page.")
        return page.get("entries", []), test.one("scan", "progress", job["id"])

    entries, progress = scan(volume["id"])
    changes = {entry["path"]: entry.get("change") for entry in entries}
    require(changes.get("manual/added.txt") == "SCAN_CHANGE_ADDED"
            and changes.get(copies["damaged.txt"]["position"]["path"]) == "SCAN_CHANGE_CHANGED"
            and changes.get(copies["missing.txt"]["position"]["path"]) == "SCAN_CHANGE_REMOVED",
            "Cached inventory Scan lost an added, changed or removed file.")
    require(all(int(progress.get(key, 0)) == 1 for key in ("added_count", "changed_count", "removed_count")),
            "Inventory difference counts do not match physical changes.")

    # A content edit with exactly the same public stat tuple must still be found by force-read.
    before_stat = test.remote(["stat", "-c", "%s|%a|%y", added]).stdout
    test.write(added, b"other")
    test.remote(["touch", "-m", "-d", "@100.000000001", added])
    require(test.remote(["stat", "-c", "%s|%a|%y", added]).stdout == before_stat,
            "Force-read fixture did not preserve size, mode and nanosecond modification time.")
    entries, progress = scan(volume["id"], force=True)
    changed = [entry for entry in entries if entry.get("change") == "SCAN_CHANGE_CHANGED"]
    require(len(changed) == 1 and changed[0]["path"] == "manual/added.txt"
            and base64.b64decode(changed[0]["sha256"]) == hashlib.sha256(b"other").digest(),
            "Force-read inventory Scan missed a stat-preserving content change.")
    positions = test.one("media", "positions", volume["id"], "--directory", "manual/")["positions"]
    require(len(positions) == 1 and positions[0]["sha256"] == changed[0]["sha256"],
            "Force-read inventory did not publish the observed content.")
    position = positions[0]
    require(int(test.one("files", "import-positions", position["id"], "--dryrun").get("file_count", 0)) == 1,
            "Inventory import did not preview one new saved File.")
    imported = test.one("files", "import-positions", position["id"])
    repeated = test.one("files", "import-positions", position["id"])
    require(int(imported.get("file_count", 0)) == 1 and int(repeated.get("existing_count", 0)) == 1
            and not int(repeated.get("file_count", 0)), "Repeated inventory import duplicated its saved File.")
    files = test.rows("ls", "--file-id", "0", "--scope", "saved", "--recursive", "--query", "name:added.txt")
    require(len(files) == 1, "Imported inventory did not appear as one saved File.")
    identity = files[0]["reference"]["file_id"]
    test.cli("library", "trim", "--files")
    test.one("files", "get", "--file-id", identity)

    # Metadata deletion and marker registration may not write or remove any physical content.
    def physical_hashes():
        return sorted(test.remote(["find", mount, "-type", "f", "-exec", "sha256sum", "-z", "--", "{}", "+"]).stdout.split(b"\0"))

    before = physical_hashes()
    proposal = test.one("media", "delete", volume["id"], "--dryrun")
    require(int(proposal.get("media_count", 0)) == 1 and len(test.one("media", "get", volume["id"])["media"]) == 1,
            "Media deletion dry run changed its target.")
    test.one("media", "delete", volume["id"])
    require(not test.one("media", "get", volume["id"]).get("media"), "Media deletion retained the catalog row.")
    require(physical_hashes() == before and test.remote(["cat", "--", mount + "/.yatm.json"]).stdout == marker,
            "Metadata deletion changed the marker or physical files.")
    registered = test.one("volume", "register", mount, "--name", "Registered acceptance Volume")["media"]
    require(registered["id"] != volume["id"] and registered["identity"] == volume["identity"],
            "Register did not reuse the physical marker under a new catalog ID.")
    require(physical_hashes() == before, "Register modified Volume bytes.")
    rebuilt, _ = scan(registered["id"])
    require(len(rebuilt) == total and all(entry.get("change") == "SCAN_CHANGE_ADDED" for entry in rebuilt),
            "Explicit Scan did not reconstruct every remaining Position.")
    require(physical_hashes() == before, "Inventory reconstruction modified physical bytes.")


def preview_assets(test, location):
    # This committed, licensed five-minute Demo excerpt is fixture data, not remote helper source.
    video = ROOT / "internal/demo/assets/big-buck-bunny.mp4"
    require(sha256(video) == "9414ee60a0156893a9d2e4d7861969d743a061e39377989d56969d8b0de50fed",
            "The reviewed Preview video fixture changed.")
    directory = test.root + "/fixtures/source/preview"
    test.remote(["mkdir", directory])
    test.upload([video], directory)
    image_name = "image\\name.png"
    test.write(directory + "/" + image_name, base64.b64decode(
        "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mNk+A8AAQUBAScY42YAAAAASUVORK5CYII="))
    settings = test.one("settings", "preview")
    original_settings = json.dumps(settings)
    video_options = next(item["video"] for item in settings["generators"] if "video" in item)
    require((video_options["timeline_width"], video_options["timeline_height"], video_options["timeline_max_frames"])
            == (320, 180, 30), "Default video Preview dimensions or frame count changed.")
    settings.update(enabled=True, command=test.install + "/yatm-preview")
    for generator in settings["generators"]:
        options = generator.get("image") or generator.get("video")
        options["format"] = "png"

    def save_settings():
        path = test.root + "/preview-settings.json"
        test.write(path, json.dumps(settings).encode() + b"\n")
        test.one("settings", "preview", "--preview-json", path)

    def generate(policy):
        job = test.one("preview", "create", "--location", location + ":preview", "--preview-policy", policy)["job"]
        test.wait_job(job)

    def assets(name):
        reply = test.one("preview", "get", "--location-id", location, "--path", "preview/" + name)
        require(reply.get("availability") == "PREVIEW_AVAILABILITY_READY", "Preview was not available after generation.")
        contents = {}
        for asset in reply.get("assets", []):
            require(asset["role"] not in contents and asset["url"].startswith("/files/preview?"), "Invalid Preview asset identity.")
            data = test.remote(["curl", "--fail", "--silent", test.url + asset["url"]]).stdout
            if asset["media_type"] == "image/png":
                require(len(data) >= 24 and data[:8] == b"\x89PNG\r\n\x1a\n" and data[12:16] == b"IHDR",
                        "Preview HTTP response is not the declared PNG.")
                require(struct.unpack(">II", data[16:24]) == (asset["width_px"], asset["height_px"]),
                        "Preview metadata does not match its encoded dimensions.")
            contents[asset["role"]] = (asset, data)
        return contents

    save_settings()
    capabilities = test.one("preview", "capabilities")
    require(capabilities.get("available") and {"image", "video"}.issubset(capabilities.get("kinds", [])),
            "Installed native helper did not report image and video support.")
    generate("missing-only")
    image = assets(image_name)
    require(set(image) == {"thumbnail"} and image["thumbnail"][0]["media_type"] == "image/png",
            "Literal-path image Preview omitted its thumbnail.")
    first = assets(video.name)
    require(set(first) == {"poster", "timeline", "timeline-map"}, "Video Preview omitted a required asset.")
    timeline, _ = first["timeline"]
    require((timeline["width_px"], timeline["height_px"]) == (3200, 540), "Video Preview did not contain thirty 320x180 frames.")
    mapping = first["timeline-map"][1].decode()
    cues = re.findall(r"(\d\d:\d\d:\d\d\.\d+) --> (\d\d:\d\d:\d\d\.\d+)", mapping)
    cells = [tuple(map(int, cell)) for cell in re.findall(r"#xywh=(\d+),(\d+),(\d+),(\d+)", mapping)]
    require(mapping.startswith("WEBVTT") and len(cues) == 30 and len(set(cells)) == 30
            and all(width == 320 and height == 180 and x + width <= 3200 and y + height <= 540
                    for x, y, width, height in cells), "Video timeline did not expose thirty bounded frames.")
    require(cues[0][0] == "00:00:00.000" and cues[-1][1] == "00:05:00.000",
            "Video timeline did not span the full five-minute fixture.")

    # Missing-only keeps the existing bytes; explicit regeneration applies the new output settings.
    video_options.update(timeline_width=160, timeline_height=90)
    save_settings()
    generate("missing-only")
    unchanged = assets(video.name)
    require({role: item[1] for role, item in unchanged.items()} == {role: item[1] for role, item in first.items()},
            "Missing-only unexpectedly replaced existing Preview assets.")
    generate("regenerate-all")
    regenerated = assets(video.name)
    require((regenerated["timeline"][0]["width_px"], regenerated["timeline"][0]["height_px"]) == (1600, 270),
            "Explicit regeneration did not apply changed output settings.")
    settings = json.loads(original_settings)
    save_settings()
