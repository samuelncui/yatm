# Upgrade from v0.1.x to v1

Status: Current supported installer procedure for upgrading a v0.1.x installation to v1.

This upgrade changes Catalog and Job storage and the Tape script contract. Schedule an outage and retain a complete installation backup throughout Alpha evaluation. The installer displays this exact document from the checksum-verified candidate package before asking to stop the service. Software versions and [data-format revisions](../architecture/persistence.md#published-data-formats) are independent.

## Run the Upgrade

Obtain the versioned installer rather than reusing a script from the old installation:

```shell
curl --fail --location --output install-release.sh \
  https://raw.githubusercontent.com/samuelncui/yatm/v1.0.0-alpha.1/install-release.sh
sudo bash install-release.sh --version v1.0.0-alpha.1 --check
sudo bash install-release.sh --version v1.0.0-alpha.1
```

The default channel remains stable; Alpha requires `--version`. See [installation](install.md#install-or-update) for prerequisites, custom installation/unit paths, and local candidate archives. `--check` downloads and verifies the candidate and reads installation metadata without stopping the service, quiescing Jobs, creating databases, or leaving an upgrade attempt in the installation.

Automatic upgrade supports Linux amd64/systemd with SQLite and configuration, Job storage, scripts, helpers and captured indexes contained in the installation. External databases or storage require coordinated manual backup. The preflight reports passed checks, manual checks and blockers. Script existence is not evidence of Tape compatibility. Busy operations, unknown versions, missing migration instructions, invalid packages and unsupported layouts stop before service interruption. Originals, Restore output and archive Media are not software migration data; mixed layouts that cannot be classified must be resolved first.

The actual upgrade shows its retained report directory before the first confirmation. Review the current/target versions, findings, backup scope, configuration diff and Settings summary. Cancel or EOF at that prompt leaves the service unchanged. A second confirmation approves a legacy database migration report after the service has stopped and its complete backup exists.

Creating empty Settings tables during migration preserves the approved configuration plan. Actual preference, Location binding or configuration changes still require a new review.

## What Changes

- Library File IDs, logical paths, tags and Note are retained. Confirmed archive evidence creates independent Position signatures and saved FileVersions; uncertain history is reported instead of invented. Opaque signatures retain their bytes, and unknown save times remain unknown.
- Each historical Job becomes a bundle containing its manifest, execution metadata and archived log. A submitted Archive item requires an unambiguous recorded physical copy; pending Restore selections require valid candidates. Legacy Restore output roots retain their original working-directory-relative meaning and are frozen for later execution.
- Migration writes the current Catalog and Job bundle revision 1 directly, including the current Position parent index and signed nanosecond timestamps. Legacy instants retain their precision, lower digits are zero-filled, and out-of-range dates fail conversion instead of wrapping. Existing pre-stable v1 data is not upgraded in place; the [timestamp contract](../architecture/persistence.md#timestamps) defines current encoding and unknown values.
- Captured legacy LTFS indexes supply validated order/extents for exact path/size matches. Missing or mismatched inventory is reported. Physical storage profiles retain their own `ltfs_v0`/`ltfs_v1` identities.
- The reviewed conversion moves legacy `paths.source` and `paths.target` to Locations and removes them from YAML; the old target becomes a preferred Restore destination. Existing registrations and user removals take precedence. This registration does not scan contents or move Library organization. [Location configuration](install.md#location-configuration-and-migration) owns authorization and subsequent editing.
- Preview generator preferences move to Settings when absent; saved preferences take precedence. Deployment paths, scripts, helpers, permissions, service working directory and unrelated configuration remain in place. Release-owned programs, frontend, documentation, licenses, Skill and templates are replaced completely. Default script templates are deployed only for a fresh installation.

## Tape Script Adaptation

Scripts are the environment adaptation boundary: they own LTFS executable paths, device mapping, vendor options and local helper logic. Preserve that customization. Installation checks never run Tape scripts, mount a cartridge, format Media or establish physical Tape readiness.

Complete data migration **before changing the legacy mount script's captured-index output directory**. Prepare reads the old script/configuration to locate existing index evidence. Keep that directory and its contents if any retained script still uses it. After migration, adapt scripts manually and validate them before the first Tape Job.

| Input | Meaning |
| --- | --- |
| `DEVICE` | Configured Tape device; helpers may resolve its SCSI generic mapping. |
| `KEY_FILE` | Encryption key file supplied to the encryption script. |
| `MOUNT_POINT` | Mount destination for the current operation. |
| `TAPE_BARCODE`, `TAPE_NAME` | Cartridge identity/name passed to formatting and encryption scripts. |
| `TAPE_DIR` | Per-Job, per-cartridge artifact directory once identity is known; write captured indexes and LTFS diagnostics here. |
| `OUT` | Output filename for the device-information script's JSON result. |

The mount script must make the current cartridge's captured Index available as `TAPE_DIR/<barcode>.schema`, where `<barcode>` is the mounted cartridge identity. Mount/unmount receive `TAPE_DIR`, not the formatting script's barcode/name inputs. The final Index must describe the completed write before YATM publishes it. Retain vendor-specific commands while updating their output destination; for an LTFS implementation supporting the bundled options, the relevant change is:

```diff
- -o work_directory=/opt/yatm/captured_indices -o capture_index
+ -o work_directory="${TAPE_DIR}" -o capture_index
```

Create `TAPE_DIR`, then resolve it to an absolute path before passing LTFS its directory options, so background execution does not reinterpret a relative `paths.work`:

```shell
mkdir -p -- "${TAPE_DIR}"
TAPE_DIR=$(cd -- "${TAPE_DIR}" && pwd -P)
```

The [bundled mount template](../../scripts/mount) illustrates this contract; its flags are not universal across LTFS distributions. The installer preserves customized active scripts; apply the relevant template changes manually.

Unmount must return only after final Index output is complete, the mount has ended, and the Tape device has been released. A successful `umount` invocation alone may precede background LTFS cleanup. An ejecting setup must also wait for its configured eject completion condition. The [bundled unmount template](../../scripts/umount) shows one bounded wait implementation; retain the equivalent behavior appropriate to the local environment. Nonzero exit or timeout reports failure rather than claiming successful publication.

Service startup acceptance and Tape acceptance are separate outcomes. A successful installer does not certify customized scripts or physical hardware. Use the [physical Tape procedure](physical-tape-e2e.md) only with explicitly assigned scratch Media.

## Backup Layout and Upgrade Phases

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
      upgrade.log
      migration.json    # when legacy database migration is needed
```

Every replacement or configuration/data update retains a complete compressed installation, including hidden files, databases, Jobs, logs and custom files. Only top-level `.backup/` is excluded. The unique attempt directory and reports have restrictive permissions; `.backup` must be a real directory. Archive creation streams through GNU tar, preserving ownership, modes, modification times, links and supported ACLs/xattrs. Content comparison and a second deterministic archive stream verify the stopped source before the final archive and checksum are published. Filesystem access/change times are not restored.

Automatic preflight accepts relative symbolic links whose resolved targets stay inside the archived installation. It rejects absolute symbolic links even when they currently point inside that directory, and links into the excluded `.backup` tree. Such layouts need a reviewed manual backup before upgrade. Initial inspection runs in temporary scratch before the installer creates reports or locks inside the installation; accepted relative links retain their targets in the extracted backup.

Space checks cover the complete installation, candidate and migration work on the installation filesystem. External databases, work directories or symlink targets require coordinated manual backup. `.backup/` is excluded from live Location access and Scan. Existing `.yatm-upgrades` enters the first complete archive; after successful acceptance, recognized installer attempts are removed and unrecognized contents are reported and retained.

| Phase | Result and failure boundary |
| --- | --- |
| Verify and preflight | Verified candidate, version/layout checks, YAML diff, Settings changes and retained attempt log; no service interruption before consent. |
| Stop and backup | Recheck active work, quiesce supported service admission, stop normally, revalidate the reviewed inputs, and create/verify the complete archive before changing data. |
| Prepare | Create staging tables and complete Job bundles; retain the JSON report on success, failure or cancellation. Existing legacy database rows remain available until Commit. |
| Report approval | Review reconciliation and every historical Job/item. Declining or failing Prepare removes only prepared output through Abort before the old service can restart. |
| Commit and validate | Activate current tables, then compare migrated Library, every historical Job/item and logs against the explicit complete backup. An uncertain Commit or failed validation keeps the service stopped. |
| Cleanup and replace | Remove confirmed obsolete active tables, old Job storage and transferred logs. Apply the approved configuration/Settings conversion after legacy evidence has been consumed. Replace release-owned files/trees completely. |
| Start and accept | Check [service-bound readiness](install.md#install-or-update), Library, Jobs, served assets and version identity. Remove successful-attempt `.work/` scratch. Report installation readiness separately from manual Tape adaptation. |

`upgrade.log` records the attempt; `config.diff` retains the reviewed YAML changes. Migration additionally retains `migration.json`. Candidate downloads, extracted releases, private review JSON and extracted legacy evidence live in `.work/`; success removes this scratch. Failures retain relevant evidence and the failed stage. A `.partial` archive is not a recovery backup. Failures before mutation restore the original service state; failures after mutation keep it stopped. A declined Prepare first aborts its staging output before restoring the old service.

Same-version reruns compare version, commit, complete managed resources and configuration conversion needs. A truly unchanged run verifies readiness and offers the [optional Skill](install.md#optional-agent-skill) without making another archive. Same-version candidate replacements use the full backup flow. Initial installations have no old installation to archive.

The installer has no durable phase-resume protocol. A previously unfinished `.yatm-upgrades/migration.pending.json` blocks entry: finish with its original installer or restore its backup first. After an uncertain mutation in the new flow, recover the complete archive before starting another upgrade.

## Configuration Review

`yatm-migrate -phase config-plan -plan-file <private-file>` generates a review without changing the installation. It contains a unified YAML diff, proposed Location imports, preserved Settings and resolved Preview preferences. The installer displays these before consent. `config-check` rechecks the exact file, all three Settings groups and relevant Locations after stop; any presence or value change invalidates the review. `config-apply -service-stopped --confirm` publishes the reviewed preferences and rewrites YAML after the backup and required legacy migration.

Missing or null `paths.access` is replaced with explicit roots equivalent to the legacy source/target authorization. Explicit access rules, including `[]`, are unchanged. Relative paths retain their original service-working-directory meaning. Equal source/target roots share one registration. Successful conversion removes the legacy YAML fields; subsequent startup does not import them or recreate deleted registrations. No persistent import checkpoint is stored. Each saved Settings group is detected independently; a saved Preview group wins, while an absent Preview group receives the resolved legacy generator preferences. Location identity/name/Ignore/restore preference, unrelated YAML fields/comments and file permissions are retained. Unsupported ambiguous YAML blocks conversion before stop. An already converted configuration has no legacy fields to import; new installations use current configuration and default Settings.

## Offline Migration and Repair

The offline sequence below supports a contained SQLite installation, with the same backup-root mapping as the installer. Stop the service and take a verified complete backup first. External databases/work roots need coordinated backup. Run the verified candidate's `yatm-migrate` from the original working directory, with its original configuration and captured indexes intact. Verify the archive checksum, then extract it with GNU tar (`--acls --xattrs --xattrs-include='*' --numeric-owner`) into a private temporary directory under `.backup/`. Use that directory as `yatm_backup_dir`; the immutable archive itself is not a `-backup-root` directory. Replace the placeholders below with the candidate, report and extracted-backup directories:

```shell
cd /opt/yatm
"$yatm_release_dir/yatm-migrate" -config ./config.yaml -phase preflight -service-stopped
"$yatm_release_dir/yatm-migrate" -config ./config.yaml -phase prepare \
  -report-file "$yatm_reports_dir/migration.json"
# Review the report and historical manifests before continuing.
"$yatm_release_dir/yatm-migrate" -config ./config.yaml -phase commit --confirm
"$yatm_release_dir/yatm-migrate" -config ./config.yaml -phase validate \
  -install-root /opt/yatm -backup-root "$yatm_backup_dir"
"$yatm_release_dir/yatm-migrate" -config ./config.yaml -phase cleanup \
  -install-root /opt/yatm -backup-root "$yatm_backup_dir" --confirm
```

On a failed or declined Prepare, use `-phase abort --confirm`; restart the legacy service only after Abort succeeds. Keep the report. Validate and clean up before resuming business activity: later Library edits or Job progress can legitimately differ from the migration baseline. Apply the previously reviewed configuration only after validation/cleanup. Cleanup is repeatable and removes only verified obsolete active artifacts, not the archive. [Migration implementation](../../internal/migrate/legacy/migrate.go) owns conversion and validation details.

Historical Job repair reads evidence from the explicitly selected backup, not legacy tables retained indefinitely in the active Catalog:

```shell
./yatm-migrate -config ./config.yaml -phase repair-job -job-id 123 \
  -install-root /opt/yatm -backup-root "$yatm_backup_dir" --confirm
```

Stop the service and inspect the selected Job before repair. A previously frozen Restore root takes precedence over later configuration edits. A replaced Job bundle is retained beside the extracted backup under `jobs-before-repair/<id>`. Move this repair evidence to the retained attempt directory before removing extraction scratch, and verify all repaired items before resuming the Job. The original archive remains unchanged.

## Failure and Complete-Backup Recovery

Program/resource replacement is not atomic. Interruption can leave a mixed tree; once Commit is uncertain or replacement has begun, keep the service stopped. Replacing only a binary or only a database is not a rollback.

1. Stop the service. Select the complete `yatm.tar.gz` recorded in the attempt report and verify its companion SHA-256 and archive readability.
2. Freeze a list of the current installation's top-level entries, excluding only `.backup`. Move those entries into a unique failed-installation directory under `.backup/`, retaining failed data and diagnostics.
3. Extract the selected archive into the **same installation root** using GNU tar with ownership, permissions, ACL and xattr restoration. Keep `.backup/` in place; never remove or move the root containing it. Restore coordinated external resources and matching service registration if applicable, then reload systemd.
4. Start the backed-up version and compare its Library, historical manifests and configuration with the pre-upgrade inventory.

Do not overlay the backup on a partly upgraded active tree, as that leaves new-only files behind. Rollback restores software and metadata; it does not undo file moves, deletions, Restore outputs or Media writes performed after the new version started. Complete-backup deletion is a separate, explicit operator decision after acceptance and the desired retention period.
