# Online Originals, File Versions, and Archive Copies

Status: Superseded design record; not the current workflow or a released compatibility baseline.

The maintained content model and coverage-to-version behavior belong to [Library](../architecture/library.md#archive-inventory). Live browsing, shared organization and the Scan pipeline are maintained in [API/UI](../architecture/api-ui.md) and [Jobs](../architecture/jobs.md). Current development acceptance is recorded in [Shared Files and Scan](files-and-scan.md).

## Product Contract

The Library provides one searchable, independently organized File per ongoing item. Editing and synchronizing an original preserves that item according to the matching policy below; it does not create a new item merely because content changed. Tags and notes do not require a backup.

```text
File -> zero or one FileLocation -> Location
File -> zero or more FileVersions
File -> zero or more FileTags
FileVersion -- exact signature lookup --> Positions -> Media
```

Location is an original-directory registration in an Executor's access namespace. Its published metadata belongs to the Library; access and execution belong to the Executor. Media retains archive-storage semantics, including permanently mounted archival Volumes.

The Library tree is logical organization. The Location tree is a read-only physical index. Initial names may mirror the originals under Unforged, but subsequent logical and physical moves are independent. Daily Archive selects the Library tree and freezes Library-relative target paths.

The first release uses manual synchronization and relatively stable ordinary files. It does not add watchers, schedules, retained edit snapshots, directory backup points, automatic restore, original rename/move/delete, mounting, device discovery, cross-Executor transport, or WebDAV. Exclude active databases, logs and downloads rather than extending ACP's consistency contract.

The [interface design](online-source-interface.md) owns navigation, Ignore syntax, and user-facing status language. Current implemented behavior remains in [Library](../architecture/library.md), [Jobs](../architecture/jobs.md), and [Media/I/O](../architecture/media-io.md) until integration is verified.

## Persistence Model

Keep existing integer catalog IDs. New catalog times use millisecond Unix values; physical modification times retain nanoseconds. All application mutations use GORM. Library transactions contain metadata operations only; large attempt manifests remain in per-Job databases.

| Record | Authoritative fields | Constraints and indexes |
| --- | --- | --- |
| File | id, parent_id, name, kind, note, created_at, updated_at | Primary id; unique (parent_id, name). No authoritative signature, hash, size, inode or physical path. |
| FileTag | file_id, tag | Primary (file_id, tag); lookup (tag, file_id). |
| Location | id, name, executor_id, canonical root_path, typed Ignore format/text, write_tracking_uuid, binding_state, revision, created_at, updated_at, last_sync_at, last_sync_job_id, last_job_id | Unique (executor_id, root_path); GORM-maintained revisions. |
| FileLocation | file_id, location_id, path, parent_path, size, mode, mtime_ns, nullable signature/hash | Primary file_id; unique (location_id, path); browse (location_id, parent_path, path); nonunique signature. |
| FileVersion | id, file_id, signature, hash, size, original mode/mtime_ns, first_archived_at, last_archived_at | Unique (file_id, signature); nonunique (signature, id); ordered File history. |
| Position | id, media_id, actual path, parent_path, is_dir, signature, hash, size, physical mode/times, storage_order, typed storage_metadata | Unique (media_id, path); browse (media_id, parent_path, path); nonunique (signature, id). No file_id or file_version_id. |
| Media | Existing kind, identity, name, immutable profile, lifecycle and capacity facts | Existing Tape/Volume contract. |

FileLocation describes ordinary files only. Derived directory rows live in a separate physical-browsing index, have no File identity, and do not represent empty directories. parent_path is derived from path for indexed immediate-child queries, not a second parent identity.

Signature stays opaque bytes in VARBINARY(256); unset values are NULL. No global Content entity, BackupRecord entity, version-to-copy join table, global signature uniqueness, encoding validator or signature repair is added. Current producers and consumers retain their local integrity requirements.

### File Versions and Content Coverage

FileVersion represents a File's content known to have been archived, not a backup operation performed for that File. When a published original's known signature matches eligible archived Positions, that content appears in the File's Saved versions without another physical backup. The association persists so changing or unlinking the original does not erase the saved content history. A search or UI render remains read-only, not a version-creation side effect.

Unarchived edits and synchronization alone do not create versions. Verified Archive publication and explicit inventory admission still establish saved content. Repeated content reuses the unique File/signature version; several matching Positions are copies of that one version. Reusing existing coverage is not a new archive event and does not advance archive times. Content, size, hash and the File's own observed restore metadata remain immutable.

C1 -> C2 -> C1 reuses C1. This is a collection of saved contents, not a complete event history. Jobs retain operation history. Use only evidenced archive times; discovering a match, importing or migrating metadata is not an invented backup time. Unknown times remain unknown.

FileLocation may have no signature. Detectable changes without a fresh hash invalidate old content facts. Unknown content is not a confident negative for archive coverage.

Exact nonempty signature equality queries Positions without owning them. One Position may serve versions of several independently organized Files, and must not be counted repeatedly in aggregates. Associate only content observed for this File, not another File's entire history, annotations or original metadata. Consumers reject contradictory copy hash/size instead of silently substituting them.

Reconcile both orders: an original appears after its archive copy, or a copy becomes known after the original. [Library](../architecture/library.md#archive-inventory) owns the bounded, metadata-only publication and startup/import reconciliation paths. No new physical backup or global File merge is required to establish the association.

Removing a Position changes coverage, not File history. Retaining a FileVersion does not retain its bytes. Restore uses the selected version's content and original metadata, and the Job's frozen logical target; another File's copy is not its historical name or metadata.

### Private Tracking Keys

```text
file_tracking_keys(
  file_id, kind, scope, key_value, details, observed_at
)
PRIMARY KEY (file_id, kind)
INDEX (kind, scope, key_value) -- nonunique candidate lookup
```

Supported kinds initially represent native-object identity and a YATM Tracking UUID. details is a typed payload for mechanism-specific guards, not arbitrary EAV configuration. Keep one latest confirmed key per mechanism. Keys may remain after a confirmed missing original to support later rediscovery, but unregistration and invalid binding scopes retire them.

UUID is a lookup value, not File.id. Copied UUIDs may have several File candidates. Native IDs require a trusted Executor/filesystem scope and available generation/birth guards; a bare inode is insufficient. Rebinding and imports invalidate installation-local native evidence. Path binding cannot reliably detect an unobserved replacement device.

Reading available tracking evidence does not authorize repairing it. Location.write_tracking_uuid defaults to false. When enabled, a missing attribute may receive a random UUID; existing values are never automatically overwritten. Unsupported attributes and write failures degrade tracking, not file-content safety.

Publish keys and FileLocations together. Large raw observations and match reasons stay in the Job bundle. Normal UI does not expose tracking_method, scope tokens, weights, or a matcher configuration framework.

## Location Lifecycle and Synchronization

Registration validates an Executor-accessible ordinary directory within allowed roots and enters Needs sync without confirmation. Only imported registrations require local path review and confirmation. Duplicate canonical roots conflict; nested registrations are allowed with a duplicate-indexing warning.

Root/Ignore changes preserve the old browsing index but suspend its content-access eligibility until successful synchronization. Name changes do not reorganize Files. Accessibility is a timestamped runtime observation, independent of binding validity. Sync, configuration and unregistration share a per-Location operation gate; conflicts return Busy. Ordinary reads do not reserve a disk.

### Attempt Pipeline

1. Reserve the Location and freeze its configuration, binding scope and revision in the Job bundle.
2. Traverse ordinary files in bounded batches, pruning Ignore rules before descent and ordering observations through database indexes.
3. Collect available content/native/UUID facts and reconcile old Files against the complete observation.
4. Validate before/after read facts and recheck directory membership/metadata before publication.
5. Atomically publish File associations, online facts, covered FileVersions, tracking keys, derived directories, Location revision and successful-sync summary, following the coverage rule above.
6. Checkpoint the Job and optionally create an independent Preview Job from the committed manifest.

Default synchronization hashes new or metadata-changed files with ACP. A metadata-only option allows unsigned originals. force_rehash reads every managed ordinary file. A usable cached hash must describe current facts; matching itself never triggers additional full-file reads. Missing/unwritable caches do not prevent a correct attempt.

Unreadable roots/subdirectories, detected drift, cancellation or publication errors preserve the complete previous index. A successfully observed empty root removes online bindings without deleting Files. Symlinks and special files are unmanaged; a former regular file changing to an unmanaged type disappears from the published original index.

Sync uses INDEXING -> COMPLETED and runner phases, not a durable FAILED status or manual Apply. RetryIndex performs a complete new attempt against current confirmed configuration. Library commit is authoritative if the later Job checkpoint fails. Do not add a cross-database transaction, crash-only journal or snapshot claim.

### Ordered File Matching

The code contains a small ordered list:

```text
path -> signature -> native object -> xattr UUID
```

Run global rounds in that order, from old Files toward current observations. Order old Files by file_id and candidates by normalized (location_id, path). Select the first eligible unoccupied candidate; consume both sides after a match. Multiple candidates are not automatically errors. Later mechanisms neither veto nor override an earlier match. Actual I/O/database failures are not misses.

Signature matching uses the prior original observation and a current actual/valid-cached signature. It does not search arbitrary historical FileVersions to inherit an archived-only File's organization. Only create new Files after every round, so newly inserted same-content entries cannot cause global deduplication.

| Observation | Result |
| --- | --- |
| Same path, changed content/inode/UUID | Retain File and organization; replace current observation. |
| Old path absent, matching content/object/UUID elsewhere | Try remaining mechanisms in order. |
| Original remains and an independent copy appears | Preserve the original; create an independent File for the unclaimed copy. |
| Objects exchange occupied paths | Files follow their paths. |
| Several eligible candidates | Deterministic first unoccupied candidate wins. |
| Unmatched new original | Create under Unforged/location-name with new-node-only collision suffixes. |

Never steal a binding because another root is inaccessible. Cross-Location reassociation requires its old binding to have been successfully confirmed absent and still-valid tracking evidence; no cross-Executor reassociation is implemented. Matching is a continuity convention, not proof of move versus copy-and-delete.

## Content Workflows

### Archive

Library selection expands directories, removes duplicate selection and freezes File ID, opaque signature, expected hash/size and Library-relative target path. If a reliable signature is missing, Archive indexing obtains expected content with ACP. Later logical edits cannot change the manifest.

Use the File's single eligible original. Missing/unavailable content is an explicit error, never an omitted item, another independently organized File, or automatic Restore. Retry may follow a relocated original only when frozen content expectations still hold.

Actual ACP transfer hash/size must match before STAGED. Keep existing Media identity, sessions, exactly-once Finalize, backend validation, target-full publication and SUBMITTED checkpoints. Publish eligible Positions and create/reuse FileVersions in the same Library transaction. Preserve selected opaque signatures rather than regenerating identity during binding.

Raw-path Archive remains a separately labeled input mode, mutually exclusive with File selection. Inputs receive stable per-Job File associations before writing; retries reuse them. A version requires evidenced archived content, either existing coverage under the rule above or verified publication of a new copy. Cancellation does not automatically delete created metadata. Different Jobs do not merge Files merely by signature.

Volume Scan remains explicit Diff -> Apply and updates physical inventory. Unknown archive entries can be explicitly added to Library as reconstructed archived Files/versions. Unknown original archive times remain unknown rather than becoming a new backup event.

### Open and Download

Bind requests to File, Location and publication revision. Validate binding, Ignore, path boundary, symlink components and recorded facts; stale entry/content returns conflict and asks for synchronization. Serve GET/HEAD/Range from the same opened descriptor without another full hash or Restore Job. Unsafe active formats are attachments, not same-origin active pages.

Unsigned originals can be read within those boundaries, but this is neither verified-content coverage nor snapshot-consistent streaming. Trusted, relatively stable sources remain the safety boundary.

### Restore and Preview

Restore selects FileVersions and queries matching Positions in bounded pages. Freeze selected content, original restore metadata and Library-relative output paths. Complete candidates by restore item/version, not File ID; multiple versions of one File cannot silently overwrite one output. Report missing copies and output collisions before execution.

Sync Preview is disabled by default. Enabled Preview uses the committed, exclusion-respecting, content-deduplicated manifest without rescanning raw directories. Obtain/validate content facts when needed. Current-original and selected-version assets are distinct views; unknown/changed current content must not display an old preview as current. Preview errors do not undo Sync and do not automatically read offline Media.

### Metadata Cleanup

Disappearance, Location removal and Job deletion do not delete original bytes, Files, versions, archive copies or Preview assets. Unregistration removes online bindings and deactivates its tracking associations.

Explicit Library cleanup rechecks online bindings and signature-matched archived copies in the deletion transaction. Inaccessible but indexed originals still protect Files. Removing historical metadata requires explicit confirmation; signature equality never establishes physical deletion ownership.

## Interfaces and Compatibility

Rename the registered-root API to LocationService. Add paginated original, FileVersion, coverage and duplicate-content queries. Keep common Job lifecycle in JobService. Extend typed Archive manifests and Restore selection; modify protobuf first and regenerate Go/TypeScript/CLI. Remove superseded v1 fields and services rather than retaining compatibility adapters. Restore explicitly selects a FileVersion.

The [temporary v1 Draft policy](../README.md#temporary-v1-draft-compatibility-policy) requires legacy compatibility only. Older v1 schemas, API clients, Job bundles and export revisions are not compatibility targets. Incompatible Draft installations must be rejected rather than silently reset. legacy migration retains its source-preserving offline validation boundary.

### Schema Upgrade and Backup

legacy migration preserves File IDs, logical organization and annotations, creates versions from confirmed archive facts and assigns copy signatures without changing physical content. Do not invent archive history for undecidable orphan records. Validate on copied databases; never modify the original legacy installation during validation. v1-to-v1 migration and splitting old Draft online associations are not required.

Library JSONL V5 contains the new coherent entity groups and reads legacy backups; v1-produced v1-V4 formats need no compatibility adapter. Export uses a consistent metadata view; imports validate complete references and roll back all batches on error. Imported roots require local confirmation and sync; installation-local native evidence is not directly trusted. legacy File replacement clears stale original/tracking references to prevent numeric-ID reuse. Import excludes relevant active operations and never imports original bytes.

## Implementation and Acceptance

Implementation order:

1. Accepted Draft and terminology, persistence models, legacy migration/import, V5 backup and cleanup protection.
2. Location configuration/Ignore, tracking evidence, bounded matching and atomic Sync.
3. Content access, both Archive inputs, version-aware Restore and Preview.
4. Generated public clients, UI, CLI and explicit-reset Demo.
5. Regression/E2E verification, then current architecture/runbooks and Implemented design archival.

Required semantic acceptance:

- Repeated edits/scans of unarchived content preserve one File and annotations without versions; first/repeat/new/reverted-content archives have correct version counts and times.
- An original such as handbook.md with one matching archived copy exposes one durable saved version without copying again. Additional identical copies increase the copy count, not the version count; subsequent original changes retain that saved version.
- Original-before-copy and copy-before-original publication converge on the same version, including import and existing catalog entries. Unsigned or contradictory content does not create a speculative association; discovering coverage does not fabricate or advance archive dates.
- Equal signatures across Files remain independent; shared Positions provide each observed content state with a saved version without inheriting other content history, annotations or restore metadata, or double counting copies.
- Path priority, cached/no hash, native/UUID fallbacks, ordered multiple candidates, replacement, swaps, copied UUIDs, and missing versus unavailable originals.
- Registration/rebinding/import, full Ignore semantics, root boundaries, dot entries, empty files, symlinks/type changes, cancellation, permission errors and scan drift.
- Bounded multipage/deep traversal, stable cursors, concurrent configuration conflicts, publication failure and post-commit Job retry.
- Stale content URLs, HEAD/Range, safe response types, Archive changes before/during transfer and immutable opaque identity.
- Multiple-version Restore, version-owned metadata, missing copies, Preview current/history separation and independent failures.
- V5 round trips, legacy migration/import, explicit rejection of incompatible Draft formats, cross-batch rollback, arbitrary opaque bytes and ID reuse.
- Demo includes unsigned, online-only, archived, changed-unarchived, unavailable, imported-unconfirmed and retryable states.

Run affected Go unit/vet/race checks, both SQLite drivers, generation/CLI coverage, frontend checks, explicit-reset Demo, isolated Volume/Preview E2E and isolated Linux LTFS file-backend regression. Physical Tape is excluded. Preserve unrelated work, remove disposable React drafts before a later authorized commit, check documentation links and git diff --check, and do not automatically commit, push or release.
