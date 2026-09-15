# Local Demo

Status: v1 Alpha 1 review fixture. Demo data is disposable and is not a migration input.

The disposable Demo uses normal Library tables, Job runners, Volume markers and
ACP transfers. It has no production mock-mode branch. Architecture and state
semantics belong to [API/UI](../architecture/api-ui.md),
[Library](../architecture/library.md) and [Jobs](../architecture/jobs.md).

## Start and Reset

```bash
./scripts/demo
```

This builds and serves the fixture at `http://127.0.0.1:18080`, normally under
`${TMPDIR}/yatm-demo`. Restarting preserves reviewer changes. Use a dedicated
temporary root and an explicit reset after fixture or Draft schema changes:

```bash
YATM_DEMO_ROOT=/private/tmp/yatm-demo-review \
YATM_DEMO_LISTEN=127.0.0.1:18090 \
YATM_DEMO_RESET=1 ./scripts/demo
```

Alpha format admission rejects older unmarked Draft data. Regenerate an existing
disposable Demo with this explicit reset; upgrading never silently clears it.

Reset recursively replaces **only the selected validated Demo root**. Its basename
must start with `yatm-demo` and it must be below a temporary directory. Never put
valuable files there. A local MP4 can be copied into the fixture with
`YATM_DEMO_VIDEO=/absolute/path/sample.mp4` on reset; its original is unchanged.

## Fixtures

- Curated Library trees, tags, Notes and visible dot directories; 125 invoice
  entries exceed one search page.
- Mounted HDD and HM-SMR Volumes, an unmounted Volume and an inspectable mock
  Tape. The mock Tape cannot perform real LTFS reads or writes.
- Registered Locations for Documents, Photos, shared duplicates, unconfirmed
  imported paths, unavailable originals, unknown content, retryable analysis,
  incoming backup selections and preferred restore destinations.
- Pending Archive/Restore plus SCAN Jobs for original collection, inventory,
  previews and recorded-copy verification. These are configurations of one kind,
  not separate Analyze/Preview/Verify implementations.
- `mutable.txt`: three recoverable saved versions and a fourth unbacked current
  revision, with one File identity and preserved Note. Saved dates are August 20,
  August 29 and September 6, 2026.
- `Photos/archive-room.png`: three versions with visibly different blue, orange
  and green content-addressed thumbnails, dated August 22, August 30 and September
  8, 2026; a video has poster/timeline assets.
- `handbook.md`: a saved version established from existing matching inventory,
  without duplicating the physical archive copy.
- Three duplicate groups with exactly **2, 3 and 4** independently organized
  originals, tagged `duplicate-review`. One member is in an unavailable Location.
- Newly arrived unadmitted content and an empty folder, visible without analysis.
- A completed Restore with a verified adopted target and normal FileLocation
  association, without pretending the entire destination was scanned.
- Real verification findings for damaged/missing files and successful reads;
  retained FileVersions do not claim that every version is recoverable.

## Browser Acceptance

Use the reset fixture for destructive or state-changing examples. Never start a
physical Tape operation from the mock fixture.

