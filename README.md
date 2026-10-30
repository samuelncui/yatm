<img src="https://raw.githubusercontent.com/samuelncui/yatm/main/frontend/favicon.svg" alt="YATM Logo" style="height: 100px; width:100px;"/>

# YATM aka Yet Another Tape Manager

YATM is an open-source archive manager for LTFS tapes and already-mounted disk Volumes. It separates logical Library organization from physical archive copies, keeping ordinary files recoverable without a YATM-specific storage format.

**The v1 Alpha line is intended for evaluation.** It includes live Location file management, persistent saved versions and a unified Scan workflow. See [GitHub Releases](https://github.com/samuelncui/yatm/releases) for available versions, changes and limits. [v0.1.x remains the stable release](https://github.com/samuelncui/yatm/releases/latest).

## Features

- Archive and Restore on Tape, HDD, and HM-SMR Media, with streaming SHA-256 verification through [ACP](https://github.com/samuelncui/acp).
- LTFS storage-order reads and sequential tar workflows with configurable records;
  both use configured encryption scripts, with public-tool recovery instructions for tar.
- Auto read order for Tape Restore and Verify: it plans the whole selection to cut tape
  positioning, and keeps recorded order when that is fastest. On the tested hardware it substantially
  shortened Restores of files scattered across a tar cartridge; see
  [Faster Restores with Auto Read Order](docs/operations/read-order.md#faster-restores-with-auto-read-order).
- A logical Library with folders, Tags, notes, paginated boolean search, and [Chonky](https://github.com/TimboKZ/Chonky)-based dual-pane browsing.
- Content-addressed image/video Previews independent of archive Media.
- Mounted Volume content access, automatic validated Scan publication, and metadata-only Media deletion.
- Live registered directories, optional basic collection, shared Library/Location organization and query filters, and selected Archive.
- Durable Archive, Restore and Scan Jobs, typed gRPC services and a scriptable CLI. Scan supports optional hashing, content lookup, previews, inventory publication and recorded-copy verification.

LTFS uses compatible hardware/software and configured adapters. Raw tar Tape uses the eligible Linux
device path; physical qualification is reported separately from protocol support. Volume-only
workflows do not require a Tape drive. See [installation requirements](docs/operations/install.md#requirements).

## Get Started

The default installer selects the stable release. Install or upgrade the Alpha using its exact-version installer; no local compilation is required:

```shell
curl --fail --location --output install-release.sh \
  https://raw.githubusercontent.com/samuelncui/yatm/v1.0.0-alpha.3/install-release.sh
sudo bash install-release.sh --version v1.0.0-alpha.3 --check
sudo bash install-release.sh --version v1.0.0-alpha.3
```

Run with permission to manage the installation and systemd service. See [Installation and Configuration](docs/operations/install.md) for prerequisites and fresh-install configuration. Existing `v0.1.x` installations follow the [upgrade guide](docs/operations/migration.md); review it before proceeding. The installer displays that guide from the verified release package and asks for approval before stopping the service.

For an isolated local review environment from a source checkout, run:

```shell
make demo
```

See [Local Demo](docs/development/demo.md#local-demo) for reset behavior and safety boundaries. The Demo uses disposable data and does not exercise a physical Tape drive.

The release includes `yatm-cli` and an optional [agent Skill](docs/operations/install.md#optional-agent-skill). Linux amd64/systemd is the primary installation target; other platform archives are experimental.

## Documentation

- [User Guide](docs/operations/index.md): installation, configuration, daily workflows and operation
  through the CLI or an agent.
- [Developer Documentation](docs/README.md): architecture, interfaces, research, tests and contribution
  rules.

Architecture and user documentation describe the current implementation and operating procedures; research records evidence and open questions. Published change history belongs to [GitHub Releases](https://github.com/samuelncui/yatm/releases); proposals and implementation plans live in Issues or pull requests until their durable result is incorporated into the current documentation.

## Thanks

- [lto-info](https://github.com/speed47/lto-info) provides the basis for Tape inspection, extended here to read the cartridge barcode. Thanks, @speed47!
