# Indexing Workflows and CLI Acceptance

Status: Implemented in the v1 development branch, verified on 2026-09-09; not a published release. This is a frozen design record. Maintain behavior in [Jobs](../architecture/jobs.md), [API/UI](../architecture/api-ui.md), [configuration operations](../operations/install.md#location-configuration-and-migration) and [CLI E2E](../operations/e2e-test.md).

## Delivery Checklist

- [x] Persist Library visibility and filter tree/search/selection before pagination.
- [x] Register original/restore directory usages, migrate legacy source/target once, and enforce administrator access ranges.
- [x] Publish Volume scans automatically while preserving complete-observation validation.
- [x] Unify Scan creation and make every Job detail reachable through normal navigation.
- [x] Add independent pane source menus and retain read-only physical trees and editable logical organization.
- [x] Replace Location configuration dialogs with routed editable pages and consistent layout spacing.
- [x] Restore cross-directory Backup/Restore selection lists with server-side expansion.
- [x] Freeze Restore destinations; reuse verified equal outputs and suffix different outputs without overwriting.
- [x] Route Preview inputs through registered/indexed selections.
- [x] Update all public clients and add bounded CLI Job waiting.
- [x] Migrate CLI-expressible business E2E steps and document a feature/command/scenario/assertion/platform matrix.
- [x] Update Demo, architecture and operations after semantic and end-to-end acceptance.

Restore-as-original linking and Media integrity-check Jobs remain separate designs. This batch does not enable automatic original reassignment, physical file organization, watchers, multi-Executor transport or physical Tape tests.

## Goal and Current Boundaries

Expose one understandable indexing action without merging original-file management with archive inventory. Indexing observes files; Archive retains verified copies. Neither a successful traversal nor a matching catalog entry is a fresh integrity guarantee for every archived byte.

This implementation used separate Volume and Location runners. Their maintained replacement is the [unified Scan contract](../architecture/jobs.md#scan). [CLI binding coverage](../../cmd/yatm-cli/coverage_test.go) and [executable acceptance](../operations/e2e-test.md) verify different layers; neither substitutes for storage fault tests.

## One User Action, Typed Execution

Use **Jobs → New → Scan**, with **Location / Media** target selection. Object-page shortcuts preselect the target. Job titles include the object type and name, for example **Scan Location · Documents** and **Scan Media · Review HDD**. Remove the competing Sync creation tab. Media scanning initially supports mounted Volumes only; do not imply Tape support.

| Target | Publication | Required boundary |
| --- | --- | --- |
| Location | Original observations, File continuity, derived browsing and covered versions | Confirmed path binding, gitignore rules, Location operation gate |
| Media | Positions, derived inventory, capacity and covered versions | Volume UUID/profile/marker validation and exclusive Media lease |

Keep typed runners and services. Shared presentation does not require a new universal Job schema or a target-type flag throughout execution. Share low-level traversal/hash code only where its semantics actually agree. Preserve ordinary files, Library organization, Archive/Restore checkpoints, and the existing independent Preview Job.

## Automatic Volume Publication

One Create returns an initialized background Job and immediately navigates to its progress. The attempt performs bounded observation, difference calculation, validation and one metadata-only Library publication. Keep the Volume lease through publication. Retain paginated changes as the result, not an approval screen; remove the public Apply operation and its waiting phase. Do not implement automation as a browser calling Apply after polling.

- Keep the old inventory until the complete observation is valid. Root/marker failures, unreadable subdirectories, cancellation and detectable drift must not publish a partial or false empty inventory.
- Revalidate membership and physical facts across the observed inventory, not only changed rows. Keep filesystem I/O outside the Library transaction. Default hashing uses current cache facts; force rehash reads all regular files without mechanically adding a second full hash pass for the former Apply phase.
- An actually empty registered Volume can remove inventory Positions. Never delete physical files, File organization or saved-version history. Changed and missing archive files are visible warnings in the result, not evidence of a healthy backup.
- Retry starts a fresh observation from current committed inventory. Library publication remains authoritative if the later Job checkpoint fails. Do not add a cross-database transaction or crash-only recovery log.
- Use the indexing/completed lifecycle with retryable phases. Scanning, validating and publishing are internal phases; the UI presents progress, results and retry without a manual commit step.

IDL, generated clients, CLI, UI, Demo and operational instructions must change together. Under the [Draft policy](../README.md#temporary-draft-compatibility-policy), no internal Draft Apply compatibility adapter is required. Do not silently execute an old waiting Job or reset existing installations; incompatible bundles need explicit handling.

## Library Visibility and Physical Browsing

The [interface design](online-source-interface.md#pane-sources-and-library-visibility) specifies per-pane Library/Location selection and optional inclusion of unbacked Files. Library always permits logical organization; Locations remain read-only physical indexes. Visibility changes do not suspend indexing, change File identity, remove annotations or migrate either tree. The [current Library contract](../architecture/library.md) owns the saved-version criterion.

## CLI and End-to-End Verification

CLI presentation mirrors target selection: `scan location <id>` and `scan media <id>`, with common Job progress/results routing to the typed services. A general `job wait` is useful for scripts: success only on completed, a distinct actionable/error outcome when execution needs intervention, and bounded waiting without automatically retrying or selecting storage.

Keep three complementary layers:

- Semantic tests: atomic publication, content matching, paging, permissions, drift, cancellation, conflicting facts and post-Library checkpoint failures. Retain targeted fault injection and internal assertions here.
- Service integration: storage and fault fixtures retain ownership of the real Executor/Library server, while CLI-expressible business requests invoke actual CLI subprocesses. Direct protocol checks remain for HTTP header/range semantics. No adapter may silently fall back to private APIs.
- Executable acceptance: build matching server and CLI binaries once, start an isolated server with production routing/configuration, and invoke CLI subprocesses with argv arrays from a Go test harness. Check exit status and decoded JSON, public catalog results, and independent bytes/hash/mode/mtime assertions. Business steps must not use direct database writes or private APIs; filesystem mutation is permitted to simulate external edits. Avoid a large shell script or a second CLI command framework.

First executable journeys cover Location registration and scan, independent duplicates and versions, Library-selected Volume Archive, explicit-version Restore, automatic Media scan after external changes, and metadata export/import into a separate catalog. Include paginated results, stale-original refusal and one interrupted/retried Job. Preserve isolated LTFS file-backend regressions on Linux; physical Tape requires separately assigned scratch media. Browser smoke tests still verify actual navigation and rendering, which CLI tests cannot cover.

Migrate CLI-expressible primary business steps to executable acceptance, then remove redundant service scenarios only after equivalent assertions pass. Maintain a feature/command/scenario/assertion/platform coverage matrix. Keep smaller fault-oriented tests that require internal injection; command-binding coverage alone is not end-to-end acceptance.

## Implementation and Acceptance

1. Implement the confirmed pane-source and Library-visibility contract alongside the indexing entry-point revision.
2. Implement automatic Volume publication and its full-observation validation; update typed contracts and retry tests.
3. Consolidate UI/CLI entry points, preserve target-specific policies, and update explicit-reset Demo fixtures.
4. Add executable CLI acceptance and retain storage-level regressions; absorb verified behavior into current architecture and operations.

Acceptance includes added/changed/removed inventory without Apply, inaccessible versus truly empty targets, complete rollback before publication, retry after committed metadata, exclusive Volume versus Location gates, unchanged version history after copy loss, deterministic target routing, and Library view changes without catalog mutations. Do not claim completion from CLI exit success alone: background Jobs must reach their required outcome and restored bytes must match independent expectations.

## Verification Record

- Go unit tests and vet across the repository passed. Affected workflow, API, Library, migration, Demo and CLI packages passed race checks and both CGO/non-CGO SQLite paths.
- Protobuf Go/TypeScript generation was repeatable; frontend formatting, lint, type checking, 97 semantic tests and production build passed.
- CLI-first installation, indexing cancellation/retry, online access/archive, mounted Volume and Preview journeys passed. The complete server/CLI workflow was repeated after Restore name-reservation changes.
- On `dev`, official LTFS file-backend archive/restore and target-full spanning-media tests passed together. No physical Tape tests ran.
- Explicit-reset Demo fixtures and actual rendered dual-pane, Settings, cross-directory selection, Restore review and Job navigation were checked. Document links, shell syntax and diff whitespace were checked.

The optional skill-manifest validator could not start because its Python environment lacked `yaml` (PyYAML). The short YATM skill frontmatter and CLI instructions were reviewed manually; no dependency was added. The frontend build retains a non-fatal size warning for its Chonky chunk.

Restore-original reassignment, Media integrity-check Jobs and the other deferred capabilities above were not enabled.
