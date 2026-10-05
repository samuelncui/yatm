# Repository Working Guide

## Working Principles

- Keep changes minimal, follow the surrounding code, and apply KISS. Reuse an existing capability when it directly satisfies the demonstrated requirement; replace or delete it when carrying it is more complex than the direct solution.
- Before changing code, investigate existing capabilities and business semantics. Add functionality only for a demonstrated gap. A narrower architecture document, test or current implementation may explain a mechanism, but its existence cannot justify that mechanism or override a broader project constraint.
- Preserve existing user-visible capabilities unless the developer explicitly authorizes their removal or reduction. Permission to refactor, optimize, replace an API or change an implementation does not authorize dropping its capabilities. Distinguish removing implicit work from removing the corresponding explicit user action.
- Keep branches shallow, prefer early returns, and evaluate a decision once per flow. Add a helper or package only when it gives a current invariant one owner or materially reduces present complexity or duplication. Multiple callers are evidence, not a sufficient reason by themselves; a hypothetical future caller or a document-defined boundary is not sufficient.
- Recreating Job work opens the normal creation form with the original selections and options. It requires review and explicit submission through the existing pre-submit checks; opening the form never submits work. Only submission creates a new Job; never reset or retry the old Job's state machine. Only Archive/Restore Media failures return to their pre-Media state for another explicit Media choice. The [Job lifecycle](docs/architecture/jobs.md#shared-lifecycle) owns this boundary.
- Do not invent domain concepts, terms or domain-bearing UI labels. [CONTEXT](CONTEXT.md) owns the vocabulary and it is closed: each entry names the words to avoid, and adding a concept, renaming one, or coining a parallel word in code, UI copy or documentation needs explicit developer authorization. Prefer the existing word; when nothing fits, say so instead of naming something new.
- Follow the supported operating model in the [architecture overview](docs/architecture/overview.md#supported-operating-model). Defensive behavior for an unsupported situation is not a user-visible capability: remove its code, tests and lower-level documentation when the supporting requirement is absent.
- Write source, comments, documentation, changelogs, commit messages, and release notes in English.
- Keep this public repository self-contained; do not reference or depend on private source repositories.

## Required Reading

Read [CONTEXT](CONTEXT.md), the [documentation convention](docs/README.md), and the current architecture document that owns the area you are changing before you change it. Record confirmed terminology, constraints and behavior in that owning document during the task, not only in the final response; the convention owns the document workflow and the delivery checks.

Maintained repository documentation describes current behavior only. Keep proposals and implementation plans in an Issue or pull request; after implementation, update the current owner and delete replaced text instead of adding history, superseded-design or redirect documents. Published change notes belong to GitHub Releases, and raw verification evidence belongs to CI artifacts. Repository documentation must be self-contained: repository-relative references resolve to files committed in the same repository, while external references use stable public URLs.

## Repository Layout

Choose a file's owner before adding it. Use the following locations; the [development guide](docs/operations/testing.md#repository-layout-and-entrypoints) owns commands and output overrides.

| Location | Owns |
| --- | --- |
| Repository root | Project metadata, root Go module, Makefile, configuration example and public installer entrypoint |
| `cmd/` | Go program entrypoints |
| `internal/` | Go implementation packages and their tests; preserve business-domain boundaries |
| `entity/` | Current protobuf sources, generated Go code, protocol helpers and their generator; TypeScript outputs stay in `frontend/src/entity/` |
| `frontend/` | Frontend source, configuration and tests; dedicated scripts belong in `frontend/scripts/` |
| `previewworker/` | Independent native-helper module; build recipes, dependency patches and source-packaging tools belong in `previewworker/build/` |
| `scripts/` | Runtime Tape adapters only |
| `build/backend/`, `build/release/` | Backend compilation/toolchain setup and cross-project release assembly/validation, respectively; source files only |
| `dev/` | Cross-project development orchestration, including code generation, Demo and shared checks |
| `e2e/` | Integration acceptance, its fixtures and optional LTFS file-backend adapters |
| `docs/`, `licenses/` | Documentation under the documentation convention, and distributed dependency notices |
| `output/` | Ignored build results and verification logs; final release archives and checksums belong in `output/releases/` |

- Do not add loose development scripts, archives or experimental Go packages at the root. Do not put build, release, Demo or test tools in runtime `scripts/`.
- Extend the existing owner's directory before introducing a new top-level directory, module or workspace tool. A new ownership boundary needs a concrete consumer and an updated layout rule in the same change.
- Keep tests beside their implementation and reusable fixtures in the owning test area. Never hide maintained tests with a blanket ignore rule such as `tests/`; private experiments and prototypes stay outside tracked source.
- Keep throwaway Go experiment and test sources outside the checkout. Git-ignored directories such as `output/` still enter `go test ./...` unless they are a separate module or excluded by Go's own directory rules; keep verification logs there, not loose experimental Go packages.
- Keep generated output separate from hand-maintained tooling. Preserve tool-native ignored output such as `frontend/dist/`; never store build output in `build/` or mix it with source. Do not hand-edit generated code or bundles.
- Treat source layout and installed package layout separately. For a move, update imports, generators, tests, Makefile/CI, packaging inputs and documentation links together; verify both direct invocation and packaged consumers.

## Backend Implementation

Follow the document that owns each area. The [architecture overview](docs/architecture/overview.md) lists the current documents and their boundaries.

| Area | Owning documents |
| --- | --- |
| Catalog, Library, Job bundles, reporting | [Persistence and recovery](docs/architecture/persistence.md), [Library](docs/architecture/library.md) |
| APIs, services, generated code | [APIs and services](docs/architecture/api.md), [Interface contracts](docs/architecture/contracts.md) |
| Jobs, Scan phases, publication | [Jobs and execution](docs/architecture/jobs.md), [Preview storage](docs/architecture/preview.md) |
| File operations and Location behavior | [Library](docs/architecture/library.md), [APIs and services](docs/architecture/api.md) |
| Media, Tape and physical I/O safety | [Media backends and file I/O](docs/architecture/media-io.md) |
| Frontend and UI design | [User interface](docs/architecture/ui.md), [Preview storage](docs/architecture/preview.md) |

## Verification

Use existing verification entrypoints first and maintain recurring checks with the implementation under the [test maintenance policy](docs/operations/testing.md#test-maintenance). Performance-sensitive changes follow [Performance Acceptance](docs/operations/testing.md#performance-acceptance).

Before remote E2E acceptance or NAS delivery, check ignored top-level workspace directories for an `AGENTS.md` and read any applicable operator notes. Such notes provide host context only and do not authorize production actions.

## Delivery and Authorization

- Obtain explicit developer authorization before changing API contracts, entity definitions or database structure/storage semantics. This includes additions, removals, renames, field types, defaults, observable behavior, relationships, tables, indexes, constraints, migrations and data-format changes.
- Present the affected contracts/models, proposed changes, consumer impact and compatibility/migration implications for approval before implementation or regeneration. A general feature, refactor or performance request is not authorization for these changes; approval of a plan that explicitly specifies them is sufficient within that scope.
- Until the first stable v1 release, changes to data formats, APIs, entities, schemas, Job bundles and metadata backups between unpublished or pre-stable v1 builds require no compatibility layers or version/revision bumps. Preserve the supported `v0.1.x` migration/import path. Remove this temporary rule when preparing the first stable v1 release; the authorization requirement above still applies.
- Read-only investigation and clearly labeled Draft proposals may precede approval. Database data modifications require authorization for the specific operation; ordinary writes within an explicitly requested business operation do not authorize schema or model changes.
- Keep commits single-purpose and exclude temporary files, local configuration, backups, generated test data, and obsolete implementations.
- For every release request, execute the [Release SOP](docs/operations/testing.md#release-sop), including content/consumer/documentation review and `make release-check` before candidate assembly. Report required checks that remain unrun; source checks do not replace exact-package acceptance or publication approval.
- Update the bundled [CLI Skill](.agents/skills/yatm/SKILL.md) in the same commit as any CLI command, flag, output or safety rule it describes; the [CLI document](docs/architecture/cli.md) owns that rule.
- Keep `master` aligned with `origin/main` and perform v1 development on `v1`. Push ACP integration only to its development branch until review is complete. Do not push YATM, merge ACP main, tag, or publish a release without explicit confirmation.
