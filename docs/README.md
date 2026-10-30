# Developer Documentation

This is the implementation and contributor entry point. For installation, configuration and daily
operation, start with the separate [User Guide](operations/index.md).

Architecture and operations describe the current YATM v1 development line; research records
the evidence and open questions behind further work.
Published versions and their change notes belong to
[GitHub Releases](https://github.com/samuelncui/yatm/releases); this tree is not a release archive.

Start with the [contributor rules](../AGENTS.md), the [domain vocabulary](../CONTEXT.md) and the
[documentation rules](development/documentation.md). Component guides live beside their code:
[frontend](../frontend/README.md), [Preview helper](../previewworker/README.md) and its
[build recipes](../previewworker/build/README.md), the
[LTFS file backend adapters](../e2e/ltfs-file-backend/README.md) and the
[cartridge memory fixtures](../internal/media/tape/testdata/README.md).

## Pre-stable Compatibility

The required compatibility baseline is the supported `v0.1.x` migration/import path. Until the first
stable v1 release, changes to data formats, APIs, entities, schemas, Job bundles and metadata backups
between unpublished or pre-stable v1 builds require no compatibility layers or version/revision bumps.
The [persistence contract](architecture/persistence.md#published-data-formats) owns the current artifact
identities and versions independently of software release numbers.

Configured Tape scripts have a separate, long-term compatibility requirement. Upgrades preserve
user adapters wherever possible; a necessary incompatible change requires explicit approval and
an actionable notice before upgrade, including pre-stable releases. The
[Tape script contract](architecture/tape.md#tape-script-compatibility) owns that boundary.

Known incompatible installations are rejected before writes. Their data is retained until the operator
chooses backup, migration, conversion or reinstallation; YATM never clears databases or physical
files automatically. Disposable development fixtures are regenerated only through an explicit reset.
Remove this temporary rule when preparing the first stable v1 release and replace it with the
published stable upgrade policy.

## Current Architecture

- [Overview and ownership](architecture/overview.md)
- [Persistence and recovery](architecture/persistence.md)
- [Library identities, organization and backups](architecture/library.md)
- [Jobs and execution](architecture/jobs.md)
- [Media backends and file I/O](architecture/media-io.md)
- [Tape devices, lifecycle and formats](architecture/tape.md)
- [Tape read planning](architecture/read-planning.md)
- [Preview storage](architecture/preview.md)
- [Interface contracts](architecture/contracts.md)
- [APIs and services](architecture/api.md)
- [User interface](architecture/ui.md)

## User Documentation

- [User Guide entry](operations/index.md)
- [Installation and service configuration](operations/install.md)
- [Upgrade from v0.1.x](operations/migration.md)
- [Offline migration and repair](operations/offline-migration.md)
- [Locations](operations/locations.md)
- [Library and mounted Volumes](operations/library.md)
- [Archive, Restore and Scan](operations/jobs.md)
- [Devices and Tape operations](operations/devices.md)
- [Tape scripts](operations/tape-scripts.md)
- [Faster Restores with Auto read order](operations/read-order.md)
- [Command-line reference](operations/cli.md)
- [Independent tar Tape recovery](operations/tar-tape-recovery.md)

## Contributing

- [Documentation rules](development/documentation.md)
- [Development checks and test environments](development/testing.md)
- [Code layout and test ownership](development/code-layout.md)
- [Automated E2E](development/e2e-test.md)
- [Performance acceptance](development/performance.md)
- [Local Demo](development/demo.md)

## Release

- [Release process](release/release.md)
- [Package acceptance](release/package-acceptance.md)
- [Physical Tape E2E](release/physical-tape-e2e.md)
- [Physical Tape host runbook](release/physical-tape-runbook.md)
- [Physical tar acceptance](release/physical-tar-acceptance.md)
- [Physical Tape measurements](release/physical-measurements.md)

## Research

- [Tape capacity and safe completion](research/tape-capacity.md)
- [Tar on Tape](research/tar-tape.md)
- [Tape read ordering](research/tape-read-ordering.md)

Research is published with the repository. Each study states its objective, source versions,
confirmed findings, community observations, unresolved questions and criteria for evaluating
possible approaches. A study describes the current state of knowledge; it does not establish
implemented behavior or authorize experiments and production changes.
