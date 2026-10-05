"""Real systemd replacement and whole-installation recovery in the owned test root."""

import json
from pathlib import PurePosixPath
import re
import time

from package_acceptance import require


def wait_for_service(test):
    # Poll the direct admission endpoint without changing the restored installation.
    for attempt in range(15):
        pid = test.remote(["systemctl", "show", test.service, "--property", "MainPID", "--value"]).stdout.strip()
        if pid.isdigit() and int(pid) > 0:
            response = test.remote(["curl", "-q", "--noproxy", "*", "--fail", "--silent", "--show-error",
                                    "--connect-timeout", "2", "--max-time", "2", test.url + "/files/_upgrade/status"],
                                   expected=None, timeout=5)
            if response.returncode == 0 and json.loads(response.stdout).get("process_id") == int(pid):
                test.remote(["systemctl", "is-active", "--quiet", test.service])
                require(test.remote(["systemctl", "show", test.service, "--property", "MainPID", "--value"]).stdout.strip() == pid,
                        "The owned service changed during readiness checks.")
                break
        if attempt < 14:
            time.sleep(1)
    else:
        require(False, "Readiness endpoint does not belong to the running test service.")
    test.one("status")
    test.remote(["env", "TMPDIR=" + test.root + "/tmp", "sh", "-c",
                 'cd "$1" && exec ./yatm-migrate -config ./config.yaml -phase frontend-check',
                 "check-frontend", test.install])
    require(test.remote(["systemctl", "show", test.service, "--property", "MainPID", "--value"]).stdout.strip() == pid,
            "The owned service changed while checking its API and frontend.")
    test.remote(["systemctl", "is-active", "--quiet", test.service])


def sqlite_wal_restart(test, location):
    path = test.install + "/config.yaml"
    config = json.loads(test.remote(["cat", path]).stdout)
    require(config["database"]["sqlite_wal"] is False, "The package fixture did not start in the default SQLite mode.")

    def restart(enabled):
        test.remote(["systemctl", "stop", test.service])
        require(test.remote(["systemctl", "show", test.service, "--property", "ActiveState", "--value"]).stdout.strip() == b"inactive",
                "SQLite mode may only change after the owned service stops.")
        config["database"]["sqlite_wal"] = enabled
        test.write(path, json.dumps(config, indent=2).encode() + b"\n")
        test.remote(["systemctl", "start", test.service])
        wait_for_service(test)

    restart(True)
    test.one("files", "metadata", "--location", location + ":changed.txt", "--note", "Persisted through WAL restart")
    test.remote(["test", "-f", test.install + "/catalog.db-wal"])
    before = test.cli("library", "export", "--output", "-")
    restart(False)
    require(test.remote(["test", "-e", test.install + "/catalog.db-wal"], expected=None).returncode == 1,
            "Disabling WAL retained an active WAL sidecar.")
    require(test.cli("library", "export", "--output", "-") == before, "Disabling WAL lost or changed committed Catalog data.")
    detail = test.one("files", "get", "--location-id", location, "--path", "changed.txt")
    require(detail["organization"]["note"] == "Persisted through WAL restart", "WAL publication did not survive both restarts.")


