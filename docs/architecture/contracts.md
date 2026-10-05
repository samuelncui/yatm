# Interface contracts

Status: Current v1 development architecture.

These rules bind every published surface together: the [gRPC and HTTP API](api.md), the
[user interface](ui.md) and the [command-line interface](cli.md). A surface document
describes what its surface publishes; a rule that governs more than one surface belongs
here, so one decision has one owner and no surface restates it.

## IDL naming

[Protobuf sources](../../entity) are authoritative; Go and TypeScript outputs are generated. The public
control API remains gRPC. All current API and entity sources in `entity/` use `package yatm.v1` and
`option go_package = "github.com/samuelncui/yatm/entity;entity"`; the frozen v0 migration adapter is
separate. Protobuf filenames use `lower_snake_case.proto`. A source file declares at most one service,
and its RPC request and response messages live in that file. Reusable entity messages can live in
another source file. The source layout stays flat because protobuf package names do not need to
mirror directories.

Service names are `PascalCase` domain nouns followed by `Service`, such as `ArchiveJobService`.
Read a method together with its service: `ArchiveJobService.Create`, `FilesService.List`, and
`LocationService.GetAccess` each name one operation without repeating the service's resource in the
method. Use a short verb for the service's primary resource. Add the secondary object when the
service exposes several resources or capabilities, such as `GetProgress`, `ListVersions`, and
`BrowsePaths`. Methods use imperative `PascalCase` verbs and established [domain words](../../CONTEXT.md);
an implementation detail, transport mode, or redundant service noun is not part of the method name.

Every RPC owns one request and one response message, including empty messages and streaming RPCs.
These are package-level names, so they carry the full operation and resource:
`ArchiveJobService.Create` uses `CreateArchiveJobRequest` and `CreateArchiveJobResponse`;
`ArchiveJobService.GetProgress` uses `GetArchiveJobProgressRequest` and
`GetArchiveJobProgressResponse`. The paired names differ only by `Request` and `Response`. No RPC
returns an entity directly, reuses another RPC's envelope, or uses `Req`, `Resp`, or `Reply`.
The `Request` and `Response` suffixes are reserved for RPC envelopes; reusable messages have noun
names such as `FileOperationResult` or `LocationEntryQuery`.

Message and enum type names use `PascalCase`; initialisms are ordinary words in protobuf names,
such as `LtfsMetadata` and `Sha256Digest`. Field and oneof names use `lower_snake_case`. Repeated
fields use plural nouns, Boolean names express the fact without an `is_` prefix, and measurable
values carry their units or count in the name where ambiguity is possible (`size_bytes`,
`timeout_seconds`, `file_count`). Retain established domain terms, such as `mtime_ns`, when their
meaning is already explicit. Enum values use the full enum type as an `UPPER_SNAKE_CASE` prefix;
the zero value is `<ENUM_TYPE>_UNSPECIFIED = 0`. Stable published contracts reserve removed field
numbers and names; the [pre-stable compatibility policy](../README.md#pre-stable-compatibility)
does not retain reservations solely for earlier v1 Alpha shapes.
These names follow the [protobuf style guide](https://protobuf.dev/programming-guides/style/) and
[Google API naming guidance](https://google.aip.dev/190); the [IDL check](../../dev/check-idl.go)
enforces package, type, RPC-envelope and field syntax, plus unambiguous unit and plural cases during
generation and `make check`. Review the subject and units of other fields against their actual values.

The shared typed router handles protobuf oneof-to-oneof dispatch; conversion, validation,
migration, and external events can use direct type switches.

## Time values

Every instant follows the [nanosecond storage contract](persistence.md#timestamps): protobuf
uses signed `int64` fields ending in `_ns`, and protobuf JSON emits decimal strings. Generated
TypeScript represents these values as `bigint`. Consumers retain that representation for
comparisons and requests; converting to a JavaScript `Date` is a display boundary. In particular,
opening and submitting an unchanged Restore creation form preserves its exact cutoff, including
sub-millisecond digits. Durations and video positions remain in their explicitly named units.
The IDL check rejects older instant-unit suffixes and unsigned or narrower `_ns` fields.

## Cross-surface naming

One word per concept on every surface, in the [domain vocabulary](../../CONTEXT.md) spelling. The Job kind that writes Media is **Archive** in protobuf, JSON, the CLI, the interface and persisted records; `backup` names the installation and Library-metadata backups that the installer and `library export`/`import` produce, never a Job, its kind or a copy of File content. File rows report whether content has an archived copy in the `archive` field, and the Library keeps saying archive, archived copy and archive date. Pre-stable clients move with a renamed field or enum instead of retaining a second vocabulary.

A Job publishes one durable state and, while an attempt is active, one live phase. `QUEUED` identifies an admitted resource wait; an idle Job reports no phase even when its runner is cached. A surface reads the durable state for the retained result and reads the live phase to identify admitted execution, including resource queueing. Media actions require an idle `READY` Job; no surface derives permission from counters, a retained manifest or a phase name.

No surface offers Job-level retry or restart. Failed or cancelled Scan execution and manifest preparation require a new Job. Only admitted Archive/Restore Media errors or operator cancellation, including while `QUEUED`, return the Job to its pre-Media `READY` state for another explicit load-Media operation, preserving successful per-file/Media checkpoints and the reason/timing. Rejections before attempt admission leave state unchanged, and process restart never restarts a Job or revives terminal `FAILED` work. The [Job lifecycle](jobs.md#shared-lifecycle) owns settlement and retained facts; transport and page-read Retry actions repeat only their reads.

## Synchronous operation reports

A synchronous operation reports what it would do under the `dryrun` field of its own request and writes when the field is absent, so it needs no separate read RPC and no per-command confirmation flag. `--dryrun` is the CLI spelling of that field: `rm`, `mv`, `mkdir`, `files import-positions`, `files remove-version`, `files locate-original`, `identical keep`, `identical merge`, `location delete`, `library import`, `library trim`, `media delete` and `job delete` all resolve and report the same entries they would change, and a report's entries keep the `UNPROCESSED` outcome rather than claiming completed work. A background Job cannot close that loop in one call and therefore gets its own `Estimate` method instead, as `ArchiveJobService.Estimate` and `RestoreJobService.Estimate` do.
