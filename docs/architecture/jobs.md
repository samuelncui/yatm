# Jobs and Execution

## Shared Lifecycle

[Job registration](../../executor/job_type.go) installs each kind's runner and typed gRPC service together. The Executor coordinates Job-ID locks, cancellation, runner lifetime, and attempt-scoped Media leases; each runner owns its own phase/state machine. Create builds a complete bundle, then indexes potentially large input asynchronously.

The [public Job contract](../../entity/job.proto) exposes kind, priority, durable status, current phase, catalog revision, timestamps and optional primary target identity/name. [Resource associations](../../executor/job_resources.go) capture frozen inputs and actually used Location/Media with source/destination roles. Archive/Preview retries following a relocated original retain the initial source and append the source resolved during execution; history never guesses from current originals. Kind, durable state and resource filters run before snapshot paging; changefeed responses include state departures so clients remove rows that no longer match. [Persistence](persistence.md) defines projection publication and recovery.

| Durable state / observation | Trigger | Allowed operations and retained facts | Failure/retry |
| --- | --- | --- | --- |
| INDEXING | Create or explicit RetryIndex | Background manifest construction or analysis; progress, logs, cancel | Error/cancel exposes WAITING_FOR_INDEX_RETRY; previously published successful ranges remain intact |
| PENDING | Executable manifest prepared | Choose Media, run, inspect results, cancel an active attempt | Attempt failure retains verified checkpoints and remaining work |
| COMPLETED | All workflow items settled | Read results/logs or delete retained Job metadata | Restore may include damaged outputs; Verify may include bad findings; inspect typed outcomes |
| Runner phase | Active in-memory execution | Distinguishes preparation, reading/writing, finalization and publication | Restart returns to the last durable checkpoint, not the former live phase |
| Error / cancellation | Attempt returns an error | Logs and partial verified results remain | RetryIndex rebuilds an incomplete index; Media attempts explicitly resume pending work |

There is no persistent FAILED state. Completion is not a universal assertion that every recovered byte or checked copy is healthy. Job deletion removes its retained execution bundle, not physical copies, versions or successful Library Restore provenance.

The durable completion checkpoint can precede the end of an active runner's cleanup. Automation waiting for completion must observe both `COMPLETED` status and `JOB_PHASE_COMPLETED`, rather than start dependent work while the preceding attempt is still finalizing resources.

## Archive

[Archive indexing](../../executor/archive/init.go) expands registered Library/Location selections and freezes expected content in a durable [item manifest](../../executor/archive/models.go). `target_path` is immutable input identity and unique for deduplication. `media_path` is empty while pending; only ACP's actual successful relative target populates it. Indexes on `(status, target_path)` and `(status, media_path)` serve copying and finalization respectively. Staging is on each item row, with no separate staging table. Raw Source execution is retained only for frozen legacy Job inputs, not new public requests.

[Selected input](../../executor/location_selections.go) combines Library visibility with live Location roots and observed file facts. Location files need neither a File ID nor prior Analyze; the shared admission path creates or updates their associations. Bounded directory expansion applies Ignore and deduplicates overlapping selections in the Job manifest. Explicit files may bypass user Ignore, never administrator authorization. Indexing freezes File ID, opaque signature, expected SHA-256/size, and Library-root-relative target path, including when selected through Location browsing. Later logical edits do not change that manifest. [Content capture](../../executor/content.go) refreshes a changed Library original before freezing its expected bytes. Each attempt uses only that File's current valid original; retries may follow its relocated binding when frozen content still matches. Unknown content is hashed through ACP during indexing. Missing inputs are explicit errors, never omissions or automatic Restore requests. [Selection inspection](library.md#visibility-and-selection) reads bounded metadata without admission or hashing; it is a review, not the frozen manifest.

