# Persistence and Recovery

## Fact Sources

| Store | Runtime tables | Authoritative data |
| --- | --- | --- |
| Executor database | `jobs`, `job_resources` | Job identity, query projections, primary target/name, captured many-to-many source/destination resources, timestamps, tombstone and revision |
| Library database | `files`, `file_tags`, `file_versions`, `file_version_archives`, `media`, `positions`, `locations`, `file_locations`, `file_tracking_keys`, `library_settings`, `location_migrations`, `restore_results`, `file_operation_results` | Organization, saved content and evidenced save times, copy health, original bindings/tokens, successful operation provenance, preferences and configuration migration |
| Archive Job database | `job`, `config`, `items`, `raw_inputs`, `observations`, `originals`, `selection_directories` | Specification, manifest, bounded live selection/matching, stable File associations, copy results, checkpoints |
| Restore Job database | `job`, `config`, `files`, `copies`, `outputs` | Frozen manifest/destination, per-File reconnect candidate/name, candidate-copy completion and stable per-item output/read/finalization facts |
| Scan Job database | `job`, `config`, `entries`, `scopes`, `observations`, `originals` | Frozen inputs and old baselines, bounded matching, content/check/Preview results, successful scopes and publication checkpoints |

These are three ownership categories. The current HTTP service shares its configured database handle between Executor and Library; Job bundles have separate SQLite databases. New databases use [GORM models](../../library/library.go), not independently maintained SQL schemas.

Request-bound file operations use a temporary SQLite `items` manifest for bounded traversal and outcomes. It is protected from Location access and removed when the request ends; it is neither a retained Job bundle nor another persistent database category.

## Published Data Formats

The Alpha 1 candidate uses the initial published formats below. Software release
numbers and artifact format revisions have separate meanings. Each format family
starts at revision 1; future revisions advance within that family.

| Artifact | Identity | Initial revision |
| --- | --- | --- |
| Shared Library/Executor Catalog | `catalog_metadata` singleton, `format = yatm-catalog` | `revision = 1` |
| Job bundle | `job.json`, `format = yatm-job-bundle` | `format_version = 1` |
| v1 Library metadata backup | JSONL header, `format = yatm-library-backup` | `version = 1` |

[Format checks](../../internal/dataformat/catalog.go) validate identity and revision
before schema changes. Fresh initialization requires an empty database; the lack
of a `jobs` table alone does not make a database empty. The supported offline legacy
migration establishes the same Catalog and bundle identities as fresh creation.
Job bundles share one format definition across runners and the migrator.

Unmarked nonempty catalogs, Draft JSONL (`yatm-library`, revisions 2–5), unmarked
Draft bundles, and unknown format revisions are rejected with their data retained.
Released-family revision 2 will therefore remain distinguishable from Draft
revision 2. Disposable development fixtures require an explicit reset.

legacy whole-object JSON remains a separate supported import. Its compressed protobuf
header, encryption-key prefix and opaque signature bytes retain their original
encoding. LTFS profiles and Volume markers identify physical storage contracts,
not Catalog or backup revisions.

## Job Bundle

Each Job owns `work/jobs/<job_id>/`: `job.json` describes the bundle, `state.db` stores execution state, and `job.log` records events. Tape-specific reports, LTFS logs, and captured indexes live below `tapes/<barcode>/`. A bundle is the retention and cleanup unit; detailed Job state is never a Library blob.

[Bundle creation](../../executor/db.go) finishes before Create returns. [Job records](../../executor/job.go) keep authoritative kind, durable status and priority in the Job database's single `job` row. Executor `catalog_kind` and `catalog_status` are indexed query projections, published with a new revision after a checkpoint; startup reconciles them from bounded Job records, never full manifests. Catalog responses hydrate authoritative values and live phase. Append-only Job resources come from frozen selections or actual operations, not mutable current FileLocation lookups.

## Mutation and Pagination Rules

- Application reads and writes use GORM so hooks, nullable signature handling, and timestamp semantics remain active. Job catalog timestamps and soft deletion use millisecond Unix values; existing physical File/Position times retain their model representations.
- Current Job mutations use the Executor's `withNextJobRevision` allocator and explicit GORM writes/transactions, not Job-model revision hooks. Every mutated row gets a distinct revision; polling orders by `(revision, id)` but exposes a revision-only cursor because those revisions are unique. Initial/older pages use the snapshot revision and Job ID; tombstones remain visible to change polling. High-frequency progress is not a catalog fact.
- Separately queried or indexed values are columns. Typed protobuf blobs hold cohesive specification/results or backend metadata; Scanner/Valuer implementations handle their encoding.
- Location revisions advance through GORM save hooks for configuration, attempted Job and successful publication changes. Live directory cursors bind directory facts, the root binding and query, not a cached directory index. Content and mutation requests bind expected objects. Catalog times use milliseconds; observed file mtimes retain nanoseconds.
- Library preferences use a singleton GORM-revisioned setting, updated with compare-and-swap. Auto collection, unbacked visibility and permanent-delete confirmation default on and remain independent. Queries resolve visibility before filtering/paging, and jobs freeze the resolved scope. Complete File exports include preferences without filtering exported content.
- Large manifests and Positions use ordered, indexed pages or streams. `parent_path` indexes direct physical children. Query aggregates from authoritative rows instead of adding duplicate counters when the query is bounded and indexed.
- File, Tag, duplicate-group and duplicate-member cursors have distinct centrally declared operation kinds as well as query bindings. A cursor from another operation cannot be reused as a directory continuation.
- Keep Library transactions metadata-only and limited to their publication work; perform source reads, ACP, mount operations, and validation before opening them.

Maintenance constraints require new catalog-visible mutation paths to maintain revisions through GORM hooks. A future batched mutation that shares revisions must carry a tie-breaking ID in its cursor; a strict revision-only `>` filter would then skip rows. This does not change the current unique-revision Job API.

## Recovery Boundary

The durable Job states are INDEXING, PENDING, and COMPLETED; failures are events that return the attempt to a retryable state. The runner owns the detailed phase. Normal shutdown allows the active end-to-end operation to finish or clean up; interrupted intermediate work returns to the previous stable checkpoint.

Library and Job commits are separate. A successful Library commit remains authoritative even if the following Job checkpoint fails; there is no distributed transaction, mid-operation attempt journal, or extra SIGKILL/power-loss recovery protocol. Archive initialization resets leftover STAGED rows. Unexpected Volume files can be examined through explicit Scan.

Restore provenance is a successful business result, not a general crash journal. Its unique operation/item key is committed with the resulting organization/original and read on retry before making new associations. Verify health is guarded by frozen Position facts and check time before its per-Job publication checkpoint. Detailed errors and manifests stay in the Job bundle.

File operation receipts publish atomically with successful association changes and retain copy provenance, not resumable execution state. Their request's temporary manifest is not retained for retry. Historical receipts survive Location unregistration. Import clears executable binding tokens; historical numeric Location IDs neither grant access nor imply a current association. [Physical-operation outcomes](library.md#physical-file-operations) define partial failure and cleanup; no offline directory cache or general filesystem rollback protocol is maintained.

[Recovery](../../executor/recovery.go) removes only literally empty incomplete Job directories. A missing bundle preserves its catalog entry and reports an error. Missing metadata/state files, invalid manifests/configuration, symlinks and nonempty orphan bundles are reported and preserved for inspection, not recursively deleted as unfinished creations. [Catalog updates](../../executor/job.go) and [kind-specific runners](jobs.md) define the implemented checkpoints. [Offline migration](../operations/migration.md) owns temporary staging/backup tables; those tables are not part of the runtime schema.
