# Online Data Sources

Status: Implemented — verified on 2026-09-06 and archived as the implementation scope. The maintained [architecture](../architecture/overview.md) and [operating guide](../operations/online-sources.md) own current behavior. Development support does not imply a published release.

## Purpose and Boundary

Catalog everyday files in place while preserving the existing Library organization and archive workflows. The distinction is archive copies versus mutable originals, not removable versus permanently attached hardware. A fixed disk used for archive copies can remain Volume Media; a working directory is an OnlineSource even when its underlying device is removable.

The baseline for this archived design used a live Source browser to list Job inputs and a Volume Scan Diff → Apply workflow for archive inventory. OnlineSource registration added persistent original indexing. The maintained [Jobs contract](../architecture/jobs.md) describes current workflows. ACP and Chonky retain their reusable roles and need no separate storage model.

## Model and Ownership

| Entity | Definition and durable facts |
| --- | --- |
| Existing File | Content identity and user organization/annotations, shared with archive copies |
| Existing Position | Published physical copy on Media; archive behavior remains unchanged |
| New OnlineSource | Named root directory bound to the current Executor; records its root path, binding verification, and last successful sync time |
| New OnlinePosition | A source-relative path observed by a successful sync, linked to its content File and recording size, mode, nanosecond mtime, and hash |
| New Sync Job | One explicit refresh of one OnlineSource, with a durable bounded manifest/Diff and result summary |

OnlineSource and OnlinePosition belong to the Library database. Scan work belongs to the Sync Job database, with the common Executor catalog/lifecycle. There is no fourth database category. OnlinePosition has unique `(source_id, path)`, indexed `(source_id, parent_path, path)` for direct children, and indexed File-ID lookup. Derived directory entries support browsing without becoming logical File identities. Tape storage order/extents and archive capacity are not online-position facts.

The existing `Source` protobuf remains a temporary filesystem selection for Archive/Preview. Do not overload it with persistent registration or add an OnlineSource Media profile. OnlineSource additionally records exclusions, a GORM-maintained revision, and the last successful Job ID. Catalog timestamps use Unix milliseconds; physical modification times retain nanoseconds. Complete configuration/results are typed structures; separately queried values are columns.

Binding states are Unconfirmed (imported), Needs sync (registered or configuration changed), and Verified (current binding successfully synchronized). Runtime accessibility has its own check time and error, independent of binding state. Name-only changes preserve verification and existing logical names. Source pages bind their cursor to the source revision and reject stale continuations. The latest attempted Job ID is distinct from the last successful Job ID, so failures remain discoverable without replacing the old index; updating either catalog fact advances the source revision.

## Root Registration and Access

- Register an accessible directory, either a whole disk root or a subtree, within the configured filesystem source access boundary. Reject path escapes and symlink traversal. Root binding is local to the Executor; distributed source discovery is outside this design.
- Persist a stable catalog ID and the selected path, not hardware identity. Do not write `.yatm.json`, discover UUIDs, initialize storage, mount/unmount/eject, reserve archive capacity, or create Backend Sessions for source reads.
- Root changes are explicit, preserve source identity and the last published index, and invalidate the binding for reads/Archive until a successful sync. Import likewise requires explicit local binding confirmation followed by sync.
- Serialize Sync and registration changes for the same source. This is a source-scoped metadata operation lock, not a physical-device lease; ordinary reads do not exclusively reserve a disk, and external applications remain free to edit files.
- Accessibility is observed at use time, not assumed from the name "online". Missing/unreadable roots retain the last index and report unavailability. A successful scan of a genuinely empty directory can remove its online locations while retaining Files and archive copies.
- Path binding cannot reliably detect that a different underlying device now occupies an accessible path, including an exposed empty directory after unmount. This is a documented limitation, not marker-equivalent identity validation; only rebuildable index metadata is reconciled.

Duplicate normalized roots on one Executor conflict. Nested sources are allowed with a duplicate-index warning; two indexed paths do not establish independent backups. Exclusions are source-relative paths matching themselves and their subtrees, not glob/regex/negation/gitignore rules. They may be nonexistent, but must not escape or exclude the root. Actual YATM runtime resources must be excluded and shown in configuration; the entire work directory is not implicitly disposable. Known archive roots must be outside the effective scan scope. Disjoint directories on one disk can serve both roles.

