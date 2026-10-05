# Installation and Service Configuration

This guide describes the current installer. The examples select the published Alpha 1 release; the same installer accepts an explicitly supplied, checksum-verified local candidate. Existing installations follow the [migration guide](migration.md).

## Requirements

- Tape workflows require an LTFS-capable LTO drive (LTO-5 or later), its SCSI generic mapping, and compatible LTFS software. Volume-only workflows do not require a Tape drive.
- Linux amd64 is the primary deployment target; the existing deployment scripts have been exercised on Debian 11/12. Other release architectures are experimental, and Windows is unsupported by the Unix-oriented mount workflow.
- Place required tools in PATH or adapt the configured scripts: LTFS formatting/mounting, `mt`, device inspection, and [Stenc](https://github.com/scsitape/stenc) for hardware encryption. The repository's Tape scripts target HPE LTFS conventions; other LTFS implementations may require script changes.
- The optional Image/Video Preview helper ships its own private native libraries; a system ffmpeg/ffprobe installation is not required. The main service and existing Preview reads work without this helper. See [native build targets](../../previewworker/build/README.md) for the helper's separate platform requirements.

## Install or Update

The [release installer](../../install-release.sh) supports Linux amd64 with systemd and requires Bash 4.4 or newer. Its `--help` output is the authoritative list of required command-line tools. Run it with an account authorized to write the installation and manage the service. The default `/opt/yatm` installation normally requires `sudo`; a dedicated writable installation and user-owned unit may use an unprivileged account. The default selects the latest stable release; Alpha requires an explicit version. Obtain that revision's installer, check, then install:

```shell
curl --fail --location --output install-release.sh \
  https://raw.githubusercontent.com/samuelncui/yatm/v1.0.0-alpha.1/install-release.sh
sudo bash install-release.sh --version v1.0.0-alpha.1 --check
sudo bash install-release.sh --version v1.0.0-alpha.1
```

For an extracted local candidate, supply its archive and companion checksum; it follows the same installation path:

```shell
sudo bash install-release.sh --version v1.0.0-alpha.1 \
  --archive ./yatm-linux-amd64-v1.0.0-alpha.1.tar.gz \
  --checksum ./yatm-linux-amd64-v1.0.0-alpha.1.tar.gz.sha256 --check
```

`--check` validates the candidate and performs read-only installation checks. Remove it to proceed to the confirmation prompts. `--install-dir` and `--service` select a dedicated installation and unit; `--config` supplies a reviewed configuration for a fresh installation. Otherwise fresh installation opens the configuration template in `$EDITOR` (default `vi`). Review the listener, database and access roots. Volume-only users leave `tape_devices: []`; no fictitious Tape device or script execution is needed to install.

A noninteractive fresh installation requires `--config /path/to/reviewed-config.yaml`. Existing-installation updates preserve their configuration and reject `--config`.

v1 packages require a matching SHA-256 checksum before any included program executes. Recognized official `v0.1.x` releases predate publisher checksums; the installer identifies this limitation before confirmation. An explicitly supplied checksum is always checked. Lower-version installation is rejected; rollback restores a matching complete backup.

For existing installations, the [upgrade guide](migration.md) owns preflight, consent, configuration conversion and recovery. Every mutating update keeps a whole-directory archive under `.backup/<datetime>/`; configuration and Settings changes are reviewed before stop. The installer displays the verified package's guide before crossing a major version. Scripts, helpers, permissions, service unit and unrelated configuration remain in place; upgrades do not adopt default script/unit templates. Script checks report manual requirements, not physical Tape compatibility.

After systemd starts, bind acceptance to the direct-loopback API process identity and the unit's running process; another instance occupying the configured port cannot satisfy the check. The installer verifies Library with `yatm-cli ls`, which reads the Library root without paging flags, then verifies Jobs, served frontend assets and version identity. Recovery waits for both the old unit and its admission endpoint instead of treating normal restart delay as failure. Installation does not execute Tape scripts. Ordinary replacement is non-atomic; [recovery](migration.md#failure-and-complete-backup-recovery) restores a complete matching backup.

For a fresh manual installation from a downloaded archive, verify its companion checksum before extraction. Then install the package and bind acceptance to the versioned programs and the systemd-owned process:

```shell
sha256sum --check "yatm-linux-amd64-${RELEASE_VERSION}.tar.gz.sha256"
sudo mkdir -p /opt/yatm
sudo tar -xzf "yatm-linux-amd64-${RELEASE_VERSION}.tar.gz" -C /opt/yatm
sudo cp /opt/yatm/templates/config.example.yaml /opt/yatm/config.yaml
sudo cp -a /opt/yatm/templates/scripts /opt/yatm/scripts
sudo cp /opt/yatm/templates/yatm-httpd.service /opt/yatm/yatm-httpd.service
sudoedit /opt/yatm/config.yaml
sudo systemctl daemon-reload
sudo systemctl enable /opt/yatm/yatm-httpd.service
sudo systemctl start yatm-httpd.service
/opt/yatm/yatm-httpd --version
/opt/yatm/yatm-cli --version
sudo systemctl show yatm-httpd.service --property MainPID,WorkingDirectory,FragmentPath
/opt/yatm/yatm-cli --server http://127.0.0.1:8080 --timeout 3s status
```

Compare each program's reported version/commit with the package `VERSION` and `COMMIT`, and confirm that the readiness endpoint belongs to the unit's `MainPID`; the automatic installer performs these checks. Review paths, database settings, listener/domain, optional Tape devices/scripts, and Preview settings before starting. Manual commands above are for a new installation only. If installing elsewhere, update the service and scripts for that location. The configuration supports SQLite and an untested MySQL option; per-Job state uses SQLite bundles.

Automatic upgrade covers standard SQLite layouts contained in the installation backup. External databases, Job storage, scripts or captured indexes require the manual procedure with coordinated backup of those resources. Other platform archives use manual installation and carry explicit runtime-validation or experimental labels.

`database.sqlite_wal: true` optionally permits Catalog reads during a write; it defaults to false.
Keep the SQLite database on a local disk even when the service runs on a NAS. Do not place it on an
SMB/NFS share or copy only a live main database file: WAL sidecars may contain committed data.
Use metadata export or stop the service before backing up the complete installation. Turning the
option off restores DELETE journal mode on startup; an unsuccessful switch prevents startup.

`paths.access` defines administrator-approved directory roots and optional ordered gitignore exclusions. Daily original/Restore directory choices live in **Settings → Locations**, not YAML. `paths.work` owns execution artifacts and is relative to the service working directory when not absolute. A relative `preview.root` resolves under it, defaulting to `previews`; an absolute Preview root is used directly. `paths.volumes` contains already-mounted archive Volume discovery roots. Restore destinations are not archive Media. See [Location configuration and migration](#location-configuration-and-migration).

Scripts isolate environment-specific LTFS and device behavior from the service. Review the [script contract and adaptation sequence](migration.md#tape-script-adaptation) before Tape use. The default unmount script ejects the cartridge. Once Archive has finished and the final Index is captured, use physical write protection when retaining a cartridge offline. Retain unexpected LTFS/index diagnostics for investigation.

## Location Configuration and Migration

Every Location supports live browsing and authorized physical operations. **Restore destination** includes it in the UI's preferred-target picker; this preference is not a write permission. CLI/API targets still pass the same authorization checks. Scan and Restore publish observations without whole-directory readiness. Ignore scopes ordinary Location reads and new associations, but never controls authorization and never rewrites existing facts. Settings provides path browsing, registration and inline configuration. Startup, registration, browsing and preference updates do not collect files or create Scan Jobs; use explicit Scan to publish originals.

The installer previews and applies legacy `paths.source` and `paths.target` conversion to Locations, then removes those YAML fields. The old target is recommended for Restore; existing registrations remain authoritative. Relative paths resolve from the original service working directory; equal normalized roots share one registration. Conversion does not scan roots or move Library nodes. Startup does not import these fields, and no migration-history table or Locations-page migration notice is retained.

The installer materializes effective legacy authorization as explicit `paths.access` before removing source/target. Existing explicit access rules remain unchanged; an explicit empty list permits no directories. YAML never overrides later Location edits. Paths outside allowed roots, symlink descendants, runtime files (including `.backup/`) and overlapping archive storage remain denied. Ignore rules cannot reopen paths outside an allowed root, and an ignored parent requires reopening before a child exception can apply. Revoking access invalidates a frozen Job rather than redirecting it.

A registered or imported path is immediately usable. Original access additionally needs a current per-file observation; Restore validates the selected path and its existing parent components, and writes can still fail if permissions or space change. Root/Ignore edits do not retarget existing records: a frozen Restore destination still requires its registration to keep the same root, and later reads resolve recorded paths under the current root. [Legacy migration](migration.md) retains frozen legacy Job inputs and configured Restore roots. Unpublished Draft data is distinct from the [published format families](../architecture/persistence.md#published-data-formats).

## Optional Agent Skill

Fresh installation, successful upgrade and same-version reruns offer the bundled YATM Skill after the service installation succeeds. The existing Skills CLI provides the interactive agent selection and replacement confirmation. It installs a user-level copy, so removing the downloaded package does not remove the Skill.

The installer uses the ordinary invoking user (`SUDO_USER` when applicable). Missing Node/npm, cancellation or Skill installation failure leaves the successful YATM installation intact. It does not install Node/npm. A root-only, unresolved-user or noninteractive session receives a manual command to run after logging in as the ordinary agent user:

```shell
DISABLE_TELEMETRY=1 DO_NOT_TRACK=1 \
  npx --yes --registry=https://registry.npmjs.org skills@1.5.0 \
  add /opt/yatm/skills/yatm --global --copy
```

Run this as the user of the selected agent. `npx --yes` allows fetching the pinned tool; Skills still asks which agents to use and confirms installation. The bundled Skill belongs to the installed YATM release and documents its supported CLI commands.

## Preview Preferences

The installer offers the optional helper before changing the service. `--with-preview` selects it explicitly; `--without-preview` skips it. Noninteractive installation skips it unless explicitly selected. The helper package must match the main package's version and commit and pass its checksum and capability probe before installation. For an unpublished local candidate, add `--preview-archive /path/to/yatm-preview-linux-amd64-VERSION.tar.gz --preview-checksum /path/to/archive.sha256` to the ordinary install command. A same-version rerun can add the helper later. No helper source or system-wide native libraries are installed.

Installing the helper does not enable generation. In **Settings → Preview**, enable generation, choose Image or Video and edit the corresponding extensions and output options. Shared controls set concurrent files, timeout, input-pixel limit and an optional helper command override. Disabled controls preserve their values. Existing Preview assets remain accessible when generation is disabled or the helper is absent. Check installation independently with `yatm-cli preview capabilities`.

The installer previews legacy generator import when no saved preferences exist, preserves saved Settings, and removes `preview.generators` from YAML. Startup does not import these options; manual upgrades must run the explicit configuration conversion. The asset storage root remains deployment configuration. See the [Preview contract](../architecture/preview.md#generation) for task snapshots, enablement and resource limits.

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
