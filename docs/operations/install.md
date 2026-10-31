# Installation and Service Configuration

This guide describes the current installer. The examples select `v1.0.0-alpha.3`; the same installer accepts an explicitly supplied, checksum-verified local candidate. Existing installations follow the [migration guide](migration.md).

## Requirements

- LTFS Tape workflows require an LTFS-capable LTO drive (LTO-5 or later), its SCSI generic mapping, compatible LTFS software and the configured adapters. Raw tar Tape uses the Linux protocol requirements below and does not require LTFS. Volume-only workflows do not require a Tape drive.
- Linux amd64 is the primary deployment target; the existing deployment scripts have been exercised on Debian 11/12. Other release architectures are experimental, and Windows is unsupported by the Unix-oriented mount workflow.
- Historical CPUs such as Pentium III are outside the maintained runtime targets. Source ports to these systems are user-maintained; a Linux 386 experimental archive does not establish support for every 32-bit x86 CPU.
- Place the required tools in PATH or adapt the configured scripts: LTFS formatting/mounting, `mt`, `fuser` from psmisc and device inspection where applicable. Both tar and LTFS use `scripts.encrypt` for encryption setup; the bundled adapter uses [Stenc](https://github.com/scsitape/stenc). The bundled unmount adapter uses `fuser` to verify device release. The repository's Tape scripts target HPE LTFS conventions; other LTFS implementations may require script changes.
- The optional Image/Video Preview helper ships its own private native libraries; a system ffmpeg/ffprobe installation is not required. The main service and existing Preview reads work without this helper. See [native build targets](../../previewworker/build/README.md) for the helper's separate platform requirements.

Tape device access, raw tar protocol requirements and device diagnostics are described in
[Device requirements](devices.md#device-requirements). Installation never runs raw preparation or
hardware qualification, and public-tool recovery needs its own
[reader qualification](tar-tape-recovery.md).

## Install or Update

The [release installer](../../install-release.sh) supports Linux amd64 with systemd and Bash 4.4 or newer; `--help` lists the command-line tools it needs. Run it with `sudo`, or an account that can write the installation directory and manage system-level systemd units; it does not manage `systemctl --user` units. The default selects the latest stable release; Alpha requires an explicit version. Download that revision's installer, run the read-only check, then install:

```shell
curl --fail --location --output install-release.sh \
  https://raw.githubusercontent.com/samuelncui/yatm/v1.0.0-alpha.3/install-release.sh
sudo bash install-release.sh --version v1.0.0-alpha.3 --check
sudo bash install-release.sh --version v1.0.0-alpha.3
```

`--check` verifies the package and the installation without changing anything. The second command asks for confirmation before it changes anything.

**Fresh installation.** The installer opens the configuration template in `$EDITOR` (default `vi`); `--config FILE` supplies a reviewed file instead and is required without a terminal. Review the listener, database and access roots. To open the UI from another computer, set `listen` to a reachable address such as `0.0.0.0:8080`, or put a [reverse proxy](#reverse-proxy) in front; the default `domain: ""` follows the page's own address. Volume-only users leave `tape_devices: []`. The installer then installs the programs, Tape script templates and systemd unit, starts the service and checks that it answers.

**Existing installation.** Upgrades keep `config.yaml`, Tape scripts, helpers, the service unit and other local files, and reject `--config`. Before stopping the service, the installer shows configuration and Settings changes and how each configured Tape script differs from the bundled template; it never replaces a script. Each upgrade keeps a backup of the installation, without Previews, under `.backup/<datetime>/`; [Recovery](migration.md#failure-and-complete-backup-recovery) restores one and removes old ones. Upgrades from v0.1.x follow the [upgrade guide](migration.md), which the installer displays before stopping the service.

**Options.** `--install-dir` and `--service` select a dedicated installation and unit. For a local candidate, supply its archive and companion checksum:

```shell
sudo bash install-release.sh --version v1.0.0-alpha.3 \
  --archive ./yatm-linux-amd64-v1.0.0-alpha.3.tar.gz \
  --checksum ./yatm-linux-amd64-v1.0.0-alpha.3.tar.gz.sha256 --check
```

v1 packages require a matching SHA-256 checksum before any included program executes. Recognized official `v0.1.x` releases predate publisher checksums; the installer identifies this limitation before confirmation. An explicitly supplied checksum is always checked. Alpha, Beta and RC versions accept dot-separated numeric revisions, such as `alpha.2.1`; revisions compare numerically and a patch follows its parent. Lower-version installation is rejected; rollback restores a matching complete backup.

Installation never runs Tape scripts. A successful installation does not certify customized scripts or physical hardware.

For a fresh manual installation from a downloaded archive, verify its companion checksum before extraction. Then install the package and bind acceptance to the versioned programs and the systemd-owned process:

```shell
RELEASE_VERSION=v1.0.0-alpha.3
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

The installer keeps programs inside the chosen installation directory. For the bare `yatm-cli`
commands in the daily guides, add that directory to the current shell's PATH:

```shell
export PATH="/opt/yatm:$PATH"
yatm-cli --version
```

Use the directory selected by `--install-dir` when different, or invoke its full `yatm-cli` path.
Persist that PATH setting in your shell profile if desired.

Compare each program's reported version/commit with the package `VERSION` and `COMMIT`, and confirm that the readiness endpoint belongs to the unit's `MainPID`; the automatic installer performs [these checks](migration.md#readiness-checks). Review paths, database settings, listener/domain, optional Tape devices/scripts, and Preview settings before starting. Manual commands above are for a new installation only. If installing elsewhere, update the service and scripts for that location. The configuration supports SQLite and an untested MySQL option; per-Job state uses SQLite bundles.

Automatic upgrade and the documented offline migration cover standard SQLite layouts contained in the installation backup. External databases, Job storage, scripts or captured indexes need a layout-specific migration procedure and coordinated backup before Prepare or Commit; the contained-layout command sequence does not cover them. See the [offline migration scope](offline-migration.md#offline-migration-and-repair). Other platform archives use manual installation and carry explicit runtime-validation or experimental labels.

`database.sqlite_wal: true` optionally permits Catalog reads during a write; it defaults to false.
Keep the SQLite database on a local disk even when the service runs on a NAS. Do not place it on an
SMB/NFS share; [Backups](#backups) explains how to copy it safely. Turning the
option off restores DELETE journal mode on startup; an unsuccessful switch prevents startup.

`paths.access` defines administrator-approved directory roots and optional ordered gitignore exclusions. Daily original/Restore directory choices live in **Settings → Locations**, not YAML. `paths.work` owns execution artifacts and is relative to the service working directory when not absolute. A relative `preview.root` resolves under it, defaulting to `previews`; an absolute Preview root is used directly. `paths.volumes` contains already-mounted archive Volume discovery roots. Restore destinations are not archive Media. See [Location configuration and migration](#locations).

For LTFS, scripts isolate environment-specific LTFS and device behavior from the service. Operators may edit configured scripts and helpers for local tool versions, platforms and hardware; bundled commands are starting points. Review the [Tape script contract](tape-scripts.md) before Tape use. The default unmount script ejects the cartridge. Once Archive has finished and the final Index is captured, use physical write protection when retaining a cartridge offline. Retain unexpected LTFS/index diagnostics for investigation.

## Locations

Register original directories and Restore destinations after installation in the
[Location guide](locations.md). An upgrade from v0.1.x converts legacy `paths.source` and
`paths.target`, as listed in [What Changes](migration.md#what-changes).

## Backups

Two things are called a backup:

| Backup | Contains | Owner |
| --- | --- | --- |
| Installation backup | The installation directory except Previews, taken by the installer before an upgrade | [Backup layout](migration.md#backup-layout-and-upgrade-phases) |
| Library metadata backup | Library, Locations, original links and saved Media keys, as JSONL; no file contents or global Settings | [Metadata backup](library.md#metadata-backup) |

File contents on Tape or Volume Media are archived copies, not backups; see
[Archive, Restore and Scan](jobs.md).

To back up a running installation, export Library metadata or stop the service and copy the
complete installation. Installation backups leave out Previews: a Scan regenerates them from originals, but content whose only copy is on Tape needs a Restore first. Copying only a live main database file is not a backup: WAL sidecars may
contain committed data.

## Optional Agent Skill

Fresh installation, successful upgrade and same-version reruns offer the bundled YATM Skill after the service installation succeeds. The existing Skills CLI provides the interactive agent selection and replacement confirmation. It installs a user-level copy, so removing the downloaded package does not remove the Skill.

The installer uses the ordinary invoking user (`SUDO_USER` when applicable). Missing Node/npm, cancellation or Skill installation failure leaves the successful YATM installation intact. It does not install Node/npm. A root-only, unresolved-user or noninteractive session receives a manual command to run after logging in as the ordinary agent user:

```shell
DISABLE_TELEMETRY=1 DO_NOT_TRACK=1 \
  npx --yes --registry=https://registry.npmjs.org skills@1.5.0 \
  add /opt/yatm/skills/yatm --global --copy
```

Run this as the user of the selected agent. `npx --yes` allows fetching the pinned tool; Skills still asks which agents to use and confirms installation. The bundled Skill belongs to the installed YATM release and documents its supported CLI commands.

## Preview Helper

The installer offers the optional helper before changing the service. `--with-preview` selects it explicitly; `--without-preview` skips it. Noninteractive installation skips it unless explicitly selected. The helper package must match the main package's version and commit and pass its checksum and capability probe before installation. For an unpublished local candidate, add `--preview-archive /path/to/yatm-preview-linux-amd64-VERSION.tar.gz --preview-checksum /path/to/archive.sha256` to the ordinary install command. A same-version rerun can add the helper later. No helper source or system-wide native libraries are installed. To enable generation afterwards, see [Scan signatures and Previews](jobs.md#scan-signatures-and-previews).

## Reverse Proxy

`listen` is the service's local binding; `domain` is the browser-facing URL used for API
and Library import/export requests. They are already independent. An explicit public URL
includes the scheme and external port when non-default, with no trailing slash:

```yaml
listen: 127.0.0.1:8080
domain: https://example.com
```

Alternatively, `domain: ""` uses root-relative `/services` and `/files` URLs, so browser
requests follow the page's current origin. CLI clients select their address independently
with `--server`. `domain` does not configure TLS, bind a socket or restrict incoming Host
values. YATM is served at the origin root; adding a path such as `/yatm` to `domain`
does not provide subpath deployment support. The [API address contract](../architecture/api.md#browser-endpoint-addresses)
describes how the browser derives its request URLs.

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

See [Library/Volume workflows](library.md) for normal operations.
