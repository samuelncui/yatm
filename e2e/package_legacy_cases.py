"""Real copied metadata acceptance using only the shipped installer, migrator and CLI."""

import json
from pathlib import PurePosixPath
import re
import shutil
import sqlite3
import tarfile

from package_acceptance import require, sha256


def prepare_metadata(source, destination):
    """Keep data evidence; never transfer the backup's runtime configuration or scripts."""
    extract_data_archive(source, destination,
                         {"tapes.db", "tapes.db-wal", "captured_indices", "job-logs", "write-reports", "jobs"})
    require((destination / "tapes.db").is_file(), "Fixture needs a root-level tapes.db and installation-relative metadata.")
    (destination / "captured_indices").mkdir(exist_ok=True, mode=0o700)


def extract_data_archive(source, destination, allowed):
    """Copy selected regular data into a new private directory without following links."""
    destination.mkdir(mode=0o700)
    with tarfile.open(source) as archive:
        for member in archive:
            path = PurePosixPath(member.name)
            require(not path.is_absolute() and ".." not in path.parts, "Metadata archive contains an unsafe path.")
            if not path.parts or path.parts[0] not in allowed:
                continue
            require(member.isfile() or member.isdir(), "Metadata evidence must contain only files and directories.")
            target = destination.joinpath(*path.parts)
            if member.isdir():
                target.mkdir(parents=True, exist_ok=True, mode=0o700)
                continue
            target.parent.mkdir(parents=True, exist_ok=True, mode=0o700)
            # Exclusive creation also rejects duplicate data members instead of changing evidence.
            with target.open("xb") as output, archive.extractfile(member) as contents:
                shutil.copyfileobj(contents, output)


def check_legacy_package(path):
    with tarfile.open(path) as archive:
        version = None
        for member in archive:
            name = PurePosixPath(member.name)
            require(not name.is_absolute() and ".." not in name.parts and (member.isfile() or member.isdir()),
                    "Legacy package contains an unsafe member.")
            if str(name) == "VERSION":
                version = archive.extractfile(member).read().decode().strip()
        require(version and re.fullmatch(r"v0\.1\.\d+", version), "Legacy package has no supported VERSION.")
        require(path.name == f"yatm-linux-amd64-{version}.tar.gz", "Legacy package filename and VERSION differ.")
    return version


