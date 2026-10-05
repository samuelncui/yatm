# Persistence and Recovery

## Fact Sources

| Store | Runtime tables | Authoritative data |
| --- | --- | --- |
| Executor catalog | `jobs`, `job_properties` | Job identity, state/name projections, Location/Media search values, timestamps and revisions |
| Library catalog | `catalog_metadata`, `files`, `file_tags`, `file_versions`, `file_version_archives`, `media`, `positions`, `locations`, `file_locations`, `file_tracking_keys` | Format identity, organization, saved content/dates, copy health, originals and tracking |
| Settings store | `settings` | Independently stored Library, Preview and Job preference groups |
| Archive Job database | `job`, `config`, `items` | Specification, frozen inputs, copy results and submission checkpoints |
| Restore Job database | `job`, `config`, `files`, `copies` | One selected version/output per File row; separate Media candidates |
| Scan Job database | `job`, `config`, `entries` | Specification and one manifest of observations, matching evidence, content/check/Preview results |

These are three ownership categories. Executor, Library and Settings use the configured Catalog connections; Job bundles have separate SQLite databases. Each table row is declared in its owning module and new databases use those GORM row definitions, not independently maintained SQL schemas or a shared models layer. Runtime snapshots and query projections are assembled explicitly and are never schema inputs.

## Catalog Connections

Plain SQLite filenames retain their working-directory-relative or absolute path meaning, including POSIX `//` prefixes, when converted to a URI. URI escaping preserves spaces, `#` and literal `%` in filenames; explicit `file:` URIs retain their caller-supplied options.

`database.sqlite_wal` defaults to `false`. The default uses one SQLite connection and explicitly selects DELETE journal mode. Enabling it selects WAL after format admission, keeps one writer and adds a separate pool of at most four read-only connections. The Catalog must reside on a local filesystem. Both drivers apply `synchronous=FULL` and a five-second busy timeout to every new connection; SQLite owns automatic checkpointing. Contradictory DSN journal/synchronous options, read-only service DSNs, in-memory WAL and WAL on another database dialect are rejected. Disabling the option must successfully restore DELETE mode before startup continues.

Read paths use the reader pool. Transactions bind every read and write to the transaction's own connection; they never borrow a second connection from the single-writer pool. Export reads one consistent transaction snapshot. WAL permits readers alongside a writer, not multiple simultaneous writers. Job, request-local and retained Identical databases keep their independent connection and lifetime policies. Read-only migration inspection does not change journal mode.

SQLite sidecars belong to the database. Backups must use the supported metadata export or a complete stopped-service installation backup; copying only a live main database file can omit committed WAL data. Startup format checks precede journal changes and schema initialization. Failed construction closes every pool already acquired, and shutdown releases readers before the writer.

Ordinary SQLite handles used by Jobs, temporary work and retained Identical results also default to a five-second busy wait on every connection, including replacements. Independent handles to the same file wait for short write transactions to finish instead of immediately failing concurrent progress reads or status writes.

Archive selection preparation uses a disposable SQLite workset under `work/.selection-*`, removed when preparation returns. Its `candidate_ids` table retains only File IDs for matching; current original facts are read from the Library, not copied into another persistent table. Scan selection ranges are derived from its specification and attempt state; matching evidence is embedded in `entries`. Neither workflow retains separate observation/original/scope tables.

Request-bound file operations use a temporary SQLite `items` manifest for bounded traversal and outcomes. It is protected from Location access and removed when the request ends; it is neither a retained Job bundle nor another persistent database category.

[Temporary database ownership](../../internal/resource/temporary.go) closes the database before removing its uniquely allocated directory, including failed construction. File operations, naming/import worksets and Keep staging share this cleanup; each caller still owns its schema and semantics. Durable Jobs and retained Identical results use their own lifetimes.

## Timestamps

Persisted instants use signed 64-bit Unix nanoseconds in explicitly named `_ns` columns and
fields. This includes the Catalog, Settings, Job manifests and attempt records, metadata backups,
Job bundle metadata and Volume markers. SQLite stores integers; JSON and JSONL store decimal
strings, so a browser never rounds an instant through a floating-point number. Optional unknown
dates remain absent; existing nonoptional unknown/unset sentinels remain zero. Epoch zero remains
a valid value where presence is represented separately, such as an optional Restore cutoff.

