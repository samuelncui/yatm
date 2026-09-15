# legacy to v1 Migration

Status: v1 Alpha 1 upgrade procedure. Alpha is a prerelease; retain a complete installation backup throughout evaluation.

legacy is frozen at `origin/main@89685cc` (`v0.1.21`). Migration requires a planned outage and a complete, tested installation backup: databases, configuration, scripts, captured indexes, and historical Job files. [Test-environment restrictions](testing.md#media-and-migration-safety) apply to validation copies.

## Recommended Upgrade

The Linux amd64/systemd [installer](install.md#install-or-update) performs the upgrade after explicit confirmation:

```shell
bash install-release.sh --version v1.0.0-alpha.1 --check
bash install-release.sh --version v1.0.0-alpha.1
```

Use the installer from the selected release. Review its current/target versions, backup scope, script findings and interruption notice. A legacy upgrade also asks you to approve the Prepare report before Commit. Cancellation or EOF before activation retains the current installation. Same-version reruns check the installation and offer its bundled Skill.

Automatic upgrade supports SQLite installations whose database, Job storage, configuration, scripts, service unit and captured indexes fit inside the installation backup. External resources and other database engines require coordinated manual backup and upgrade. The installer reports these layouts before service interruption. [Configuration ownership](install.md#install-or-update) covers preserved files and optional template adoption.

## Phases and Guarantees

- **Preflight:** inspect configuration, formats, bundles, active operations, backup coverage and script contracts without creating databases or modifying service admission. Jobs performing an operation such as copying block upgrade; waiting-for-Media and stable paused states are eligible. After consent, recheck and quiesce v1 admission, stop the service normally, and take the complete backup. The existing operation rollback semantics remain the recovery basis.
- **Prepare:** [migration code](../../migrate/legacy/migrate.go) creates v1 staging tables and complete `work/jobs/<job_id>/state.db` bundles without changing active legacy database rows. An existing legacy Job directory is preserved by the migration's directory handling. Copied legacy protobuf definitions remain in the migration-only package.
- **Library reconciliation:** [File/Media conversion](../../migrate/legacy/library.go) preserves File IDs and logical organization. Confirmed archive facts populate independent Position signatures and FileVersions; non-empty bytes are preserved verbatim and unset signatures use NULL. Different Files may share content without merging. Isolated File signatures do not fabricate archive history: uncertain facts are reported and retained in the source backups. Unknown archive times remain unknown.
- **Tape facts:** use the legacy mount configuration's captured-index directory. Exact path/size matches receive validated LTFS order/extents; missing or mismatched Positions are omitted. A reconciled Tape becomes `ltfs_v1`; missing/empty captured indexes retain `ltfs_v0` compatibility ordering. Rebuild derived directory rows from retained physical files.
- **Jobs:** preserve manifest content and recover empty/dirty records into retryable states. Retain a submitted Archive item only when its path/size identify one physical Position among Media recovered from its old Job log. Fail prepare when a pending Restore item loses every valid candidate. Prepare freezes the legacy configured Restore target after resolving its original working-directory-relative meaning; repair preserves an already frozen root over later configuration changes. Neither creates that directory during migration. Missing authorized output descendants can be created by execution. A missing configured target is reported instead of guessing where to write.
- **Commit:** require a successful report and explicit confirmation. Swap staged tables into service while retaining `jobs_legacy`, `files_legacy`, `tapes_legacy`, and `positions_legacy`. Per-Job bundles are already at their final paths.
- **Abort/Cleanup:** Abort removes migration-owned prepared output and preserves legacy data; repeat prepare remains supported. Cleanup removes retained legacy backups only after separate confirmation and successful validation. A failed signature check is not permission to edit or merge the original Files.

The [runtime persistence model](../architecture/persistence.md#published-data-formats) defines the Alpha Catalog, Job bundle and Library backup families. legacy migration/import remains supported. Alpha 1 establishes the published v1 baseline; unpublished Draft data is rejected with its original contents retained. A downgrade restores the matching complete backup.

## Script Review

legacy Tape script contracts must be reviewed before stopping the service. The current scripts capture the final LTFS Index and complete mount/unmount cleanup before returning. The preflight report identifies the configured scripts and expected changes. Choose the supplied templates explicitly or adapt retained custom scripts before proceeding.

Prepare reads the original legacy configuration and mount-script captured-index location before template replacement. Preserve its working-directory-relative path meaning. Legacy `paths.source` and `paths.target` become Locations idempotently on startup; the old target becomes a preferred Restore destination. This conversion does not scan directories or rearrange the Library.

## Manual Upgrade

Use a checksum-verified release archive. The example below assumes the standard installation layout; include every external database, script, bundle and captured-index directory in the coordinated backup for a custom layout. Run the candidate migrator from the original service working directory:

```shell
yatm_release_dir="$(mktemp -d /tmp/yatm-v1-release.XXXXXX)"
tar -xzf "yatm-linux-amd64-${RELEASE_VERSION}.tar.gz" -C "$yatm_release_dir"
cd /opt/yatm

"$yatm_release_dir/yatm-migrate" -config ./config.yaml -phase preflight
systemctl stop yatm-httpd.service
cp -a /opt/yatm "/opt/yatm.bak.$(date +%Y%m%d%H%M%S)"
"$yatm_release_dir/yatm-migrate" -config ./config.yaml -phase prepare
```

Inspect the report, including omitted inventory and uncertain history, and verify prepared Library and Job manifests item-by-item before committing. On a failed or declined Prepare, run `-phase abort --confirm`; restart legacy only after Abort succeeds. Keep the old configuration and scripts in place through Commit.

```shell
"$yatm_release_dir/yatm-migrate" -config ./config.yaml -phase commit --confirm
```

Replace only the managed programs (`yatm-httpd`, `yatm-cli`, `yatm-export-library`, `yatm-lto-info`, `yatm-migrate`), frontend, version files, documentation, licenses, bundled Skill and templates. Preserve active `config.yaml`, scripts, unit, unknown files and backups. Adopt reviewed templates separately. Do not overlay the entire archive onto a customized installation.

Start the service, then verify `yatm-cli --version`, `yatm-cli info`, Library listing, all historical Jobs and frontend resources. The offline `-phase frontend-check` checks rendered assets using the selected configuration. Review pending legacy Jobs before resuming them; migration validation itself does not mount Media or execute content Jobs.

## Failure and Complete-Backup Recovery

Installation uses ordinary file replacement, not an atomic program-tree switch. An interruption can leave a mixed tree. Once Commit is uncertain or replacement has begun, the installer leaves the service stopped and reports its phase and backup location. Do not start either version against an uncertain mixture.

1. Stop the service and retain the failed installation and migration report for diagnosis.
2. Restore the complete timestamped installation backup, including matching executables, database, Job bundles, configuration, scripts and captured indexes. Restore any coordinated external resources as well.
3. Start the backed-up version and compare Library entries and historical Job manifests with the pre-upgrade inventory.

Rollback means restoring that complete set; replacing only the executable or only the database is insufficient. Source originals and archive Media are not copied by the metadata upgrade and must retain their existing protection.

## Backup Retention

After checking the running v1 service and migrated items, separately authorize retained-backup cleanup:

```shell
./yatm-migrate -config ./config.yaml -phase cleanup --confirm
```

Keep the complete installation backup independently of migration table cleanup. Backup deletion is a separate operator decision after successful validation.
