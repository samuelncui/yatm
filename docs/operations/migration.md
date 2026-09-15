# Upgrade from v0.1.x to v1

Status: Supported `v0.1.x` to `v1.0.0-alpha.1` procedure; acceptance evidence and limits are recorded in the [release notes](../releases/v1.0.0-alpha.1.md).

This upgrade changes Catalog and Job storage and the Tape script contract. Schedule an outage and retain a complete installation backup throughout Alpha evaluation. The installer displays this exact document from the checksum-verified candidate package before asking to stop the service. Software versions and [data-format revisions](../architecture/persistence.md#published-data-formats) are independent.

## Run the Upgrade

Obtain the versioned installer rather than reusing a script from the old installation:

```shell
curl --fail --location --output install-release.sh \
  https://raw.githubusercontent.com/samuelncui/yatm/v1.0.0-alpha.1/install-release.sh
bash install-release.sh --version v1.0.0-alpha.1 --check
bash install-release.sh --version v1.0.0-alpha.1
```

The default channel remains stable; Alpha requires `--version`. See [installation](install.md#install-or-update) for prerequisites, custom installation/unit paths, and local candidate archives. `--check` downloads and verifies the candidate and reads installation metadata without stopping the service, quiescing Jobs, creating databases, or leaving an upgrade attempt in the installation.

Automatic upgrade supports Linux amd64/systemd with SQLite and configuration, Job storage, scripts, helpers and captured indexes contained in the installation. External databases or storage require coordinated manual backup. The preflight reports passed checks, manual checks and blockers. Script existence is not evidence of Tape compatibility. Busy operations, unknown versions, missing migration instructions, invalid packages and unsupported layouts stop before service interruption. Originals, Restore output and archive Media are not software migration data; mixed layouts that cannot be classified must be resolved first.

The actual upgrade shows its retained report directory before the first confirmation. Review the current/target versions, findings, backup scope and this guide. Cancel or EOF at that prompt leaves the service unchanged. A second confirmation approves the prepared migration report after the service has stopped and its complete backup exists.

## What Changes

- Library File IDs, logical paths, tags and Note are retained. Confirmed archive evidence creates independent Position signatures and saved FileVersions; uncertain history is reported instead of invented. Opaque signatures retain their bytes, and unknown save times remain unknown.
- Each historical Job becomes a bundle containing its manifest, execution metadata and archived log. A submitted Archive item requires an unambiguous recorded physical copy; pending Restore selections require valid candidates. Legacy Restore output roots retain their original working-directory-relative meaning and are frozen for later execution.
- Captured legacy LTFS indexes supply validated order/extents for exact path/size matches. Missing or mismatched inventory is reported. Physical storage profiles retain their own `ltfs_v0`/`ltfs_v1` identities.
- Legacy `paths.source` and `paths.target` register Locations idempotently on startup; the old target becomes a preferred Restore destination. This registration does not scan their contents or move Library organization. [Location configuration](install.md#location-configuration-and-migration) owns authorization and subsequent editing.
- Active configuration, scripts, helpers, permissions, service working directory and unknown user files remain in place. Release-owned programs, frontend, documentation, licenses, Skill and templates are replaced completely. Default script templates are deployed only for a fresh installation.

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

Create `TAPE_DIR` before using it. The [bundled mount template](../../scripts/mount) illustrates this contract; its flags are not universal across LTFS distributions.

Unmount must return only after final Index output is complete, the mount has ended, and the Tape device has been released. A successful `umount` invocation alone may precede background LTFS cleanup. An ejecting setup must also wait for its configured eject completion condition. The [bundled unmount template](../../scripts/umount) shows one bounded wait implementation; retain the equivalent behavior appropriate to the local environment. Nonzero exit or timeout leaves the Job retryable rather than claiming successful publication.

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
  .yatm-upgrades/
    OWNER
    <timestamp>.XXXXXX/
      v0.1.8.backup/
      release/
      reports/
```

The owned upgrade directory and its reports use restrictive permissions. An unrelated directory at the reserved path is a conflict. Each backup uses a frozen top-level entry list and excludes only the installer-owned `.yatm-upgrades`; later upgrades do not recursively copy previous backups. The backup is verified before migration and is not modified by cleanup or historical Job repair. Free-space checks apply to the installation filesystem, including backup, candidate and migration needs. Upgrade artifacts are excluded from live Location access.

| Phase | Result and failure boundary |
| --- | --- |
| Verify and preflight | Verified candidate, version/layout checks, findings and retained attempt log; no service interruption before consent. |
| Stop and backup | Recheck active work, quiesce supported service admission, stop normally, and create/verify the complete backup before changing data. |
| Prepare | Create staging tables and complete Job bundles; retain the JSON report on success, failure or cancellation. Existing legacy database rows remain available until Commit. |
| Report approval | Review reconciliation and every historical Job/item. Declining or failing Prepare removes only prepared output through Abort before the old service can restart. |
| Commit and validate | Activate current tables, then compare migrated Library, every historical Job/item and logs against the explicit complete backup. An uncertain Commit or failed validation keeps the service stopped. |
| Cleanup and replace | Remove confirmed obsolete active tables, old Job storage and transferred logs; preserve backup and user resources. Replace release-owned files/trees completely. |
| Start and accept | Check [service-bound readiness](install.md#install-or-update), Library, Jobs, served assets and version identity. Report installation readiness separately from manual Tape adaptation. |

`reports/upgrade.log` records the attempt; `preflight.json`, `backup.entries` and `migration.json` retain its findings, backup list and migration result. Cancellation does not erase the attempt report. Same-version reruns check the installed identity/readiness and offer the [optional Skill](install.md#optional-agent-skill) without making another installation backup.

## Offline Migration and Repair

The offline sequence below supports a contained SQLite installation, with the same backup-root mapping as the installer. Stop the service and take a verified complete backup first. External databases/work roots need separately reviewed expert coordination; the backup-root commands reject those layouts instead of guessing a mapping. Run the verified candidate's `yatm-migrate` from the original service working directory, with its original configuration and captured indexes intact. Replace the placeholders below with the retained candidate and complete-backup directories:

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

On a failed or declined Prepare, use `-phase abort --confirm`; restart the legacy service only after Abort succeeds. Keep the external report. Validate and clean up before resuming business activity: later Library edits or Job progress can legitimately differ from the migration baseline. Cleanup is repeatable and removes only verified obsolete active artifacts, not the complete backup. [Migration implementation](../../migrate/legacy/migrate.go) owns conversion and validation details.

Historical Job repair reads evidence from the explicitly selected backup, not legacy tables retained indefinitely in the active Catalog:

```shell
./yatm-migrate -config ./config.yaml -phase repair-job -job-id 123 \
  -install-root /opt/yatm -backup-root "$yatm_backup_dir" --confirm
```

Stop the service and inspect the selected Job before repair. A previously frozen Restore root takes precedence over later configuration edits. A replaced Job bundle is retained beside the selected backup under `jobs-before-repair/<id>`. Retain repair evidence and verify all repaired items before resuming the Job.

## Failure and Complete-Backup Recovery

Program/resource replacement is not atomic. Interruption can leave a mixed tree; once Commit is uncertain or replacement has begun, keep the service stopped. Replacing only a binary or only a database is not a rollback.

1. Select and verify the complete backup recorded in the attempt report. Stop the service.
2. Freeze a list of the current installation's top-level entries, excluding only the owned `.yatm-upgrades`. Move those entries into a new `failed-installation` directory inside the attempt, retaining failed data and diagnostics.
3. Copy the selected complete backup's contents back into the **same installation root**, preserving attributes. Keep `.yatm-upgrades` in place; never remove or move the installation root that contains it. Restore coordinated external resources and the matching service registration if applicable.
4. Start the backed-up version and compare its Library, historical manifests and configuration with the pre-upgrade inventory.

Do not overlay the backup on a partly upgraded active tree, as that leaves new-only files behind. Rollback restores software and metadata; it does not undo file moves, deletions, Restore outputs or Media writes performed after the new version started. Complete-backup deletion is a separate, explicit operator decision after acceptance and the desired retention period.
