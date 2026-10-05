# Library

Status: Current v1 Library model.

## Organization and Content

[The private File row](../../internal/library/file_row.go) stores logical parent/name, kind, note and nanosecond creation/update times. Parent/name is unique. [File](../../internal/library/file.go) is the Library-facing identity and presentation view assembled explicitly from that row. Tags live in FileTag with primary (file_id, tag) and lookup (tag, file_id). Tag suggestions use indexed literal-prefix ranges and bounded cursor pages, returning only referenced Tags with their File counts. File mode, mtime, hash, size, signature and content summary in browsing responses are projections from its original or, when no original exists, its latest saved version; they are never columns in `files`. An unsigned original does not fall back to historical content. Row-only traversal, validation and mutation never trigger projection queries; callers that promise a File view hydrate it explicitly.

Logical construction, merging and traversal use `File.Kind` explicitly. Saving display projections cannot change it. Legacy adapters translate the legacy mode into a kind at their boundary; ordinary GORM saves do not carry that compatibility behavior.

Logical moves validate an existing directory ancestry, rejecting self/descendant moves, missing parents and cycles. Nested destination creation and the final move share a metadata transaction, so a rejected edit leaves no intermediate directories. `MkdirAll` treats `.` as the existing directory rather than creating a dot node. Ancestor queries return the complete bounded path or an error for cycles/excessive depth, never a silently truncated breadcrumb. Root ID zero is virtual; reserved Trash ID -1 is an ordinary persisted directory when present.

[FileLocation](../../internal/library/file_location.go) uses file_id as its primary key: one File has zero or one original. It records Location ID, relative path, derived parent_path, mode, nanosecond mtime, size and nullable content facts. Location/path is unique; parent and signature indexes support association queries. This is a last-observed association, not proof of current existence. Location browsing reads real directories, including empty and dot directories; there is no persistent physical directory cache.

[FileVersion](../../internal/library/file_version.go) records saved content and its original restore metadata, independently of which File initiated physical archival. Only (file_id, signature) is unique. Repeated archival of identical content reuses the version and updates its latest archive time, preserving the first time and immutable content/mode/mtime. C1 → C2 → C1 reuses C1. Unknown historical archive times are NULL. Unarchived edits do not create versions.

The latest saved version is ordered by last archive time descending, with unknown last dates after known dates, then by version ID descending. Library projections, default Restore and saved Preview use that same order. A first archive date can be displayed when the last date is unknown; that display fallback does not change which version is latest.

[Archive observations](../../internal/library/version_history.go) retain evidenced save timestamps with primary key `(version_id, archived_at_ns)`. Publication records them in the same metadata transaction as the version; repeated evidence is idempotent. They allow a later Restore cutoff to select an intermediate save of reused content. Metadata backups include the observations and validate their version references on import; replacing Files removes obsolete history. Restore and coverage admission never use their execution time as a backup date.

Restore's **at or before** policy selects the newest recorded save not later than the inclusive cutoff, with version ID breaking equal-time ties. Existing first/last dates remain valid evidence, but do not reconstruct missing intermediate saves; unknown dates cannot qualify. This is a selection of recorded content, not proof of the complete file or directory state at that time. History lost before observation recording cannot be recovered from content hashes, file modification times or import time.

Signature remains opaque bytes in VARBINARY(256), with empty values normalized to NULL. There is no global signature uniqueness or automatic File merge. The current producer and consuming integrity checks retain their specific SHA-256/size requirements; they are not general storage encoding validation.

## Archive Inventory

[Media](../../internal/library/media.go) retains its Tape/Volume contract: unique kind/identity, immutable typed profile and physical lifecycle/capacity facts.

[Position](../../internal/library/position.go) stores Media ID, actual path, derived parent_path, directory flag, signature/hash/size, physical mode/times, storage order and typed storage metadata. It has no File or FileVersion owner column. Media/path remains unique; indexed parents support direct-child browsing. Derived directory Positions are not ownership or deletion units.

