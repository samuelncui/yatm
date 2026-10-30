# Local Demo

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
| Media | Random-write HDD, sequential-write HM-SMR, offline Volume, uninitialized disk, metadata-only LTFS Tape probe and a tar Tape whose profile and member locators await Add to Library; inventory additions/changes/removals, healthy/mismatched/missing Verify findings and a Tape-bound pending check |

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
count and host.

For each size, use explicit Find with Library and with Locations selecting Shared files. Review
far scrolling within the large group and across the pairs, collapse/expand, size sorting in
both directions under the [Library sorting contract](../architecture/library.md#identical-file-tools),
hidden files, and independent Delete/Keep in the disposable Location. Repeat Find
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
