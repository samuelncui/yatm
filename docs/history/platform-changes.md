# YATM Platform Change Summary

Status: Historical development summary, archived 2026-09-06; not a release announcement. This summarizes development changes from `origin/main@89685cc` (`v0.1.21`). See the [current architecture](../architecture/overview.md) for maintained behavior and the [v1 snapshot](platform-design.md) for historical context.

## Media

- Tape and mounted offline disks use one `Media` Library model and API.
- `Position.tape_id` is now `media_id`; Archive and Restore manifests use `media_path` and `media_id`.
- Typed Tape and Volume profiles hold immutable backend facts. Runtime read/write capabilities are derived from those profiles.
- HDD supports concurrent random read/write, HM-SMR supports concurrent random read and sequential write, and Tape supports sequential read/write.
- Media deletion removes Library metadata only. It never changes physical Tape or Volume files.

## Backend and Execution

- A narrow Backend factory creates typed Tape or Volume Read/Write Sessions.
- Archive and Restore runners still own paging, ACP, per-file results, progress, and Library commits.
- Sessions own physical preparation, safe path resolution, capabilities, marker or captured-index validation, and finalization.
- Tape devices and Volume UUIDs are leased for the complete Job attempt.
- Job phases use generic Media terminology.
- Preview remains independent of Media and device leases.

## Archive

- Archive records `media_path` only after one file copies successfully.
- Tape FORMAT rejects an existing Library identity; explicit Media deletion is required before reformatting.
- Tape APPEND and Volume Archive use the same short base36 path prefix without a batch entity or pre-created directory.
- Tape Finalize always reconciles the captured LTFS index. It records storage order and retains only verified files, or the continuous verified prefix on ENOSPC.
- Volume Finalize validates `.yatm.json` and never scans or removes the physical directory tree.
- Successful files are committed even when another file fails. ACP attempts to remove only the explicitly failed target file.
- Volume capacity uses `statfs.available - max(1 GiB, capacity * 1%)`; a deterministic fitting prefix is sent to ACP.

## Restore

- Restore accepts Tape or Volume targets through one Media target.
- Tape reads follow captured storage order and remain sequential.
- Volume reads use the mounted filesystem directly and validate the marker before and after copying.
- A verified restored file immediately completes all physical candidates for that logical file.

## Volume and Scan

- Volume Initialize atomically creates `.yatm.json` with UUID, creation time, and immutable profile, then registers the Media in the Library.
- Volume Register preserves an existing marker and files while recreating only deleted Library Media metadata.
- YATM discovers only already mounted Volumes under configured roots; it does not mount or eject them.
- Media responses expose live mounted and available-byte state without persisting it. Mounted concurrent-random-readable Positions support direct GET, HEAD, Range, Open, and Download.
- `ScanJob` provides explicit `Diff -> Apply`. Its current backend supports mounted Volume Media.
- Scan hashes only added or metadata-changed files by default and may reuse a valid ACP signature cache. Force rehash reads every regular file.
- Scan excludes the marker, symlinks, special files, and empty directories.
- Diff entries live only in the Scan Job DB and are paged by path. Apply normally validates metadata and any present ACP cache; Force rehash reads every Added/Changed file before updating Positions and logical Files in one Library transaction.
- Scan Apply and Media Delete never modify the Volume filesystem.

## Database

- Library runtime tables are exactly `files`, `file_tags`, `media`, and `positions`. `files.note` and `file_tags` provide logical File annotations without a separate Tag catalog.
- Archive Job DB uses `job`, `config`, and `items`.
- Restore Job DB uses `job`, `config`, and `copies`.
- Preview Job DB remains `job`, `config`, and `items`.
- Scan Job DB uses `job`, `config`, and `entries`.
- The Executor catalog remains `jobs`; `SCAN` is a new Job kind.
- Offline migration stages `jobs_staging`, `files_staging`, `media_staging`, and `positions_staging`; Commit retains `jobs_legacy`, `files_legacy`, `tapes_legacy`, and `positions_legacy` until explicit Cleanup.

## API and Frontend

- Public Media APIs replace Tape-only list, inspect, delete, and position APIs. Media and Position browsing is paginated.
- Archive and Restore services expose Tape/Volume oneof targets.
- Scan Job service exposes create, progress, paginated Diff entries, and apply.
- The frontend provides a unified Media browser, shared Inspect flow, Volume initialization/registration and mounted state, Tape/Volume Archive and Restore, online Open/Download, cached/forced Scan, Diff review, and confirmed Apply. Library Files support multi-value tags, notes, boolean search in either pane, and cross-pane result location.
- Generated Go and TypeScript bindings come from protobuf sources.

## Migration

- The frozen legacy wire schema remains under `migrate/legacy/pb/`.
- Offline prepare converts legacy Tape data into typed Media profiles and streams Jobs, Files, Media, and Positions into staging tables.
- Captured LTFS indexes provide order and storage metadata for migrated Tape Positions.
- Legacy Job logs are copied into the corresponding Job bundle. Submitted Archive items retain their status only when path and size identify one physical Position among the Media IDs recovered from that log.
- Commit and cleanup remain explicit confirmed operations. Library export uses JSON Lines with embedded File annotations; the [persistence contract](../architecture/persistence.md#published-data-formats) owns supported format identities and legacy import.
