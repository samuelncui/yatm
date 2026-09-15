# YATM Platform Design

Status: Implemented — historical development snapshot, archived 2026-09-06. This is not a release announcement or the current architecture contract. Follow the [current architecture](../architecture/overview.md) and [documentation convention](../README.md) for ongoing changes.

## Principles

- Tape and mounted offline disks are both `Media`.
- Archive and Restore own the ACP copy flow. A Media Backend prepares a concrete Session, resolves physical paths, and finalizes backend-specific state.
- One Job attempt exclusively leases each Tape device or Volume UUID that it uses. Capabilities control concurrency only within that attempt.
- The Executor database is a Job catalog. Each Job stores its durable state and manifest in its own `state.db`.
- The Library database owns logical Files, Media identities, and physical Positions.
- Large manifests, captured indexes, and Scan Diffs use bounded SQL cursors or streams.
- YATM manages copied files, not physical directories. Media deletion never deletes files from Tape or Volume.
- Preview remains independent of Media, capabilities, and device leases.

The legacy baseline is `origin/main@89685cc` (`v0.1.21`). It upgrades through explicit offline migration.

## Storage

The platform has three durable stores:

| Store | Data |
| --- | --- |
| Executor DB | Job ID, Executor ID, catalog timestamps, revision |
| Job DB | Job kind, priority, durable status, typed specification, bounded manifest or Diff |
| Library DB | File, Media, Position |

Each Job owns one Bundle:

```text
work/jobs/<job_id>/
  job.json
  state.db
  job.log
  tapes/<barcode>/
    yatm-report.json
    ltfs.log
    <barcode>.schema
```

The Tape subdirectory holds LTFS artifacts only. Volume data remains on the mounted Volume.

## Database Design

The following list is the complete runtime schema. Scan state is not stored in the Library, and Media capabilities are derived from the typed profile rather than duplicated as columns.

| Database | Tables |
| --- | --- |
| Executor catalog | `jobs` |
| Library | `files`, `file_tags`, `media`, `positions` |
| Archive Job DB | `job`, `config`, `items` |
| Restore Job DB | `job`, `config`, `copies` |
| Preview Job DB | `job`, `config`, `items` |
| Scan Job DB | `job`, `config`, `entries` |

Offline migration additionally uses temporary `jobs_staging`, `files_staging`, `media_staging`, and `positions_staging` tables. During the explicit Commit step, the legacy tables are retained as `jobs_legacy`, `files_legacy`, `tapes_legacy`, and `positions_legacy` until the explicit Cleanup step removes them. None are part of the running service schema.

### Executor catalog

`jobs` contains catalog and polling data only:

```sql
CREATE TABLE jobs (
  id          BIGINT PRIMARY KEY,
  executor_id VARCHAR(128) NOT NULL,
  created_at  BIGINT NOT NULL,
  updated_at  BIGINT NOT NULL,
  deleted_at  BIGINT NOT NULL DEFAULT 0,
  revision    BIGINT NOT NULL
);
```

Job execution state lives in the Job Bundle, not this table.

### Library

`media` generalizes the legacy `tapes` table:

| Column | Meaning |
| --- | --- |
| `id` | Library Media ID |
| `kind` | Tape or Volume |
| `identity` | Tape barcode or Volume UUID |
| `name` | User-visible name |
| `profile` | Typed protobuf oneof with immutable backend attributes |
| `create_time`, `destroy_time` | Lifecycle timestamps |
| `capacity_bytes`, `written_bytes` | Capacity statistics |

`(kind, identity)` is unique. Tape profiles store serial number, encryption key reference, and LTFS format. Volume profiles store serial number and HDD or HM-SMR type.

`positions` stores physical files and derived directory rows:

| Column group | Meaning |
| --- | --- |
| `file_id` | Bound logical File, or zero until Scan binds it |
| `media_id`, `path` | Unique physical location |
| `parent_path`, `is_dir` | Immediate-child browsing index and derived directory marker |
| `mode`, `mod_time`, `write_time`, `size`, `hash` | Physical file facts |
| `storage_order`, `storage_metadata` | Sequential-read order and backend metadata |

`(media_id, path)` is unique. `(media_id, parent_path, path)` pages immediate children directly. Only sequential-read Media records storage order; Tape gets it from the captured LTFS index. Derived directory Positions support browsing but are never Archive, cleanup, or deletion units.

`files` continues to represent the logical Library tree and stores a bounded user note. `file_tags` stores the many-to-many projection with primary key `(file_id, tag)` and lookup index `(tag, file_id)`. Tags are normalized names derived from referenced rows; there is no independent Tag table or lifecycle. Media deletion removes only the selected `media` and `positions` rows. It does not modify `files`, annotations, a Volume marker, or any physical file.

