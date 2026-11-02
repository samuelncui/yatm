# Upgrade from v0.1.x to v1

Status: Current supported installer procedure for upgrading a v0.1.x installation to v1.

This upgrade converts the v0.1.x Library and Job history into the v1 Catalog and Job bundles,
moves the old source and target directories into Locations, and changes the Tape script contract.
It follows on its own from start to finish; the installer displays this exact document from the
checksum-verified package before stopping the service. The examples use `v1.0.0-alpha.3`, the
default `/opt/yatm` installation and the `yatm-httpd.service` unit; add `--install-dir` and
`--service` for another one. Software versions and
[data-format revisions](../architecture/persistence.md#published-data-formats) are independent.

## Before You Start

- `cat /opt/yatm/VERSION` shows `v0.1.x`.
- The installation is Linux amd64 with systemd and SQLite, and its configuration, database,
  scripts, captured indexes and Job storage all live inside `/opt/yatm`. The check below refuses
  other layouts before stopping anything; they need the
  [offline migration procedure](offline-migration.md) and a coordinated backup of the external parts.
- No Job is running, and you can stop YATM for the upgrade. Large Libraries take minutes.
- The installation filesystem has free space for about twice the installation (the backup plus
  migration work).
- Keep the backup the upgrade creates for as long as you evaluate the Alpha.

## Step-by-Step Upgrade

1. **Download the v1 installer.** Do not reuse the `install-release.sh` inside the old installation:

   ```shell
   curl --fail --location --output install-release.sh \
     https://raw.githubusercontent.com/samuelncui/yatm/v1.0.0-alpha.3/install-release.sh
   ```

2. **Fix the `encrypt` script** if Tape devices are configured. The v0.1.x template reports success
   even when encryption could not be enabled, and the check refuses it. Replace
   `/opt/yatm/scripts/encrypt` (or the file `scripts.encrypt` names) with the
   [script under Tape Script Adaptation](#tape-script-adaptation); it works with v0.1.x as well, so
   the old service keeps running.

3. **Run the read-only check.** It verifies the package and the installation and changes nothing:

   ```shell
   sudo bash install-release.sh --version v1.0.0-alpha.3 --check
   ```

   Expect, in order:
   - `error:` lines are blockers. Fix them and rerun; nothing was changed.
   - `Manual check:` lines. For an unmodified v0.1.x installation these are the `readinfo`
     replacement and the differences between each configured Tape script and the v1 template.
     They are handled after the upgrade, in step 7.
   - `Requested installation: v0.1.x → v1.0.0-alpha.3`.
   - The configuration diff and `Settings:` lines: `paths.source` and `paths.target` become
     Locations (the target as the preferred Restore destination) and Preview preferences move into
     Settings. Read [What Changes](#what-changes) for the rest.

4. **Upgrade.** Run the same command without `--check`:

   ```shell
   sudo bash install-release.sh --version v1.0.0-alpha.3 --with-preview
   ```

   - The installer shows this guide and asks `Continue with installation?`. Answering anything but
     `y` leaves the old service running unchanged.
   - It stops the service, writes the backup and its checksum, and converts the data. Then it prints
     the migration report and asks `Approve this migration report and commit the migration?`.
     [Review the report](#review-the-migration-report) before answering. Declining restarts the old
     service unchanged.
   - After approval it validates the result against the backup, replaces the programs, starts v1
     and checks that it answers. It ends with `Installed v1.0.0-alpha.3.` and the backup path.

5. **Check the result.**
   - `cat /opt/yatm/VERSION` shows the v1 version, and
     `/opt/yatm/yatm-cli --server http://127.0.0.1:<port> status` answers, with the port from
     `listen` in `config.yaml`.
   - In the UI, the Library shows your Files and Tapes, **Jobs** lists the migrated history, and
     **Settings → Locations** lists the old source and target directories.

6. **Find original files again.** The migration links no Library File to its original yet. Scan the
   source Location to record which originals still exist; see [Archive, Restore and Scan](jobs.md).

7. **Adapt the Tape scripts before the first Tape Job.** Follow
   [Tape Script Adaptation](#tape-script-adaptation): `mount`, `umount` and `readinfo` need the
   changes listed there. The installer never changes your scripts.

8. **Remove what v1 no longer uses**, once you are satisfied; the backup still holds them:
   - `library.json`, `tapes.db.bak*`, `bak/`, `db_backups/`, `write-reports/` and old unit files
     such as `tape-writer.service`;
   - `captured_indices/`, after the mount script writes to `TAPE_DIR` (its Indexes are already in
     the v1 Catalog);
   - scripts that `config.yaml` does not name and no configured script calls.

If the upgrade fails, the installer prints the failed stage and the backup directory. Before the
service stopped, the old service keeps running unchanged. After that, YATM stays stopped. Return to
v0.1.x with the installed copy of the v1 installer, or the downloaded one if the upgrade failed
before installing it:

```shell
sudo bash /opt/yatm/install-release.sh --restore-backup <backup directory name>
```

[Restore a Backup](upgrade.md#restore-a-backup) describes what the command does. Rollback restores
software and metadata; it does not undo file moves, deletions, Restore output or Media writes made
since the backup.

## Review the Migration Report

Prepare prints the report as JSON and keeps it as `migration.json` in the attempt directory. It holds counts and warnings, not a per-file listing:

| Field | Check |
| --- | --- |
| `library_files`, `library_tapes`, `library_positions` | Match what the old UI showed: roughly the number of Files, every Tape you own, and their recorded copies. |
| `library_storage_positions` | Copies placed from a captured LTFS Index. `0` while the old installation has captured `.schema` files means Prepare did not find them. |
| `migrated_jobs`, `archive_items`, `restore_items` | Your Job history; `deleted_jobs` were already deleted and are not migrated. |
| `warnings` | Read each one; see below. |

| Warning | Meaning | Decision |
| --- | --- | --- |
| `Tape … Position … was omitted` | A recorded copy disagrees with, or is absent from, that Tape's captured Index. The copy is not migrated. | Approve a few you recognize as stale. Decline when many appear or a whole Tape is affected; check the captured-index directory and rerun. |
| `… Files have no confirmed archived content` | Library Files with no archived copy, such as never-archived originals. | Normal. |
| `job … has no legacy creation time`, `… empty … manifest`, `… no configured legacy restore target` | Historical Job details that could not be recovered exactly. | Informational. |

Declining aborts Prepare and restarts the old service unchanged.

Creating empty Settings tables during migration preserves the approved configuration plan. Actual preference, Location binding or configuration changes still require a new review.

## What Changes

- Library File IDs, logical paths, tags and Note are retained. Confirmed archive evidence creates independent Position signatures and saved FileVersions; uncertain history is reported instead of invented. Opaque signatures retain their bytes, and unknown save times remain unknown.
- Each historical Job becomes a bundle containing its manifest, execution metadata and archived log. A submitted Archive item requires an unambiguous recorded physical copy; pending Restore selections require valid candidates. Legacy Restore output roots retain their original working-directory-relative meaning and are frozen for later execution.
- Migration writes the current Catalog and Job bundle revision 1 directly, including the current Position parent index and signed nanosecond timestamps. Legacy instants retain their precision, lower digits are zero-filled, and out-of-range dates fail conversion instead of wrapping. Existing pre-stable v1 data is not upgraded in place; the [timestamp contract](../architecture/persistence.md#timestamps) defines current encoding and unknown values.
- Tape barcodes are stored trimmed and upper-case, as by the legacy JSON import and Media registration. Captured indexes are still located by the barcode as legacy scripts recorded it.
- Captured legacy LTFS indexes supply validated order/extents for exact path/size matches. Missing or mismatched inventory is reported. Physical storage profiles retain their own `ltfs_v0`/`ltfs_v1` identities.
- The reviewed conversion moves legacy `paths.source` and `paths.target` to Locations and removes them from YAML; the old target becomes a preferred Restore destination. Existing registrations and user removals take precedence. This registration does not scan contents or move Library organization. [Location configuration](locations.md) owns authorization and subsequent editing.
- Preview generator preferences move to Settings when absent; saved preferences take precedence. Deployment paths, scripts, helpers, permissions, service working directory and unrelated configuration remain in place. Release-owned programs, frontend, documentation, licenses, Skill and templates are replaced completely. Default script templates are deployed only for a fresh installation.

## Tape Script Adaptation

**Action required before the first Tape Job.** Upgrades keep your scripts. The unmodified
v0.1.x templates hide failures or write the captured Index to the wrong place:

| Script | v0.1.x template problem | Fix |
| --- | --- | --- |
| `encrypt` | Exits successfully when every `stenc` attempt fails, so a Job writes without encryption. | **Before upgrading.** When Tape devices are configured, the installer refuses an unmodified v0.1.x `encrypt`. Use the script below, keeping your local `stenc` options. |
| `mount` | Captures the Index into a fixed `captured_indices` directory. | **After migration.** Write to `TAPE_DIR` as described below and in the [bundled mount template](../../scripts/mount). |
| `umount` | Returns before the final Index is written and the device is released. | **After upgrading.** Use the [bundled umount template](../../scripts/umount)'s bounded wait. |
| `readinfo` | Reports an empty barcode when the probe fails. | **After upgrading.** Replace it with the [bundled readinfo template](../../scripts/readinfo); it needs v1 `yatm-lto-info --barcode`. The installer lists an unmodified copy as a manual check. |

The installer recognizes only byte-identical v0.1.x templates. An edited copy can carry the same
problems; compare it with the diff that `--check` prints for each configured script.

This `encrypt` works with both v0.1.x and v1:

```bash
#!/usr/bin/env bash
set -ex

mt -f "${DEVICE}" load
sleep 5

for i in {1..60}; do
    if stenc -f "${DEVICE}" -e on -k "${KEY_FILE}" -kd "${TAPE_BARCODE}" -a 1 --ckod; then
        sleep 3
        exit 0
    fi
    sleep 5
done
exit 1
```

Complete data migration **before changing the legacy mount script's captured-index output directory**. Prepare reads the old script/configuration to locate existing index evidence. Keep that directory and its contents if any retained script still uses it.

Update the mount output destination while retaining vendor-specific commands: replace the fixed
`-o work_directory=/opt/yatm/captured_indices` with `-o work_directory="${TAPE_DIR}"` and use the
index capture arguments for the installed LTFS distribution from the
[mount template table](tape-scripts.md#mount-and-unmount).

[Tape scripts](tape-scripts.md) owns the complete input, output, mount and unmount contract.
Service startup acceptance and Tape acceptance are separate outcomes. A successful installer does
not certify customized scripts or physical hardware.

## Configuration Review

`yatm-migrate -phase config-plan -plan-file <private-file>` generates a review without changing the installation. It contains a unified YAML diff, proposed Location imports, preserved Settings and resolved Preview preferences. The installer displays these before consent. `config-check -service-stopped -plan-file <private-file>` rechecks the exact file, all three Settings groups and relevant Locations after stop; any presence or value change invalidates the review. `config-apply -service-stopped -plan-file <private-file> --confirm` publishes the reviewed preferences and rewrites YAML after the backup and required legacy migration. All phases use the same `-config` and service working directory.

Missing or null `paths.access` is replaced with explicit roots equivalent to the legacy source/target authorization. Explicit access rules, including `[]`, are unchanged. Relative paths retain their original service-working-directory meaning. Equal source/target roots share one registration. Successful conversion removes the legacy YAML fields; subsequent startup does not import them or recreate deleted registrations. No persistent import checkpoint is stored. Each saved Settings group is detected independently; a saved Preview group wins, while an absent Preview group receives the resolved legacy generator preferences. Location identity/name/Ignore/restore preference, unrelated YAML fields/comments and file permissions are retained. Unsupported ambiguous YAML blocks conversion before stop. An already converted configuration has no legacy fields to import; new installations use current configuration and default Settings.