def installation_replacement(test):
    install = test.install
    hidden = install + "/.acceptance-hidden"
    test.write(hidden, b"retained operator data\n")
    test.remote(["chmod", "640", hidden, install + "/config.yaml"])
    test.remote(["setfattr", "-n", "user.yatm_acceptance", "-v", "preserved", hidden])
    test.remote(["ln", "--", hidden, install + "/.acceptance-hard"])
    test.remote(["ln", "-s", "--", ".acceptance-hidden", install + "/.acceptance-link"])
    test.write(install + "/scripts/operator-data", b"operator-owned helper data\n")
    preserved = ["config.yaml", test.service, ".acceptance-hidden", "scripts/operator-data"]

    def hashes(directory):
        # Hash only stable customization; Catalog equivalence is checked through its public export.
        return [test.remote(["sha256sum", "--", directory + "/" + name]).stdout.decode().split()[0] for name in preserved]

    original = hashes(install)

    def check_customization(directory):
        require(hashes(directory) == original, "Replacement or recovery changed operator-owned content.")
        require(test.remote(["stat", "-c", "%a", directory + "/config.yaml"]).stdout.strip() == b"640",
                "Configuration permissions were not retained.")
        require(test.remote(["stat", "-c", "%a", directory + "/.acceptance-hidden"]).stdout.strip() == b"640",
                "Hidden-file permissions were not retained.")
        inodes = test.remote(["stat", "-c", "%i", directory + "/.acceptance-hidden", directory + "/.acceptance-hard"]).stdout.splitlines()
        require(len(inodes) == 2 and inodes[0] == inodes[1], "A retained hard link became an independent copy.")
        require(test.remote(["readlink", "--", directory + "/.acceptance-link"]).stdout.strip() == b".acceptance-hidden",
                "A relative symbolic link changed meaning.")
        require(test.remote(["getfattr", "--only-values", "-n", "user.yatm_acceptance", directory + "/.acceptance-hidden"]).stdout == b"preserved",
                "A supported user attribute was lost.")

    def catalog():
        return test.cli("library", "export", "--output", "-")

    before_catalog = catalog()
    stable_job_fields = ("id", "status", "kind", "created_at_ns", "updated_at_ns", "error")

    def job_inventory():
        page = test.one("job", "list", "--limit", "100")
        require(not page.get("has_more"), "Installation fixture exceeded one Job page.")
        return [{field: row.get(field) for field in stable_job_fields} for row in page.get("jobs", [])]

    jobs = job_inventory()

    def check_application():
        test.verify_programs(install)
        test.one("status")
        require(catalog() == before_catalog, "Replacement or recovery changed Library metadata.")
        require(job_inventory() == jobs,
                "Replacement or recovery changed historical Job identities or durable state.")
        check_customization(install)

    def backup_hashes():
        return sorted(test.remote(["find", install + "/.backup", "-name", "yatm.tar.gz", "-type", "f",
                                   "-exec", "sha256sum", "--", "{}", "+"]).stdout.splitlines())

    backups = []
    for attempt in (1, 2):
        obsolete = install + f"/docs/acceptance-obsolete-{attempt}.txt"
        test.write(obsolete, b"owned managed-tree fixture\n")
        pid = test.remote(["systemctl", "show", test.service, "--property", "MainPID", "--value"]).stdout
        old_backups = backup_hashes()
        if attempt == 1:
            # The managed-tree difference reaches confirmation without changing candidate bytes.
            for answer in (b"n\n", b""):
                cancelled = test.installer(body=answer)
                require(b"Installation cancelled" in cancelled.stdout, "Decline or EOF did not cancel replacement.")
                require(test.remote(["systemctl", "show", test.service, "--property", "MainPID", "--value"]).stdout == pid,
                        "Cancelled replacement interrupted the original service.")
                require(backup_hashes() == old_backups, "Cancelled replacement created or changed a complete backup.")
                check_application()
        result = test.installer(body=b"y\n")
        matches = re.findall(rb"^Installation backup destination: (.+/yatm\.tar\.gz)$", result.stdout, re.MULTILINE)
        require(len(matches) == 1, "Replacement did not identify one complete installation backup.")
        archive = matches[0].decode()
        require(archive.startswith(install + "/.backup/") and ".." not in PurePosixPath(archive).parts,
                "Installer backup escaped its owned directory.")
        test.remote(["sh", "-c", 'cd "$1" && sha256sum -c yatm.tar.gz.sha256', "check-backup", str(PurePosixPath(archive).parent)])
        require(set(old_backups).issubset(backup_hashes()), "A later replacement changed an earlier complete backup.")
        members = test.remote(["tar", "-tzf", archive]).stdout.decode().splitlines()
        require(not any(name == "./.backup" or name.startswith("./.backup/") for name in members),
                "A complete backup recursively included its retained backups.")
        require(all("./" + name in members for name in preserved), "The complete backup omitted custom or hidden files.")
        require(f"./docs/acceptance-obsolete-{attempt}.txt" in members,
                "The complete backup omitted the managed file awaiting replacement.")
        mirror = test.root + f"/backup-inspection-{attempt}"
        test.remote(["mkdir", mirror])
        test.remote(["tar", "--acls", "--xattrs", "--xattrs-include=*", "--numeric-owner", "-xzf", archive, "-C", mirror], timeout=300)
        check_customization(mirror)
        require(test.remote(["test", "-e", obsolete], expected=None).returncode == 1,
                "Managed-tree replacement retained an obsolete package member.")
        require(test.remote(["test", "-e", str(PurePosixPath(archive).parent) + "/.work"], expected=None).returncode == 1,
                "Successful replacement retained its extraction scratch.")
        check_application()
        test.remote(["rm", "-rf", "--", mirror])
        backups.append(archive)

    # Rehearse the documented same-root recovery, retaining the displaced tree instead of overlaying it.
    test.write(install + "/.acceptance-new-only", b"must not survive recovery\n")
    test.remote(["systemctl", "stop", test.service])
    require(test.remote(["systemctl", "show", test.service, "--property", "ActiveState", "--value"]).stdout.strip() == b"inactive",
            "Recovery requires a stopped service.")
    old_backups = backup_hashes()
    test.remote(["sh", "-c", 'cd "$1" && sha256sum -c yatm.tar.gz.sha256',
                 "check-recovery-backup", str(PurePosixPath(backups[-1]).parent)])
    retained = test.remote(["mktemp", "-d", install + "/.backup/failed-installation.XXXXXXXX"]).stdout.decode().strip()
    require(re.fullmatch(re.escape(install) + r"/\.backup/failed-installation\.[A-Za-z0-9]{8}", retained),
            "Unexpected failed-installation path.")
    entries = test.remote(["find", install, "-mindepth", "1", "-maxdepth", "1", "!", "-name", ".backup", "-print0"]).stdout.split(b"\0")
    entries = [entry.decode() for entry in entries if entry]
    require(entries and all(str(PurePosixPath(entry).parent) == install for entry in entries), "Recovery selected an unowned entry.")
    test.remote(["mv", "--", *entries, retained + "/"], timeout=300)
    test.remote(["tar", "--acls", "--xattrs", "--xattrs-include=*", "--numeric-owner", "-xzf", backups[-1], "-C", install], timeout=300)
    # Compare the stopped tree before startup can update databases or logs.
    test.remote(["tar", "--acls", "--xattrs", "--xattrs-include=*", "--numeric-owner", "-dzf", backups[-1], "-C", install], timeout=300)
    require(test.remote(["test", "-e", install + "/.acceptance-new-only"], expected=None).returncode == 1,
            "Recovery overlaid the failed tree and retained a new-only file.")
    require(test.remote(["cat", retained + "/.acceptance-new-only"]).stdout == b"must not survive recovery\n",
            "Recovery discarded the displaced installation instead of preserving it.")
    require(test.remote(["cat", install + "/docs/acceptance-obsolete-2.txt"]).stdout == b"owned managed-tree fixture\n",
            "Recovery did not restore the selected backup's managed tree.")
    check_customization(install)
    test.remote(["systemctl", "daemon-reload"])
    test.remote(["systemctl", "start", test.service])
    wait_for_service(test)
    require(backup_hashes() == old_backups, "Recovery changed the retained complete backups.")
    check_application()
    test.report["installation_backups"] = backups
    test.report["recovery_preserved_installation"] = retained
    test.save()
