# Development Checks and Test Environments

Run checks in proportion to changed behavior. Commands below run from the repository root; Go tests use isolated fixtures and do not authorize access to production data or hardware.

## Test Maintenance

Use the [existing entrypoints](#repository-layout-and-entrypoints) first for tests, regressions
and performance work: `go test`, maintained scripts, and the frontend's native Vitest and Node
test runners. Replace recurring manual agent sequences with reproducible checked-in harnesses
and fixtures in the owning test area. Extend an existing runner or script before adding another.

Keep reusable release, test and benchmark scripts and synthetic or public fixtures in this
repository. Supply machine-specific hosts, paths and private inputs through arguments or
environment variables. Keep credentials, real backups, deployment configuration and sensitive
results outside tracked source and public artifacts.

Maintain tests, harnesses and fixtures with the implementation. Update or remove obsolete
assertions, unsupported scenarios and unused tooling in the same change. Cover observable
behavior, invariants and failure boundaries; avoid duplicate tests at the same boundary and
assertions that merely repeat the implementation. Distinct unit, adapter and E2E boundaries
retain their purpose. Keep verification proportional; a small edit need not create new automation.

Record source identities, exact commands, passes, failures, skips and unrun checks with their
reasons. [Performance Acceptance](performance.md#performance-acceptance) owns measurement rules. Existing
physical Media, device, migration and visual acceptance remains required where applicable:
automate repeatable setup and checks where useful, and record each manual step's result and
why it needs an operator or visual judgment. Tar physical acceptance runs only through the
installed package: the Python controller's tar stages drive the installed service and `yatm-cli`,
like LTFS PT-01..PT-08, and the independent reader checks public-tool recovery. There are no Go
hardware tests; [physical Tape E2E](../release/physical-tar-acceptance.md#tar-acceptance) owns the stages. Follow [Media and Migration Safety](../release/release.md#media-and-migration-safety);
automation does not expand access or publication authorization.

### Check Scope and Reuse

During development and review, test the changed behavior and its affected callers. Choose the
scope before running commands; include unchanged code when a shared dependency, contract or
configuration change can affect it. A new commit by itself does not invalidate every earlier
check. Batch related fixes before expensive integration checks.

| Change | Checks during development and review |
| --- | --- |
| Documentation | Document links/examples and `git diff --check` |
| Go implementation | Changed packages and affected callers; add race checks for concurrency/shared state and both drivers for SQLite behavior |
| Frontend | Affected Vitest tests and type checking; style/lint checks for touched presentation, browser interactions for affected visible behavior |
| API, persistence or shared infrastructure | Both sides of the changed boundary, generated output where relevant, and the workflows that consume it |
| Installer, packaging or test tooling | The owning script tests and the affected installation/package workflow |
| Performance-sensitive implementation | The affected native benchmarks, once per source; larger fixtures when traversal or scale behavior changes |

Use runner selection (`go test PACKAGE -run PATTERN`, Vitest file filters and explicit script
tests) rather than creating another test framework. After fixing a failure, rerun its reproducer
and affected suite/callers. Do not restart unrelated passed suites to get another all-green run.
A failed or interrupted full run remains incomplete; record the completed checks separately.

For slow verification, report the whole workflow's elapsed time, including setup, transfers,
queueing, retries and diagnosis, separately from test execution. Use existing runner/CI timings
to identify the costly stages; individual step timings do not explain the whole wait.

Final source/package checks and the reuse of release and hardware evidence follow
[Evidence Reuse](../release/release.md#evidence-reuse).

## Repository Layout and Entrypoints

File ownership and placement rules are maintained in [Repository Layout](../../AGENTS.md#repository-layout). Follow those rules when adding or moving files; this section owns the development commands and output overrides.

The root [Makefile](../../Makefile) is the common development/CI entrypoint. Its default target displays help; implementation stays with the owning project or script. Frontend compilation remains in package.json. Use `make build`, `make backend`, `make backend-linux`, `make frontend`, `make release`, `make preview`, `make generate`, or `make demo` / `make demo-dev`. `make check` runs documentation and IDL naming checks, Go unit tests/vet, compilation and vet of the E2E harness, frontend checks, tool-script tests and shell syntax checks; `make test-race` and `make test-e2e` are explicit additional checks. Set `YATM_E2E_BIN_DIR` to the extracted candidate directory for `make test-e2e`; the [E2E guide](e2e-test.md) also documents source-based harness runs. Native Preview module validation remains separate from the root Go test suite.

Scripts locate the checkout independently of the caller's working directory. `RELEASE_DIRECTORY` overrides the final archive directory; relative values resolve from the caller's working directory before scripts change directories. Build tools are source files, not build output. The root `install-release.sh` remains the user installation entrypoint, and installation package paths are independent of source layout.

Packages under `internal/` are not an external Go API; `entity` and the public gRPC contracts are.

## Local Checks

Select checks using [Check Scope and Reuse](#check-scope-and-reuse). For example, a legacy
migration repair checks its implementation and installer caller:

```shell
go test ./internal/migrate/legacy ./cmd/migrate
go vet ./internal/migrate/legacy ./cmd/migrate
go test -race ./internal/migrate/legacy ./cmd/migrate
CGO_ENABLED=0 go test ./internal/migrate/legacy ./cmd/migrate
git diff --check
```

The coverage below is an inventory for choosing affected checks, not a requirement to run
every listed suite after each change. The [Release SOP](../release/release.md#release-sop) owns full verification.

For content-model changes, cover opaque nullable signatures, unique File/version pairs, independent same-content Files, one original per File, repeated-content version reuse, shared-copy lookup, cross-batch import rollback, and evidence-based legacy migration with source preservation.

Restore association checks include existing original associations, ignored outputs, multi-version candidate freezing, exact-content adoption, namespace collisions, Media finalization failure and explicit Library/Job publication failure. Scan verification checks include real uncached reads, per-item versus device errors, absent baselines and content-guarded health publication. Run both SQLite drivers and race checks for `./internal/executor/restore`, `./internal/executor/scan` and `./internal/library`; imported restored Files and original associations must remain intact, and imported bad-copy observations must not become healthy or unchecked accidentally.

For Files and Location changes, inspect callers in `./internal/executor/scan`, `./internal/executor/observation`, `./internal/executor/fileops`, `./internal/treeops`, `./internal/ignore`, `./internal/demo`, and `./cmd/yatm-cli`; run the affected unit/race checks and select the affected [live-file, Volume, or Preview CLI E2E](e2e-test.md). Cover optional File identity, pure browsing versus explicit collection, complete directory List, paged Search and changefeed departures, Ignore versus authorization, guarded references, physical-operation results without persistent receipts and ordered continuity where changed. Shared organization changes run the contract against its affected Library, filesystem and object-storage-semantic adapters: directory merges, retained target associations, duplicate selections, conflicts, cancellation, bounded traversal and partial publication. Do not add user Copy acceptance to the supported move/mkdir/delete contract.

Scan tests exercise one pipeline with known-only, fill-missing and force-read policies, optional comparison/Preview and each publication policy. Known-only cache misses must not hash; verification must neither trust cache nor replace its saved baseline. Preview failures must preserve otherwise valid observations, while source-read and physical-identity failures prevent publication of the current scope; a new Scan reads its complete selection. `analyze`, `preview create` and `verify` CLI presets must resolve to this same Scan service, not separate runners.

Capacity checks cross multiple filesystem and manifest pages, cover the 256-level traversal boundary, reject an over-depth tree, enforce the 1,000-row public result window and cancel while a recursive directory stack is open. On platforms exposing `/dev/fd`, the cancellation test records descriptor counts before traversal and after unwinding and requires no retained descriptor. The test suite does not claim a platform-independent peak RSS or fixed Job-database size; any such release evidence must be measured against the exact packaged candidate and stated with its fixture, filesystem and SQLite mode.

Canonical CLI subprocess tests cover minimal/projected `ls`, `files get/metadata`, `mv/rm/mkdir`, independent `preview get`, typed Archive/Restore estimates and `scan create/run/results`; protocol tests cover Preview URLs and reject full-file byte routes. Read tests assert request counts, SQL cost, omitted computation and absence of collection. The [E2E coverage matrix](e2e-test.md) records complete server/CLI workflows; subprocess transport tests alone do not establish full business acceptance. Ignore tests compare against Git in an isolated temporary repository when Git is available; production matching does not invoke Git. Check the SQLite paths with both the default CGO driver and `CGO_ENABLED=0 go test ./internal/library ./entity ./internal/executor/... ./internal/apis ./internal/migrate/legacy ./internal/demo ./internal/preview/... ./internal/media/... ./internal/resource`; this includes the external-memory read planner and temporary-resource tests themselves. Regenerate IDL with `bash dev/generate.sh` and verify a second generation produces no changes.

Replacing a workflow starts from an inventory of the baseline implementation's UI/CLI actions, results and edge cases; each maps to its replacement and regression coverage or to an explicitly approved removal. Acceptance verifies end-to-end behavior, not only the new interface shape, and stale mocks are updated with their real consumers; passing tests without capability coverage do not prove feature parity.

For frontend changes, run the scripts in [package.json](../../frontend/package.json), regenerate protobuf Go/TypeScript outputs when IDL changes, and keep the lockfile reproducible. Vitest owns tests under `frontend/src`; Node tooling tests under `frontend/scripts` run through their explicit `node --test` command in the frontend checks. A test belongs to one runner. Use active-LTS Node and supported stable frontend dependencies, and keep the initial bundle bounded. Check shell syntax for changed scripts and run builds/generation checks when those paths change. Documentation changes run `node dev/check-documents.mjs`; the check uses Node and Git only.

## CLI Coverage and Skill

[Command registration](../../cmd/yatm-cli/main.go) attaches one command to each public
service method, so every operation has a CLI spelling. [CLI coverage
tests](../../cmd/yatm-cli/coverage_test.go) hold the two together by resolving every
public RPC to one real parser leaf and rejecting a binding whose RPC no longer exists, so
a new public method without its command fails that check instead of leaving the surface
unreachable. [Local checks](#local-checks) run it.

The CLI surface and its bundled [Skill](../../.agents/skills/yatm/SKILL.md) change
together: a command, flag, output or safety rule it describes must update the Skill in the
same commit. The Skill supplies operational guidance alongside the matching installed
CLI help and JSON output. Installed copies refresh through the
[optional Skill step](../operations/install.md#optional-agent-skill).