Verified Archive publication writes Positions and creates/reuses the selected File's version in the same metadata transaction. [Volume Scan publication](../../internal/library/scan.go) reconciles physical inventory without creating logical File identities. Explicit [inventory admission](../../internal/library/catalog.go) creates independent Files under Unforged using known copy facts, with unknown archive times; discovering content is not a new archive event. Only new logical names receive collision suffixes.

Exact signature lookup pages matching Positions and independently organized duplicate Files. One Position can serve several Files' versions. Coverage does not imply present readability or a fresh integrity check.

[Covered-original reconciliation](../../internal/library/file_coverage.go) creates a missing FileVersion when a published original's known signature matches non-directory Positions. Admission and original-record Scan publication scope reconciliation to their published originals; Archive, inventory Scan and direct Position publication scope it to affected inventory. Completed imports reconcile the facts they publish. All associations are metadata-only, transactionally published, and processed in File-ID pages with bounded copy pages. Read-only catalog queries and startup never create versions.

The version keeps this original's mode/mtime and content facts, not another File's organization or history. Known copy hashes can supply a missing original hash; contradictory sizes or known hashes fail the publication transaction. Initial archive dates use the earliest/latest evidenced dates from matching saved content, never copy mtime or discovery time; absent evidence stays NULL. Existing versions are untouched by coverage reconciliation, so more copies do not create more versions or advance archive dates. Later original changes, unlinking or copy removal retain the saved version.

## Registered Originals

[Location](../../internal/library/location.go) stores name, Executor/canonical root, preferred Restore destination flag, one JSON configuration for Ignore text and mmap content reads, revision and nanosecond observation/configuration times. Values queried separately, including Restore preference, remain columns. Mmap defaults off. Every Location supports live originals; preference is not write authorization. A Location's entries have exactly one shape per layer: the recorded association is one [FileLocation](../../internal/library/file_location.go), a listed entry is one [entity.LocationEntry](../../entity/location.proto), and what a read observed is one [library.ObservedEntry](../../internal/library/observed_entry.go) - the manifest a Scan publishes and the before/after facts of its result, never a stored entity. Readers return the association itself and read retained evidence separately. Duplicate roots on an Executor conflict. Registration is immediately browsable and usable, including after an import. Name changes do not reorganize Files. [Configuration migration and access](../operations/install.md#location-configuration-and-migration) own administrator boundaries and explicit installer conversion.

GORM hooks advance revisions for configuration, attempts and association publication. Search continuations bind directory facts and query; complete directory List has no cursor. Accessibility is a separate timed runtime observation.

Each FileLocation records the relative path and observed facts of its original. Root/Ignore changes and import keep existing records but never retarget them: later reads resolve the recorded path under the current root and report what they observe. Live requests carry a relative path with an optional File association. Access, admission and preparation resolve that path afresh and report the object they actually observe; no complete-analysis readiness state exists.

**Location Ignore scope.** Ignore governs this Location's ordinary content: live browsing and name queries, explicit measurement, Scan/Archive selection expansion — including an explicitly named path — and the new associations those reads publish. It never rewrites recorded facts or blocks writes: existing originals, Files, versions and Trash remain, explicit admission stays available, physical file operations still apply, and Restore still writes its output, withholding only the association when that output matches Ignore. Administrator boundaries and runtime/archive resources remain mandatory everywhere and are never reopened by an Ignore exception.

**Location Ignore matching.** The typed rule text is matched with Git's gitignore pattern format and evaluation semantics relative to this Location's root: a name rule matches at any depth, a separator rule is root-relative, a trailing slash is directory-only, the last matching pattern decides, and an excluded parent is never reopened by a child exception. Matching is byte-exact and case-sensitive, independent of the host filesystem, and never reads filesystem `.gitignore` files, the index or tracked-file state. One compiled rule set serves every decision: a directory resolves its own decision once and each enumerated entry is then decided by name, so no read path recompiles rules or re-derives ancestor state per entry. The [conformance tests](../../internal/ignore/ignore_conformance_test.go) own the executable corpus and cost checks.

### Location State