def run_legacy(test):
    args = test.args
    source_hash = sha256(args.legacy_fixture)
    original = args.out / "legacy-input"
    prepare_metadata(args.legacy_fixture, original)
    legacy_version = check_legacy_package(args.legacy_package)
    fixture = args.out / "legacy-metadata.tar.gz"
    with tarfile.open(fixture, "w:gz") as archive:
        for item in sorted(original.iterdir()):
            archive.add(item, arcname=item.name)
    test.report["legacy_inputs"] = {"version": legacy_version, "package_sha256": sha256(args.legacy_package),
                                    "source_sha256": source_hash, "fixture_sha256": sha256(fixture)}
    test.save()
    test.upload([args.legacy_package, fixture], test.root)
    test.remote(["mkdir", test.install])
    for archive, digest in ((args.legacy_package, test.report["legacy_inputs"]["package_sha256"]),
                            (fixture, test.report["legacy_inputs"]["fixture_sha256"])):
        require(test.remote(["sha256sum", test.root + "/" + archive.name]).stdout.split()[0].decode() == digest,
                "Transferred legacy input checksum differs.")
        test.remote(["tar", "--no-same-owner", "--no-same-permissions", "-xzf", test.root + "/" + archive.name,
                     "-C", test.install])
    config = {"domain": test.url, "listen": test.url.removeprefix("http://"),
              "database": {"dialect": "sqlite", "dsn": "./tapes.db"},
              "paths": {"work": ".", "source": "./originals", "target": "./restored", "access": [], "volumes": []},
              "tape_devices": [], "scripts": {}}
    test.write(test.install + "/config.yaml", json.dumps(config, indent=2).encode() + b"\n")
    test.remote(["mkdir", "-p", test.install + "/originals", test.install + "/restored"])
    unit = ("[Unit]\nDescription=YATM isolated legacy acceptance\n[Service]\nType=simple\nWorkingDirectory="
            + test.install + "\nExecStart=" + test.install + "/yatm-httpd -config " + test.install
            + "/config.yaml\n[Install]\nWantedBy=multi-user.target\n")
    test.write(test.install + "/" + test.service, unit.encode())
    test.remote(["systemctl", "link", test.install + "/" + test.service])
    test.remote(["systemctl", "daemon-reload"])
    require(test.remote(["systemctl", "is-active", test.service], expected=None).returncode != 0,
            "Legacy service unexpectedly running.")

    def migrate(phase, *arguments):
        return test.remote(["sh", "-c", 'cd "$1" && shift && exec "$@"', "migrate-copy", test.install,
                            "env", "TMPDIR=" + test.root + "/tmp", test.root + "/package/yatm-migrate",
                            "-config", "./config.yaml", "-phase", phase,
                            *arguments], timeout=600)

    def abort():
        result = test.installer(body=b"y\nn\n")
        require(b"Prepared migration aborted" in result.stdout, "Installer did not reach and abort Prepare.")
        migrate("abort", "--confirm")
        require(migrate("schema").stdout.strip() == b"legacy", "Aborted migration changed the active schema.")

    def upgrade():
        result = test.installer(body=b"y\ny\n")
        matches = re.findall(rb"^Installation backup destination: (.+/yatm\.tar\.gz)$", result.stdout, re.MULTILINE)
        require(len(matches) == 1, "Installer did not identify one complete legacy backup.")
        backup = matches[0].decode()
        require(backup.startswith(test.install + "/.backup/") and ".." not in PurePosixPath(backup).parts,
                "Legacy backup escaped the owned installation.")
        test.report["legacy_backup"] = backup

    # The stopped copy exercises Prepare/Abort through the real installer without running old software.
    test.case("legacy-readonly-installer-review", lambda: test.installer(check=True))
    test.case("legacy-prepare-decline-and-abort", abort)
    test.case("legacy-exact-package-installer-upgrade", upgrade)
    test.verify_programs(test.install)
    test.one("status")

    def roundtrip():
        snapshot = test.root + "/legacy-export.jsonl"
        test.cli("library", "export", "--output", snapshot, timeout=600)
        before = test.remote(["sha256sum", snapshot]).stdout.split()[0]
        test.cli("library", "import", "--input", snapshot, timeout=600)
        after = test.root + "/legacy-export-after.jsonl"
        test.cli("library", "export", "--output", after, timeout=600)
        require(test.remote(["sha256sum", after]).stdout.split()[0] == before,
                "Legacy Library changed during a complete JSONL roundtrip.")
        test.report["legacy_export_sha256"] = before.decode()
    test.case("legacy-complete-jsonl-roundtrip", roundtrip)

    test.remote(["systemctl", "stop", test.service])
    from legacy_metadata import check

    def observe(name):
        observed = test.root + "/" + name + ".tar.gz"
        test.remote(["tar", "-czf", observed, "-C", test.install, "tapes.db", "jobs"], timeout=600)
        target = args.out / (name + ".tar.gz")
        test.run(["scp", "-q", args.host + ":" + observed, str(target)], timeout=600)
        migrated = args.out / name
        extract_data_archive(target, migrated, {"tapes.db", "jobs"})
        return migrated

    migrated = observe("legacy-observed")
    result = check(original, migrated)
    test.report["independent_legacy_metadata"] = result

    def repair():
        # Reuse the installer's checked backup, never the original external backup location.
        backup = test.report["legacy_backup"]
        preserved = test.root + "/legacy-preserved"
        test.remote(["sh", "-c", 'cd "$1" && sha256sum -c yatm.tar.gz.sha256', "check-backup",
                     str(PurePosixPath(backup).parent)])
        test.remote(["mkdir", preserved])
        test.remote(["tar", "--acls", "--xattrs", "--xattrs-include=*", "--numeric-owner", "-xzf", backup,
                     "-C", preserved], timeout=600)
        common = ["-install-root", test.install, "-backup-root", preserved]
        migrate("cleanup", *common, "--confirm")
        if not result["jobs"]:
            test.report["limits"].append("Fixture has no retained Job; historical Job repair is untested.")
            return
        job = result["jobs"][0]["id"]
        with sqlite3.connect(migrated / "tapes.db") as db:
            revisions = dict(db.execute("SELECT id,revision FROM jobs"))
        revisions[job] = max(revisions.values()) + 1
        migrate("repair-job", *common, "-job-id", str(job), "--confirm")
        migrate("validate", *common)
        test.report["independent_repaired_metadata"] = check(original, observe("legacy-repaired"), revisions=revisions)
    test.case("legacy-repeated-cleanup-and-historical-repair", repair)
    require(sha256(args.legacy_fixture) == source_hash, "Original copied backup changed.")
    test.report["limits"].append("Legacy acceptance uses copied metadata and Archive history; no original file or physical Tape is accessed.")
    test.save()
