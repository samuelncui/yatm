# User Guide

Start here to install, configure and operate YATM. The [glossary](../../CONTEXT.md) defines the
terms these guides use. These guides describe the current software;
implementation details, contributor instructions and research have a separate
[developer documentation entry](../README.md).

## Install and Configure

- [Installation and service configuration](install.md): supported environments, installation,
  updates, service settings and the optional operating Skill for agents.
- [Upgrade from v0.1.x](migration.md): data import, Tape-script adaptation and recovery from an
  interrupted upgrade.
- [Offline migration and repair](offline-migration.md): run migration phases outside the installer
  when the upgrade guide or a maintainer directs it, and repair historical Jobs.
- [Locations](locations.md): register original directories and Restore destinations, browse live
  files, configure Ignore rules and operate on original files.
- [Devices and Tape operations](devices.md): inspect configured devices, save Device settings and
  load LTFS or tar Tape for a Job.
- [Tape scripts](tape-scripts.md): adapt the configured scripts to the local LTFS distribution,
  drive and helpers.

## Daily Use

Use one YATM service for one operator. Avoid conflicting changes to the same files, Library items
or paths, and keep an operation's selected content, referenced configuration and mounted Media
unchanged until it finishes. Independent Jobs and internal copying may still run concurrently.

- [Library and mounted Volumes](library.md): organize and search files, register mounted Volumes,
  back up Library metadata and find identical files.
- [Archive, Restore and Scan](jobs.md): copy files to Media, restore saved versions and publish
  content facts, inventory and integrity results.
- [Faster Restores with Auto read order](read-order.md): shorten Restores of files scattered across a
  Tape, and tune the read parameters it uses.
- [Command-line reference](cli.md): run the same operations from scripts and agents.
- [Independent tar Tape recovery](tar-tape-recovery.md): retain cartridge parameters and recover
  ordinary files with public tools, including the required physical reader qualification.

LTFS and raw tar use the configured encryption adapter and the existing saved Media key. Raw tar
requires Linux; writes establish the selected hardware compression policy and reads retain the
recorded decoding policy. Independent encrypted recovery uses an explicitly retained protected
Library export and a compatible public unlock procedure. The
[recovery guide](tar-tape-recovery.md#tested-reader-and-required-parameters) lists physically checked
reader combinations and remaining limits. Consult the
[release notes](https://github.com/samuelncui/yatm/releases) for the installed version.

## Operating with an Agent

Install the optional YATM operating Skill using the
[installation instructions](install.md#optional-agent-skill). It guides an agent through the same
CLI operations and safety boundaries used by an operator. Repository `AGENTS.md`, architecture
documents and test procedures are contributor guidance, not prerequisites for daily use.
