# Repository Coding Rules

## General

- Keep changes minimal, follow the surrounding code, and apply KISS. Do not add abstractions for speculative capabilities.
- Keep branches shallow, prefer early returns, and evaluate a decision once per flow. Add helpers only for reuse, an independent domain boundary, or a material reduction in complexity.
- Write source, comments, documentation, changelogs, commit messages, and release notes in English.
- Keep artifacts outcome-oriented: final behavior, current constraints, and durable maintenance rationale, without conversation history, rejected alternatives, or backtracking.
- Keep this public repository self-contained; do not reference or depend on private source repositories.
- Keep commits single-purpose and exclude temporary files, local configuration, backups, generated test data, and obsolete implementations.

## Documentation Contract

- Read [CONTEXT](CONTEXT.md), the [documentation convention](docs/README.md), and relevant [current architecture](docs/architecture/overview.md) before changing a domain or workflow.
- Update confirmed terminology, design constraints, and behavior in their owning repository documents during the task. Do not leave durable requirements only in conversation or the final response.
- Put unsettled or unimplemented functionality in a clearly marked Draft; never present it as current architecture, API, or Demo support.
- Update affected architecture, operational guides, and tests together with code. After implementation and verification, absorb the behavior into current architecture and archive its design as Implemented.
- Keep each fact in one owning document and link to it. Use sparse ADRs only for hard-to-reverse, non-obvious tradeoffs.
- Preserve the distinction between the development implementation and a published release. Maintain useful old document entry points as short navigation stubs.
- Follow the [compatibility policy](docs/README.md#temporary-draft-compatibility-policy): preserve `v0.1.x` migration/import and supported published v1 data. Software versions and data-format revisions are independent. Unpublished Draft revisions do not require compatibility layers. Remove this temporary exception before the first stable v1 release and publish the stable API/upgrade policy.
- Check document links, status labels, code anchors, and `git diff --check` before delivery; do not add a documentation toolchain without a concrete requirement.

## Persistence and Extension Rules

Follow [persistence](docs/architecture/persistence.md), [Library](docs/architecture/library.md), and [Jobs](docs/architecture/jobs.md) as the maintained contracts.

- Preserve the Executor catalog, Library, and per-Job database ownership boundaries. Keep detailed execution state and large manifests in the Job bundle, and complete bundle creation before Create returns.
- Use GORM for application reads/writes. Use millisecond Unix timestamps with `autoCreateTime:milli`, `autoUpdateTime:milli`, and `softDelete:milli` for new catalog timestamps.
- Preserve durable catalog revision publication through the GORM mutation path; new catalog-visible mutation paths must maintain revisions through hooks. Keep high-frequency runtime progress out of catalog revisions, and use tie-safe cursors whenever multiple rows can share a revision.
- Keep separately queried/indexed values in columns; define cohesive blobs explicitly and use Scanner/Valuer at the ORM boundary.
- Bound manifests and queries with streaming, ordered pages, and stable cursors. Use `parent_path` for indexed immediate-child listing; do not add a second parent identity or duplicate derivable counters.
- Keep File organization independent of content. Signature bytes on FileLocation, FileVersion, and Position stay opaque `VARBINARY(256)`; only `(file_id, signature)` on FileVersion is unique. Normalize unset signatures to NULL without global encoding checks or automatic File merges.
- Let the runner own its phase/state machine; the Executor owns Job-ID locking, cancellation, lifetime, and exclusive device allocation.
- Preserve the end-to-end operation recovery boundary and separate Library/Job commits. Keep Library transactions metadata-only; do not add crash-only recovery protocols without explicit approval.
- Keep per-Job tables kind-specific with simple names such as `config`, `items`, and `copies`.

## APIs and Generated Code

Follow [API/UI architecture](docs/architecture/api-ui.md).

- Keep gRPC as the public API unless a separate redesign is approved. Keep common catalog/lifecycle methods in JobService and kind-specific operations in typed Job services.
- Register each Job runner and its service together; expose Job kind for client routing and use concise service-local method names.
- Change protobuf sources/generators first, then regenerate Go and TypeScript. Never edit generated files by hand.
- Use `tools.NewRouter` and `tools.Method` for typed oneof-to-oneof dispatch, with action variants for error-only handlers. Preserve direct type switches for conversion, validation, migration, and external events.

## Physical Data and Preview Safety

Follow the exact [Archive/Restore/Scan checkpoints](docs/architecture/jobs.md), [Media/I/O guarantees](docs/architecture/media-io.md), and [Preview contract](docs/architecture/preview.md) before changing these paths.

- Keep Archive/Restore ACP orchestration in the runners. Keep Backends stateless and Session interfaces narrow, taking the Job `*gorm.DB` and typed target directly.
- Preserve immutable Archive target identity, actual successful Media paths, per-item staging, exactly-once Write Finalize, verified publication, and submitted-only progress.
- Preserve Tape final-Index validation and continuous-prefix publication on target-full; keep Volume Finalize marker-based without directory scans/deletion.
- Never use Media prefixes or directories as batch/rollback/deletion units. Media Delete remains metadata-only.
- Manage only already-mounted Volumes; never mount/unmount/eject them. Do not bypass UUID/profile/path validation.
- Preserve ordinary LTFS file recovery, format-bound versioning, compatible append identity, and explicit delete-before-format for an existing barcode.
- Hash real transferred content only in ACP, never by rereading completed targets. Treat signature xattrs as disposable caches and preserve bounded streaming, backpressure, and no-space errors through cleanup.
- Keep one Scan pipeline with explicit signature and result policies: Media inventory requires complete validated observation under its lease, original publication requires successful scopes under the Location gate, and copy verification requires uncached reads against frozen prior facts. Preview is an optional stage and is unavailable on sequential Media. Location browsing reads actual directories without cached fallback or analysis prerequisites. Ordinary file operations use the shared planning engine within their request, not a Job; adapters provide guarded primitives, with no file overwrite or physical directory rollback. Archive creates FileVersions with verified Position publication; restore selected versions.

## Frontend and Verification

- Preserve the compact dual-pane/Inspector interactions, ordinary filesystem/Library polling, revision-based Job pagination, and current-speed versus historical-ETA distinction documented in [API/UI](docs/architecture/api-ui.md).
- Fix shared file-browser behavior in Chonky instead of per-page CSS/event workarounds. Preserve contextual root labels, visible dot-directory navigation, independent Tape drawers, and stable panel sizing.
- Use supported stable frontend tooling, active-LTS Node, reproducible lockfiles, and bounded initial bundles.
- Add semantic tests for changed behavior. Run unit tests, vet, race, frontend/generation/build checks, shell syntax checks, and [E2E](docs/operations/e2e-test.md) proportionally; state exact failed or unrun checks.
- New primary business capabilities require matching CLI commands and real CLI-subprocess E2E acceptance recorded in the E2E coverage matrix. Migrate existing CLI-expressible business steps instead of invoking private services or writing database rows; retain internal fault injection as semantic/integration coverage.
- For frontend-visible changes, update `internal/demo` and the [Demo guide](docs/operations/demo.md), run its semantic tests, and smoke-test an explicit reset fixture. Include actionable and unavailable states; the Demo does not replace unit/E2E coverage.
- Follow [test environment safety](docs/operations/testing.md): use an isolated Linux host with the official LTFS file backend; physical Tape tests require explicitly assigned scratch media.
- On remote `dev` acceptance, use only YATM binaries from the corresponding verified release package. Keep orchestration local through SSH; transfer no repository source, separately compiled test harness or helper source. Local/CI source-based tests remain separate evidence.
- Test migration only on timestamped `cp -a` copies of explicitly approved backups. Keep private fixture locations outside the repository. Never modify source backups or production installations during validation.
- Follow the [offline migration guide](docs/operations/migration.md). Verify every migrated historical Job item-by-item, including non-empty Jobs, rather than comparing only catalog counts. Keep commit and backup cleanup separately confirmed.
- Clean only validated temporary test resources after verification. Record review-comment decisions one at a time, implement the confirmed batch, then run expensive E2E once for the batch.

## Delivery Authorization

- Keep `master` aligned with `origin/main` and perform v1 development on `v1`.
- Push ACP integration only to its development branch until review is complete.
- Do not push YATM, merge ACP main, tag, or publish a release without explicit confirmation.