Configuration changes, confirmation, deletion, and synchronization of the same source are mutually exclusive and return Busy instead of queuing or cancelling. Imported paths require explicit local confirmation. Changed roots or exclusions retain the cached index but disable its content access until successful sync. Ordinary reads do not acquire an exclusive disk lease.

## Synchronization

One click creates a typed Sync Job and performs the following operation without a separate user Apply action:

1. Lock the source's sync/configuration boundary, validate its root binding, and start a fresh bounded manifest/Diff in the Job database.
2. Traverse in bounded directory batches, pruning exclusions before descent, persist the complete manifest in fixed-size Job batches, and use indexed database path order to merge with previous OnlinePositions. Reuse ACP primitives, not the Volume runner or its marker/prefix/capacity policies. Symlinks and special files are excluded; dot-prefixed directories are ordinary entries, and the Volume-only `.yatm.json` exclusion does not apply. Empty directories have no index rows.
3. By default hash added and metadata-changed files through one bounded targetless ACP stream, permitting metadata-valid cache hits. `force_rehash` rereads every regular file. ACP cache writes remain best-effort xattrs, not required writes to source content or directory structure.
4. Verify observed file facts around hashing and recheck additions/changes/removals and root accessibility before publication. Read errors, permission failures, cancellation, or detected drift discard the incomplete attempt and preserve the previous published index. No File content I/O occurs inside the Library transaction.
5. Stream the complete validated differences through a metadata-only Library transaction, bind content, update locations, and advance the source's last-successful-sync facts together. Do not expose a partial source index or partly imported Files.
6. Publish the Job completion and summary. Counts derive from durable diff entries rather than duplicate Library counters. A Library commit remains authoritative if the later Job checkpoint fails; a retry reconciles from that committed index without duplicate content identities.

Unchanged metadata can reuse indexed facts only in the same Verified binding. Root changes and imported bindings require a fresh content assessment. Hash reads validate before/after facts, and a final traversal checks membership and metadata before publication. A former regular file becoming a symlink or special file is a legitimate removal from the managed set on a successful scan.

Sync uses INDEXING → COMPLETED; scanning, validation, and publication are runner phases, not new durable failure states. RetryIndex discards partial attempt data and reads the current confirmed configuration again. Publication checks the source revision in the Library transaction; a later Job checkpoint failure never reverses that commit. There is no cross-database transaction or crash journal.

External writers are not frozen. The first release manages relatively stable ordinary files; continuous writers such as logs, live databases, and incomplete downloads belong in exclusions. ACP is not extended. Metadata/cache checks can miss edits that preserve all checked facts; `force_rehash` handles such content changes. Even a forced traversal is an observation over an interval, not a filesystem point-in-time snapshot. Changes after observation can require another sync; reads and Archive validate their own inputs.

## Content and Organization Rules

- Exact signature equality reuses an existing File, including across online roots and archive Media. Only genuinely new signed content creates a File under `Unforged/<source-name>/...`.
- Renaming/moving a physical file with unchanged content updates its OnlinePosition, not the File's logical path, Tags, or note. Multiple physical paths may reference the same File.
- Changed content rebinds the online location to its new signature's File. If that identity already exists, reuse it. Otherwise import it as new Unforged content without inheriting the previous File's organization or annotations. The old File and all archive Positions remain unchanged.
- Name conflicts affect only new import names, using an available numeric suffix; do not move, overwrite, or trash existing organized entries to accommodate an online import. Apply this rule to conflicting parent names as well as leaf names.
- Disappearance removes only the OnlinePosition. Source deletion removes only its registration/locations. Neither operation deletes physical files, File metadata, archive Positions, or Preview bundles. Explicit Library Trim must retain a File while either kind of position references it, including currently unavailable sources.
- An old File without any remaining physical copy is a metadata record, not a recoverable historical version. Indexing an original is not a backup, and an online location does not count as an archive copy.

New duplicate content takes its initial name from deterministic path order. Numeric leaf suffixes precede the extension; directory conflicts also get numeric suffixes. New observations never overwrite existing File identity fields or impose generic signature-format validation. Trim rechecks both location kinds inside its File-deletion transaction; an unavailable source's last successful index still protects its Files.

## Interfaces and Consumers

