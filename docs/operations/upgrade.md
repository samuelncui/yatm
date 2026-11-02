# Upgrade a v1 Installation

Follow these steps to move an existing v1 installation to a newer release. A v0.1.x installation
follows the [v0.1.x upgrade guide](migration.md) instead; a new installation follows the
[installation guide](install.md). The examples use `v1.0.0-alpha.3` in `/opt/yatm` with the
`yatm-httpd.service` unit; add `--install-dir` and `--service` for another installation.

## Step-by-Step Upgrade

1. **Read the release notes** for every release between the installed version
   (`cat /opt/yatm/VERSION`) and the target on the
   [releases page](https://github.com/samuelncui/yatm/releases). They name Settings or Tape script
   changes that need your attention.

2. **Let running Jobs finish.** The upgrade stops before touching the service while a Job is
   running. `yatm-cli --server http://127.0.0.1:8080 job list` shows their state.

3. **Download the target version's installer.** Do not reuse the copy inside the installation:

   ```shell
   curl --fail --location --output install-release.sh \
     https://raw.githubusercontent.com/samuelncui/yatm/v1.0.0-alpha.3/install-release.sh
   ```

4. **Run the read-only check** and read its output:

   ```shell
   sudo bash install-release.sh --version v1.0.0-alpha.3 --check
   ```

   - An `error:` line is a blocker; nothing was changed. Fix it and rerun.
   - `Settings:` lines and the YAML diff are the configuration changes the upgrade will apply.
   - `Manual check:` lines show how each configured Tape script differs from this release's
     template. Your scripts are never replaced; compare the differences with
     [Tape scripts](tape-scripts.md) before the next Tape Job.

5. **Upgrade.** Run the command again without `--check` and confirm. The installer stops the
   service, writes a [backup](#installation-backups) under `.backup/<datetime>.XXXXXX/`, replaces
   the programs, starts the service and runs its [readiness checks](#readiness-checks). It prints
   the backup and report paths at the end. Add `--with-preview` to keep or add the Preview helper
   without being asked.

6. **Check the result.** `cat /opt/yatm/VERSION` shows the new version, and
   `yatm-cli --server http://127.0.0.1:8080 status` answers. Open the UI and check the Library,
   Jobs and **Settings**.

7. **Remove old backups** once you no longer need to roll back; see
   [Remove Old Backups](#remove-old-backups).

If the upgrade fails, the installer prints the failed stage and the backup to use. A failure before
the service stopped leaves it running unchanged; after that, the service stays stopped until you
[restore the backup](#restore-a-backup).

## Restore a Backup

Program/resource replacement is not atomic. Interruption can leave a mixed tree; once Commit is uncertain or replacement has begun, keep the service stopped. Replacing only a binary or only a database is not a rollback.

Restore a complete backup by its directory name under `.backup/`:

```shell
sudo bash /opt/yatm/install-release.sh --restore-backup 20261101002201.lmnljR
```

The command checks that the service unit belongs to this installation, verifies the archive
checksum and free space, asks for confirmation, stops the service, moves the current installation
into a new `.backup/replaced-<datetime>.XXXXXX/` directory, extracts the backup into the same root,
then reloads systemd and starts the service. The live Preview root never moves. A backup made before
Previews were excluded has no `preview-root` file and contains its own Previews; restoring it moves
the current Previews into the replaced directory with everything else. If the restore fails partway,
the message names the replaced directory to recover from. Add `--install-dir` and `--service` for a custom installation. If the installed script is
damaged, run the same option from the verified release package. Compare the Library, historical
Jobs and configuration with the pre-upgrade state, then remove the replaced directory when it is no
longer needed.

Rollback restores software and metadata; it does not undo file moves, deletions, Restore outputs or Media writes performed after the new version started.

## Remove Old Backups

Each backup holds the whole installation except Previews. Keep the newest complete backups and remove
the rest with:

```shell
sudo bash /opt/yatm/install-release.sh --prune-backups 1
```

It keeps at least one, lists what it keeps and removes and asks before deleting. `replaced-*` directories and incomplete
attempts are listed for manual review and never removed.

## Installation Backups

All retained installer artifacts stay below the existing installation root:

```text
/opt/yatm/
  config.yaml
  scripts/
  yatm-httpd
  tapes.db
  jobs/
  .backup/
    <datetime>.XXXXXX/
      yatm.tar.gz
      yatm.tar.gz.sha256
      config.diff
      preview-root      # when the Preview root is inside the installation
      upgrade.log
      migration.json    # when legacy database migration is needed
```

Every replacement or configuration/data update retains a complete compressed installation, including hidden files, databases, Jobs, logs and custom files. Top-level `.backup/` and a Preview root inside the installation are excluded: the attempt records its path in `preview-root` and a restore leaves the live directory in place. A Scan regenerates Previews from originals; content whose only copy is on Tape needs a Restore first, because Tape sources do not generate Previews. The unique attempt directory and reports have restrictive permissions; `.backup` must be a real directory. Archive creation streams through GNU tar, preserving ownership, modes, modification times, links and supported ACLs/xattrs. Content comparison and a second deterministic archive stream verify the stopped source before the final archive and checksum are published. Filesystem access/change times are not restored.

Automatic preflight accepts relative symbolic links whose resolved targets stay inside the archived installation. It rejects absolute symbolic links even when they currently point inside that directory, and links into the excluded `.backup` tree. Such layouts need a reviewed manual backup before upgrade. Initial inspection runs in temporary scratch before the installer creates reports or locks inside the installation; accepted relative links retain their targets in the extracted backup.

Space checks cover the backed-up installation, candidate and migration work on the installation filesystem. External databases, work directories or symlink targets require coordinated manual backup. `.backup/` is excluded from live Location access and Scan. Existing `.yatm-upgrades` enters the first complete archive; after successful acceptance, recognized installer attempts are removed and unrecognized contents are reported and retained.

## Upgrade Phases

| Phase | Result and failure boundary |
| --- | --- |
| Verify and preflight | Verified candidate, version/layout checks, YAML diff, Settings changes and retained attempt log; no service interruption before consent. |
| Stop and backup | Recheck active work, quiesce supported service admission, stop normally, revalidate the reviewed inputs, and create/verify the complete archive before changing data. |
| Prepare (v0.1.x only) | Create staging tables and complete Job bundles; retain the JSON report on success, failure or cancellation. Existing legacy database rows remain available until Commit. |
| Report approval (v0.1.x only) | [Review the migration report](migration.md#review-the-migration-report). Declining or failing Prepare removes only prepared output through Abort before the old service can restart. |
| Commit (v0.1.x only) | Activate current tables. An uncertain Commit keeps the service stopped. |
| Cleanup and replace | Cleanup fully compares migrated Library, every historical Job/item and logs against the explicit complete backup before removing obsolete active tables, old Job storage and transferred logs. Failed validation or cleanup keeps the service stopped. Apply the approved configuration/Settings conversion after legacy evidence has been consumed, then replace release-owned files/trees completely. |
| Start and accept | Run the [readiness checks](#readiness-checks). Remove successful-attempt `.work/` scratch. Report installation readiness separately from manual Tape adaptation. |

`upgrade.log` records the attempt; `config.diff` retains the reviewed YAML changes. Migration additionally retains `migration.json`. Candidate downloads, extracted releases, private review JSON and extracted legacy evidence live in `.work/`; success removes this scratch. Failures retain relevant evidence and the failed stage. A `.partial` archive is not a recovery backup. Failures before mutation restore the original service state; failures after mutation keep it stopped. A declined Prepare first aborts its staging output before restoring the old service.

Same-version reruns compare version, commit, complete managed resources and configuration conversion needs. A truly unchanged run verifies readiness and offers the [optional Skill](install.md#optional-agent-skill) without making another archive. Same-version candidate replacements use the full backup flow. Initial installations have no old installation to archive.

The installer has no durable phase-resume protocol. A previously unfinished `.yatm-upgrades/migration.pending.json` blocks entry: finish with its original installer or restore its backup first. After an uncertain mutation in the new flow, recover the complete archive before starting another upgrade.

## Readiness Checks

After systemd starts, acceptance binds to the direct-loopback API process identity and the unit's running process; another instance occupying the configured port cannot satisfy the check. The installer then reads the Library root with `yatm-cli ls`, lists Jobs, checks the served frontend assets and compares each program's version identity. Recovery waits for both the old unit and its admission endpoint instead of treating normal restart delay as failure.
