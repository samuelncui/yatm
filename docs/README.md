# Documentation

These documents describe the current YATM v1 development line. Start with
[installation](operations/install.md), the [v0.1.x upgrade](operations/migration.md),
[Library and Media workflows](operations/library.md), or [live Locations](operations/locations.md).
Published versions and their change notes belong to
[GitHub Releases](https://github.com/samuelncui/yatm/releases); this tree is not a release archive.

## Pre-stable Compatibility

The required compatibility baseline is the supported `v0.1.x` migration/import path. Until the first
stable v1 release, changes to data formats, APIs, entities, schemas, Job bundles and metadata backups
between unpublished or pre-stable v1 builds require no compatibility layers or version/revision bumps.
The [persistence contract](architecture/persistence.md#published-data-formats) owns the current artifact
identities and versions independently of software release numbers.

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
- [Preview storage](architecture/preview.md)
- [Interface contracts](architecture/contracts.md)
- [APIs and services](architecture/api.md)
- [User interface](architecture/ui.md)
- [Command-line interface](architecture/cli.md)

## Operations

- [Installation and service configuration](operations/install.md)
- [Library and mounted Volume workflows](operations/library.md)
- [Locations](operations/locations.md)
- [v0.1.x to v1 migration](operations/migration.md)
- [Development checks and test environments](operations/testing.md)
- [Automated E2E](operations/e2e-test.md)
- [Physical Tape E2E](operations/physical-tape-e2e.md)

## Release Packages

The release package includes this index and the four operator guides: installation, migration,
Library/Media workflows and Locations. Architecture and developer validation remain repository
documentation; release packaging uses an explicit allowlist rather than copying the whole tree.

## Documentation Ownership

Every maintained fact has one owner. Other documents link to that owner instead of restating it.

| Location | Owns | Does not own |
| --- | --- | --- |
| Root `README.md` | Released product introduction, quick start and documentation entry points | Development status, architecture detail or repeated runbooks |
| Root `CONTEXT.md` | Domain vocabulary and core domain invariants | Schemas, algorithms, task notes or UI layout |
| Root `AGENTS.md` | Contributor rules, safety gates, repository layout and required verification | Product or architecture contracts |
| `docs/architecture/` | Current implemented behavior, module interfaces, invariants and durable constraints | Proposals, implementation plans, acceptance transcripts or superseded behavior |
| `docs/operations/` | Current setup, migration, use, maintenance and reproducible validation | Domain definitions or another architecture specification |
| `.agents/skills/yatm/` | Installed agent operating and safety instructions | Human-facing architecture or change history |

## Decision Authority

Within overlapping scope, broader accepted constraints bound narrower descriptions: the repository
engineering rules, the domain invariants and compatibility policy, the architecture overview, area
architecture, operations guides, and finally implementation and tests. A lower level may provide
evidence and implementation detail, but its existence cannot justify complexity or override a broader
decision. When they conflict, update the lower-level documents, code and tests together.

The repository intentionally has no documentation archive, ADR archive, design backlog or redirect
stubs. Git already retains deleted text and rationale. Future work belongs in an Issue or pull
request. Published change notes belong to GitHub Releases. Raw acceptance logs, checksums,
measurements, screenshots and host-specific reports belong in CI artifacts rather than Markdown.

## Update Workflow

1. Establish the intended behavior from the decision authority above, then inspect `CONTEXT.md`, the
   owning document and the implementation. A mismatch is something to resolve, not a reason to
   promote the current implementation into a requirement.
2. Update the implementation, semantic tests, owning architecture document and affected operating
   guide together. Describe the final behavior in present tense; remove the replaced text instead of
   marking it historical, implemented or superseded.
3. Keep unsettled designs out of the maintained documentation tree. A PR or Issue may explain the
   problem, alternatives, compatibility impact and acceptance plan; after implementation, copy only
   the durable result into its current owner.
4. Keep implementation detail only when callers or maintainers must know it to use the module
   correctly. Function names, CSS placement, one measurement, one candidate checksum and step-by-step
   development history belong in code, tests or verification output.
5. Keep repository documentation self-contained. Every repository-relative link or file reference
   must resolve to a tracked file, or to a new file committed in the same change; external references
   use stable public URLs. Private notes, ignored artifacts and machine-local paths are never sources
   for repository documentation.
6. Before delivery, run `make check` or at minimum `node dev/check-documents.mjs` and
   `git diff --check`. The document check enforces the allowed tree, complete index, valid relative
   links and the absence of private/history references.

Use English Markdown, short topic-based kebab-case filenames and repository-relative links. Current
architecture filenames are version-independent. Adding a new documentation file requires a distinct
current owner that cannot be expressed clearly in an existing file; update this index in the same
change.

## UI Prototypes

Build UI prototypes with the existing React modules, styling and layout, and capture their rendered
pages for review. Store temporary sources and screenshots under ignored `frontend/.drafts/`, then
remove them after the accepted behavior and constraints have been recorded in the owning current
architecture document. Generated concept images are not interface specifications.