- Add `OnlineSourceService` for register, list/get, explicit update/delete, and paginated location queries by source or File. Add `SyncJobService` for Create, progress, and paginated result entries; register the runner and service through the existing Job-kind path. Reuse common Job cancellation, retry-index, logs, and deletion. Keep Volume `ScanJobService` and its explicit Apply unchanged.
- Add a data-source page with registration, cached browsing, current accessibility, last successful sync, manual Sync/Force rehash, and result summaries. File Inspector shows online locations separately from archived copies. Keep ordinary polling and current Library organization/search interactions.
- Add safe Open/Download for verified online bindings. Resolve only source-relative paths, reject symlink/path escapes, and check recorded file facts before streaming. Known drift is a conflict prompting sync, not permission to serve another known content identity as the selected File. Direct streaming is not a new transfer-integrity or snapshot guarantee.
- Extend Archive creation with Library File selection, mutually exclusive with existing raw filesystem input. Resolve available online locations and persist selected File identity, expected hash/size, and immutable logical `target_path` in the Job manifest. Missing sources are explicit retryable errors, never silently omitted files.
- At copy time compare ACP's actual streamed hash/size with the expected File. Drift cannot publish a replica of the selected identity; it leaves that item retryable. A retry may use another online location of the same File, but must not silently select newer content. Preserve existing target Media Sessions, STAGED/Finalize/submit behavior, and raw-path Archive input.
- Include online entities in the next Library JSONL export version, retaining older import readers and transactional import behavior. Imported root bindings are unverified until locally confirmed and synced. Metadata backups do not contain source file bytes.

### API, UI, and CLI

OnlineSourceService owns Create, List, Get, Update, Confirm, Delete, and paginated locations by physical parent or File. SyncJobService owns Create, progress/summary, and result pages; common lifecycle methods remain in JobService. Create finishes bundle initialization before returning and scans asynchronously. Protobuf changes precede generated Go/TypeScript and CLI changes. Large online location sets do not become FileGet payloads.

Library gains an Online Sources page with root/exclusions, binding/accessibility, last sync and Job results, cached physical browsing, Force rehash, retry, and confirmation. Cached browsing is labelled as the last successful index and remains usable offline. Inspector separates Online locations from Archived copies; only the latter count as archive replicas. Library filters can select a source and archive-copy presence from published metadata. Physical browsing has no rename/move/delete; logical Library organization is unchanged. CLI covers each new public RPC, online downloads, and Library-selected Archive.

### Content access and Archive

The independent online-content endpoint supports GET, HEAD, Range, and download names. A request binds both position and expected File identity; rebinding must not silently open another File. Check the current binding, effective exclusions, boundaries, symlink components, and recorded facts on every access, then serve from the same open descriptor. Known drift is a conflict prompting sync. Do not add a Restore Job or an extra full-file hash. Unsafe active formats use attachment responses rather than same-origin active documents. Trusted stable roots are the boundary; concurrent malicious path replacement and streaming snapshot consistency are not promised.

Library-selected Archive expands directories, deduplicates overlapping selections, and freezes File ID, opaque signature, expected SHA-256/size, and the Library-root-relative target path. Logical edits after indexing do not alter that target. Candidate locations use deterministic order and must be Verified; no available source is an explicit error, not an omission or automatic Restore. ACP's actual transferred hash/size must match before STAGED. Publication explicitly binds the frozen File identity, not a regenerated v1 signature. Retries may choose another location for identical content. Existing raw inputs, Media Sessions, Finalize, verified publication, and SUBMITTED checkpoints remain compatible.

### Optional Preview

Sync offers Generate previews, default off; update-outdated is valid only when enabled. After successful publication, construct an independent Preview Job from the published immutable manifest, not a new raw-directory walk. Deduplicate content, preserve valid previews by default, and include unchanged content without a preview. Freeze expected content facts so a later path overwrite cannot be addressed as the previous content. Sync remains successful if companion creation or execution fails; expose the child Job ID or creation error separately. Creation can be retried by another sync with preview enabled. No cross-Job transaction or automatic compensation is added.

### Backup and maintenance

Export V4 includes Sources, exclusions, and online positions from a consistent metadata view; legacy/v1/V3 imports remain readable. An online import group requires its corresponding Source, Position, and File records with validated references; old numeric IDs are not evidence of identity. Imports roll back in their entirety on conflicts, including across batches. All imported roots are Unconfirmed. Source revisions are local, and imported Job links are cleared because Library exports do not contain Job bundles.