File and directory annotations follow the logical File ID across rename, move, and Trash. Directory merges union tags and retain both distinct notes. Complex Library search is parsed by the pinned querystring parser, compiled to bound GORM expressions, and paged by an opaque query-bound cursor. Result paths are resolved from bounded ancestor reads.

### Common Job DB

Every Job DB contains one `job` row and one typed `config` row:

```sql
CREATE TABLE job (
  id       INTEGER PRIMARY KEY CHECK (id = 1),
  kind     INTEGER NOT NULL,
  status   INTEGER NOT NULL,
  priority INTEGER NOT NULL
);
```

Durable Job status is deliberately small:

```text
INDEXING --manifest or Diff complete--> PENDING --work complete--> COMPLETED
```

Archive and Restore phases use generic Media names: `WAITING_FOR_MEDIA`, `PREPARING_MEDIA`, `COPYING_TO_MEDIA`, `COPYING_FROM_MEDIA`, and `FINALIZING_MEDIA`. Scan uses `WAITING_FOR_SCAN_APPLY` and `APPLYING_SCAN`.

### Archive Job DB

`items` is the Archive manifest:

| Column | Meaning |
| --- | --- |
| `id`, `status`, `size` | Cursor, PENDING/STAGED/SUBMITTED state, expected size |
| `target_path` | Stable logical target suffix used for ordering and retry |
| `media_path` | Actual Media path, written only after one file copies successfully |
| `media_id` | Written only after the Library commit succeeds |
| `data` | Source manifest protobuf |
| `result` | ACP hash and file facts; Tape Finalize also attaches LTFS position data |

`STAGED` means the file copied successfully in the current operation and is eligible for backend validation and Library commit. It is not a separate physical batch or recovery journal.

### Restore Job DB

`copies` contains one row per candidate physical copy:

| Column | Meaning |
| --- | --- |
| `file_id`, `status` | Logical file and PENDING/COMPLETED state |
| `size`, `hash`, `target_path` | Restore validation and destination |
| `media_id`, `media_path` | Physical source |
| `storage_order` | Sequential-read order when required |

Selection, streaming, completion, and pagination need no cross-database joins.

### Scan Job DB

`config` stores `media_id` and `force_rehash`. `entries` stores only the Diff:

| Column | Meaning |
| --- | --- |
| `path` | Primary key and SQL cursor |
| `change` | Added, Changed, or Removed |
| `size`, `mode`, `mtime_ns`, `sha256` | Facts required by Apply |

Counts and bytes are aggregated from `entries`; there is no duplicate summary row. Unchanged files and directories are not stored.

## Media Backend

`Backend` is a factory with `NewReadSession` and `NewWriteSession`. It receives the Job `*gorm.DB` and typed protobuf Media target directly. The runner remains responsible for paging items, running ACP, persisting individual results, and committing the Library.

Each Session exposes:

- `Capabilities()` for ACP scheduling.
- `Inspect()` as a zero-I/O Media snapshot getter.
- `SourcePath()` or `TargetPath()` for safe physical paths.
- `Finalize()` for backend-specific unmounting, marker validation, captured-index reconciliation, and target-full normalization.

Write Finalize is called exactly once after ACP returns, including cancellation and errors, with a cleanup context that does not inherit the expired attempt deadline. Session has no `Close`, rollback policy, public resource key, or checkpoint result.

Capabilities use one access scale for each direction:

| Media | Read | Write |
| --- | --- | --- |
| HDD | Concurrent random | Concurrent random |
| HM-SMR | Concurrent random | Sequential |
| Tape | Sequential | Sequential |

Concurrent random access uses configured ACP concurrency, random access uses one device thread, and sequential access enables ACP linear-device behavior. Only concurrent-random read is directly online-readable by consumers.

## Archive

One Media operation follows this order:

1. Create a typed Write Session and acquire its attempt-scoped lease.
2. Page PENDING items in `target_path` order. The Session maps each path to the mounted Tape or Volume.
3. ACP copies and hashes each file. Only a successful single-file result writes `media_path` and changes the item to STAGED.
4. Always call Session Finalize after ACP returns.
5. Stream remaining STAGED items into one Library transaction through `CommitMedia`.
6. Update those items to SUBMITTED with the committed `media_id`. Complete the Job when no PENDING item remains.
7. Return the joined copy, Finalize, and commit errors after committing any verified successful files.

Tape FORMAT rejects an identity already present in the Library; the operator must explicitly delete its metadata first. APPEND requires an existing `ltfs_v1` Tape. APPEND and Volume use the same short base36 path prefix. It is only a collision-resistant file-path namespace: the Session does not pre-create it, and it has no database row.

Tape Finalize always unmounts and streams the captured LTFS index. Normal completion keeps every exact path-and-size match. Target-full completion keeps only the continuous verified prefix. Invalid or missing captured-index data resets affected STAGED items to PENDING.

