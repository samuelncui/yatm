# YATM frontend

Use Node.js 24 and the repository's pnpm lockfile. During development/review, select
affected Vitest files (for example `pnpm exec vitest run src/components/files-browser.test.ts`)
and run `pnpm typecheck`; add the relevant style/lint, build or browser checks when those
boundaries change. `pnpm check` runs all formatting, lint, tests, type checking and the
production build for final integration/release. The
[check-scope policy](../docs/operations/testing.md#check-scope-and-reuse) owns evidence reuse.

DOM-free tests declare `// @vitest-environment node`. Shared setup loads browser helpers only in
jsdom. Component tests and benchmarks retain jsdom; tests default to two workers.

## Shared file browser

YATM consumes the published `@samuelncui/chonky` and
`@samuelncui/chonky-icon-fontawesome` 0.3.6 packages. The lockfile resolves the
registry artifacts directly; no local checkout or dependency patch is required.
See the [Chonky component documentation](https://github.com/samuelncui/Chonky/tree/v0.3.6/packages/chonky).

Shared row presentation, grouping, selection, keyboard handling, drag/drop,
breadcrumbs and browser footers belong in Chonky. YATM supplies queries,
pagination, metadata, operation capabilities and action handlers through its
supported interfaces. Group layout actions belong to the caller's Options menu.

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
global Find. Do not point this mutating suite at a normal installation.

The [E2E guide](../docs/operations/e2e-test.md#packaged-browser-acceptance) owns fixture
and server setup. Traces and failure screenshots go to `output/browser-results`.
These checks complement unit tests of request interleavings and the backend's
large-data checks; they do not replace physical Media acceptance.

## Frontend benchmarks

Run `pnpm bench` separately from other builds and tests. The single case in
[`files-browser.bench.ts`](src/components/files-browser.bench.ts) measures production
directory projection/accumulation for 10,000 entries in 500-row streamed batches.

It uses Vitest's iteration loop with a 50 ms warmup and 250 ms measurement
budget, completing at least one operation per phase. Fixture construction is outside
measurement; mock replies and final-result assertions are inside. This is a CPU-oriented
elapsed timing in Node/jsdom with immediate mocked RPC replies. It does not measure
network/server work, full Files-pane stream publication, Identical updates, Chonky
rendering, browser paint, frame rate or scrolling latency. Existing Identical structural
and UI tests, and Chonky browser checks, retain their correctness and layout coverage.

For comparison, run the [comparison runner](scripts/compare-performance.mjs) from the
repository root, separately from other builds, tests and measurements. Supply three Git
checkout roots and a new output directory outside all three checkouts; its parent must
already exist. The harness checkout supplies the common benchmark file above. Use the
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
same tracked benchmark file; each retains its own implementation, test helpers, Vitest
configuration and dependency lockfile. Supplied checkouts remain unchanged.

Both copies finish `pnpm install --frozen-lockfile` using the public npm registry before
measurement, with disposable home, configuration, cache and store directories. Both sides
use one recorded pnpm version; project package-manager switching is disabled. The runner checks
that both copies use its Node executable and that their lockfiles remain unchanged. Each
then runs `pnpm exec vitest bench --run --maxWorkers=1` for this case exactly once: baseline
with `--outputJson`, then candidate with `--compare` and `--outputJson`. No `bench` package
script is required in either source.

Output contains `pair.json` with source commits and tracked-file hashes, lockfile and harness
hashes, runner/tool identities, environment facts, commands and completion status. Source
hashes describe the files before the common harness overlay. Any native `baseline.json` and
`candidate.json` results and install/benchmark logs produced before a failure are retained;
disposable copies are removed when the runner exits normally or reports an error. Missing,
invalid or unmatched measurements fail the command and leave `complete: false`.

Vitest reports the comparison without a custom sampling loop or regression gate. A single
short run can be noisy; its timing report complements semantic tests and is not browser
performance acceptance. `pnpm check` does not run these opt-in benchmarks. Run the tooling
tests separately without installing dependencies or measuring performance:

```sh
node --test frontend/scripts/compare-performance.test.mjs
```

## Inline feedback

Use `Feedback` for inline errors and any Alert with actions; put controls in its `action` slot.
It owns message wrapping, icon/action alignment, responsive placement and surrounding
space. Callers retain their existing retry and cancellation behavior. The lint rule
rejects direct MUI error Alerts and Alerts with controls outside this component.
Use the shared `errorMessage` formatter for failures; it decodes RPC status text once
and preserves ordinary errors and malformed wire text without throwing.

## State and styles

The [UI architecture](../docs/architecture/ui.md#state-and-styling) owns shared state,
preference persistence, styling boundaries and initialization order. Use the application
Redux hooks for shared selections, committed Settings and Jobs return snapshots; keep
form drafts and request lifetimes with their views.

Run `pnpm styles:check` for known style-boundary violations and `pnpm lint` for component
usage rules. Include browser checks of loading/disabled states, portals, nested browsers
and narrow layouts when changing shared presentation.