[Live preparation](../../executor/selection_stage.go) stages each Location's complete selected scope in the Job's `observations` and `originals` tables before association. The shared matcher completes all higher-priority rounds before considering weaker evidence; traversal order cannot let a copied UUID preempt a later native-identity match. Known YATM copy provenance constrains an output to independent admission or its own previously admitted File, never arbitrary signature/UUID inheritance. `selection_directories` retains only temporary directory references for final membership validation. File facts and relinquished old paths are revalidated before a metadata-only publication, without recording a Location-wide Analyze or removing unselected originals. Published File IDs freeze in the Job before releasing the Location gate; content preparation follows those IDs and checks selected facts before and after reading, allowing safe relocation but never adopting a replacement File at the old path. Memory remains bounded by input roots and database pages.

An optional companion Scan with Preview enabled starts only after Archive indexing succeeds, from the frozen item paths and expected content. It never independently re-admits the same live roots or competes with Archive preparation for their Location gate. Progress exposes its Job ID or separate creation error; Preview failure does not undo a prepared or completed backup.

One [Media operation](../../executor/archive/media.go) proceeds in this order:

1. Acquire the target's exclusive attempt lease and create its typed Write Session.
2. Page PENDING items in target order through one continuous, bounded ACP stream.
3. For every input, compare ACP's actual transferred hash/size with the frozen expectation before accepting the result. Persist each accepted result as STAGED immediately, including the actual Media path, hash, size, mode, and times.
4. Call Write Finalize exactly once after ACP returns, including error/cancellation, using a cleanup context without the original deadline.
5. Publish only backend-verified STAGED items with `CommitMedia`. Create or reuse the frozen File’s FileVersion in that same transaction, preserving its opaque signature and own restore metadata. Raw inputs already have per-Job File associations; no later signature-based File merge or binding occurs.
6. Mark published items SUBMITTED with their Media ID; complete the Job when nothing remains pending. Return copy/finalization/publication errors after retaining eligible successes.

The [result stream](../../executor/archive/stream.go) validates completed copy facts. STAGED is a candidate result, not durable archive success. Initialization resets leftover unsubmitted STAGED items and attempt report data to PENDING; resetting also clears Media path and result. Only submitted files count toward durable progress, historical speed, ETA, and reports; verified zero-byte files count too.

[Media finalization](media-io.md) defines which candidates survive normal completion, cancellation, target-full conditions, or validation failure. Prefix directories have no batch identity or cleanup ownership.

## Restore

[Restore indexing](../../executor/restore/init.go) accepts file/directory selections following a latest or inclusive recorded-time cutoff policy, plus explicit FileVersion IDs. [Shared selection resolution](../../library/restore_selection.go) serves both inspection and indexing. Explicit versions replace automatic selection for their File, including a File inside a selected directory; several explicit versions remain separate outputs. Roots and overlaps are deduplicated with bounded traversal. It pages signature-matched Positions into bounded [candidate rows](../../executor/restore/models.go), freezing version content, own mode/mtime, File identity and Library-root-relative target path. Missing copies are errors, not permission to substitute older versions.

No saved version, only newer backups, and unknown backup dates are unmatched selection results, separate from a chosen version with no usable copy. Unmatched selections block preparation by default. `skip_unmatched_versions` requires a cutoff and explicit consent; it skips only automatic unmatched selections, never missing-copy errors. An all-skipped selection cannot become an executable Job. Inspection reports unmatched/skipped counts and resolved version/date details only for bounded explicit regular-file roots or explicit versions, not an entire directory manifest. Directory membership and logical output paths use the Library at preparation, not historical directory snapshots.

The complete manifest and each File's reconnect candidate/logical name freeze in one Job metadata transaction. Once frozen, RetryIndex only retries output preparation/publication; it cannot replace selected versions from a later Library view. An incomplete first manifest remains rebuildable.

[Destination admission](../../executor/restore_destination.go) freezes a confirmed Location's root, Executor and binding token plus the chosen relative directory; preference is not permission and no full Scan is required. Each real attempt holds the Location operation gate through publication, rechecks authorization and rejects a changed binding; waiting for Media holds no such gate. [Output reservations](../../executor/restore/output.go) persist each item's actual path. Unowned equal content is adopted only after a real ACP read and metadata restoration. Conflicts use `stem.restored.<base36-version-id>.ext`, then numeric suffixes. Frozen-path conflicts fail rather than silently choosing new names. Migrated legacy Jobs retain their explicitly configured legacy root without guessed original association.

