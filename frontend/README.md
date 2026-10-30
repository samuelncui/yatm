# YATM frontend

Use Node.js 24 and the repository's pnpm lockfile. During development/review, select
affected Vitest files (for example `pnpm exec vitest run src/components/files-browser.test.ts`)
and run `pnpm typecheck`; add the relevant style/lint, build or browser checks when those
boundaries change. `pnpm check` runs all formatting, lint, tests, type checking and the
production build for final integration/release. The
[evidence reuse policy](../docs/release/release.md#evidence-reuse) owns evidence reuse.

DOM-free tests declare `// @vitest-environment node`. Shared setup loads browser helpers only in
jsdom. Component tests and benchmarks retain jsdom; tests default to two workers.

## Shared file browser

YATM consumes the published `@samuelncui/chonky` and
`@samuelncui/chonky-icon-fontawesome` 0.3.10 packages. The lockfile resolves the
registry artifacts directly; no local checkout or dependency patch is required.
See the [Chonky component documentation](https://github.com/samuelncui/Chonky/tree/v0.3.10/packages/chonky).

Shared row presentation, grouping, selection, keyboard handling, drag/drop,
breadcrumbs and browser footers belong in Chonky. YATM supplies queries,
pagination, metadata, operation capabilities and action handlers through its
supported interfaces. Group layout actions belong to the caller's Options menu.
Chonky also owns the native scroll container for ordinary, grouped and sparse rows;
application pages do not reserve content padding for scrollbars.

Consumer regression tests cover file status, measured rows, loading/navigation,
drag/drop, selection waitlists and the independent identical-file tool. Keep
these tests when upgrading the published dependency.

## Packaged browser acceptance

`pnpm test:e2e` runs Chromium against an already running, isolated Demo server.
Install the browser once with `pnpm exec playwright install chromium`. Set
`YATM_BROWSER_URL` to its loopback URL (an SSH tunnel is also supported), and
`YATM_BROWSER_COMMIT` to the expected packaged commit. The tests verify the page's
embedded identity, exercise both file panes and Identical sorting, random scrolling,
hidden files, collapse and selection, and delete two disposable Demo files while
holding the first response. They assert that these interactions do not start another
global Find. Cold Settings checks delay code and data independently; Job results checks use
a 100,000-row synthetic RPC response to exercise the real virtualizer, long paths and delayed
page completion without creating 100,000 physical files. Do not point this mutating suite at
a normal installation.

[Package acceptance](../docs/release/package-acceptance.md#packaged-browser-acceptance) owns the
fixture, server and run procedure; this section owns the browser installation. Traces and failure screenshots go to `output/browser-results`.
These checks complement unit tests of request interleavings and the backend's
large-data checks; they do not replace physical Media acceptance.

## Frontend benchmarks

Run `pnpm bench` separately from other builds and tests. The maintained cases are:

- [`files-browser.bench.ts`](src/components/files-browser.bench.ts): production directory
  projection/accumulation for 10,000 entries in 500-row streamed batches.
- [`job-result-window.bench.ts`](src/components/job-result-window.bench.ts): the shared Job
  result cache visiting 1,000, 10,000, 100,000 and 1,000,000 rows in 200-row pages. Each
  operation starts with an empty cache and retains only nearby pages. Native task teardown
  checks the final retained row order, range and cache bound outside timing.

Both use Vitest's iteration loop with a 50 ms warmup and 250 ms measurement
budget, completing at least one operation per phase. Fixture construction is outside
measurement. Directory mock replies and final-result assertions are inside its timing.
These are CPU-oriented elapsed timings in Node/jsdom; directory RPC replies are immediate
mocks, while the cache cases make no RPC calls. They do not measure
network/server work, full Files-pane stream publication, Identical updates, Chonky
rendering, browser paint, frame rate or scrolling latency. Existing Identical structural
and UI tests, and Chonky browser checks, retain their correctness and layout coverage.

For comparison, run the [comparison runner](scripts/compare-performance.mjs) from the
repository root, separately from other builds, tests and measurements. Supply three Git
checkout roots and a new output directory outside all three checkouts; its parent must
already exist. The harness checkout supplies both common benchmark files above. Use the
current Node.js >=24 and pnpm on one machine:

```sh
node frontend/scripts/compare-performance.mjs \
  --baseline "$PERF_BASELINE" --candidate "$PERF_CANDIDATE" \
  --harness "$PERF_HARNESS" --out "$PERF_OUTPUT"
```

The runner copies current tracked `frontend/` files, including tracked edits and
deletions, into disposable directories. It excludes untracked/ignored files, existing
dependencies and Git metadata, and rejects symlinks and tracked private environment or
registry configuration. Stage new source files before comparing. Both copies receive the
same two tracked benchmark files; each retains its own implementation, test helpers, Vitest
configuration and dependency lockfile. Supplied checkouts remain unchanged.

Both copies finish `pnpm install --frozen-lockfile` using the public npm registry before
measurement, with disposable home, configuration, cache and store directories. Both sides
use one recorded pnpm version; project package-manager switching is disabled. The runner checks
that both copies use its Node executable and that their lockfiles remain unchanged. Each
then runs `pnpm exec vitest bench --run --maxWorkers=1` for all five cases exactly once: baseline
with `--outputJson`, then candidate with `--compare` and `--outputJson`. No `bench` package
script is required in either source.

Output contains `pair.json` with source commits and tracked-file hashes, lockfile and harness
hashes, runner/tool identities, environment facts, commands and completion status. Source
hashes describe the files before the common harness overlay. Any native `baseline.json` and
`candidate.json` results and install/benchmark logs produced before a failure are retained;
disposable copies are removed when the runner exits normally or reports an error. Missing,
invalid, failed, duplicate, unexpected or unmatched measurements fail the command and leave
`complete: false`. Both native reports must contain the exact two files and five maintained
cases with positive sample counts, means and rates, and matching identities.

Vitest reports the comparison without a custom sampling loop or regression gate. A single
short run can be noisy; its timing report complements semantic tests and is not browser
performance acceptance. `pnpm check` does not run these opt-in benchmarks. Run the tooling
tests separately without installing dependencies or measuring performance:

```sh
node --test frontend/scripts/compare-performance.test.mjs
```

The cache correctness tests separately verify that a million visited rows keep a bounded
cache. Cache timings exclude RPC and DOM rendering; the real browser regression checks
scroll geometry and cursor requests. The [Go result benchmarks](../docs/development/performance.md#performance-acceptance)
measure backend paging separately.

## Inline feedback

Use `Feedback` for inline errors and any Alert with actions; put controls in its `action` slot.
It owns message wrapping, icon/action alignment, responsive placement and surrounding
space. Callers retain their existing retry and cancellation behavior. The lint rule
rejects direct MUI error Alerts and Alerts with controls outside this component.
Use the shared `errorMessage` formatter for failures; it decodes RPC status text once
and preserves ordinary errors and malformed wire text without throwing.

## State and Styling

Each fact has one owner. Redux stores shared application state; it does not replace the router,
controlled forms or request lifetimes.

| State                                                                                | Owner and lifetime                                                                                                                                                                                                                |
| ------------------------------------------------------------------------------------ | --------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Archive/Restore selections and Restore version policy                                | Application Redux store; hydrate browser-session preferences once, then persist changes. Successful submission clears only the submitted revision, preserving later edits.                                                        |
| Committed Library/Preview/Job/Device Settings                                        | Application Redux store; only a current Get or successful Save publishes values. Older reads cannot replace a Save or a newer read, and an initial failure leaves values unknown until explicit Retry. Editors keep local drafts. |
| Configured Locations                                                                 | Application Redux store; fetch the complete catalog once, share choices with Settings, and publish successful configuration edits. Live availability and latest analysis use detail reads.                                        |
| File list sorting and folders-first                                                  | Application Redux store, persisted to local browser storage by list identity, independently of the current path.                                                                                                                  |
| Jobs return position, loaded rows, revisions and tombstones                          | In-memory Redux snapshot per route filter; request lifetimes remain local and late responses cannot publish after leaving.                                                                                                        |
| Navigation and active route filters                                                  | Router.                                                                                                                                                                                                                           |
| Panes, sparse Identical pages, log/result windows, dialog drafts and pending actions | The owning view or shared feature hook; discard or invalidate them when their request or source changes.                                                                                                                          |

The [application state module](src/state/store.ts) uses serializable protobuf JSON
and memoized decoded selectors. Controllers, timers and in-flight promises remain outside Redux.
Committed Settings and Job snapshots are not persisted. Browser storage is an optional preference
boundary: failed reads use the established default, and failed writes cannot turn a successful
server operation into a failure. A preference retains its own commit rule, such as a confirmed
Restore destination or successful Scan submission; merely editing a draft does not persist it.

Instants follow the [shared time contract](../docs/architecture/contracts.md#time-values). Decoded values remain
`bigint`; serializable state uses decimal strings. [Shared date helpers](src/tools/time.ts)
convert to `Date` only for display and date controls, with range validation and correct rounding
before the Unix epoch. An unchanged Restore cutoff keeps its original nanoseconds rather than
round-tripping through the control's lower precision.
File rows keep their original nanoseconds as the shared browser date-sort key; saved-version
ordering and Preview selection follow the Library's [latest saved version order](../docs/architecture/library.md#organization-and-content)
with exact nanosecond comparisons. A first-date display fallback does not replace an unknown last archive date in that order.

Shared read hooks keep requests local to each consumer. Preview and saved-version reads share their
feature hooks; the [Location-choice hook](src/components/use-location-choices.ts) owns
the root menu, Library filters, Restore, Locate original and Identical scope's choices from the
same configured Location catalog as Settings. The application preloads its complete paged catalog;
opening a chooser and filtering its names use that shared cache. An initial read shows loading,
and a failed read exposes Retry. Settings Refresh explicitly reloads the catalog; successful
configuration changes update it. A successful Library import refreshes the catalog and supersedes any earlier read. Late reads cannot overwrite newer edits. Live filesystem state
and latest analysis remain separate detail reads. Selection, Restore eligibility checks and save
actions remain with their components. Library filters retain their selected Location and label
when canceled and reopened. Media search keeps its successful current query/page when reopened;
changed queries/pages and explicit Retry perform a new read. Scan Location search and the Location choices
in Jobs filters use the same configured catalog; changing a Job filter retains loaded Media choices. The RPC transport does not log request or
response bodies; request owners present failures, and obsolete cancellation is not an application error.

Styling has four owners:

| Concern                                                     | Implementation                                                                                                                                                                                                 |
| ----------------------------------------------------------- | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Application colors, type, shape and framework-wide defaults | The application theme.                                                                                                                                                                                         |
| Shared component appearance and interactive states          | The owning React component, using MUI `styled`, `sx`, theme overrides and supported slots. Reuse Feedback, ActionRow, Settings shells, search popup shells and navigation instead of copying their appearance. |
| Page layout and native application-owned elements           | Co-located Less/CSS; shared components keep these styles together with their markup, and no stylesheet targets another component's private DOM or state classes.                                               |
| Measured row positions and dimensions                       | Dynamic styles owned by the virtual list or resizer.                                                                                                                                                           |

`styled` and `sx` use MUI's Emotion styling system. Less styles page layout and application-owned
HTML; it does not reskin framework components. A shared component's native markup may retain
co-located Less, while MUI controls use the theme and component APIs above.
Components own text alignment; root layout containers leave it unset.
Settings editors keep one shared page shell across lazy code loading and the first data read,
with one progress indicator in that shell. Revisiting committed Settings uses the shared cache;
a failed read remains an actionable error until Retry. The router owns the selected tab,
including detail and trailing-slash routes.
The [entrypoint](src/main.tsx) configures MUI utility names before importing component
modules, including production chunks. Use exported utility classes or slots instead of hardcoded
`app-Mui*` selectors. The application theme wraps application controls, including controls placed
inside Chonky slots; Chonky owns its theme and browser appearance. Do not override all Chonky
buttons from an application stylesheet. The [application FileBrowser](src/components/file-browser.tsx)
owns its layout element and supplies Chonky's public theme props, so pages need no `.chonky-*`
stylesheet selectors. Chonky owns the native scroll container for ordinary, grouped and sparse
rows. Rows fill its client width; the browser allocates scrollbar space. Fixed content insets
must not stand in for a scrollbar, including when the list fits without scrolling.
Component states must work under lazy route loading,
portals, narrow layouts and nested browsers without depending on import order.

`pnpm styles:check` and ESLint reject known boundary violations. Direct configured-Location
list reads and Settings Get/Update calls are restricted to their shared owners, as are Job result
page reads. Visual acceptance still checks
alignment, wrapping, focus, disabled/loading states and both browser panes; a static guard cannot
prove rendered appearance.

[MUI class-name initialization](src/mui-classnames.ts) must run before component imports in both the application entry and browser test setup. The entry dynamically loads [application bootstrap](src/bootstrap.tsx) after initialization, preserving that order across production shared chunks. Cached internal selectors and rendered classes must use the same prefix; otherwise default select overflow and outlined-label styles can fail. Verify long selected values and floating labels in real rendering.

The [application theme](src/theme.ts) owns MUI primitive appearance; `sx` and `styled` use the same Emotion engine. Shared components own repeated structure and placement, including tabs, section cards, action rows, confirmations, feedback, result rows and saved-version choices. Less owns page layout and scoped third-party adaptations; it does not restyle those shared components. Bootstrap loads the reset before importing the application and its global styles. Component styles stay scoped, so opening a lazy route cannot change another surface. Chonky retains its own Emotion styles and notifications retain their distributed CSS.

### Checks

Run `pnpm styles:check` for known style-boundary violations and `pnpm lint` for component
usage rules. Include browser checks of loading/disabled states, portals, nested browsers
and narrow layouts when changing shared presentation.

## UI Prototypes

Build UI prototypes with the existing React modules, styling and layout, and capture their rendered
pages for review. Store temporary sources and screenshots under ignored `frontend/.drafts/`, then
remove them after the accepted behavior and constraints have been recorded in the owning current
architecture document. Generated concept images are not interface specifications.
