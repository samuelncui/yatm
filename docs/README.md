# Documentation

These documents describe development toward `v1.0.0-alpha.1`. Start with [installation](operations/install.md), [v0.1.x upgrade](operations/migration.md), or the [candidate release notes](releases/v1.0.0-alpha.1.md). Daily use is covered by [Library/Media](operations/library.md), [live Locations](operations/online-sources.md), and the bundled [CLI Skill](../.agents/skills/yatm/SKILL.md).

## Temporary Draft Compatibility Policy

The supported release sequence is `v0.1.x → v1.0.0-alpha.1 → v1.0.0`. Preserve legacy migration/import and a forward upgrade/import path for published v1 data. The [persistence contract](architecture/persistence.md#published-data-formats) owns artifact identities and revisions independently of software versions. Alpha APIs may change with release notes; supported published data retains a path forward.

The temporary exception applies to unpublished Draft revisions: their schemas, APIs, Job bundles and backup formats do not require compatibility adapters. Reject unsupported development data without deleting installations or physical data; regenerate disposable fixtures explicitly. Withdrawn experimental installations require separately preserved data and an explicit reinstall; the public installer has no release-number downgrade exception.

Before the first stable v1 release, remove this temporary exception and its contributor reference, publish the stable API/upgrade policy, and audit stale Draft claims. The exception does not apply to `v1.0.0-alpha.1` or later once published.

## Current Architecture

- [Overview and ownership](architecture/overview.md)
- [Persistence and recovery](architecture/persistence.md)
- [Library identities, organization, and backups](architecture/library.md)
- [Jobs and execution](architecture/jobs.md)
- [Media backends and file I/O](architecture/media-io.md)
- [Preview storage](architecture/preview.md)
- [APIs and user interface](architecture/api-ui.md)
- [Entity state navigation](architecture/states.md)

## Designs and Decisions

- **Requirements baseline:** [Online file management](designs/online-files-requirements.md)
- **Draft:** [Files interaction and metadata restore](designs/files-interaction-consistency.md)
- **Draft:** [v1 Alpha release and upgrade delivery](designs/v1-alpha-release.md)
- **Candidate:** [v1 Alpha 1 changes and acceptance status](releases/v1.0.0-alpha.1.md)
- **Accepted:** [File organization is independent of content](decisions/0002-file-organization-and-content.md)
- **Superseded:** [Content identity owns Library organization](decisions/0001-content-identity.md)

## Operations

- [Installation and service configuration](operations/install.md)
- [Library and mounted Volume workflows](operations/library.md)
- [Online-source workflows](operations/online-sources.md)
- [v0.1.x to v1 migration](operations/migration.md)
- [Local Demo](operations/demo.md)
- [Development checks and test environments](operations/testing.md)
- [Automated E2E](operations/e2e-test.md)
- [Physical Tape E2E](operations/physical-tape-e2e.md)

## History

- **Implemented:** [Shared Files, query filters and configurable Scan](history/files-and-scan.md)
- **Superseded:** [Browsing, Restore association and copy integrity](history/browsing-restore-integrity.md)
- **Superseded:** [Online originals, File versions and archive copies](history/online-file-versions.md)
- **Implemented:** [Live Locations and persistent File organization](history/location-file-organization.md)
- **Implemented:** [Shared Library and Location organization](history/shared-file-operations.md)
- **Implemented:** [Indexing workflows and CLI acceptance](history/indexing-workflows.md)
- **Implemented:** [Locations and File content interface](history/online-source-interface.md)
- **Implemented:** [Content-grouped duplicates in Locations](history/duplicate-content-groups.md)
- **Implemented:** [Online data-source design and acceptance](history/online-sources.md)
- [Platform design snapshot](history/platform-design.md)
- [Platform development change summary](history/platform-changes.md)

Historical documents are frozen context, not the maintenance target for current behavior. Old document URLs remain as navigation stubs.

## Documentation Convention

| Location | Owns | Does not own |
| --- | --- | --- |
| Root README | Product introduction, quick start, documentation entry points | Detailed architecture or repeated runbooks |
| Root CONTEXT | Domain vocabulary, core domain invariants, navigation | Schemas, algorithms, task notes |
| Root AGENTS | Contributor instructions, safety gates, required document updates | A duplicate architecture specification |
| `architecture` | Implemented behavior, boundaries, constraints, code anchors | Unimplemented proposals |
| `designs` | Proposed changes, interfaces, failure behavior, acceptance scenarios | Claims of current capability |
| `decisions` | Sparse numbered ADRs for durable, non-obvious tradeoffs | Routine implementation choices or discussion transcripts |
| `operations` | Setup, migration, maintenance, and reproducible validation | Another definition of the domain model |
| `history` | Implemented/superseded designs and historical development summaries | Current design authority |

Use English Markdown, short topic-based kebab-case filenames, and repository-relative links. Current architecture filenames are version-independent; version names belong in migration guides and historical records. Link to the owning topic instead of repeating its definition. Link important claims to source files without duplicating generated schemas.

### Update Workflow

1. Read the relevant current topic and glossary before changing its behavior. Check the implementation when documentation and code disagree.
2. Record confirmed domain terms and design constraints in their owning document during the task, not only in the final response. Keep unsettled or unimplemented behavior in a design marked **Draft**. Do not silently change code to resolve an uncertain discrepancy.
3. Update code, semantic tests, affected current documentation, and any affected operational or Demo guide together. A design describes scope, new boundaries/interfaces, data flow, failure semantics, compatibility, acceptance cases, and implementation order; include open questions only when unresolved.
4. After implementation and verification, update the current architecture, mark the design **Implemented** and move it to history. Mark replaced proposals **Superseded** and link their replacement. Update the index and preserve useful old entry points with short links.
5. Before delivery, check links, code anchors, status labels, and duplicate facts manually, and run `git diff --check`. No additional documentation toolchain or CI is required.

Record outcomes and durable maintenance rationale, never chat history, backtracking, or rejected work. ADRs use sequential names such as `0001-content-identity.md`, a status, and a short decision/rationale; create one only when the choice is hard to reverse, surprising without context, and grounded in a real tradeoff.

### UI Prototypes

Build UI prototypes in React using the existing application's components, styling, and layout. Capture the rendered pages in a browser; generated concept images are not UI specifications. Label proposed behavior and sample data explicitly.

Keep an independent page source and entry point for each prototype, with local run instructions and its screenshot. Shared application components and draft fixtures may be reused. Store temporary frontend prototypes under the ignored `frontend/.drafts/` directory for review and implementation reference, then remove their code and screenshots before committing. Keep accepted behavior and constraints in the owning design document, independently of these disposable artifacts.
