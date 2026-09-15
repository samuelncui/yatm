# Installation and Service Configuration

This guide describes v1 Alpha 1. See the [release notes](../releases/v1.0.0-alpha.1.md) for acceptance evidence and limits, and the [migration guide](migration.md) for `v0.1.x` installations.

## Requirements

- Tape workflows require an LTFS-capable LTO drive (LTO-5 or later), its SCSI generic mapping, and compatible LTFS software. Volume-only workflows do not require a Tape drive.
- Linux amd64 is the primary deployment target; the existing deployment scripts have been exercised on Debian 11/12. Other release architectures are experimental, and Windows is unsupported by the Unix-oriented mount workflow.
- Place required tools in PATH or adapt the configured scripts: LTFS formatting/mounting, `mt`, device inspection, and [Stenc](https://github.com/scsitape/stenc) for hardware encryption. The repository's Tape scripts target HPE LTFS conventions; other LTFS implementations may require script changes.
- Preview generators require the tools selected in configuration, including ffmpeg/ffprobe for the bundled image/video generators.

## Install or Update

The [release installer](../../install-release.sh) supports Linux amd64 with systemd. It requires Bash, curl, jq, tar and sha256sum. Run it with an account authorized to manage the installation and service. The default selects the latest stable release; Alpha requires an explicit version. Obtain that revision's installer, check, then install:

```shell
curl --fail --location --output install-release.sh \
  https://raw.githubusercontent.com/samuelncui/yatm/v1.0.0-alpha.1/install-release.sh
bash install-release.sh --version v1.0.0-alpha.1 --check
bash install-release.sh --version v1.0.0-alpha.1
```

For an extracted local candidate, supply its archive and companion checksum; it follows the same installation path:

```shell
bash install-release.sh --version v1.0.0-alpha.1 \
  --archive ./yatm-linux-amd64-v1.0.0-alpha.1.tar.gz \
  --checksum ./yatm-linux-amd64-v1.0.0-alpha.1.tar.gz.sha256 --check
```

`--check` validates the candidate and performs read-only installation checks. Remove it to proceed to the confirmation prompts. `--install-dir` and `--service` select a dedicated installation and unit; `--config` supplies a reviewed configuration for a fresh installation. Otherwise fresh installation opens the configuration template in `$EDITOR` (default `vi`). Review the listener, database, access roots and Preview tools. Volume-only users leave `tape_devices: []`; no fictitious Tape device or script execution is needed to install.

A noninteractive fresh installation requires `--config /path/to/reviewed-config.yaml`. Existing-installation updates preserve their configuration and reject `--config`.

v1 packages require a matching SHA-256 checksum before any included program executes. Recognized official `v0.1.x` releases predate publisher checksums; the installer identifies this limitation before confirmation. An explicitly supplied checksum is always checked. Lower-version installation is rejected; rollback restores a matching complete backup.

For existing installations, the [upgrade guide](migration.md) owns preflight, consent, migration, complete in-root backup and recovery. The installer displays the verified package's guide before crossing a major version. It preserves active configuration, scripts, helpers, permissions, service unit and unknown user files; upgrades do not adopt default script/unit templates. Script checks report manual requirements, not physical Tape compatibility.

Startup acceptance matches the direct-loopback API process identity to the systemd unit's running process before and after checking Library, Jobs and served frontend assets. Another instance occupying the configured port cannot satisfy this check. A same-version rerun verifies the installed identity and offers the optional Skill without replacing programs. Installation does not execute Tape scripts. Ordinary replacement is non-atomic; [recovery](migration.md#failure-and-complete-backup-recovery) restores a complete matching backup.

For a fresh manual installation from a verified archive:

```shell
mkdir -p /opt/yatm
tar -xzf "yatm-linux-amd64-${RELEASE_VERSION}.tar.gz" -C /opt/yatm
cp /opt/yatm/templates/config.example.yaml /opt/yatm/config.yaml
cp -a /opt/yatm/templates/scripts /opt/yatm/scripts
cp /opt/yatm/templates/yatm-httpd.service /opt/yatm/yatm-httpd.service
vim /opt/yatm/config.yaml
systemctl enable /opt/yatm/yatm-httpd.service
systemctl start yatm-httpd.service
```

Review paths, database settings, listener/domain, optional Tape devices/scripts, and Preview settings before starting. Manual commands above are for a new installation only. If installing elsewhere, update the service and scripts for that location. The configuration supports SQLite and an untested MySQL option; per-Job state uses SQLite bundles.

Automatic upgrade covers standard SQLite layouts contained in the installation backup. External databases, Job storage, scripts or captured indexes require the manual procedure with coordinated backup of those resources. Other platform archives use manual installation and carry explicit runtime-validation or experimental labels.

`paths.access` defines administrator-approved directory roots and optional ordered gitignore exclusions. Daily original/Restore directory choices live in **Settings → Locations**, not YAML. `paths.work` owns execution artifacts, and `paths.volumes` contains already-mounted archive Volume discovery roots. Restore destinations are not archive Media. See [Location configuration and migration](#location-configuration-and-migration).

Scripts isolate environment-specific LTFS and device behavior from the service. Review the [script contract and adaptation sequence](migration.md#tape-script-adaptation) before Tape use. The default unmount script ejects the cartridge. Once Archive has finished and the final Index is captured, use physical write protection when retaining a cartridge offline. Retain unexpected LTFS/index diagnostics for investigation.

## Location Configuration and Migration

Every Location supports live browsing and authorized physical operations. **Restore destination** includes it in the UI's preferred-target picker; this preference is not a write permission. CLI/API targets still pass the same authorization checks. Scan, collection and Restore publish observations without whole-directory readiness. Ignore controls automatic collection and recursive selection, not authorization. Settings provides path browsing, registration and inline configuration. Existing registrations are never silently traversed at startup; registration or enabling auto collection can start basic Scan Jobs.

On first startup, legacy `paths.source` and `paths.target` migrate to Locations; the old target is recommended for Restore. Relative paths resolve from the service's working directory, as before. Equal normalized roots combine into one registration. A durable per-Executor checkpoint makes this import idempotent, including after users change or remove a migrated Location. Startup never scans these roots or moves Library nodes. Settings exposes the migration summary and conflicts; conflicts stop initialization rather than silently selecting a different root.

If `paths.access` is omitted, legacy source/target paths supply authorization boundaries. Configure explicit access roots before removing those legacy YAML entries; an explicit empty list permits no directories. YAML never overrides later Location edits. Paths outside allowed roots, symlink descendants, runtime files and overlapping archive storage remain denied. Ignore rules cannot reopen paths outside an allowed root, and an ignored parent requires reopening before a child exception can apply. Revoking access invalidates a frozen Job rather than redirecting it.

Imported paths require local confirmation. Original access additionally needs a current per-file observation; Restore validates the selected path and its existing parent components, and writes can still fail if permissions or space change. Root/Ignore edits rotate the binding token, invalidating old observations and frozen Restore targets. [Legacy migration](migration.md) retains frozen legacy Job inputs and configured Restore roots. Unpublished Draft data is distinct from the [published format families](../architecture/persistence.md#published-data-formats).

## Optional Agent Skill

Fresh installation, successful upgrade and same-version reruns offer the bundled YATM Skill after the service installation succeeds. The existing Skills CLI provides the interactive agent selection and replacement confirmation. It installs a user-level copy, so removing the downloaded package does not remove the Skill.

The installer uses the ordinary invoking user (`SUDO_USER` when applicable). Missing Node/npm, cancellation or Skill installation failure leaves the successful YATM installation intact. It does not install Node/npm. A root-only, unresolved-user or noninteractive session receives a manual command to run after logging in as the ordinary agent user:

```shell
DISABLE_TELEMETRY=1 DO_NOT_TRACK=1 \
  npx --yes --registry=https://registry.npmjs.org skills@1.5.0 \
  add /opt/yatm/skills/yatm --global --copy
```

Run this as the user of the selected agent. `npx --yes` allows fetching the pinned tool; Skills still asks which agents to use and confirms installation. The bundled Skill belongs to the installed YATM release and documents its supported CLI commands.

## Reverse Proxy

YATM serves its control API and frontend through the configured HTTP endpoint with gRPC support. The existing Nginx deployment example below uses TLS and basic authentication; adapt its certificates, authentication file, host, and upstream to the installation:

```nginx
server {
    listen 443 ssl http2;
    listen [::]:443 ssl http2;
    server_name example.com;
    include includes/ssl.conf;

    proxy_connect_timeout 60;
    proxy_send_timeout 3600;
    proxy_read_timeout 3600;
    send_timeout 3600;
    client_max_body_size 4g;
    proxy_buffer_size 1024k;
    proxy_buffers 4 2048k;
    proxy_busy_buffers_size 2048k;
    http2_max_requests 10000000;

    location / {
        auth_basic "restricted";
        auth_basic_user_file includes/passwd;
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_pass http://127.0.0.1:8080;
    }
}
```

See [Library/Volume workflows](library.md) for normal operations and [testing](testing.md) for isolated validation requirements.
