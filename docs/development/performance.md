# Performance Acceptance

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
| `fast` | 10,000-entry List, Ignore, Search and Scan; 1,000/10,000-entry Archive result pages | Related pull requests and local changes |
| `critical` | List/Search/Scan and Archive result pages with 100,000 entries | Traversal, SQL, projection and buffering changes |
| `release` | Critical List/Search/Scan, Archive pages at 1,000/10,000/100,000/1,000,000 entries, and candidate-only million-entry correctness checks | Release acceptance |
| `migration` | 10,000 Files and 20,000 physical copies on disk SQLite | Legacy archived-content reconciliation changes |
| `read-fast` | Representative tar/LTFS planning benchmarks and all five development seeds | Restore planning development and affected pull requests |
| `read` | The same planning benchmarks, all five development and five acceptance seeds, and fixed references | Complete Restore route-quality acceptance |
| `read-resources` | Streamed 100,000/1,000,000-item budget and cancellation cases, plus production tar evaluator cases | Restore planning resource and cleanup acceptance |

For List/Scan choose one of `fast`, `critical` or `release`; do not repeat the smaller tiers inside
a release run. Use `migration` separately when its reconciliation changes. Large-data correctness
checks remain separate from timing and run once. Frontend changes also use the relevant list
interaction and virtualization tests in the frontend suite, and the
[frontend benchmark](../../frontend/README.md#frontend-benchmarks) uses Vitest's native comparison
with one invocation per source. Browser tests cover actual rendering and interaction. Shared loading or virtual-list changes
include a cold route, deliberately delayed code/data responses, an error and explicit Retry,
and forward/reverse/distant scrolling with long rows. Verify request counts, selected menus
and real scroll geometry: a mocked virtualizer cannot establish those layout properties.

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

Result browsing uses `go test ./internal/executor/scan -run '^$' -bench '^BenchmarkScanListEntries$'
-benchmem -count=1`: a retained 100,000-entry manifest, first-page counts, forward/reverse
cursors and far offsets, with warm or reopened runners and Preview presence checks. Fixture
creation is outside timing. This isolates the public handler from Scan execution, RPC transport
and browser rendering. Keep its small fixture correctness check in the regular Go suite.

Archive result paging uses `go test ./internal/executor/archive -run '^$'
-bench '^BenchmarkArchiveManifestPages$' -benchmem -count=1`. It queries retained on-disk
manifests of 1,000, 10,000, 100,000 and 1,000,000 synthetic files in 200-row pages, including an initial
total, forward/reverse cursors and a far offset. Fixture construction and correctness
checks are outside timing. This measures manifest queries and result projection, not
Archive preparation, RPC transport, browser rendering or physical Tape I/O.

Legacy migration changes use the same collector with `--tier migration`; it runs
`BenchmarkPrepareArchivedVersions` once on each source with `-benchtime=1x`, records identities
and feeds the same comparison command. Each operation consumes fresh on-disk staging tables,
including duplicate copies across pages; setup and result-count checks remain outside the timer.
This focused check measures archived-content reconciliation, while complete migration acceptance
uses the packaged installer and a copied legacy backup through the
[package controller](../release/package-acceptance.md#remote-packaged-binary-acceptance).

Restore planning separates deterministic route quality from representative execution timing.
The common harness runs against both the frozen old source and the candidate with identical
inputs and models. `read-fast` uses the development seed set; `read` also includes the separate
acceptance seed set. Each report retains the complete case inventory, generator version,
seed, selection and population sizes, input/model hashes, full inputs, LOGICAL and retained
routes, best working cost, adoption result and termination reason. Small exact references
enumerate every complete file permutation under the actual initial-state and completion semantics.
Nominal admission makes their eligible and unconstrained minima coincide; sensitivity results remain
separate from search. Fixed references supply signed cost gaps for both implementations;
falling short of a reference does not stop measurement or discard a complete route.
The fixed mixed308 case retains its measured LTFS layout and the fixed 1.5 TB population retains
its original mixed sizes and 20% selection. Synthetic boundary cases may span more logical data
than a physical LTO-5 cartridge: their purpose is to force distinct continuous runs and exercise
31/32/33-run search boundaries, not to predict that cartridge's capacity or elapsed time.
The quality and representative timing harnesses use the same 300-second budget on both sources,
matching the Device default; the fixed 1.5 TB workload must not receive a shortened acceptance
budget. Explicit resource deadline/cancellation tests retain their separate budgets. Reports retain
bounded per-pass search diagnostics so exhausted budgets can be explained without rerunning or
resampling the workload.
Both read tiers check that the scenario budget reaches the instrumented production evaluator;
quality and timing consumers cannot independently override it.
The two fixed 1.5 TB cases retain their LOGICAL, generated and selected costs, including a tied
LOGICAL fallback. Complete the selected experiments before comparing route quality, planning
cost and resource use; no minimum improvement or per-case no-regression rule selects the winner.

The [route comparison](../../dev/performance/read-quality.mjs) reports wins, ties, losses and
exclusions overall and by backend/family, with median, P90 and worst absolute/relative cost
changes and exact-reference gaps. These percentiles describe the fixed input population,
not timing confidence. Every case retains its input and routes, including the identified worst
case. LOGICAL-to-old, LOGICAL-to-new, old-to-new, best-working-to-retained and model-plus-planning
costs remain separate. A complete route retained at budget expiry remains comparable and carries
an explicit termination label; missing models, skipped cases and failed runs retain their status
and cannot provide an invented cost. Every nominal increase, fixed-reference gap and parameter
tradeoff stays visible. Policy labels are descriptive metadata, not a validator allowlist or an
acceptance rule. The comparison completes even when a route is slower than LOGICAL, an old
implementation or a reference. Member coverage, replay consistency and evidence integrity remain
correctness checks. Synthetic one-factor rescoring of locate, longitudinal and read coefficients
at 0.5/1/2 is sensitivity evidence, not physical-device evidence.

Representative planning benchmarks use one fresh operation (`-benchtime=1x`) per source and
report total time/allocations, indexing, search/scoring, standalone full retained-route scoring,
nominal provider evaluator calls (`evaluations/op`), optional all-model planner diagnostic counts
(`total-evaluations/op`), temporary disk bytes, modeled route cost and model-plus-planning cost.
Selections of at most 200 members also report `lower-bound-model-s/op`: a Held-Karp bound on
the same modeled costs, below every complete route, so its distance to the retained cost bounds the
remaining search gap. It is a reported quantity, not a pass threshold; larger selections omit it.
The all-model count includes nominal and variant scoring calls; an older source without that
diagnostic reports it as unavailable, and the pair comparison does not compare it. Process
high-water RSS includes native SQLite allocations and is cumulative across the test binary;
it is not an isolated per-case peak. The explicit `read-resources` tier streams large inputs,
checks complete consumption or authoritative-manifest fallback, exercises cancellation during
ingestion and after optimization state allocation, and verifies temporary-directory cleanup.
On cancellation, `plan-ns/op` includes cleanup performed before `PlanReadSequence` returns;
`consume-cleanup-ns/op` then measures post-return settling and checks. For successful plans,
the latter also includes execution-index traversal and `Close`.
Its disk sampling includes SQLite journals and may miss peaks between samples. Resource and
CPU/allocation increases above 10% remain investigation warnings rather than automatic failures;
route costs and parameter sensitivity are likewise reported for review without automatic rejection.

Use repeatable `--package ./internal/apis`, `--package ./internal/executor/scan` or
`--package ./internal/executor/archive` to compare only that package's suites within the
selected tier. Package names must match the manifest;
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
the initial result and explain the finding. Finish the comparison before selecting an implementation;
weigh observed route quality, planning cost, resource use and applicability together, including
losses and tradeoffs. Do not repeat the whole suite until it passes or automatically replace the
baseline. The comparison reports measurements, not release approval.

Keep `pair.json`, raw `.bench` files, structural logs and `comparison.json` as verification
artifacts outside maintained documentation. Read tiers additionally seal paired full-case JSON
reports and logs. Failed quality runs retain full inputs and any partial routes for diagnosis;
failed or unexecuted cases cannot complete acceptance. An interrupted collection remains incomplete.

The [Performance workflow](../../.github/workflows/performance.yml) measures a PR's exact head
and event base with the base revision's reviewed harness. It also runs the frontend's native
directory and 1,000/10,000/100,000/1,000,000-row Job cache comparison serially after the Go
measurements. Both collectors retain source/harness identities and raw results; missing,
failed or unmatched cases cannot complete a comparison. Manual critical/release runs use the
requested full candidate commit and `PERFORMANCE_ACCEPTED_BASELINE`, an explicitly accepted full
commit SHA. Both sources run once on that runner. The workflow is read-only, retains evidence on
failure, and never updates the baseline or publishes a release. Missing tools/harness require a
reviewed change; a missing or invalid baseline fails the hosted comparison.
Affected pull requests also run `read-fast` when Media/Restore planning sources, the common
performance harness, its workflow or the canonical Restore fixture change. Explicit `read` and
`read-resources` dispatches select full quality and large-resource evidence separately.

Release preparation requires one completed, reviewed `release` comparison of the final source
against the recorded accepted source baseline, collected locally or through the hosted workflow.
Both sides must use the same host, tools and reviewed harness under the conditions above. Evidence
must identify the exact baseline, candidate and harness commits and content hashes, including any
dirty changes, with the commands, raw results and flag resolutions. Complete the structural checks
and relevant frontend checks as well. Review the completed results and record the implementation
choice and tradeoffs before candidate or source-push approval. An incomplete comparison remains
open work. A completed local comparison
satisfies this requirement without uploading its baseline or repeating it on a hosted runner.
Candidate assembly runs its source and package gates without automatically invoking Performance.

After measurement, review the diff against each measured operation. Retain its evidence when
the relevant runtime source, dependencies, harness, fixtures and measurement settings are
unchanged, recording the measured and final commits. Documentation or unrelated runtime/tooling
changes do not require repeating that operation. Rerun only affected benchmarks and resolve their
flags; do not repeat the whole release tier for a change to one component. The final source
preflight and acceptance of the exact release archives remain required.

## Large Identical Results

Use the native benchmark for before/after Find and retained-page costs with the same 10,000-
and 100,000-File fixtures. Fixture creation is outside the measured operations:

```shell
go test ./internal/library -run '^$' -bench '^BenchmarkIdenticalRetainedResult$' -benchmem -count=1
```

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