Volume Finalize never scans the physical tree. It reopens and compares `.yatm.json`, prefixes successful paths, and leaves successful STAGED files for the common Library commit. Ordinary copy errors and ENOSPC do not discard other successful files. ACP attempts to remove only the explicitly failed target file; YATM performs no directory-level cleanup.

Volume reserves `max(1 GiB, filesystem capacity * 1%)`. Target selection emits only the deterministic prefix that fits `statfs.available - reserve`. If the first pending file cannot fit, the operation returns an explicit capacity error. Remaining items wait for another Media. Backend code normalizes physical ENOSPC to `ErrTargetNoSpace`.

## Restore

Restore pages candidate positions and asks the Read Session for each physical source path. Sequential-read Media is queried in storage order; random-read Media uses path order. Session capabilities configure ACP concurrency.

Tape Session identifies, decrypts, mounts, and finally unmounts the cartridge. Volume Session discovers the already mounted UUID and validates the immutable marker both before and after the copy. Each successfully hashed destination immediately marks every candidate for that logical file COMPLETED.

## Volume

YATM manages filesystems that the operating system has already mounted. It does not mount, unmount, or safely eject a Volume.

Volume Initialize atomically creates `.yatm.json` at the root with version, UUID, creation time, and immutable typed profile, then creates the Library Media row. Volume Register reads an existing marker and recreates only a deleted Library Media row; it never changes the marker or files. UUID is the authoritative identity. If the Initialize Library insert fails, only the marker created by that request is removed.

Initialize and Register accept only a configured discovery root or its direct child. Archive, Restore, online reads, and Scan reject duplicate mounted UUIDs, profile conflicts, symlink path components, and paths outside the Media root. Media list and Inspect attach non-persistent mount and filesystem-available-byte state.

A mounted concurrent-random-readable Volume can serve a published Position through HTTP GET or HEAD, including byte ranges and download disposition, without creating a Restore Job. Each request revalidates the marker, profile, physical path, and stored file facts.

## Scan

`ScanJob` is generic at the Job and API level. The current execution backend accepts only an already mounted Volume.

Scan is an explicit `Diff -> Apply` flow:

1. Job indexing leases the Volume UUID and merges an ordered physical-file stream with ordered Library Positions.
2. The default compares path, type, size, mode, and mtime, then hashes only Added or metadata-changed files; a valid ACP signature xattr may satisfy the hash.
3. `force_rehash` rereads every regular file and refreshes its signature cache.
4. `.yatm.json`, symlinks, special files, and empty directories are excluded.
5. A complete Diff moves the Job to PENDING and `WAITING_FOR_SCAN_APPLY`.
6. Apply revalidates the marker and metadata before opening the Library transaction. A valid conflicting ACP cache entry reports drift; forced Apply rereads every Added or Changed file. Drift returns the Job to INDEXING for a fresh Diff.
7. The Library transaction applies entries by path cursor, rebuilds derived directory Positions, recalculates `written_bytes`, and binds content signatures or imports new logical Files under `Unforged/<volume-name>/...`.

Apply never modifies the Volume filesystem. Files left by an interrupted Archive can therefore be discovered and explicitly imported. Deleting the Scan Job removes its Job Bundle and Diff only.

## API and UI

- Archive and Restore accept Tape/Volume oneof targets.
- Media list, inspect, delete, and paginated positions APIs are shared across backends. Media list also pages results.
- Volume Initialize creates a new marker; Volume Register reuses an existing marker after metadata-only deletion.
- Online-readable mounted Volume Positions expose range-capable GET and HEAD content endpoints.
- Scan Job service exposes create, progress, paginated entries, and apply.
- The frontend provides a paginated Media browser, shared Inspect flow, mounted-state controls, Volume Initialize/Register, Tape/Volume Archive and Restore, online Open/Download, cached/forced Scan, Diff review, and confirmed Apply.
- Preview hashes one Job through one targetless ACP stream and offers explicit force rehash.

## Migration and Failure Boundary

The offline migration converts legacy Tape rows to Media profiles, `tape_id` to `media_id`, and Archive/Restore paths to their generic fields. It streams data through staging tables and swaps them after validation and explicit confirmation. Legacy Job logs are copied into Job bundles. A submitted legacy Archive item remains submitted only when `(path, size)` identifies one physical Position among the Media IDs recovered from its Job log; absent or ambiguous items return to pending. Library exports use JSON Lines with embedded annotations. The [persistence contract](../architecture/persistence.md#published-data-formats) owns the supported format identity and legacy whole-object reader; prototype export numbering is not a release sequence.

One Archive, Restore, Scan Diff, or Scan Apply attempt is the normal recovery boundary. Normal shutdown lets it finish or run cleanup. An interrupted process returns to the previous durable checkpoint on restart; this version does not add an attempt journal or automatic repair for a crash between separate Library and Job database commits. Scan is the explicit mechanism for examining unexpected Volume files.