Sources with second or millisecond precision fill the remaining digits with zero. Nanoseconds
retain available precision but do not guarantee uniqueness or preserve a source timezone. The
supported signed range is approximately 1677–2262. [Checked conversion](../../internal/dataformat/time.go)
rejects instants outside that range before persistence instead of allowing `UnixNano` to wrap.
Runtime filesystem and Media descriptors may keep `time.Time`; their persistence boundaries own
the conversion. The supported v0.1.x reader still accepts its original date encoding.

Elapsed durations, timeouts and video offsets retain their documented units. Runtime elapsed-time
measurement uses Go's monotonic clock where available; persisted attempt start/finish instants
do not change the millisecond unit of the public elapsed duration.

## Published Data Formats

The current v1 implementation uses revision 1 for the Catalog and Job bundle. The Library JSONL
contract uses version 1. Until the first stable v1 release, changes to these formats between
unpublished or pre-stable v1 builds require no compatibility readers or migrations and no
version/revision bumps; the same marker does not promise a compatible layout. The supported
`v0.1.x` migration/import path remains. Software release numbers and data-format revisions are
independent.

| Artifact | Identity | Current revision |
| --- | --- | --- |
| Shared Library/Executor Catalog | `catalog_metadata` singleton, `format = yatm-catalog` | `revision = 1` |
| Job bundle | `job.json`, `format = yatm-job-bundle` | `format_version = 1` |
| Library metadata backup | JSONL header, `format = yatm-library-backup` | `version = 1` |

[Format checks](../../internal/dataformat/catalog.go) validate identity and revision
before schema changes. Incompatible pre-stable revisions are rejected without rewriting or deleting their data. Fresh initialization requires an empty database; the lack
of a `jobs` table alone does not make a database empty. The supported offline legacy
migration establishes the same Catalog and bundle identities and revision 1 as fresh creation.
Job bundles share one format definition across runners and the migrator.

An older Alpha Catalog with the same revision but obsolete Settings, file-operation tables,
tracking UUID evidence or pre-nanosecond timestamp columns is rejected before writes. Bundle,
backup and Volume marker readers likewise require their current timestamp shape. Existing
pre-stable v1 layouts require an explicit conversion or reinstall.

Unmarked nonempty catalogs, prototype JSONL headers, unmarked Draft bundles and
unknown format revisions are rejected with their data retained. Prototype
identifiers do not reserve revisions in the supported families. Disposable
development fixtures require an explicit reset.

Legacy whole-object JSON remains a separate supported import. Its compressed protobuf
header, encryption-key prefix and opaque signature bytes retain their original
encoding. LTFS profiles and Volume markers identify physical storage contracts,
not Catalog revisions or backup versions. Existing wire names such as the compressed
protobuf header, key/signature headers and `ltfs_v1` identify actual encodings;
they are independent of the `v0.1.x` and v1 software release names.

## Job Bundle

Each Job owns `work/jobs/<job_id>/`: `job.json` describes the bundle, `state.db` stores execution state, and `job.log` records events. Tape-specific reports, LTFS logs, and captured indexes live below `tapes/<barcode>/`. The complete append-only log remains with its Job until deletion; it is not rotated or truncated. A bundle is the retention and cleanup unit; detailed Job state is never a Library blob.

[Bundle creation](../../internal/executor/db.go) finishes before Create returns. [Job records](../../internal/executor/job.go) keep authoritative kind, durable status and priority in the Job database's single `job` row. The private `jobs` catalog row contains only identity, navigation, revision and query projections; runtime `Job` snapshots combine that row with the authoritative Job record and the attached runner's live phase. The Executor's `catalog_kind` is an indexed query projection, published with a new revision when the durable state changes. A Job's state and live phase are read from its own record on every hydration, so no catalog column carries either. Append-only `job_properties(job_id, key, value)` stores `location_id` and `media_id` values for catalog filters, without source/destination roles. Values come from frozen selections or actual operations.

## Mutation and Pagination Rules