[Native name reservation](../../executor/restore/output_names.go) uses temporary directory-only namespaces under each actual existing output parent, including parent aliases and nested mounts. Filesystem lookup handles case and Unicode equivalence; no SQL collation guesses those rules. A target whose new directories do not inherit its observed name rules is rejected, as are selected file/directory conflicts. Reservations are rebuilt from bounded Job pages on retry. Probe directories contain no file content, are excluded dynamically from original readers and are removed after indexing; interrupted leftovers contain only directories and cannot become indexed ordinary files.

The [Restore runner](../../executor/restore/media.go) opens a typed Read Session and streams available candidates. Sequential-read Media follows backend storage order; random-read Media uses ordinary path order. [Copy completion](../../executor/restore/stream.go) compares ACP's real hash and size with the expected content and restores the version's own mode/mtime. The output retains read-source and ready facts, but Library association and item/alternative completion wait for successful Session Finalize. Failed final identity checks retain complete bytes without claiming success; retry cannot adopt its own unfinalized output as an unrelated preexisting file. Completion is per item/version, not every version of its File. Metadata restoration may invalidate disposable ACP caches; completed targets are not reread to rebuild caches. Restore does not share Archive's STAGED/submit state machine.

Candidate listing reports missing catalog Media explicitly, including its ID; it never silently removes a frozen candidate from the response. Catalog metadata must be restored before that candidate can be used.

### Restore Outcomes

Indexing designates the latest selected version per File by last archive time, then ID, with unknown times last. This candidate is frozen independently of Media order; failure or Ignore does not promote an older version. [Library publication](../../library/restore_result.go) applies these rules only after complete content, version metadata and Media identity verification:

| Situation | Result | Retention and retry |
| --- | --- | --- |
| Candidate; original File has no FileLocation | Reconnect that File | Preserve its organization, tags and Note |
| Any existing binding, or another selected version | New independent File | Frozen logical parent and version-suffixed name; only this saved version and evidenced archive dates, no copied tags/Note |
| Output matches Location Ignore | Restored, unlinked | Preserve bytes; disclose before copying; do not bypass Ignore |
| Library association fails | Content retained, publication incomplete | Retry verifies retained output and resumes; no new suffix or duplicate File |
| Complete read mismatches expected bytes with salvage enabled | Recovered with damage | Retain actual bytes/facts, no expected-version or original association |
| Interrupted read | Failed attempt | ACP cleans partial target; no bad-block skip/fill or prefix salvage |

Successful business results use `(operation_uuid, item_id)` in Library. File, FileLocation, tracking and the result publish atomically before the Job checkpoint; no physical directory cache is maintained. Retry recognizes a committed result instead of interpreting its own prior association as a new conflict. A recovered FileLocation uses the current binding token and does not claim a Location-wide observation. Imported result history cannot authorize a local retry. A lost logical parent or path owned by another File is an association conflict; existing original bindings, including stale/unavailable ones, are never stolen.

Default Restore candidates are healthy or unchecked and are still validated during transfer. `allow_damaged_copies`, frozen in the specification and off by default, additionally permits damaged/previously unreadable copies after normal candidates; known missing copies are not candidates. It never relaxes identity/path checks. Results distinguish verified, damaged, unlinked and pending items; merely producing a file is not verified success.

## Scan

[ScanJobService](../../entity/job_scan.proto) owns one SCAN kind, [runner](../../executor/scan/runner.go), manifest and [pipeline](../../executor/scan/pipeline.go) for Location/Library selections and Volume/Tape Media. Content acquisition, comparison, Preview, validation and publication are stages, not delegated Jobs. Source adapters supply access, enumeration, order, leases and identity checks. Ordinary browsing and Refresh remain request-bound reads.

1. Freeze source bindings and old content baselines into the Job bundle. Media keeps identity, profile, expected Positions and storage order; Location selections keep confirmed root tokens and bounded selected scopes.
2. Enumerate real entries in ordered, bounded pages. Location directory selections prune Ignore and mandatory exclusions; explicit ordinary files may bypass user Ignore, never authorization. Media inventory requires fresh physical enumeration, including the current trusted LTFS index for Tape; old Positions cannot stand in for it.
3. Acquire content facts under the signature policy below, optionally look up matching Library content, and optionally generate Preview assets in this same pipeline.
4. Recheck observations and directory membership; finalize Media identity before publishing any positive or negative result.
5. Publish the selected result policy through metadata-only Library transactions, then checkpoint the Job independently. Keep detailed results and scopes in paged Job tables.

