# Offline Migration and Repair

The support procedure for running a v0.1.x migration phase by phase outside the installer and for
repairing historical Jobs. Ordinary upgrades follow the [upgrade guide](migration.md);
use this only when that guide sends you here or a maintainer directs it.

The offline sequence below supports a contained SQLite installation, with the same backup-root mapping as the installer. Confirm that layout before Prepare or Commit: Cleanup rejects a non-SQLite backup or configuration, Job storage, mount script or captured-index path outside the preserved installation. Additional external backups alone do not make this sequence applicable.

Stop the service and take a verified complete backup. Run the verified candidate's `yatm-migrate` from the original working directory, with its original configuration and captured indexes intact. Verify the archive checksum, then extract it with GNU tar (`--acls --xattrs --xattrs-include='*' --numeric-owner`) into a private temporary directory under `.backup/`. Use that directory as `yatm_backup_dir`; the immutable archive itself is not a `-backup-root` directory. Set `yatm_release_dir`, `yatm_reports_dir` and `yatm_backup_dir` to existing absolute candidate, private report and extracted-backup directories. The configuration plan path below must be new:

```shell
cd /opt/yatm
"$yatm_release_dir/yatm-migrate" -config ./config.yaml -phase preflight \
  -install-root /opt/yatm -service-stopped
"$yatm_release_dir/yatm-migrate" -config ./config.yaml -phase config-plan \
  -plan-file "$yatm_reports_dir/config-plan.json"
# Review the configuration diff, Location imports and preserved Settings before Prepare.
"$yatm_release_dir/yatm-migrate" -config ./config.yaml -phase config-check \
  -plan-file "$yatm_reports_dir/config-plan.json" -service-stopped
"$yatm_release_dir/yatm-migrate" -config ./config.yaml -phase prepare \
  -report-file "$yatm_reports_dir/migration.json"
# Review the report and historical manifests before continuing.
"$yatm_release_dir/yatm-migrate" -config ./config.yaml -phase commit --confirm
"$yatm_release_dir/yatm-migrate" -config ./config.yaml -phase cleanup \
  -install-root /opt/yatm -backup-root "$yatm_backup_dir" --confirm
"$yatm_release_dir/yatm-migrate" -config ./config.yaml -phase config-apply \
  -plan-file "$yatm_reports_dir/config-plan.json" -service-stopped --confirm
```

The installer runs Cleanup once after Commit. Cleanup owns the complete validation before deleting obsolete artifacts. To inspect a committed migration and obtain a validation report separately, use the standalone phase:

```shell
"$yatm_release_dir/yatm-migrate" -config ./config.yaml -phase validate \
  -install-root /opt/yatm -backup-root "$yatm_backup_dir"
```

On a failed or declined Prepare, use `-phase abort --confirm`; restart the legacy service only after Abort succeeds. Keep the report. Complete Cleanup before resuming business activity: later Library edits or Job progress can legitimately differ from the migration baseline. Apply the previously reviewed configuration only after Cleanup succeeds. Then replace release-owned files using the verified package and perform the installation guide's service-bound readiness checks before resuming use. Cleanup is repeatable and removes only verified obsolete active artifacts, not the archive. [Migration implementation](../../internal/migrate/legacy/migrate.go) owns conversion and validation details.

Historical Job repair reads evidence from the explicitly selected backup, not legacy tables retained indefinitely in the active Catalog:

```shell
./yatm-migrate -config ./config.yaml -phase repair-job -job-id 123 \
  -install-root /opt/yatm -backup-root "$yatm_backup_dir" --confirm
```

Stop the service and inspect the selected Job before repair. A previously frozen Restore root takes precedence over later configuration edits. A replaced Job bundle is retained beside the extracted backup under `jobs-before-repair/<id>`. Move this repair evidence to the retained attempt directory before removing extraction scratch, and verify all repaired items before resuming the Job. The original archive remains unchanged.