- Application reads and writes use GORM so hooks, nullable signature handling, and timestamp semantics remain active. Job catalog timestamps and soft deletion follow the shared nanosecond storage contract.
- Library catalog updates use operation-local GORM model values because update callbacks and save hooks can mutate the model passed to a write.
- Current Job mutations use the Executor's `withNextJobRevision` allocator and explicit GORM writes/transactions, not Job-model revision hooks. Every mutated row gets a distinct revision; polling orders by `(revision, id)` but exposes a revision-only cursor because those revisions are unique. Initial/older pages use the snapshot revision and Job ID; tombstones remain visible to change polling. High-frequency progress is not a catalog fact.
- Explicit transactions are avoided by default: an existing operation boundary or a single statement carries ordinary writes. A transaction is introduced only for a concrete consistency requirement that nothing else satisfies; the invariant that requires it is documented, its scope stays minimal, and it never substitutes for operation admission, validation or recovery. Catalog transactions never span physical I/O or long-running work.
- Separately queried or indexed values are columns. Location's unindexed Ignore and mmap settings share one JSON `config` column; its separately queried Restore preference remains a column. Typed protobuf blobs hold cohesive specification/results or backend metadata; Scanner/Valuer implementations handle their encoding.
- Location revisions advance through GORM save hooks for configuration, attempted Job and successful publication changes. A live Search cursor binds the Location, path, query and lexical continuation, not a directory snapshot. Request references name the Location and relative path; an operation resolves the current object once for the action it is performing.
- Global configuration uses `settings(key, value, created_at_ns, updated_at_ns)`. The independent typed groups in the [Settings module](../../internal/settings/settings.go) bind `library`, `preview` and `jobs` to protobuf JSON values, defaults and validation. Reads never insert defaults, and each save replaces one complete group. A malformed stored group is reported instead of becoming defaults. Unbacked visibility and Trash-removal confirmation default on and remain independent; preference changes never collect files. Queries resolve visibility before filtering/paging, and jobs freeze the resolved scope. Library metadata backups do not contain Settings. Incompatible pre-stable automatic-collection/physical-delete schemas are rejected before AutoMigrate; data is preserved for an explicitly selected conversion or reinstall.
- Large manifests and Positions use ordered, indexed pages or streams. `parent_path` indexes direct physical children. Query aggregates from authoritative rows instead of adding duplicate counters when the query is bounded and indexed.
- File, Tag, duplicate-group and duplicate-member cursors have distinct centrally declared operation kinds as well as query bindings. A cursor from another operation cannot be reused as a directory continuation.
- Keep Library transactions metadata-only and limited to their publication work; perform source reads, ACP, mount operations, and validation before opening them.

Maintenance constraints require new catalog-visible mutation paths to maintain revisions through the path that owns them: GORM save hooks for Library rows and the Executor's revision allocator for Job rows.

## Recovery Boundary

The durable Job states are `PREPARING`, `READY`, `COMPLETED` and `FAILED`. A runner reports a live phase only while an attempt is active; execution is not another durable state. Preparation and Scan failure or cancellation are terminal. Only Archive/Restore Media failure or cancellation returns to pre-Media `READY` for another explicit Media choice. A retained manifest cannot admit a `FAILED` Job. Normal shutdown lets an active operation finish or clean up; process restart does not resume an interrupted live phase. [Jobs](jobs.md#shared-lifecycle) owns recreation through a new Job.

Library and Job commits are separate. A successful Library commit remains authoritative even if the following Job checkpoint fails; there is no distributed transaction, mid-operation attempt journal, or extra SIGKILL/power-loss recovery protocol. Archive initialization resets leftover STAGED rows. Unexpected Volume files can be examined through explicit Scan.

Restore publishes Library associations before its Job checkpoint. If that checkpoint fails, the Library commit remains authoritative and the error is returned; there is no permanent receipt, inferred replay or automatic completion repair. Verify reads against frozen Position facts and publishes the resulting historical observation after Media identity validation. Detailed per-item results remain in each Job bundle.

File operations update their original associations and return the request outcome without a persistent result table or automatic metadata retry. Their temporary manifest is removed at request end. [Physical-operation outcomes](library.md#physical-file-operations) define partial failure and explicit follow-up; no filesystem rollback is attempted.

[Recovery](../../internal/executor/recovery.go) removes only literally empty incomplete Job directories. A missing bundle preserves its catalog entry and reports an error. Missing metadata/state files, invalid manifests/configuration, symlinks and nonempty orphan bundles are reported and preserved for inspection, not recursively deleted as unfinished creations. [Catalog updates](../../internal/executor/job.go) and [kind-specific runners](jobs.md) define the implemented checkpoints. [Offline migration](../operations/migration.md) owns temporary staging/backup tables; those tables are not part of the runtime schema.
