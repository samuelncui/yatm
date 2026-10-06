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
reasons. [Performance Acceptance](#performance-acceptance) owns measurement rules. Existing
physical Media, device, migration and visual acceptance remains required where applicable:
automate repeatable setup and checks where useful, and record each manual step's result and
why it needs an operator or visual judgment. Follow [Media and Migration Safety](#media-and-migration-safety);
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

Finish review and fixes before the final full source and package acceptance. The candidate CI
runs `make release-check` once before its platform matrix; a duplicate local full preflight is
not required. Use `make check` for an intentional full integration check, not after every edit.
If a final check finds a defect, verify the fix locally at its affected boundaries before
starting the replacement candidate. Changes to shared contracts or infrastructure may justify
wider checks; record the reason instead of automatically repeating everything.

Retain passing evidence for unaffected components when their implementation, relevant
dependencies, configuration, fixtures, harness and environment remain unchanged. Record the
old and final source identities and the reviewed diff that establishes this. New archives still
need their own content, identity and checksum checks. Rebuilt binaries require relevant package
smoke tests; changed behavior requires its package workflows. Acceptance of earlier bytes alone
cannot certify a new archive, but a new archive does not require repeating unrelated benchmarks
or every exploratory/manual check.

Hardware verification may use locally built binaries after the affected source checks pass;
GitHub availability and completion of the full platform matrix are not prerequisites. Build
from the reviewed commit with the repository's existing scripts and record the source,
toolchain, build flags, binary checksums and runtime adapters. Use the release package when
installation or package layout is part of the check. Follow the
[physical Tape execution procedure](physical-tape-e2e.md#automated-stages) for host context,
absolute LTFS adapter paths, the scoped two-partition file-backend precheck and durable staged
execution. Hardware work can start independently of CI's final release-byte acceptance;
that acceptance remains required for release delivery. Source CI and other platform builds can
run in parallel. Keep the final artifacts' identity, content and affected package smoke checks;
reuse expensive hardware results, including a full Tape write, when comparison confirms that
the relevant source, dependencies, build configuration and runtime inputs are unchanged.
If those inputs differ or cannot be established, repeat the affected checks and explain why.

Reuse downloaded candidate files for subsequent checks when their run, commit and checksums
match. Transfer only the packages and fixtures needed by the selected host and cases. A new
report or another case selection does not require another download. Complete-set validation
belongs to candidate CI; use its run and artifact records for checks already completed there.

## Repository Layout and Entrypoints

File ownership and placement rules are maintained in [Repository Layout](../../AGENTS.md#repository-layout). Follow those rules when adding or moving files; this section owns the development commands and output overrides.

The root [Makefile](../../Makefile) is the common development/CI entrypoint. Its default target displays help; implementation stays with the owning project or script. Frontend compilation remains in package.json. Use `make build`, `make backend`, `make backend-linux`, `make frontend`, `make release`, `make preview`, `make generate`, or `make demo` / `make demo-dev`. `make check` runs documentation and IDL naming checks, Go unit tests/vet, compilation and vet of the E2E harness, frontend checks, tool-script tests and shell syntax checks; `make test-race` and `make test-e2e` are explicit additional checks. Set `YATM_E2E_BIN_DIR` to the extracted candidate directory for `make test-e2e`; the [E2E guide](e2e-test.md) also documents source-based harness runs. Native Preview module validation remains separate from the root Go test suite.

Scripts locate the checkout independently of the caller's working directory. `RELEASE_DIRECTORY` overrides the final archive directory; relative values resolve from the caller's working directory before scripts change directories. Build tools are source files, not build output. The root `install-release.sh` remains the user installation entrypoint, and installation package paths are independent of source layout.

Internal packages are not an external Go API: former root implementation imports now use `github.com/samuelncui/yatm/internal/...`, without forwarding packages. The `entity` import path, public gRPC contracts, legacy wire encodings and storage formats are unchanged by this organization.

## Remote Candidate Acceptance

On the explicitly selected isolated acceptance host, package acceptance uses the corresponding checksum-verified release package, built locally or by CI. Earlier hardware diagnosis may use locally compiled application binaries under [Check Scope and Reuse](#check-scope-and-reuse). The maintained controller can invoke the server/CLI and installer through SSH or run on the authorized test host with `--host local`. For host-local execution, transfer the controller and its package validators alongside the reviewed archives, checksums, runtime adapters, fixture data and operating instructions. Application execution still uses packaged programs; do not deploy the application checkout, separately compiled test binaries such as `e2e.test`, or an in-process test server. Existing systemd, LTFS/FUSE and other host dependencies remain environmental prerequisites.

Choose an existing, spacious, xattr-capable mounted filesystem and create a uniquely scoped test directory beneath it. Place packages, installation copies, backups, Restore outputs and a private `TMPDIR` inside that root; export `TMPDIR` for remote installer and migration invocations as well as the controller. Check available space before extraction and each capacity fixture; the test owns only that directory and its explicitly registered test resources. A temporary pathname does not imply a disposable filesystem or sufficient capacity.

Local development and CI may compile and run Go test harnesses. Those results are separate from remote packaged-binary acceptance. If a fault-injection scenario requires private functions or a custom in-process server, run it locally/CI and record that limit; do not describe it as remote candidate acceptance. The [E2E guide](e2e-test.md#remote-packaged-binary-acceptance) owns the remote procedure.

For official LTFS file-backend acceptance, explicitly configure the optional `templates/testing/ltfs-file-backend` adapters from the package. They operate only on scoped virtual-cartridge directories and are not default installation scripts; their README describes identity and capacity fixtures. Physical Tape remains a separate acceptance boundary.

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
every listed suite after each change. The [Release SOP](#release-sop) owns full verification.

For content-model changes, cover opaque nullable signatures, unique File/version pairs, independent same-content Files, one original per File, repeated-content version reuse, shared-copy lookup, cross-batch import rollback, and evidence-based legacy migration with source preservation. These checks do not repair or maintain compatibility with old Draft databases.

Restore association checks include existing original associations, ignored outputs, multi-version candidate freezing, exact-content adoption, namespace collisions, Media finalization failure and explicit Library/Job publication failure. Scan verification checks include real uncached reads, per-item versus device errors, absent baselines and content-guarded health publication. Run both SQLite drivers and race checks for `./internal/executor/restore`, `./internal/executor/scan` and `./internal/library`; imported restored Files and original associations must remain intact, and imported bad-copy observations must not become healthy or unchecked accidentally.

For Files and Location changes, inspect callers in `./internal/executor/scan`, `./internal/executor/observation`, `./internal/executor/fileops`, `./internal/treeops`, `./internal/ignore`, `./internal/demo`, and `./cmd/yatm-cli`; run the affected unit/race checks and select the affected [live-file, Volume, or Preview CLI E2E](e2e-test.md). Cover optional File identity, pure browsing versus explicit collection, complete directory List, paged Search and changefeed departures, Ignore versus authorization, guarded references, physical-operation results without persistent receipts and ordered continuity where changed. Shared organization changes run the contract against its affected Library, filesystem and object-storage-semantic adapters: directory merges, retained target associations, duplicate selections, conflicts, cancellation, bounded traversal and partial publication. Do not add user Copy acceptance to the supported move/mkdir/delete contract.

Scan tests exercise one pipeline with known-only, fill-missing and force-read policies, optional comparison/Preview and each publication policy. Known-only cache misses must not hash; verification must neither trust cache nor replace its saved baseline. Preview failures must preserve otherwise valid observations, while source-read and physical-identity failures prevent publication of the current scope; a new Scan reads its complete selection. `analyze`, `preview create` and `verify` CLI presets must resolve to this same Scan service, not separate runners.

Capacity checks cross multiple filesystem and manifest pages, cover the 256-level traversal boundary, reject an over-depth tree, enforce the 1,000-row public result window and cancel while a recursive directory stack is open. On platforms exposing `/dev/fd`, the cancellation test records descriptor counts before traversal and after unwinding and requires no retained descriptor. The test suite does not claim a platform-independent peak RSS or fixed Job-database size; any such release evidence must be measured against the exact packaged candidate and stated with its fixture, filesystem and SQLite mode.

Canonical CLI subprocess tests cover minimal/projected `ls`, `files get/metadata`, `mv/rm/mkdir`, independent `preview get`, typed Archive/Restore estimates and `scan create/run/results`; protocol tests cover Preview URLs and reject full-file byte routes. Read tests assert request counts, SQL cost, omitted computation and absence of collection. The [E2E coverage matrix](e2e-test.md) records complete server/CLI workflows; subprocess transport tests alone do not establish full business acceptance. Ignore tests compare against Git in an isolated temporary repository when Git is available; production matching does not invoke Git. Check the SQLite paths with both the default CGO driver and `CGO_ENABLED=0 go test ./internal/library ./entity ./internal/executor/... ./internal/apis ./internal/migrate/legacy ./internal/demo ./internal/preview/...`. Regenerate IDL with `bash dev/generate.sh` and verify a second generation produces no changes.

Replacing a workflow starts from an inventory of the baseline implementation's UI/CLI actions, results and edge cases; each maps to its replacement and regression coverage or to an explicitly approved removal. Acceptance verifies end-to-end behavior, not only the new interface shape, and stale mocks are updated with their real consumers; passing tests without capability coverage do not prove feature parity.

For frontend changes, run the scripts in [package.json](../../frontend/package.json), regenerate protobuf Go/TypeScript outputs when IDL changes, and keep the lockfile reproducible. Vitest owns tests under `frontend/src`; Node tooling tests under `frontend/scripts` run through their explicit `node --test` command in the frontend checks. A test belongs to one runner. Use active-LTS Node and supported stable frontend dependencies, and keep the initial bundle bounded. Check shell syntax for changed scripts and run builds/generation checks when those paths change. Documentation changes run `node dev/check-documents.mjs`; the check uses Node and Git only.

## Performance Acceptance

Use fixed, representative benchmarks to record current interface performance and detect
regressions. Compare a repair with its preserved pre-repair source, a pull request with its exact
base, and a release with its recorded accepted source baseline. Do not silently replace that
baseline or require comparison with the first Alpha release.

Run each standard Go benchmark **once per source**, with `-count=1 -benchmem`. Go calibrates the
iterations within that run; there is no minimum number of separate samples or confidence-interval
gate. Use the same reviewed harness, fixture sizes, Go patch version, SQLite mode, CPU setting
and filesystem for both sources. Measure baseline and candidate serially, with other builds,
tests and agents stopped during timing. Record source identities, commands and raw results;
a skipped case, missing metric or incomplete run is not evidence.

The [suite manifest](../../dev/performance/suites.json) selects the appropriate scope:

| Tier | Coverage | Use |
| --- | --- | --- |
| `fast` | 10,000-entry List, Ignore, Search and Scan benchmarks | Related pull requests and local changes |
| `critical` | The same workloads with 100,000 entries | Traversal, SQL, projection and buffering changes |
| `release` | Critical benchmarks plus candidate-only 1,000,000-entry correctness checks | Release acceptance |
| `migration` | 10,000 Files and 20,000 physical copies on disk SQLite | Legacy archived-content reconciliation changes |

For List/Scan choose one of `fast`, `critical` or `release`; do not repeat the smaller tiers inside
a release run. Use `migration` separately when its reconciliation changes. Large-data correctness
checks remain separate from timing and run once. Frontend changes also use the relevant list
interaction and virtualization tests described in [Local Checks](#local-checks), and the
[frontend benchmark](../../frontend/README.md#frontend-benchmarks) uses Vitest's native comparison
with one invocation per source. Browser tests cover actual rendering and interaction.

List/Search fixtures associate half the entries with catalog Files and request attributes,
status, operations and navigation. They cover random/reverse names, 16 directory levels,
102 Ignore rules with exclusion and negation, and the first and next 100-row Search pages.
List records first-batch and complete-response latency with a receiver retaining all rows;
Search records each page's response latency. Both report allocations. Setup is outside the timer
and Go's benchmark loop reuses the fixture during calibration. The million-entry List check
verifies completeness, ordering and bounded batches with a receiver retaining only counters and
the previous name. Go `B/op` excludes native SQLite memory; these checks do not claim a peak RSS bound.

Scan covers one selection across one, three and nine Locations, a small physical selection in a
large recorded Location, and renamed originals across three Locations. Each case reports Create,
completion, first-result-page and total latency, plus allocations. Scan intentionally uses
`-benchtime=1x -count=1`: it modifies catalog state, so a second operation on that same catalog
would measure a different workload. Catalog setup and Job deletion remain outside the timer.
The million-entry Scan check verifies completion and all result pages once on the candidate.

Legacy migration changes use the same collector with `--tier migration`; it runs
`BenchmarkPrepareArchivedVersions` once on each source with `-benchtime=1x`, records identities
and feeds the same comparison command. Each operation consumes fresh on-disk staging tables,
including duplicate copies across pages; setup and result-count checks remain outside the timer.
This focused check measures archived-content reconciliation, while complete migration acceptance
uses the packaged installer and a copied legacy backup through the
[package controller](e2e-test.md#remote-packaged-binary-acceptance).

Use repeatable `--package ./internal/apis` or `--package ./internal/executor/scan` to compare
only that package's suites within the selected tier. Package names must match the manifest;
structural checks and common harness files follow the selected packages. `pair.json` records
requested, selected, executed and skipped packages and structural scope. A completed subset is
not a full release comparison. Baseline and candidate use independent mutable temporary fixtures.
The manifest and shared harness are captured once and applied identically to both sources.

Fixture-only changes can measure preparation separately with
`go test ./internal/apis -run '^$' -bench '^BenchmarkFilesFixtureSetup$' -benchtime=1x -benchmem -count=1`.
It measures fresh file creation and catalog seeding at 10,000 and 100,000 entries; deferred cleanup
is outside the timer. It is not a List/Search runtime comparison. Reuse their existing measurements
when their implementation and equivalent input distribution remain unchanged.

The collector snapshots the supplied sources and installs the same reviewed harness in both.
It records commits and actual source hashes, including dirty changes, then compiles both suites
before measuring. Each suite executes its standard Go test binary once per source. This is the
same benchmark mechanism as `go test -run '^$' -bench PATTERN -benchmem -benchtime=1s -count=1`;
precompilation keeps compilation out of the measurement window. It adds no custom sampling loop.

```shell
node --test dev/check-performance.test.mjs dev/performance/*.test.mjs

PERF_OUTPUT="$(mktemp -d)/pair"
node dev/performance/collect.mjs \
  --baseline "$PERF_BASELINE" --candidate "$PERF_CANDIDATE" \
  --harness "$PERF_HARNESS" --out "$PERF_OUTPUT" \
  --tier fast --environment "$PERF_ENVIRONMENT" --idle
node dev/check-performance.mjs "$PERF_OUTPUT"
```

Set the checkout paths and environment label explicitly. Output must be a new directory outside
all source checkouts. Defaults are `--cpu 2`, `--benchtime 1s` and `--cgo 1`; use `--cgo 0` for
pure-Go SQLite. The harness's `go.mod` selects the exact Go patch toolchain. Only ordinary source
files are supported; symlinks/submodules and incompatible harnesses fail instead of silently
changing the inputs. Sources are measured on the same host, not compared with unrelated old
machine timings. Ambient Go configuration and workspace/module edits are disabled.

The [comparison](../../dev/check-performance.mjs) reports each measured time/allocation value and
percentage change. An increase above 10% flags the case for review; it does not prove a code
regression or fail a completed comparison. CI highlights these flags as warnings. Missing, invalid
or failed measurements still fail the command. Review the flagged implementation and measurement
conditions before accepting the change; if necessary, rerun only that case on both sources. Keep
the initial result and explain the finding. Confirmed regressions must be fixed; unexplained flags
remain open acceptance work. Do not repeat the whole suite until it passes or automatically replace
the baseline. The comparison reports measurements, not release approval.

Keep `pair.json`, raw `.bench` files, structural logs and `comparison.json` as verification
artifacts outside maintained documentation. An interrupted collection remains incomplete.

The [Performance workflow](../../.github/workflows/performance.yml) measures a PR's exact head
and event base with the base revision's reviewed harness. Manual critical/release runs use the
requested full candidate commit and `PERFORMANCE_ACCEPTED_BASELINE`, an explicitly accepted full
commit SHA. Both sources run once on that runner. The workflow is read-only, retains evidence on
failure, and never updates the baseline or publishes a release. Missing tools/harness require a
reviewed change; a missing or invalid baseline fails the hosted comparison.

Release preparation requires one completed, reviewed `release` comparison of the final source
against the recorded accepted source baseline, collected locally or through the hosted workflow.
Both sides must use the same host, tools and reviewed harness under the conditions above. Evidence
must identify the exact baseline, candidate and harness commits and content hashes, including any
dirty changes, with the commands, raw results and flag resolutions. Complete the structural checks
and relevant frontend checks as well. Resolve every flag before candidate or source-push approval;
an incomplete comparison or unexplained flag remains open work. A completed local comparison
satisfies this requirement without uploading its baseline or repeating it on a hosted runner.
Candidate assembly runs its source and package gates without automatically invoking Performance.

After measurement, review the diff against each measured operation. Retain its evidence when
the relevant runtime source, dependencies, harness, fixtures and measurement settings are
unchanged, recording the measured and final commits. Documentation or unrelated runtime/tooling
changes do not require repeating that operation. Rerun only affected benchmarks and resolve their
flags; do not repeat the whole release tier for a change to one component. The final source
preflight and acceptance of the exact release archives remain required.

## Large Identical Results

The opt-in performance fixture builds isolated 10,000- and 100,000-File catalogs with many pairs,
a large group, a long transitive chain, saved versions, hidden names and unknown sizes. It measures
one complete Find and retained 200-row reads, then checks every sort direction, random offsets and
File ID position lookups against an independent result. Page checks verify that catalog collection
and component construction do not run again.

```shell
YATM_IDENTICAL_PERF=1 go test -count=1 ./internal/library \
  -run '^TestIdenticalLargeCatalogPerformance$' -v -timeout 30m
CGO_ENABLED=0 YATM_IDENTICAL_PERF=1 go test -count=1 ./internal/library \
  -run '^TestIdenticalLargeCatalogPerformance$' -v -timeout 30m
```

These measurements cover query staging and retained SQL reads, not API member observation or
browser rendering. Go allocation measurements exclude fixture construction and native SQLite
memory; use an external process measurement when peak memory matters. The ordinary unit suite
skips this fixture, and the default Demo remains small enough for interactive review.

## Local Demo

`make demo-dev` prepares the same Demo fixture and builds the backend incrementally, then serves
Vite hot reload at `http://localhost:5173`, proxying the selected backend listen address.
`YATM_DEMO_FRONTEND_PORT` overrides the strict frontend port. A port conflict fails startup;
both children stop on signals or either child's exit. Fixture reset remains explicit through
`YATM_DEMO_RESET=1`. `make demo` retains the production frontend build for packaged-browser checks.

`make demo` builds and serves the disposable review fixture at `http://127.0.0.1:18080`, normally
under the system temporary directory. Restarting preserves reviewer changes. Select an isolated
temporary root and reset it explicitly after fixture or pre-stable format changes. Stop its server
before resetting that root; concurrent reviews use distinct roots:

```shell
YATM_DEMO_ROOT=/private/tmp/yatm-demo-review \
YATM_DEMO_LISTEN=127.0.0.1:18090 \
YATM_DEMO_RESET=1 make demo
```

Reset replaces only a validated Demo root whose basename begins with `yatm-demo` and which is below
a temporary directory. Never place valuable files there. The fixture uses normal Library tables,
Job runners, Volume markers and ACP transfers; it has no production mock branch and must never be
used for a physical Tape operation.

The default fixture keeps five Locations and thirteen Jobs. Setup retains only Jobs needed by current
Location links or distinct review results; saved versions and Preview assets remain. A complete
Documents Scan supplies its LastSync link while later partial Scans supply current results.
Archive preparation collects Incoming's sources directly, and one Scan publishes their content
observations and generates Previews. The retained examples cover these review paths:

| Fixture | Review coverage |
| --- | --- |
| Library | Annotations, mixed-field search, 101 invoices spanning two 100-result pages, logical organization and Trash, saved and unknown legacy archive dates |
| Documents | Original-only, unknown and covered content under the `archive-review` tag, unarchived edits, three text/image saved versions and date cutoffs, Ignore and dot directories, unadmitted arrivals, empty folders, failed Scan with a reconnected source |
| Shared files | One 201-member group with three dot files, three additional pairs, three/four-member groups, arbitrary scrolling, collapse, hidden rows and independent Delete; a.txt/b.txt connect transitively to unavailable c.txt with different current sizes |
| Unavailable originals | Retained originals under an inaccessible root, cross-Location duplicates and saved-version evidence |
| Incoming | Four Archive sources across two directories, a pending Archive and a completed Scan with image/video Previews |
| Restored files | Preferred Restore destination, pending multi-file Restore and completed adoption of identical existing output |
| Media | Random-write HDD, sequential-write HM-SMR, offline Volume, uninitialized disk and metadata-only Tape probe; inventory additions/changes/removals, healthy/mismatched/missing Verify findings and a Tape-bound pending check |

For large browser reviews, opt in with `YATM_DEMO_IDENTICAL_FILES`, forwarded to `cmd/demo` as
`-identical-files`. The integer is the **total number of members across Shared files' Large group
and Many groups together**, including the default 207 members; it excludes the three/four-member
groups, transitive-history example and other Demo files. Values below 207 are rejected before
reset or writes. Any explicit count, including 207, requires reset when reusing an existing root;
omit the option on later restarts to preserve the fixture and reviewer changes.

```shell
YATM_DEMO_ROOT=/private/tmp/yatm-demo-identical-10k \
YATM_DEMO_IDENTICAL_FILES=10000 YATM_DEMO_RESET=1 make demo

YATM_DEMO_ROOT=/private/tmp/yatm-demo-identical-100k \
YATM_DEMO_IDENTICAL_FILES=100000 YATM_DEMO_RESET=1 make demo
```

Run one server at a time or set distinct listen addresses. At 10,000 members there is one
5,000-member group and 2,500 pairs; at 100,000 there are 50,000 members in that group and 25,000
pairs. Smaller totals retain at least the original 201-member group, with rounding assigned to
that group so every other group is a complete pair. Every eightieth member of the large group
has a dot name. Existing feature examples and the five Locations/thirteen Jobs remain intact.
Setup writes added files in bounded batches, then uses one normal complete Scan of Shared files
to publish signatures and replace its setup Job. Cancellation stops pending batches and drains
active Jobs before closing the catalog. Large setup duration and disk use depend on the requested
count and host; it has no fixed 20-second Scan deadline or maximum fixture count.

For each size, use explicit Find with Library and with Locations selecting Shared files. Review
far scrolling within the large group and across the pairs, collapse/expand, name/size sorting in
both directions, hidden files, and independent Delete/Keep in the disposable Location. Repeat Find
after mutations. The small saved-history example still exercises transitive Library grouping.

Use an explicit Find for either identical source; Locations uses current records rather than
saved-version links. `Photos/preview-video.mp4` is a licensed, silent five-minute Big Buck Bunny
excerpt at 640×360 with an actual native-worker poster, thirty distinct 320×180 timeline frames
in a three-row sprite and VTT spanning 00:00–05:00. The timeline uses the production defaults of
320×180 tiles, a 10-second interval and at most 30 frames. These bundled
assets pass through normal content-addressed Preview publication and require no installed helper.
Its File Note displays the credit and license; [asset attribution and reproduction](../../licenses/demo-video.LICENSE)
records the exact source, tools, commands and checksums. Bundled assets do not establish acceptance
of a current native-helper release.

`YATM_DEMO_VIDEO=/path/to/clip.mp4 YATM_DEMO_RESET=1 make demo` preserves the optional MP4 override.
It generates derivatives with `yatm-preview` on `PATH` or beside the executable and requires that
native helper. To reproduce the bundled output and check override creation and reuse:

```shell
YATM_TEST_PREVIEW_HELPER=/path/to/yatm-preview go test -count=1 ./internal/demo \
  -run 'TestBundledVideoAssetsReproduceWithNativeWorker|TestPrepareVideoOverrideUsesNativeWorker'
```

Frontend-visible changes update `internal/demo` and its semantic tests. Keep fixtures deterministic,
bounded and representative of actionable, unavailable, empty and error states. Verify with
`go test ./internal/demo`, `bash -n dev/demo.sh`, an explicit reset and a browser smoke test. Demo
review supplements semantic and E2E tests; the exact visible behavior belongs to the UI architecture
and automated tests rather than a fixture transcript.

## Release SOP

Run this procedure for every release. The release workflow enforces the automatic gates below;
source review and physical acceptance retain their own explicit decisions.

1. **Review scope and repository content.** Compare the public baseline with the final source by
   Files/Locations, Identical, Jobs, Preview, installation/migration and API/CLI. Map retained
   capabilities to their consumers and meaningful tests; resolve unclear removals before freezing
   source. Check Demo coverage, including large results and real licensed media where relevant.
   Review terminology and affected operator/architecture instructions against actual commands and
   behavior. Update the existing owner, remove superseded claims and close verified issue records.
   Check the [Tape script compatibility contract](../architecture/media-io.md#tape-script-compatibility)
   against the published adapters: run unchanged historical scripts through the current caller,
   and verify that upgrade preserves customized scripts/helpers. For an explicitly approved
   incompatibility, check the actionable installer notice and the adapted script boundary.
   Cover the [Tape lifecycle](../architecture/media-io.md#tape-lifecycle), including empty electronic
   barcode FORMAT, subsequent identity checks, APPEND and Restore.
   Keep these checks in the automated suite; a new-template hardware pass alone does not cover upgrades.
   Inspect modules, tools and temporary files for a current runtime, generator, test or packaging
   consumer before deleting them. Review ignored local directories separately; CI cannot inspect
   workstation leftovers. Accepted limitations remain documented and do not become release blockers.
2. **Audit the history and squash unpublished development.** Scan the development range before
   squashing, including intermediate source and commit messages. Review credentials, internal
   services, personal paths, real data, configuration and private notes manually as well as with
   the pinned scanner. Rotate an exposed real credential before attempting public-history cleanup;
   history rewriting needs separate authorization. Recheck the public branch tip, retain a local
   backup and squash only unpublished commits into one release commit whose parent is that tip.
   Do not publish the backup branch or rewrite already public history. Review the final diff and
   commit message again; squashing does not sanitize final content. Finish required dependency
   releases and update their pins before freezing YATM.
3. **Complete review and fixes with affected checks.** Review the integrated source and finish
   focused fixes/verification until no unresolved, unaccepted finding remains. Use
   [Check Scope and Reuse](#check-scope-and-reuse); do not run a full preflight per fix.
   Complete the [performance acceptance](#performance-acceptance) before source-push approval,
   retaining unaffected measurements and checking only changed operations again.
   Start affected hardware verification from a reviewed local build as soon as its source and
   nonphysical checks pass, following [Check Scope and Reuse](#check-scope-and-reuse). Do not
   wait for GitHub or the full release matrix to investigate or verify a hardware fix.
   For workflow edits, validate GitHub syntax and expression contexts before pushing with
   `go run github.com/rhysd/actionlint/cmd/actionlint@v1.7.12 -shellcheck= -pyflakes=`;
   script diagnostics remain covered by the existing shell checks.
4. **Run the final source preflight on the clean release commit.** The candidate workflow owns
   the final full run before its platform matrix. A local full run is available for an intentional
   integration check or diagnosis, but is not a prerequisite that CI must then repeat.
   Put Bash 4.4 or newer on `PATH` for
   the Linux installer unit tests; macOS's bundled Bash 3.2 is insufficient. Install the frozen frontend dependencies,
   pinned scanner and the protobuf tools used by current generated source: protoc 26.0,
   protoc-gen-go 1.33.0 and protoc-gen-go-grpc 1.3.0. Preview acceptance also requires a separate
   FFmpeg fixture generator with libx264, 10-bit libx265, PNG, lavfi and display-rotation support.
   Native release CI also prepares the checksum-pinned CC0 camera samples from the
   [worker fixture inventory](../../previewworker/testdata/raw-fixtures.json), so real-camera
   acceptance runs for every advertised RAW family instead of skipping for missing local data.
   `node build/release/check-preview-fixtures.mjs` generates tiny real inputs and fails on missing
   tools or features. Linux CI builds the pinned generator with
   `bash build/release/build-test-ffmpeg.sh OUTPUT_DIRECTORY` after installing pkg-config,
   libx264-dev, libx265-dev and zlib1g-dev, and adds its `bin` directory to `PATH`. This is a test
   dependency; installed YATM and its packaged helper do not need it. Use a complete Git checkout and run:

   ```shell
   RELEASE_VERSION=v1.0.0-alpha.2 RELEASE_COMMIT="$(git rev-parse HEAD)" \
     GITLEAKS_BIN=/path/to/scanner/gitleaks make release-check
   ```

   The preflight rejects dirty or mismatched inputs, incomplete history, unowned source paths,
   runtime databases/logs, local configuration, compiled output, archives and scratch files.
   It scans current source and all history reachable from HEAD, then runs documentation/IDL,
   Go/frontend/build-tool checks, full race checks, both SQLite paths and two clean generations.
   Native Preview race tests and vet run with its private libraries during each runnable helper build.
   Its corresponding-source archive has a separate inventory that retains required vendor/native
   sources. Filename gates do not prove that a module is used or documentation is semantically correct.
5. **Build and accept one candidate set.** GitHub Actions requires the exact reviewed commit to
   exist in this repository's remote. Obtain explicit authorization for that source push before
   dispatch; this does not authorize a tag, Release or asset upload. Dispatch the Release candidate
   workflow with `publish=false`. Its platform matrix waits for `make release-check`. Candidate CI
   validates the complete [package set](#release-backend-builds), public archive content, packaged documents, licenses,
   identities and checksums. Check the exact extracted bytes at affected package boundaries
   for installation/upgrade, supported `v0.1.x` migration, Archive, Restore, Scan, Volume/LTFS
   and native Preview. Apply [Check Scope and Reuse](#check-scope-and-reuse) to retain valid
   earlier behavior and hardware evidence instead of repeating every workflow after a rebuild.
   Use the [local package controller](e2e-test.md#remote-packaged-binary-acceptance) with the exact
   Linux main/Preview archives, `--ltfs` and a new private evidence directory for its named workflows.
   Its report does not replace separate browser, legacy-backup or physical-media acceptance.
   Complete the [Media and migration gates](#media-and-migration-safety), including separately
   authorized scratch Tape acceptance. A skipped or unavailable required check remains open.
6. **Approve and publish the accepted bytes.** Review the final public source, commit message,
   Release text and all assets. Obtain publication approval before tag or Release creation and
   asset upload; any further source push also needs explicit authorization.
   Follow the existing [upload procedure](#release-backend-builds) with the successful candidate
   run ID; it rechecks provenance, content and checksums without rebuilding. Verify public asset
   checksums against the accepted files after upload.
7. **Record evidence and clean owned resources.** Report passes, failures, accepted limitations and
   unrun checks accurately. Keep raw verification output in CI artifacts and sensitive evidence
   private. Remove only temporary processes, directories and test resources owned by that run.

## Release Backend Builds

Linux backend builds require Zig **0.15.2** and always enable CGO, selecting the existing mattn C SQLite driver. `build/backend/build.sh` preserves version/commit metadata and uses external static musl linking. `build/backend/cross-linux.sh` defaults to amd64; set `GOARCH` and, for ARM, `GOARM` explicitly. Non-Linux release builds retain their existing `CGO_ENABLED=0` strategy.

| Go target | Zig target | CPU selection |
| --- | --- | --- |
| linux/amd64 | x86_64-linux-musl | default |
| linux/386 | x86-linux-musl | default |
| linux/arm64 | aarch64-linux-musl | default |
| linux/arm, GOARM=5 | unsupported | Zig 0.15.2 lacks the ARMv5 `__sync` atomics required by Go CGO |
| linux/arm, GOARM=6 | arm-linux-musleabihf | arm1176jzf_s |
| linux/arm, GOARM=7 | arm-linux-musleabihf | cortex_a7 |
| linux/s390x | s390x-linux-musl | default |

ARMv5 and other unmapped Linux targets or ARM values fail before compilation. ARMv5 remains an explicitly allowed failure in the CI matrix and produces no candidate archive. There is no automatic pure-Go fallback. Every built Linux program passes `build/release/check-static-linux.mjs`: ELF program headers must have no interpreter or dynamic segment, Go build metadata must enable CGO, and no native Preview module may enter the main dependency graph. This excludes shared libc/SQLite/FFmpeg dependencies. Runtime acceptance remains Linux amd64; other mapped release targets are experimental until exercised on matching hardware.

Distributed main binaries omit Go debug data and strip native debug data at the Linux external
link, so compiler and cache paths do not become public package content. Package scanning checks
the final executable strings as well as text files; trimming Go source paths alone is insufficient.

```shell
GOARCH=amd64 ./build/backend/cross-linux.sh
node build/release/check-static-linux.mjs output/yatm-httpd
```

The [optional native Preview helper](../../previewworker/build/README.md) has its own module, build, platform baseline and private native libraries. Its native dependencies are never linked into the main binaries.

The source-check job builds the frontend once and collects its locked production dependency
notices. The internal `shared-frontend` artifact records version, commit, lockfile hash and every
file checksum. Each main-platform build validates its identity and exact file inventory, then
copies the same frontend and Node notices and adds its own Go/native notices. Source maps, private
configuration, missing files and extra files are rejected. The internal manifest is never copied
into a release archive. Local `make release` calls the same collector; set
`FRONTEND_ARTIFACT_DIRECTORY` to reuse a previously collected artifact with matching inputs.
Preview native-object caching follows the [native build guide](../../previewworker/build/README.md#native-compiler-cache).

Release validation requires exactly nine main archives, three optional helper archives and three matching source archives. Each main archive still passes `build/release/check-release.mjs`; `build/release/check-candidate-set.mjs` additionally verifies the complete platform set, matching identities, helper file boundaries and corresponding-source checksums. Linux E2E receives the extracted helper through `YATM_TEST_PREVIEW_HELPER` so native Preview acceptance cannot silently skip.

Release builds require clean committed source and a matching `RELEASE_COMMIT`. Inputs are exported
from that commit, so ignored workstation files cannot enter recursive copies. The platform matrix
starts only after the source checks, full race suite and both SQLite paths pass. Frontend tests use
two workers to bound local and CI resource use.

Public content checks use checksum-pinned Gitleaks 8.30.1 with its default credential rules and
additional personal-path/private-service checks. The installer also pins the default rule source;
scans retain its credential rules while removing global dependency/generated-file exclusions.
Install it with
`bash build/release/install-gitleaks.sh /path/to/scanner`, set `GITLEAKS_BIN` to that directory's
`gitleaks`, and run `node build/release/check-content.mjs source . HEAD`. A different revision range
can inspect unpublished development before squashing. `node build/release/check-content.mjs artifacts /path/to/candidates` checks
nested archives and embedded executable/library strings, normalizing npm `.tgz` names in a private
scan copy so the scanner opens them. Source maps and dependency sources are
included; the only project exception is the exact deterministic physical-Tape fixture seed at its
owned path. A repository's `.gitleaksignore` may contain explicitly approved historical fingerprints
in full `commit:file:rule:line` form. Only the Git history scan reads them; current source and package
scans always use an empty ignore file. Commit-free, wildcard and path/rule-wide exceptions are
rejected. Additions require a separate decision about the specific already published finding;
new commits remain fully scanned. Checksum-validated dependency sources have rule-specific exceptions for public source
identifiers, deterministic test values and recorded upstream build/sample paths. Each is restricted
to its exact nested member and matched value; other credentials or paths in those members still fail. Failures
report rule counts without match snippets, and temporary scanner reports are
deleted instead of uploaded as public CI evidence. Scanner regression tests use the same executable.

The **Release candidate** workflow runs only through `workflow_dispatch`. Set `version` and leave
`publish=false` to build the candidate set at the selected commit, validate every package and run the
full Linux E2E suite against extracted candidate binaries and the native helper. A successful run
retains the complete accepted set, companion checksums and `SHA256SUMS` in `accepted-candidates`.
Build completion establishes neither performance acceptance nor publication approval.
Creating a GitHub Release does not start a build or upload.

For local or remote package acceptance, download only the selected platform's artifacts into
one directory identified by the candidate run. For example, Linux amd64 acceptance uses:

```shell
gh run download "$CANDIDATE_RUN_ID" --repo samuelncui/yatm \
  --name candidate-linux-amd64 --name candidate-preview-linux-amd64 \
  --dir "$CANDIDATE_DIRECTORY"
```

Multiple selected artifacts are extracted into per-artifact subdirectories. The Preview artifact
includes its helper and matching corresponding-source archive. Verify the
downloaded checksums and expected commit, and retain these files for subsequent cases and physical
acceptance. Record the final successful run and artifact IDs; tests started before candidate CI
finishes remain provisional until that same run succeeds. Do not download the complete
`accepted-candidates` merely to repeat passed CI inventory, content or checksum checks locally.
The publication job retrieves that complete artifact on GitHub when it needs all release assets.

To deliver those accepted bytes, explicitly create the matching Release and tag first, then dispatch
with the same `version`, `publish=true` and its successful build `candidate_run_id`. Publishing runs
no builds or E2E; it downloads that run's immutable artifact by ID. The run must belong to this
repository and the Release candidate workflow, identify a manual build of that version, and have
completed successfully. Its accepted artifact must still be available and unexpired. The release
tag, including an annotated tag, must resolve to the run's exact commit, and the Release prerelease
flag must match the version. Candidate identity comes from run metadata and package contents rather
than the upload checkout. Release tools come from `v1`, pinned to the same commit for preflight and
upload; no code from the candidate artifact is executed during upload.

Upload repeats provenance and public-content checks, validates the exact 9+3+3 set and every checksum
with the existing package validator, and sends the unchanged archives and checksum files to that
existing Release.
Only the upload job has `contents: write`; candidate lookup and download use scoped `actions: read`.
The workflow neither creates a Release or tag nor replaces existing assets. An expired artifact needs
a new build and acceptance run before publication. Mocked provenance and upload tests run through
`node --test build/release/publish-candidate.test.mjs` without publishing anything.

## Media and Migration Safety

- Run [mounted Volume and LTFS file-backend E2E](e2e-test.md) for affected copy/publication behavior. Use the official LTFS file backend on the isolated Linux acceptance host through packaged binaries and local orchestration; Volume tests use isolated already-mounted directories.
- Run [physical Tape E2E](physical-tape-e2e.md) only with explicitly assigned scratch media and isolated databases/configuration. A documentation update is not authorization to format, write, or test a cartridge.
- Test migration only on timestamped `cp -a` copies of explicitly approved source backups. Keep private source paths and detailed reports outside the public repository. Never modify source backups or production installations during validation.
- Compare every migrated Job item-by-item, including non-empty historical Jobs, not only catalog counts. Cover prepare, abort, repeat prepare, commit, and separately confirmed cleanup.
- Clean only validated temporary test media, mounts, databases, Job bundles, and migration copies after validation. Never use production paths as cleanup targets.
- Batch confirmed review fixes before expensive E2E rather than repeating the full suite after each comment. Record actual results and unrun checks accurately; do not claim planned tests passed.

## Delivery Gates

The candidate preserves [independent format identities](../architecture/persistence.md#published-data-formats) and legacy migration/import. Record acceptance for the exact candidate bytes, reusing unaffected source-level evidence under [Check Scope and Reuse](#check-scope-and-reuse); earlier development results alone do not certify a rebuilt release. Preserve a forward path for supported published data. Before the first stable v1 release, replace the [pre-stable compatibility policy](../README.md#pre-stable-compatibility) with the published stable API/upgrade policy.

Branch, push and publication gates follow [Delivery and Authorization](../../AGENTS.md#delivery-and-authorization). Release packages include only the operator documents allowlisted in the [documentation convention](../README.md#release-packages); development and architecture documents remain in the repository checkout.