Importing replacement Files without online data clears old online positions and retains source configurations as Unconfirmed, preventing File-ID reuse from misbinding old locations. A maintenance gate excludes related online work during replacement. Imports never change source bytes. Schema changes are incremental and never rebuild signatures or introduce an old v1 signature repair workflow.

## Implementation and Acceptance

The development implementation follows these dependency boundaries:

1. Add source/location models, typed catalog APIs, backup compatibility, and online-reference-aware Library Trim.
2. Add the Sync Job, bounded traversal/reconciliation, transactional publication, and retry tests.
3. Add validated online reads, Library-selected Archive input, and optional independent Preview manifests with expected-content verification.
4. Add frontend entry points, incremental/paged queries, deterministic Demo fixtures, and end-to-end coverage.

Required semantic and E2E cases:

- Missing/unreadable root versus successfully scanned empty root; partial permission failure; root relocation and unverified imported bindings.
- More than one page of files/differences; cancellation, mid-scan modification, failed publication, retry after a committed Library result, and two Sync attempts for one source.
- Rename without content change; duplicate content in multiple locations; changed content that is new or already cataloged; name conflicts without altering old organization.
- Removal/source deletion preserves original files, old Files, annotations, and archive copies; explicit Trim retains online-only Files even while their source is unavailable.
- Cache hit, unsupported/read-only xattrs, forced detection of same-metadata edits, empty files, dot directories, and symlink/path-escape rejection.
- Open/Download of available content and stale-content rejection; Archive source changes before/during copying, unavailable inputs, retries using identical content, and existing Tape/Volume workflow regression.
- New and legacy backup imports, local root confirmation after import, and Demo states for indexed/unavailable sources, sync retry/results, online-only Files, archived Files, and changed content.

Additional acceptance covers whole-disk/subdirectory roots, duplicates/nesting, nonexistent exclusions and changes, file/directory type transitions, frozen Archive paths and opaque identity binding, stale File-bound download URLs, HEAD/Range and active-content safety, Preview default/explicit/deduplicated/error-independent behavior, V4 round trips, legacy replacement with reused File IDs, and maintenance conflicts. Demo must exercise available, unavailable, Unconfirmed, online-only, archived, overwritten, and retryable states through normal APIs and an explicit reset smoke test.

Verify relevant Go tests, vet, race, both SQLite drivers, IDL generation, frontend checks, CLI coverage, isolated Volume/Preview E2E, and LTFS file-backend regression when copying changes. Check document links and `git diff --check`. Do not add a documentation toolchain, automatically commit/push, or publish a release.

Acceptance completed with local Go unit/vet/race checks, both SQLite drivers, reproducible Go/TypeScript generation, CLI coverage, 54 frontend tests and a production build, document-link checks, and an explicit-reset Demo smoke test that archived an online original to a mounted Volume and retried Sync. On Linux with Go 1.24.4, all five non-physical [E2E tests](../operations/e2e-test.md) passed: `TestLTFSFullTapeSpansMediaAndRestores`, `TestLTFSArchiveRestore`, `TestOnlineSourceSyncReadArchive`, `TestPreviewOutdatedPolicy`, and `TestVolumeArchiveRestoreScan`. The LTFS cases used the official file backend and isolated temporary storage. Physical Tape tests were excluded; this acceptance does not certify physical hardware or a release.

First-version limits are manual synchronization, local path bindings, and read-only management of source content/structure. Watchers, schedules, historical content storage, physical rename/delete, automatic source-to-archive replication policies, and filesystem snapshots are outside this design.

## References

- [digiKam Collections](https://docs.digikam.org/en/setup_application/collections_settings.html) separates local, removable, and network collection roots. This supports treating access/binding rules separately from the shared catalog.
- [IMatch Indexing](https://www.photools.com/help/imatch/index.php?name=rmh_config_indexing.htm) distinguishes rescanning accessible folders from keeping offline folders in the database. This informs the unavailable-source versus successful-removal distinction.
- [Kopia Getting Started](https://kopia.io/docs/getting-started/) distinguishes source directories from the repository that stores snapshot content. This informs the separation between an online index and retained archive copies, not a claim that a traversal is an atomic snapshot.
