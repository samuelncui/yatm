<img src="https://raw.githubusercontent.com/samuelncui/yatm/main/frontend/favicon.svg" alt="YATM Logo" style="height: 100px; width:100px;"/>

# YATM aka Yet Another Tape Manager

YATM is an open-source archive manager for LTFS tapes and already-mounted disk Volumes. It separates logical Library organization from physical archive copies, keeping ordinary files recoverable without a YATM-specific storage format.

**v1.0.0-alpha.1 is an unreleased candidate.** It introduces live Location file management, persistent saved versions and a unified Scan workflow. Read the [candidate release notes](docs/releases/v1.0.0-alpha.1.md) for changes and acceptance status. [v0.1.x remains the stable release](https://github.com/samuelncui/yatm/releases/latest).

## Features

- Archive and Restore on Tape, HDD, and HM-SMR Media, with streaming SHA-256 verification through [ACP](https://github.com/samuelncui/acp).
- LTFS storage-order reads, append to compatible Tapes, and hardware-encryption integration through configured device scripts.
- A logical Library with folders, Tags, notes, paginated boolean search, and [Chonky](https://github.com/TimboKZ/Chonky)-based dual-pane browsing.
- Content-addressed image/video Previews independent of archive Media.
- Mounted Volume content access, automatic validated Scan publication, and metadata-only Media deletion.
- Live registered directories, optional basic collection, shared Library/Location organization and query filters, and selected Archive.
- Typed gRPC services and durable Archive, Restore and Scan Jobs with retryable checkpoints. Scan supports optional hashing, content lookup, previews, inventory publication and recorded-copy verification; ordinary file operations remain request-bound.

Tape workflows require compatible LTFS hardware/software; Volume-only workflows do not require a Tape drive. See [installation requirements](docs/operations/install.md#requirements).

## Get Started

The default installer selects the stable release. Once the Alpha candidate is published, install or upgrade using its exact-version installer; no local compilation is required:

```shell
curl --fail --location --output install-release.sh \
  https://raw.githubusercontent.com/samuelncui/yatm/v1.0.0-alpha.1/install-release.sh
bash install-release.sh --version v1.0.0-alpha.1 --check
bash install-release.sh --version v1.0.0-alpha.1
```

Run with permission to manage the installation and systemd service. See [Installation and Configuration](docs/operations/install.md) for prerequisites and fresh-install configuration. Existing `v0.1.x` installations follow the [upgrade guide](docs/operations/migration.md); review it before proceeding. The installer displays that guide from the verified release package and asks for approval before stopping the service.

For an isolated local review environment from a source checkout, run:

```shell
./scripts/demo
```

See [Local Demo](docs/operations/demo.md) for prerequisites, fixture coverage, reset behavior, and safety boundaries. The Demo uses disposable data and does not exercise a physical Tape drive.

The release includes `yatm-cli` and an optional [agent Skill](docs/operations/install.md#optional-agent-skill). Linux amd64/systemd is the primary installation target; other platform archives are experimental.

## Documentation

- [Documentation index](docs/README.md)
- [Library and Volume workflows](docs/operations/library.md)
- [Live Locations and Scan](docs/operations/online-sources.md)
- [Domain glossary](CONTEXT.md) and [current architecture](docs/architecture/overview.md)
- [Testing and delivery gates](docs/operations/testing.md)

Architecture documents describe the current implementation. Draft designs track proposed work and pending acceptance; historical documents are not current specifications.

## Thanks

- [lto-info](https://github.com/speed47/lto-info) provides the basis for Tape inspection, extended here to read the cartridge barcode. Thanks, @speed47!