| Area | Review |
| --- | --- |
| Query bar | One querystring input, More filters, then Search in both Library and Location; changing source does not change toolbar layout. Search/Clear remain usable at narrow widths. |
| Query semantics | `tag:invoice` spans 125 Library results; load another page and refresh. In Location, `name:*.md`, `tag:duplicate-review`, type/size/time expressions use the same grammar over the current directory. Invalid fields show an error. |
| More filters | Structured controls compose the query. Grouped duplicates is an explicit view choice; typing `has:duplicates` alone can remain an ordinary result list. |
| Duplicates | Clear earlier criteria, enable grouped duplicates, expand each 2/3/4 group, page members and inspect separate Files. Saved-only Library visibility must not hide online duplicate members. Search does not hash files. |
| Status | Search `tag:backup-review`. handbook is green, mutable is green with an unbacked-change mark, online-only is yellow, unknown content and inaccessible originals are gray. Archive-only usable history is blue; no known usable content is red. Follow the full [state matrix](../architecture/library.md#file-and-version-display-states), not an expiry rule. |
| File rows | File/folder name, time and size columns align; folders reserve the status slot without an aggregate dot. Hover/focus short status text opens left. |
| Navigation | Either root arrow switches both Files panes to the same source. Positions remain independent. Root names return home; long breadcrumbs fold by available width; Copy path includes the source. |
| Details | Double-click a file opens details, not bytes. The inline Location chip opens its live root; the original path opens the containing folder and reveals the file. No redundant original Open button. Archived copies retain explicit Open; there is no Download action. Unadmitted files have real properties, not invented versions. Changing saved image versions replaces the thumbnail without stale responses. Library metadata Export remains available in Settings. |
| Organization | Use disposable entries to mkdir, rename, move, merge directories and delete in both sources. Library changes logical organization; Location changes disk paths. Neither exposes Copy. Leaf conflicts do not overwrite. No Job or expanding progress banner is created. |
| Drag/drop | Drag between same-source directories or into a waitlist. One preview follows the pointer, no pane flashing or layout shift; waitlist drops select rather than move. |
| Selection lists | Add files and folders from separate directories. Open Unforged → Documents inside either waitlist, return with parent/root breadcrumbs, and page larger folders. In Restore choose mutable.txt's version inside the folder: the folder stays selected and the explicit version overrides its automatic descendant once. The single Backup…/Restore… footer action opens options, estimates and unavailable-input warnings in a modal. Cancel preserves selections/options and creates no Job. Dynamic rows do not overlap. Footer and list share one outline. |
| Restore target | A compact preferred-Location selector sits above live folders, with New folder/Cancel/Choose directly below the bounded list. Check narrow screens and long names: no sidebar, horizontal overflow or empty band below the actions. Clicking the current breadcrumb (including the root) leaves the loaded folder usable. Failed pagination clears stale rows and Retry reloads the first page. Enter a directory, create one, then Cancel: selection stays unchanged but created folders remain. Choose remembers a binding-safe target. No preferred target links to Settings. |
| Restore versions | Add mutable.txt and archive-room.png, choose a September 1, 2026 cutoff: both resolve to their second version. Pin a different version, change the cutoff, and verify the custom choice remains; apply the policy to all to reset it. Add handbook.md (saved after that cutoff), verify the unmatched row remains and blocks preparation, then explicitly skip it. Unknown archive dates and missing copies are distinct blockers. Cancel creates no Job. |
| Settings | Inline Location edits, Ignore, confirmation and compact related Jobs; Include unbacked files is under Settings → Library. Pure pickers do not collect files. |
| Scan | Choose source, Signatures, Results and Preview in order. Location/Media use server-side name/path/identity search with required selection and bounded pages. Typing alone cannot start a Job. Successful choices persist across reloads; remembered source identities are revalidated. Known-only leaves cache misses unknown; verification forces real reads. Preview offers Don't generate, Generate missing and Regenerate all; Tape disallows Preview. Scanned folders and results remain dismissible while loading. |
| Jobs | Filter kind/status/resource, open a detail, return via Jobs/All/sidebar and preserve list state. Read dialogs cannot switch a fixed Tape check to a Volume. |

For a complete mounted-Volume interaction, select mutable.txt, add it to Backup,
prepare and write to Review HDD. Its current content gains a fourth version without
changing logical organization. Restore one saved version through the preferred
target chooser and compare output bytes with the selected archived content, not
with the newer original. Confirmations are application dialogs; Cancel/Escape
must not start a Job or mutation.

Unavailable Location browsing clears rows and offers Retry; a real empty folder
shows the normal empty state. Existing Library organization survives either case.
The imported Reference documents name is ordinary user content; its confirmation
prompt comes from the imported binding state.

## Maintenance

Update `internal/demo` and semantic assertions whenever visible fields, actions,
states, Job options or empty/error cases change. Keep fixtures deterministic,
bounded and isolated, with actionable and unavailable examples. Use normal domain
APIs/runners; do not add Demo-only schemas or production flags.

Run `go test ./internal/demo`, `bash -n scripts/demo`, an explicit reset and actual
browser smoke tests. Demo acceptance supplements [CLI E2E](e2e-test.md), unit
tests and official LTFS file-backend validation; it never replaces them.
