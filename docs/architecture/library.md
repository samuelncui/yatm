# Library

Status: Current development Library model. See the [candidate notes](../releases/v1.0.0-alpha.1.md) for acceptance status.

## Organization and Content

[File](../../library/file.go) stores logical parent/name, kind, note and millisecond creation/update times. Parent/name is unique. Tags live in FileTag with primary (file_id, tag) and lookup (tag, file_id). File mode, mtime, hash and size in browsing responses are display projections from its original or, when no original exists, its latest saved version; they are not persisted File identity. An unsigned original does not fall back to historical content.

Logical construction, merging and traversal use `File.Kind` explicitly. Saving display projections cannot change it. Legacy adapters translate the legacy mode into a kind at their boundary; ordinary GORM saves do not carry that compatibility behavior.

Logical moves validate an existing directory ancestry, rejecting self/descendant moves, missing parents and cycles. Nested destination creation and the final move share a metadata transaction, so a rejected edit leaves no intermediate directories. `MkdirAll` treats `.` as the existing directory rather than creating a dot node. Ancestor queries return the complete bounded path or an error for cycles/excessive depth, never a silently truncated breadcrumb. Root ID zero is virtual; reserved Trash ID -1 is an ordinary persisted directory when present.

[FileLocation](../../library/file_location.go) uses file_id as its primary key: one File has zero or one original. It records Location ID, relative path, derived parent_path, mode, nanosecond mtime, size and nullable content facts. Location/path is unique; parent and signature indexes support association queries. This is a last-observed association, not proof of current existence. Location browsing reads real directories, including empty and dot directories; there is no persistent physical directory cache.

[FileVersion](../../library/file_version.go) records saved content and its original restore metadata, independently of which File initiated physical archival. Only (file_id, signature) is unique. Repeated archival of identical content reuses the version and updates its latest archive time, preserving the first time and immutable content/mode/mtime. C1 → C2 → C1 reuses C1. Unknown historical archive times are NULL. Unarchived edits do not create versions.

[Archive observations](../../library/version_history.go) retain evidenced save timestamps with primary key `(version_id, archived_at)`. Publication records them in the same metadata transaction as the version; repeated evidence is idempotent. They allow a later Restore cutoff to select an intermediate save of reused content. Metadata backups include the observations and validate their version references on import; replacing Files removes obsolete history. Restore and coverage admission never use their execution time as a backup date.

Restore's **at or before** policy selects the newest recorded save not later than the inclusive cutoff, with version ID breaking equal-time ties. Existing first/last dates remain valid evidence, but do not reconstruct missing intermediate saves; unknown dates cannot qualify. This is a selection of recorded content, not proof of the complete file or directory state at that time. History lost before observation recording cannot be recovered from content hashes, file modification times or import time.

Signature remains opaque bytes in VARBINARY(256), with empty values normalized to NULL. There is no global signature uniqueness or automatic File merge. The current producer and consuming integrity checks retain their specific SHA-256/size requirements; they are not general storage encoding validation. See the [identity decision](../decisions/0002-file-organization-and-content.md).

## Archive Inventory

[Media](../../library/media.go) retains its Tape/Volume contract: unique kind/identity, immutable typed profile and physical lifecycle/capacity facts.

[Position](../../library/position.go) stores Media ID, actual path, derived parent_path, directory flag, signature/hash/size, physical mode/times, storage order and typed storage metadata. It has no File or FileVersion owner column. Media/path remains unique; indexed parents support direct-child browsing. Derived directory Positions are not ownership or deletion units.

Verified Archive publication writes Positions and creates/reuses the selected File's version in the same metadata transaction. [Volume Scan publication](../../library/scan.go) reconciles physical inventory without creating logical File identities. Explicit [inventory admission](../../library/catalog.go) creates independent Files under Unforged using known copy facts, with unknown archive times; discovering content is not a new backup event. Only new logical names receive collision suffixes.

Exact signature lookup pages matching Positions and independently organized duplicate Files. One Position can serve several Files' versions. Coverage does not imply present readability or a fresh integrity check.

[Covered-original reconciliation](../../library/file_coverage.go) creates a missing FileVersion when a published original's known signature matches non-directory Positions. Admission and original-record Scan publication scope reconciliation to their published originals; Archive, inventory Scan and direct Position publication scope it to affected inventory. Library startup and completed imports reconcile existing catalog facts. All associations are metadata-only, transactionally published, and processed in File-ID pages with bounded copy pages. Read-only catalog queries never create versions.

