# Upgrade from v0.1.x to v1

Status: Current supported installer procedure for upgrading a v0.1.x installation to v1.

This upgrade changes Catalog and Job storage and the Tape script contract. Schedule an outage and retain a complete installation backup throughout Alpha evaluation. The installer displays this exact document from the checksum-verified candidate package before asking to stop the service. Software versions and [data-format revisions](../architecture/persistence.md#published-data-formats) are independent.

## Run the Upgrade

Obtain the versioned installer rather than reusing a script from the old installation:

```shell
curl --fail --location --output install-release.sh \
  https://raw.githubusercontent.com/samuelncui/yatm/v1.0.0-alpha.3/install-release.sh
sudo bash install-release.sh --version v1.0.0-alpha.3 --check
sudo bash install-release.sh --version v1.0.0-alpha.3
```

The default channel remains stable; Alpha requires `--version`. See [installation](install.md#install-or-update) for prerequisites, custom installation/unit paths, and local candidate archives. `--check` downloads and verifies the candidate and reads installation metadata without stopping the service, quiescing Jobs, creating databases, or leaving an upgrade attempt in the installation.

Automatic upgrade supports Linux amd64/systemd with SQLite and configuration, Job storage, scripts, helpers and captured indexes contained in the installation. The [offline migration procedure](offline-migration.md) has the same contained-layout requirement. External resources need coordinated backup and a layout-specific migration procedure before Prepare or Commit; this guide does not provide that procedure. The preflight reports passed checks, manual checks and blockers. Script existence is not evidence of Tape compatibility. Busy operations, unknown versions, missing migration instructions, invalid packages and unsupported layouts stop before service interruption. Originals, Restore output and archive Media are not software migration data; mixed layouts that cannot be classified must be resolved first.

The actual upgrade shows its retained report directory before the first confirmation. Review the current/target versions, findings, backup scope, configuration diff and Settings summary. Cancel or EOF at that prompt leaves the service unchanged. A second confirmation approves a legacy database migration report after the service has stopped and its complete backup exists.

Creating empty Settings tables during migration preserves the approved configuration plan. Actual preference, Location binding or configuration changes still require a new review.

## What Changes

- Library File IDs, logical paths, tags and Note are retained. Confirmed archive evidence creates independent Position signatures and saved FileVersions; uncertain history is reported instead of invented. Opaque signatures retain their bytes, and unknown save times remain unknown.
- Each historical Job becomes a bundle containing its manifest, execution metadata and archived log. A submitted Archive item requires an unambiguous recorded physical copy; pending Restore selections require valid candidates. Legacy Restore output roots retain their original working-directory-relative meaning and are frozen for later execution.
- Migration writes the current Catalog and Job bundle revision 1 directly, including the current Position parent index and signed nanosecond timestamps. Legacy instants retain their precision, lower digits are zero-filled, and out-of-range dates fail conversion instead of wrapping. Existing pre-stable v1 data is not upgraded in place; the [timestamp contract](../architecture/persistence.md#timestamps) defines current encoding and unknown values.
- Captured legacy LTFS indexes supply validated order/extents for exact path/size matches. Missing or mismatched inventory is reported. Physical storage profiles retain their own `ltfs_v0`/`ltfs_v1` identities.
- The reviewed conversion moves legacy `paths.source` and `paths.target` to Locations and removes them from YAML; the old target becomes a preferred Restore destination. Existing registrations and user removals take precedence. This registration does not scan contents or move Library organization. [Location configuration](locations.md) owns authorization and subsequent editing.
- Preview generator preferences move to Settings when absent; saved preferences take precedence. Deployment paths, scripts, helpers, permissions, service working directory and unrelated configuration remain in place. Release-owned programs, frontend, documentation, licenses, Skill and templates are replaced completely. Default script templates are deployed only for a fresh installation.

## Tape Script Adaptation

**Action required before the first Tape Job:** v1 requires a completed captured Index at
`TAPE_DIR/<barcode>.schema`. A legacy script that writes elsewhere or captures no Index is
incompatible until adapted. Keep the old capture directory through migration, then update the
mount output and ensure unmount waits for final Index output and device release as described below.
The installer displays this notice before upgrade and preserves active scripts for manual adaptation.

Complete data migration **before changing the legacy mount script's captured-index output directory**. Prepare reads the old script/configuration to locate existing index evidence. Keep that directory and its contents if any retained script still uses it. After migration, adapt scripts manually and validate them before the first Tape Job.

Update the mount output destination while retaining vendor-specific commands: replace the fixed
`-o work_directory=/opt/yatm/captured_indices` with `-o work_directory="${TAPE_DIR}"` and use the
index capture arguments for the installed LTFS distribution from the
[mount template table](tape-scripts.md#mount-and-unmount).

The installer preserves customized active scripts; apply the relevant [bundled template](../../scripts/mount)
changes manually. [Tape scripts](tape-scripts.md) owns the complete input, output, mount and unmount contract.

Service startup acceptance and Tape acceptance are separate outcomes. A successful installer does not certify customized scripts or physical hardware.

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
| Commit | Activate current tables. An uncertain Commit keeps the service stopped. |
| Cleanup and replace | Cleanup fully compares migrated Library, every historical Job/item and logs against the explicit complete backup before removing obsolete active tables, old Job storage and transferred logs. Failed validation or cleanup keeps the service stopped. Apply the approved configuration/Settings conversion after legacy evidence has been consumed, then replace release-owned files/trees completely. |
| Start and accept | Check [service-bound readiness](install.md#install-or-update), Library, Jobs, served assets and version identity. Remove successful-attempt `.work/` scratch. Report installation readiness separately from manual Tape adaptation. |

`upgrade.log` records the attempt; `config.diff` retains the reviewed YAML changes. Migration additionally retains `migration.json`. Candidate downloads, extracted releases, private review JSON and extracted legacy evidence live in `.work/`; success removes this scratch. Failures retain relevant evidence and the failed stage. A `.partial` archive is not a recovery backup. Failures before mutation restore the original service state; failures after mutation keep it stopped. A declined Prepare first aborts its staging output before restoring the old service.

Same-version reruns compare version, commit, complete managed resources and configuration conversion needs. A truly unchanged run verifies readiness and offers the [optional Skill](install.md#optional-agent-skill) without making another archive. Same-version candidate replacements use the full backup flow. Initial installations have no old installation to archive.

The installer has no durable phase-resume protocol. A previously unfinished `.yatm-upgrades/migration.pending.json` blocks entry: finish with its original installer or restore its backup first. After an uncertain mutation in the new flow, recover the complete archive before starting another upgrade.

## Configuration Review

`yatm-migrate -phase config-plan -plan-file <private-file>` generates a review without changing the installation. It contains a unified YAML diff, proposed Location imports, preserved Settings and resolved Preview preferences. The installer displays these before consent. `config-check -service-stopped -plan-file <private-file>` rechecks the exact file, all three Settings groups and relevant Locations after stop; any presence or value change invalidates the review. `config-apply -service-stopped -plan-file <private-file> --confirm` publishes the reviewed preferences and rewrites YAML after the backup and required legacy migration. All phases use the same `-config` and service working directory.

Missing or null `paths.access` is replaced with explicit roots equivalent to the legacy source/target authorization. Explicit access rules, including `[]`, are unchanged. Relative paths retain their original service-working-directory meaning. Equal source/target roots share one registration. Successful conversion removes the legacy YAML fields; subsequent startup does not import them or recreate deleted registrations. No persistent import checkpoint is stored. Each saved Settings group is detected independently; a saved Preview group wins, while an absent Preview group receives the resolved legacy generator preferences. Location identity/name/Ignore/restore preference, unrelated YAML fields/comments and file permissions are retained. Unsupported ambiguous YAML blocks conversion before stop. An already converted configuration has no legacy fields to import; new installations use current configuration and default Settings.

## Failure and Complete-Backup Recovery

Program/resource replacement is not atomic. Interruption can leave a mixed tree; once Commit is uncertain or replacement has begun, keep the service stopped. Replacing only a binary or only a database is not a rollback.

1. Stop the service. Select the complete `yatm.tar.gz` recorded in the attempt report and verify its companion SHA-256 and archive readability.
2. Freeze a list of the current installation's top-level entries, excluding only `.backup`. Move those entries into a unique failed-installation directory under `.backup/`, retaining failed data and diagnostics.
3. Extract the selected archive into the **same installation root** using GNU tar with ownership, permissions, ACL and xattr restoration. Keep `.backup/` in place; never remove or move the root containing it. Restore coordinated external resources and matching service registration if applicable, then reload systemd.
4. Start the backed-up version and compare its Library, historical manifests and configuration with the pre-upgrade inventory.

Do not overlay the backup on a partly upgraded active tree, as that leaves new-only files behind. Rollback restores software and metadata; it does not undo file moves, deletions, Restore outputs or Media writes performed after the new version started. Complete-backup deletion is a separate, explicit operator decision after acceptance and the desired retention period.