| Signature policy | Content reads |
| --- | --- |
| KNOWN_ONLY | Reuse applicable observations or ACP's cache-only read; cache misses remain unsigned, with no hash fallback |
| FILL_MISSING | Reuse first, then hash unknown or changed ordinary files through ACP |
| FORCE_READ | Read every selected ordinary file; cached facts cannot substitute |

Valid cached SHA-256/size facts do not replace opaque YATM signatures. Matching prior content retains its existing signature. Reuse requires applicable metadata and binding/native evidence; metadata-preserving changes require FORCE_READ. Cache read failures never silently turn KNOWN_ONLY into hashing. The scan is an observation over an interval, not a filesystem snapshot.

| Result policy | Publication boundary |
| --- | --- |
| REPORT_ONLY | Retain results without admitting Files or replacing inventory; requested derivative assets may be generated |
| PUBLISH_ORIGINALS | Publish only completely successful Location scopes, preserving failed and unobserved scopes |
| PUBLISH_INVENTORY | Automatically replace inventory only after complete trustworthy Media observation and identity validation; no Apply step |
| VERIFY_COPIES | Require real uncached reads against frozen old Position baselines; publish guarded historical check findings, not replacement content |

Inventory publication permits unsigned content, does not manufacture health checks, and preserves applicable prior bad observations. A genuinely empty observed scope can remove original references or inventory Positions, never File organization or saved history. Location failures leave successful scopes published and the Job awaiting index retry. Inventory failures preserve the complete old inventory. Library commits remain authoritative after a failed Job checkpoint; no cross-database transaction or crash-only journal is introduced.

Verification findings distinguish match, mismatch, missing, unreadable and unavailable baseline. A per-file issue permits continued checking; device errors/cancellation retain completed observations and leave the rest unexamined. Final identity failure prevents health publication. Content-token/time guards reject stale updates. Expected hashes are never replaced by damaged bytes. Mounted Volume scans execute automatically; Tape scans wait for an explicit matching device through ReadMedia. Waiting holds no Media lease. Sequential sources do not support Preview or implicit Restore/staging.

Preview is optional and off by default. Its policy selects missing-only generation or forced regeneration of all identified content. KNOWN_ONLY without a usable identity skips generation and counts it. Generator failures are per-item findings, not a rollback of otherwise valid scanning; drift and identity failures still invalidate their scope. Asset addressing and validity belong to [Preview storage](preview.md).

## Analyze

Location analysis is the original-publication configuration of [Scan](#scan), not a separate runner or Job kind.

## Verify

Integrity checking is Scan's VERIFY_COPIES result policy; [copy health](media-io.md#copy-health) remains independent of inventory and accessibility.

[Matching](../../executor/observation/matching.go), shared with live Archive/Preview preparation, runs complete global rounds across successful scopes: path → available signature → native identity → xattr UUID. Old File IDs and new paths are ordered; first unclaimed candidate wins and later rounds never override earlier matches. Matching itself does not trigger hashing. Signature matching uses prior/current original observations, not arbitrary versions. Remaining observations become independent Files. Copies stay independent even with shared UUIDs or content; occupied paths preserve their File despite replaced content or inode, without retaining invalid content facts.

Cross-Location reassociation requires a successfully removed old binding and valid same-Executor tracking evidence. Unavailable Locations and unobserved ranges cannot lose bindings to another analysis. Tracking keys are published with originals; optional UUID writes are create-only and best-effort. Matching is organization continuity, not proof of move versus copy-and-delete.

## Location File Operations

Ordinary mutations use the shared [organization engine](library.md#shared-organization-interface), not Jobs. Library edits, browsing, annotation and manual original relocation also remain request-bound. Initial whole-Location collection uses Scan with KNOWN_ONLY and PUBLISH_ORIGINALS.