| Axis/state | Trigger | Allowed operations and retained facts | Failure/retry |
| --- | --- | --- | --- |
| Registered | Explicit registration or import | Live browsing, admission, analysis, authorized file operations and Restore | Root/Ignore changes keep records but change what later reads resolve; no readiness flag gates use |
| Last complete original scan | Successful whole-Location record publication only | Historical timestamp/Job, independent of browsing and single-file Restore | Failed ranges and individual publication do not claim full coverage |
| Unchecked / accessible / inaccessible | Timed runtime path observation | Describes reachability, not content identity or archive health | A later check replaces the observation; no index deletion |

`Imported directory` is not a state; names are user-defined, and an imported registration is used like any other.

`last_sync_job_id` and `last_sync_at_ns` advance together only after successful whole-Location original-record publication. The Scan caller supplies that coverage fact from the validated selection; Library never infers it from observed rows. Completed partial selections publish their observed facts and confirmed absence while retaining both complete-scan markers. `last_job_id` still identifies the latest attempt.

Scan creation normalizes Location selections to `ALL` regardless of the requested Library visibility scope. An empty Location path therefore covers the whole Location under its existing Ignore and access rules. Library selections retain their visibility scope and never establish whole-Location coverage.

[Admission](../../internal/executor/location_admission.go) shares path → available valid signature → native identity matching across explicit and automatic collection. Rounds finish within the observed batch before unmatched entries become independent Files under Unforged/location-name. Existing paths win; later evidence cannot steal a still-present or unavailable original. [Scan](jobs.md#scan) matches bounded Job entries and publishes complete selected Location ranges through metadata transactions. Failed or unobserved ranges never imply disappearance. Content changes preserve organization and invalidate stale content facts; versions follow the archive-evidence rule above.

[Private tracking keys](../../internal/library/file_tracking.go) use primary (file_id, kind) and nonunique (kind, scope, key_value), with typed validation details, observation time and Location provenance. Candidates require trustworthy same-Executor evidence. Scoped native identities are evidence, not File IDs. An available native identity records the Executor and device in its scope and the inode in its key value, without writing a marker into the file. Birth time and generation constrain matching only when their recorded values are nonzero: Darwin records both platform values, while Linux records neither. Available signature or native evidence can preserve continuity under the matching order above. Matching does not prove that a move or copy occurred or provide a platform-independent guarantee against inode reuse. Positively missing originals may retain keys; rebindings, import, explicit deletion and unregistration invalidate untrusted evidence. A root or Ignore change keeps it: evidence confirms a later observation only when that observation agrees with it, so evidence recorded under an older configuration leaves a File unconfirmed rather than claimed, and dropping it would silently unconfirm every File of the Location. Tracking never opens the observed file or reads/writes xattrs. Retired tracking UUID markers are left untouched. Catalogs containing obsolete UUID evidence are rejected before writes; importing such JSONL evidence rolls back the entire import.

Whether a File is backed up is not decided by tracking evidence: archive coverage is the recorded content signature matching published Positions, read from the catalog alone. Tracking evidence decides whether the content currently on disk is the recorded one. A listing reports what it observed - including `Local file present` for an original whose facts it read - and states `Current content not checked` where the evidence was not confirmed, rather than implying coverage; operations that act on bytes confirm identity themselves. Explicit manual relocation replaces only the original association, refusing a path owned by another File; it preserves organization and cannot move physical bytes.

Location operations use the [supported single-operator model](overview.md#supported-operating-model): the operator does not issue conflicting mutations or replace selected files while an operation uses them. Requests therefore do not add filesystem locks, repeated observations or publication-time filesystem checks to manufacture multi-user semantics. Metadata transactions still keep one requested catalog mutation atomic. Catalog import is performed with no active Job, and a later revision may require stopping the service first. [Live access](../../internal/executor/location_live.go) owns administrator/path validation; [file operation publication](../../internal/library/file_operation.go) updates original paths after moves and removes bindings after deletion, never logically reorganizing existing Files or transferring annotations to copies.

## Visibility and Selection

[Physical file operations](#physical-file-operations) and ordinary logical edits finish within their requests; they are independent of visibility and background collection.

[Library settings](../../internal/library/settings.go), stored as the typed `library` group in `settings`, persist independent default-on **Include unbacked files** and **Confirm Delete** preferences. Browsing, Location registration and preference changes never collect or create Jobs. Original Add to Library opens the unified Scan form; annotation saves, Archive and Restore establish associations inside their own workflows. Delete confirmation controls the browser interaction, not server validation or CLI consent. Saved-only queries retain logical directories and regular Files with any FileVersion. Visibility does not alter direct reads, retention, export, Location browsing or duplicate search.

This preference is edited in **Settings → Library**, separately from import/export. Archive and Restore selection inspection resolves `DEFAULT` against that setting in its catalog transaction. Archive inspection counts only Files with an original association and reports unlinked Files separately as missing originals; directories need no association of their own. Restore inspection reports unusable copies and ignored targets. Unknown byte sizes remain distinct from known sizes lacking a signature, and unavailable size facts are never replaced with zero.

Complete directory List and paged search filter before returning rows; [selection expansion](../../internal/library/selections.go) filters before its bounded pages. Explicit `ALL` and `SAVED` scopes keep scripts independent of browser preferences; `DEFAULT` resolves the durable setting. Live Location selections need no File ID or previous analysis. [Explicit Data Usage](api.md#measurement) measures matching roots across all pages, includes matched directories' contents, and deduplicates overlaps; ordinary list reads do not perform recursive size calculation. [Selection inspection](../../internal/executor/location_inspection.go) combines Library queries with bounded live traversal, without admission or hashing. Live browsing, queries, measurement and Archive/Scan selection expansion omit entries matching administrator rules, runtime/archive resources and user Ignore, while explicit admission stays available ([Location Ignore scope](#registered-originals)). Preparation resolves real File identities, deduplicates overlaps and records logical archive targets; transfer results own the archived content facts. Estimates expose unknown sizes and missing inputs rather than treating them as zero.

Original selection expansion yields ordered, bounded batches. The first eligible selection owns
overlapping Files; duplicate roots and already covered subtrees do not repeat traversal. Explicit
root identities and shared ancestors are read in batches, and saved-version visibility is applied
before child pagination. An earlier SAVED selection does not hide unbacked leaves from a later ALL
selection. Original and version facts are read once per batch for overlap checks and inspection,
without physical admission. Original preparation omits unused compatibility projections; Restore
and Data Usage retain their original-or-saved size and mode facts.

Observation admission and publication share transaction-local batches of original, identity and
tracking facts. Unchanged observations avoid mutation work; changed rows are written in the same
metadata transaction, followed by covered-version reconciliation for the admitted set or published
Location. Callers retain allocated File IDs and derived content facts from publication.
Learning a previously missing hash preserves an unchanged content state's opaque signature.
Changed metadata, contradictory native evidence, an existing different hash or an explicitly
different opaque signature still supplies a new content basis.

## Annotation, Search and Cleanup

[Metadata editing](../../internal/library/file_metadata.go) validates and normalizes tags and notes transactionally. Directory merges union tags and retain distinct notes; ordinary File collisions never merge identities.

[Search](../../internal/library/file_search.go) supports names, tags, note, type, size, modification time, location:<id>, has:original, has:archive and has:unknown with query-bound cursors. Archive coverage uses the original signature when an original exists, otherwise the latest version. An unsigned original is unknown, not confidently unarchived. Accessibility does not erase indexed references.

Library Search and query-based measurement evaluate last-recorded/published Catalog facts before pagination or root selection. Size and modification time use the recorded original, or the latest saved version when there is no original. Missing or inaccessible originals retain their recorded matches. Live Location queries use the current directory observation and invalidate changed content facts before matching. Both sources preserve the same grammar and Boolean grouping. `has:archive` is known inventory coverage, not a default-restorable-copy or row-color predicate. Requested visible-row observations can show newer attributes and uncertainty without changing Library query membership. Only publication of newer Catalog facts changes that membership; refreshing a live display does not publish those facts.

**has:duplicates** returns each indexed original having another FileLocation with the same nonempty opaque signature, across all Locations and within a Location. It excludes unsigned originals, archive-only Files and historical-version-only matches. Other search predicates narrow returned Files without narrowing the duplicate comparison. Ordinary FileSearch retains its flat name/ID-paged response; inaccessible Locations retain their last published contribution. No hashing or File merging occurs during search.

The [Identical file tools](#identical-file-tools) own connected-component matching, retained results
and member observation. They are separate from the flat `has:duplicates` search predicate.

[File content summaries](../../internal/library/file_content.go) batch-query associations, current/history copy counts and latest-version availability without filesystem or Media reads. [Original observation](../../internal/executor/file_observation.go) supplements visible entries with an explicit check time and present, missing, unlinked, unavailable or unchecked status. It checks paths and facts, not hashes. Unknown or changed originals lose current-coverage validity; history remains independently queryable. Physical Positions are counted once across a File's versions, and restore-candidate counts use the same content-baseline and known-bad-copy rules as Restore.

Observed disappearance within a completely validated Location selection, explicit physical deletion or unregistration can remove recorded originals, never Files, versions, archive bytes or Preview. Mere failed access cannot remove associations. Media deletion removes inventory metadata only. Trash remains logical organization.

### File and Version Display States

The primary color combines current local presence with known default Restore candidates. It does not measure health or archive recency.

| Local original | Known selectable archive copy | Color | Short meaning |
| --- | --- | --- | --- |
| Present | Yes | Green | Local and archived |
| Present | No | Yellow | Local only |
| Missing or unlinked | Yes | Blue | Archive only |
| Missing or unlinked | No | Red | No available content source |
| Either axis unknown | Any | Gray | Identify the unconfirmed axis |

Present requires a current observation; FileLocation existence alone is insufficient. Missing means the associated path was observed absent. Unlinked means no association, not proof that matching bytes do not exist elsewhere. Inaccessible and unobservable originals remain unknown. With unknown current content and no selectable historical copy, archive coverage is unknown rather than definitely absent. A saved version without a selectable Position does not count as an archived copy. Unchecked copies can be candidates; known damaged, missing or unreadable copies are excluded by default. Candidate existence never promises immediate Media accessibility.

| Additional scenario | Primary color and supplement |
| --- | --- |
| Present; current content has a candidate | Green |
| Present; current content uncovered but history has a candidate | Green + unbacked-change marker |
| Present; current content unknown but history has a candidate | Green + unknown-content marker |
| Present; current content unknown and no historical candidate | Gray: archive unconfirmed |
| Missing/unlinked; latest saved version has a candidate | Blue; distinguish missing from unlinked in the tooltip |
| Missing/unlinked; only an older version has a candidate | Blue + latest-version-unavailable warning |
| Missing/unlinked; every saved version lacks candidates | Red |
| Present; known saved copies are all unusable | Yellow + copy-problem marker when current coverage is known; gray when it remains unknown |
| Original inaccessible; history has a candidate | Gray: local inaccessible, archive known |
| Original unchecked; history has a candidate | Gray: local unconfirmed, archive known |
| Candidate remains, but another recorded copy is abnormal | Green or blue by local presence + copy-problem marker |

Lists retain at most one supplement: copy/latest-version problem, then unbacked changes, then unknown content. Inspector and tooltips retain all facts. Shape and accessible text accompany color. Directories reserve the same status-column width but do not invent recursive coverage.

[Copy health](media-io.md#copy-health) records historical actual-read observations and time. It has no automatic expiry and does not control the primary color. An unloaded Tape is not damaged; successful archive writing is not a read-back check. A new matching-content read on the expected Media and successful Session finalization are required to replace a bad finding. File/FileVersion display states are derived projections, not persisted state machines; [Restore outcomes](jobs.md#restore-outcomes) remain separate.

Explicit [Trim](../../internal/library/library.go) removes dangling inventory and, when requested, regular Files having neither an original nor saved history. References are rechecked inside the deletion transaction. Unavailable indexed originals still protect Files. Version history is deliberately retained even after its last Position disappears; signature equality never establishes a physical-delete cascade.

## Shared Organization Interface

[FilesService](../../entity/files.proto) owns List, Search, Get, UpdateMetadata, Mkdir, Move and Remove, plus paged versions/copies/duplicates and explicit association operations. List returns one complete directory snapshot as streamed batches, with identity, breadcrumbs and total in the first batch and no cursor or limit. Search remains paged and query-bound. Both return reference/name/kind/path; include groups add attributes, status, operations or navigation. Basic reads bypass full-File hydration; attributes/status use batch queries and status uses existence rather than exact copy counts. Both sources share query grammar and apply it before returning rows. Location reads never use index fallback. Get supplies organization and original navigation independently of Preview/history.

Status projection derives archive existence from each File's versions and indexed Positions. Every existence lookup keeps the File as its selective starting point, and each batch reads facts in bounded metadata rounds rather than scanning all Positions or issuing one query per row.

Get supplies an applicable `content_reference` for explicit content workflows such as Archive and Preview. References are not content identities or transferable authorization; no HTTP route serves the referenced file bytes.

Named Files methods express creation, movement/rename and removal. All selections belong to one source. [The common engine](../../internal/treeops/plan.go) owns deduplication, bounded preflight, relative paths, conflicts, recursive directory merge and ordering. [Library](../../internal/library/file_tree.go) and [filesystem](../../internal/executor/fileops/tree.go) adapters provide guarded primitives, not separate workflows. Ordinary operations finish within their request and stream individual outcomes plus a final summary.

Same-name directories merge recursively, unioning Library tags and distinct notes; same-name files or type conflicts fail even when signatures match. Moving to the same location succeeds without mutation. A preflight conflict leaves that selected root untouched. The [executor](../../internal/treeops/run.go) reports actual per-primitive paths and outcomes, including entries left unprocessed after an earlier failure or cancellation. Library roots use metadata transactions for structural changes; failed transactions expose no created File IDs. Physical partial success is not rolled back. No ordinary operation creates a Job.

Provider references and name resolution remain opaque to shared planning. Directory-kind hints distinguish a virtual prefix from an ordinary object with the same display name. The [object-store contract test](../../internal/treeops/object_store_test.go) exercises paged prefixes, conditional failures and a provider without native directory moves. It is not an S3 implementation; no SDK or credential framework is installed.

## Physical File Operations

The shared service applies Location primitives within one registered Location. Requests complete without a durable execution bundle or retry handle; disconnect/cancel stops pending work. Inputs freeze relative paths without requiring File IDs or a prior Scan.

[Preparation](../../internal/treeops/plan.go) pages selected scopes into a protected temporary SQLite manifest, including empty directories, ignored content and symlink objects. [Physical guards](../../internal/executor/fileops/manifest.go) never follow links or enter another registered root, protected runtime/archive range or submount. Mount identity is checked on macOS and Linux; Linux requires `statx` mount IDs to detect same-device bind mounts. Cross-Location transfer and overwrite are unsupported.

[Execution](../../internal/executor/fileops/execute.go) applies the preflighted source and target paths once. Linux and macOS native rename use atomic no-replace semantics; the experimental FreeBSD implementation checks for an existing target before ordinary rename. Recursive merge moves children and removes the emptied source only after successful children. Remove moves the selected root as it exists at execution time into an owned `.trash/<UUID>/<name>` on the same filesystem. Unknown Trash directories, links or markers are rejected. Failure never falls back to copying or unlinking, and the executor does not run a second full-tree validation pass. CLI `rm --dryrun` reports the removal plan; omitting `--dryrun` performs the removal.

| Item outcome | Meaning and retained data |
| --- | --- |
| SUCCEEDED | Physical action and Library publication settled; do not repeat it |
| FAILED | Physical work or publication failed; inspect the error and current paths before another request |
| PUBLICATION_PENDING | Physical change remains, but its Library update failed; do not repeat the physical mutation |
| UNPROCESSED | Request stopped before this item; no success is claimed |

The final `completed` summary means execution settled, not universal success. Partial failure retains previously successful items. An admitted physical primitive completes its filesystem change, Library update and result recording with cancellation detached and without a separate cleanup deadline. Cancellation stops subsequent primitives; logical roots retain their metadata transaction and rollback boundary. Move/delete cannot be rolled back and may leave publication pending if the Library update fails. A fresh live observation or explicit original relocation can reconcile surviving paths; there is no automatic replay, cross-request resume or directory-wide rollback.

[Library publication](../../internal/library/file_operation.go) updates moved original paths and removes deleted original/tracking associations while preserving organization, tags, Note, versions and archive Positions. Merge publication changes only successfully moved paths. An occupied stale association can be released only at the exact successful target. No persistent operation-result row is written; publication failures are returned directly. The temporary manifest is removed at request end.

Location Trash remains browsable. User payloads can Move to ordinary directories in the same Location, while the root, ownership marker and batch containers are protected. Trash cannot be a Scan, Archive, admission or Restore target, even by direct selection; it is not a global browse exclusion. Removal disconnects original/tracking associations for the whole subtree and preserves logical Files, annotations, versions and copies. Moving out restores only the physical location; explicit Scan, annotation or Archive can subsequently manage it. Neither source exposes permanent file removal or automatic Trash clearing. Library retains its existing empty logical-directory cleanup boundary.

## Identical File Tools

The [identical tools](../../entity/files.proto) query recorded signatures only. Library includes ordinary regular Files, including unbacked Files, and excludes logical Trash. Each File contributes its current known original signature and every saved version signature. An unsigned original adds no matching edge, but its Location and path remain part of the fingerprint when saved versions connect its File to a Library group. Connected components include transitive matches; they do not imply that all current originals are identical. Locations groups use only current recorded signatures from explicitly selected whole Locations; repeated selections contribute once. No query starts Scan, computes hashes or publishes associations.

[Query staging](../../internal/library/identical_query.go) streams catalog facts into disposable SQLite tables through GORM. Signature minima settle small components; unresolved long chains use bounded disk-backed traversal without pairwise member expansion. One explicit Find retains the complete result temporarily. Dense positions keep groups in their displayed title order and index members by File ID, name or recorded size in either direction; ties use File ID and unknown sizes follow known sizes. Size comes from the recorded original, or the latest saved version when there is no original. When hidden files are excluded, each group uses its first visible member for the title. A bounded row read or position lookup reuses the requested order without rebuilding components. Group/member continuations retain their File ID order. Continuation cursors require the result ID; a missing or expired ID fails without collecting again. The service retains at most two results and expires each after 30 minutes idle. Find and targeted component SQLite files live in a private directory under configured `paths.work`; a lifetime lock excludes another service using that directory, and startup removes only its abandoned result directories. Clean shutdown closes retained results before releasing the lock. A restart invalidates result IDs, so an unavailable result requires another explicit Find. Older directories in shared system temporary storage are never swept automatically. The lock guards only disposable staging cleanup, not Library operations. No result enters Library backups or changes persistent data formats. Group fingerprints cover only their own membership, matching signatures and recorded originals. Keep and Merge independently validate the current connected component before changing it. Member evidence is a bounded sample of shared current/version signatures; only requested members receive live observations.

**Merge into** selects one target and merges its complete authoritative Library group in a metadata transaction. The target keeps its name, logical path, identity and original. Sources retain their identities in separate folders under the existing Library Trash checkpoint, with original/tracking associations detached; physical files are untouched. The response counts source Files retired by the committed transaction; a dry run reports the number it would retire. Existing tag union and distinct-Note rules apply. Versions are combined by signature, preferring target metadata, then ascending source File/version IDs. Signature equality determines version identity; the preferred version keeps its content metadata unchanged. Merge does not validate or repair conflicting size/hash records. Saved-time observations are unioned and endpoints recomputed, without inventing a save at merge time. The write transaction protects the related metadata changes from partial database-write failure; it does not inspect unrelated Job bundles.

**Keep this** selects one observed Location member. [Execution](../../internal/apis/identical_keep.go) declares the involved Locations, observes the complete current group once, and stages physical references before any deletion. Ordinary bounded Remove batches retain existing Trash, object guards and publication semantics. Results identify each File and aggregate successes, failures and pending publication; neither failures nor refreshes replay completed operations. **Delete** uses existing Remove on only the selected references, grouped by Location. Independent files in the same Location may be removed concurrently; only first-time creation of its owned Trash marker is briefly coordinated.

Retained Location members keep their displayed path and physical reference together. After an
original moves, an observation of its new path supplies neither actions nor current status or
attributes for the retained old path. Another explicit Find supplies the current group.

**Remove version** removes only the selected FileVersion and save observations. It retains the File, other versions, Positions and Preview, and does not trigger coverage reconciliation. Later ordinary coverage publication can establish that version again.

Frozen Restore manifests retain their selected IDs and content facts. Publication accepts that immutable version snapshot after catalog removal or reassignment and creates an independent restored File when reconnecting the source is no longer valid. Restore outcomes stay in the Job bundle; the Library stores the resulting organization and associations.

## Backup and Legacy Compatibility

[Library metadata backups](../../internal/library/jsonl.go) export Files/versions, Media/Positions, Locations/originals and tracking evidence using the [version 1 JSONL format](persistence.md#published-data-formats). Each Location record includes its complete JSON configuration. The header declares only its format, version and included Library entities. Settings and operation receipts are outside this boundary, and import always preserves the target installation's Settings. Location data requires its complete File/Location/FileLocation group. Imports validate live relationships and roll back replacement on invalid input, including cross-batch conflicts. Media-only replacements preserve the immutable identity, kind and profile of retained Position owners; reused numeric IDs cannot redirect archived copies. This also applies to legacy Tape-only imports.

Import rebuilds derived Position directories, so their IDs may change, and issues fresh Location revisions. Physical Position IDs come from the imported records. Compare derived directories by Media and path and exclude Location revisions when verifying a metadata roundtrip. Location configuration, including empty or whitespace-only Ignore text, is preserved; default rules apply only to new registrations.

Import is a quiesced operator action, used with no active Job. It keeps the authored Location configuration, and a complete Location group restores its recorded original associations; installation-local Job links are cleared and tracking evidence becomes inactive. A Files-only replacement clears original associations and tracking evidence instead. Position health/check times remain historical observations tied to content, and known bad copies remain excluded by default. File replacement without originals clears stale original/tracking references.

[Explicit inventory admission](../../internal/library/catalog.go) accepts regular Position IDs and directory Position IDs. Directories expand their signed regular descendants in bounded path pages before one metadata transaction creates the required logical parents and independent Files; inventory directories themselves never become saved content. Overlapping roots and repeated Positions are deduplicated. Empty or unsigned-only selections report that nothing could be admitted instead of fabricating a mirrored directory.

Admission may be scoped to one Media: a selection from another Media rejects the request instead of narrowing it, and a Media without a selection supplies its own top-level directory Positions as roots. A `dryrun` request classifies the same candidates without creating Files, versions or directory nodes, so a later write with the same selection reports the counts the report announced. Content already present under the Media's `Unforged` import directory is skipped and counted instead of admitted again, which is a path-derived check rather than stored ownership: an entry moved or renamed inside Library no longer matches and is admitted a second time. The directory count reports only unique newly created logical parents; reused parents do not increment it. Dry runs retain prospective names in a disposable SQLite workset across inventory pages, leaving the catalog unchanged and removing the workset when the request ends.

Skipped existing entries create no File or version and return no new File ID. A selected directory
containing signed existing entries is not an unsigned-only selection. Existing-entry lookup follows
the same collision suffixes as creation: regular Files cannot serve as parents, directories cannot
stand in for admitted content, and repeated admission reuses an already resolved collision path.

[Legacy whole-object import](../../internal/library/json_legacy.go) uses bounded temporary staging. [Offline legacy migration](../operations/migration.md) preserves IDs/organization and copies signatures without rehashing, deriving versions only from observed archive evidence. Isolated File content facts do not create invented history.

Legacy whole-object JSON and the version 1 metadata format have explicit readers. Draft JSONL versions and unsupported schemas are rejected before replacement; data is retained for inspection. The [persistence contract](persistence.md#published-data-formats) owns artifact identities and version handling.