The version keeps this original's mode/mtime and content facts, not another File's organization or history. Known copy hashes can supply a missing original hash; contradictory sizes or known hashes fail the publication transaction. Initial archive dates use the earliest/latest evidenced dates from matching saved content, never copy mtime or discovery time; absent evidence stays NULL. Existing versions are untouched by coverage reconciliation, so more copies do not create more versions or advance archive dates. Later original changes, unlinking or copy removal retain the saved version.

## Registered Originals

[Location](../../library/online.go) stores name, Executor/canonical root, preferred Restore destination flag, typed Ignore text, optional UUID writing, binding state/token, revision and millisecond observation/configuration times. Every Location supports live originals; preference is not write authorization. Duplicate roots on an Executor conflict. Registration is locally confirmed and immediately browsable; only imported roots require path confirmation. Name changes do not reorganize Files. [Configuration migration and access](../operations/install.md#location-configuration-and-migration) own administrator boundaries and one-time YAML import.

GORM hooks advance revisions for configuration, attempts and association publication. Physical page continuations bind directory facts, binding token and query. Accessibility is a separate timed runtime observation.

Each FileLocation carries its `observed_binding_token`. Root/Ignore changes, import and rebinding rotate the Location token; Library records remain, but old observations cannot authorize access. Live requests carry relative path, binding token and expected object facts, with an optional File association. Access, admission and preparation revalidate these facts; no complete-analysis readiness state exists. User Ignore limits automatic collection and recursive selection, not access or physical-operation permissions.

### Location State

| Axis/state | Trigger | Allowed operations and retained facts | Failure/retry |
| --- | --- | --- | --- |
| UNCONFIRMED | Import | Library history/configuration; confirm the local path before filesystem access | Invalid confirmation preserves records and blocked binding |
| CONFIRMED | Registration or explicit local confirmation | Live browsing, admission, analysis, authorized file operations and Restore | Root/Ignore changes invalidate old object tokens, not whole-directory readiness |
| Last complete original scan | Successful whole-Location record publication only | Historical timestamp/Job, independent of browsing and single-file Restore | Failed ranges and individual publication do not claim full coverage |
| Unchecked / accessible / inaccessible | Timed runtime path observation | Describes reachability, not content identity or backup health | A later check replaces the observation; no index deletion |
| Busy | Active same-Location Scan, configuration, admission, file operation or Restore attempt | Conflicting mutations return Busy; ordinary reads do not reserve the disk | Operation completion/cleanup releases the gate; no automatic queue |

`Imported directory` is not a state; names are user-defined. The Demo uses ordinary names and shows a path-review prompt only for UNCONFIRMED bindings.

[Admission](../../executor/location_admission.go) shares path → available valid signature → native identity → UUID matching across explicit and automatic collection. Rounds finish within the observed batch before unmatched entries become independent Files under Unforged/location-name. Existing paths win; later evidence cannot steal a still-present or unavailable original. [Scan](jobs.md#scan) matches bounded Job observations and publishes successful original scopes through metadata transactions. Failed or unobserved ranges never imply disappearance. Content changes preserve organization and invalidate stale content facts; versions follow the archive-evidence rule above.

[Private tracking keys](../../library/file_tracking.go) use primary (file_id, kind) and nonunique (kind, scope, key_value), with typed validation details, observation time and Location provenance. Candidates require trustworthy same-Executor evidence. Scoped native identities and UUIDs are evidence, not File IDs. Positively missing originals may retain keys; rebindings, import, explicit deletion and unregistration invalidate untrusted evidence. UUID writing is optional, create-only and best-effort. Explicit manual relocation replaces only the original association, with expected-binding and occupied-path checks; it preserves organization and cannot move physical bytes.

Same-Location mutations are mutually exclusive. A maintenance gate excludes import from identity-sensitive active operations without reserving disks for ordinary reads. Composite File metadata responses, logical edits, version lookup and content reads hold admission across lookup and hydration so imported numeric IDs cannot change meaning mid-operation. [Live access](../../executor/location_live.go) owns object/path validation; [file operation publication](../../library/file_operation.go) updates original paths after moves and removes bindings after deletion, never logically reorganizing existing Files or transferring annotations to copies.

## Visibility and Selection

[Physical file operations](#physical-file-operations) and ordinary logical edits finish within their requests; they are independent of visibility and background collection.

[Library settings](../../library/settings.go) persist independent default-on auto collection, **Include unbacked files**, and permanent-delete confirmation. Auto collection explicitly admits basic facts at registration and from the Files workflow; pure directory reads and target choosers do not collect. Off→on starts known-only Scans for confirmed Locations and returns their IDs/errors. Startup never silently traverses existing roots. Disabling collection does not remove Files or cancel Jobs; explicit annotation, admission, analysis and Backup still collect required records. Delete confirmation controls only the browser dialog, never server validation or CLI authorization. Saved-only queries retain all logical directories and regular Files with any FileVersion, even after original or last-copy loss. Both scopes remain editable; visibility does not alter direct-ID reads, retention, export, Location browsing or duplicate search.

This preference is edited in **Settings → Library**, separately from import/export. Selection inspection distinguishes unknown byte sizes from known sizes lacking a signature, reports unusable originals/copies and ignored Restore targets, and never substitutes zero for unavailable size facts.

Tree pages, recursive sizes, search and [selection expansion](../../library/selections.go) filter before pagination. Explicit `ALL` and `SAVED` scopes keep scripts independent of browser preferences; `DEFAULT` resolves the durable setting. Live Location selections need no File ID or previous analysis. [Selection inspection](../../executor/location_inspection.go) combines Library queries with bounded live traversal, without admission or hashing. Directory selections honor Ignore; explicitly selected regular files remain included. Preparation resolves real File identities, deduplicates overlaps and freezes content plus logical archive targets. Estimates expose unknown sizes and missing inputs rather than treating them as zero.

## Annotation, Search and Cleanup

[Metadata editing](../../library/file_metadata.go) validates and normalizes tags and notes transactionally. Directory merges union tags and retain distinct notes; ordinary File collisions never merge identities.

[Search](../../library/file_search.go) supports names, tags, note, type, size, modification time, location:<id>, has:online, has:archive and has:unknown with query-bound cursors. Archive coverage uses the original signature when an original exists, otherwise the latest version. An unsigned original is unknown, not confidently unarchived. Accessibility does not erase indexed references.

Library search evaluates last-published catalog facts, not a full-filesystem inspection. Live Location queries use the current directory observation and invalidate changed content facts before matching. The grammar is shared, but these observation times can differ after an external edit. `has:archive` is known inventory coverage, not a default-restorable-copy or row-color predicate. Visible-row inspection can therefore show newer uncertainty than the query that selected a Library result. Refresh/Scan supplies newer facts without making every Library query traverse all Locations.

**has:duplicates** returns each indexed original having another FileLocation with the same nonempty opaque signature, across all Locations and within a Location. It excludes unsigned originals, archive-only Files and historical-version-only matches. Other search predicates narrow returned Files without narrowing the duplicate comparison. Ordinary FileSearch retains its flat name/ID-paged response; inaccessible Locations retain their last published contribution. No hashing or File merging occurs during search.

[Duplicate catalog queries](../../library/duplicate_groups.go) separately page global signature groups and their members. Filters choose groups with at least one matching member; expansion retains all members and marks those outside the filter. Counts describe all known candidates, not checked filesystem coverage. Matching non-directory Positions are counted once per signature. Group cursors use full signature bytes; member cursors use File ID and bind to signature/query. A streamed Location-revision fingerprint detects catalog changes. [Member observation](../../apis/duplicate_groups.go) checks current metadata and tracking evidence one page at a time, without hashing or writing UUIDs, distinguishing confirmed, changed and unavailable entries. Unopened members remain unchecked. Changed membership requires refresh; Scan can expand content coverage without claiming a filesystem snapshot.

[File content summaries](../../library/file_content.go) batch-query associations, current/history copy counts and latest-version availability without filesystem or Media reads. [Original observation](../../executor/file_observation.go) supplements visible entries with an explicit check time and present, missing, unlinked, unavailable or unchecked status. It checks paths and facts, not hashes. Unknown or changed originals lose current-coverage validity; history remains independently queryable. Physical Positions are counted once across a File's versions, and restore-candidate counts use the same content-baseline and known-bad-copy rules as Restore.

Confirmed disappearance during successful scope analysis, explicit physical deletion or unregistration can remove online bindings, never Files, versions, archive bytes or Preview. Mere failed access cannot remove associations. Media deletion removes inventory metadata only. Trash remains logical organization.

### File and Version Display States

The primary color combines current local presence with known default Restore candidates. It does not measure health or backup recency.

| Local original | Known selectable backup | Color | Short meaning |
| --- | --- | --- | --- |
| Present | Yes | Green | Local and backed up |
| Present | No | Yellow | Local only |
| Missing or unlinked | Yes | Blue | Backup only |
| Missing or unlinked | No | Red | No available content source |
| Either axis unknown | Any | Gray | Identify the unconfirmed axis |

Present requires a current observation; FileLocation existence alone is insufficient. Missing means the associated path was observed absent. Unlinked means no association, not proof that matching bytes do not exist elsewhere. Inaccessible, imported-unconfirmed and invalid bindings remain unknown. With unknown current content and no selectable historical copy, backup coverage is unknown rather than definitely absent. A saved version without a selectable Position does not count as a backup. Unchecked copies can be candidates; known damaged, missing or unreadable copies are excluded by default. Candidate existence never promises immediate Media accessibility.

| Additional scenario | Primary color and supplement |
| --- | --- |
| Present; current content has a candidate | Green |
| Present; current content uncovered but history has a candidate | Green + unbacked-change marker |
| Present; current content unknown but history has a candidate | Green + unknown-content marker |
| Present; current content unknown and no historical candidate | Gray: backup unconfirmed |
| Missing/unlinked; latest saved version has a candidate | Blue; distinguish missing from unlinked in the tooltip |
| Missing/unlinked; only an older version has a candidate | Blue + latest-version-unavailable warning |
| Missing/unlinked; every saved version lacks candidates | Red |
| Present; known saved copies are all unusable | Yellow + copy-problem marker when current coverage is known; gray when it remains unknown |
| Original inaccessible; history has a candidate | Gray: local inaccessible, backup known |
| Original unchecked; history has a candidate | Gray: local unconfirmed, backup known |
| Candidate remains, but another recorded copy is abnormal | Green or blue by local presence + copy-problem marker |

Lists retain at most one supplement: copy/latest-version problem, then unbacked changes, then unknown content. Inspector and tooltips retain all facts. Shape and accessible text accompany color. Directories reserve the same status-column width but do not invent recursive coverage.

[Copy health](media-io.md#copy-health) records historical actual-read observations and time. It has no automatic expiry and does not control the primary color. An unloaded Tape is not damaged; successful archive writing is not a read-back check. A new matching-content read and final Media identity check are required to replace a bad finding. File/FileVersion display states are derived projections, not persisted state machines; [Restore outcomes](jobs.md#restore-outcomes) remain separate.

Explicit [Trim](../../library/library.go) removes dangling inventory and, when requested, regular Files having neither an original nor saved history. References are rechecked inside the deletion transaction. Unavailable indexed originals still protect Files. Version history is deliberately retained even after its last Position disappears; signature equality never establishes a physical-delete cascade.

## Shared Organization Interface

[FilesService](../../entity/files.proto) provides List, Get, Inspect, Collect and UpdateMetadata for either source. Entries have provider-owned references, display paths, observations, optional File associations/content facts and allowed operations. Pure List/Get calls do not collect; callers explicitly request collection. Both sources use the same query grammar, including available tags, notes and content facts; unsupported predicates are errors instead of silently ignored filters. Filtering precedes pagination. Location failures return errors without an indexed fallback; directory/query-bound cursors do not promise filesystem snapshots.

Get supplies a separate guarded `content_reference` immediately before [content access](../../apis/files_service.go); clients forward it to `/files/content` and do not synthesize it from a stable File ID. It supports GET/HEAD/Range through the same opened object. References are not content identities or transferable authorization.

[FileOperationService.Execute](../../entity/file_operation.proto) accepts one typed specification for Library or Location move/rename, mkdir and deletion. No user-level copy operation is offered. All selections belong to one scope. [The common engine](../../internal/treeops/plan.go) owns duplicate/overlap removal, bounded preflight, relative-path shortcuts, conflicts, recursive directory merge and execution ordering. Both [Library primitives](../../library/file_tree.go) and [filesystem primitives](../../executor/fileops/tree.go) implement Stat, ListChildren, ResolveChild and ApplyPrimitive; neither implements a separate recursive organization workflow. Direct Library MoveFile/Delete calls also use this engine.

Same-name directories merge recursively, unioning Library tags and distinct notes; same-name files or type conflicts fail even when signatures match. Moving to the same location succeeds without mutation. A preflight conflict leaves that selected root untouched. The [executor](../../internal/treeops/run.go) reports actual per-primitive paths and outcomes, including unprocessed entries after a race. Library roots use metadata transactions and reload annotations before structural changes; failed transactions expose no created File IDs. Physical partial success is not rolled back. No ordinary operation creates a Job.

Provider references and name resolution remain opaque to shared planning. Directory-kind hints distinguish a virtual prefix from an ordinary object with the same display name. The [object-store contract test](../../internal/treeops/object_store_test.go) exercises paged prefixes, conditional failures and a provider without native directory moves. It is not an S3 implementation; no SDK or credential framework is installed.

## Physical File Operations

The shared service applies Location primitives within one confirmed Location. Requests complete without a durable execution bundle or retry handle; disconnect/cancel stops pending work. Inputs freeze relative paths, binding token and object facts without requiring File IDs or a prior Scan.

[Preparation](../../internal/treeops/plan.go) pages selected scopes into a protected temporary SQLite manifest, including empty directories, ignored content and symlink objects. [Physical guards](../../executor/fileops/manifest.go) never follow links or enter another registered root, protected runtime/archive range or submount. Mount identity is checked on macOS and Linux; Linux requires `statx` mount IDs to detect same-device bind mounts. Cross-Location transfer and overwrite are unsupported.

[Execution](../../executor/fileops/execute.go) holds the Location gate through physical work and metadata publication. Native rename uses atomic no-replace semantics after complete selected-scope preflight. Recursive merge moves children and removes the emptied source only after successful children; each item rechecks targets and request-created ancestors. Delete revalidates frozen entries and removes directories individually, so new children prevent parent deletion. Browser confirmation settings do not weaken server checks; CLI deletion requires `--confirm-delete`.

| Item outcome | Meaning and retained data |
| --- | --- |
| SUCCEEDED | Physical action and Library publication settled; do not repeat it |
| FAILED | Physical work or publication failed; inspect the error and current paths before another request |
| PUBLICATION_PENDING | Physical change remains, but its Library update failed; do not repeat the physical mutation |
| UNPROCESSED | Request stopped before this item; no success is claimed |

The final `completed` summary means execution settled, not universal success. Partial failure retains previously successful items. Once a physical mutation completes, bounded metadata cleanup can settle its association despite request cancellation. Move/delete cannot be rolled back and may leave publication pending. A fresh live observation or explicit original relocation can reconcile surviving paths; there is no automatic replay, cross-request resume or directory-wide rollback.

[Library publication](../../library/file_operation.go) updates moved original paths and removes deleted original/tracking associations while preserving logical organization, tags, Note, versions and archive Positions. Merge publication changes only successfully moved paths; existing destination originals survive. An occupied stale association can be released only at that exact successful target, never across its subtree. Successful `(operation_uuid, item_id)` receipts publish atomically with these changes; they retain provenance, not pending work. The temporary manifest is removed at request end.

Retained copy-provenance records store produced native identity and nullable admitted File ID. [Receipt-aware admission](../../library/copy_admission.go) prevents a recorded copy from inheriting another File's organization; later observations follow only its admitted owner, subject to path priority and eligibility. Deletion releases the receipt owner. [Manual original relocation](../../library/relocate_original.go) replaces the selected File's receipt claims with a checked target without stealing an occupied path. Imported receipts retain history but cannot authorize local matching. These ownership records do not expose a user copy workflow.

## Backup and Legacy Compatibility

[Library metadata backups](../../library/jsonl.go) use the [published JSONL format](persistence.md#published-data-formats) and export one consistent metadata view containing Files/versions, Media/Positions, Locations/originals, tracking evidence, preferences and successful operation results. Online data requires its complete File/Location/FileLocation group; Restore provenance additionally requires source versions. File operation receipts retain historical Location IDs even after unregister, with executable tokens cleared on import. Imports validate live relationships and roll back the whole replacement on invalid input, including cross-batch conflicts. Media-only replacements preserve the immutable identity, type and profile of retained Position owners; reused numeric IDs cannot redirect archived copies. This also applies to legacy Tape-only imports.

Imported roots become Unconfirmed with rotated binding tokens, cleared original-observation tokens, installation-local Job links cleared and tracking evidence inactive. Confirmation plus new per-file observation is required before access. Imported Restore results retain history but have no executable binding token and cannot authorize retries. Position health/check times remain historical observations tied to their imported content facts; check-Job links are cleared, and known bad copies remain excluded by default. File replacement without originals clears stale online/tracking/Restore-result references and marks retained roots Unconfirmed, preventing numeric-ID reuse from creating false associations.

[Legacy whole-object import](../../library/json_legacy.go) uses bounded temporary staging. [Offline legacy migration](../operations/migration.md) preserves IDs/organization and copies signatures without rehashing, deriving versions only from confirmed archive evidence. Isolated File content facts do not create invented history.

Legacy import and the current metadata format have explicit readers. Old Draft headers and unsupported schemas are rejected before replacement; data is retained for inspection. The [persistence contract](persistence.md#published-data-formats) owns artifact identities and revision handling.
