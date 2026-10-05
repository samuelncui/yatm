# APIs and services

Status: Current v1 development architecture.

The published programmatic surface comprises gRPC services, HTTP resource endpoints,
and their contracts. [User interface](ui.md) owns the browser behavior built on them,
the [command-line interface](cli.md) owns the scripted surface that calls the same
methods, and [Interface contracts](contracts.md) owns the rules shared across surfaces.

| Service | Responsibility |
| --- | --- |
| [LibraryService](../../entity/library_service.proto) | Tags and Library maintenance |
| [MediaService](../../entity/media_service.proto) | Media, inventory, Volume candidate discovery, Volume initialize/register and device discovery |
| [PreviewService](../../entity/preview.proto) | Existing current-content or saved-version Preview metadata and role-bound resource URLs; independent optional-helper capabilities |
| [JobService](../../entity/job.proto) | Common catalog, get/list, delete/cancel and logs |
| [ArchiveJobService](../../entity/job_archive.proto) | Estimate, Create, GetCreation, WriteMedia, progress, manifest pages |
| [RestoreJobService](../../entity/job_restore.proto) | Estimate, Create, GetCreation, RestoreMedia, progress, Media/candidate pages |
| [FilesService](../../entity/files.proto) | Projected List, explicit Measure, basic Get, annotations, Mkdir/Move/Remove, versions, copies, single-signature duplicates, retained identical Find results with bounded rows, per-group member sorting and group/member pages, Keep/Merge, inventory admission and original relocation |
| [ScanJobService](../../entity/job_scan.proto) | Configurable source/content/result policies, GetCreation, ReadMedia, progress and paged entries |
| [LocationService](../../entity/location.proto) | Register/list/get/update/delete, access summary and administrator discovery of unregistered paths |
| [SettingsService](../../entity/settings.proto) | Typed Get/Update for Library visibility/removal confirmation, Preview generation and Job pipeline limits |

Archive/Restore use typed Tape/Volume operation targets. A kind's service is registered with its runner; Job listings include kind and optional target identity/name for routing and filtered history. New input browsing reads registered Location directories or administrator-authorized paths. Archive accepts Library/Location `selections`; Scan accepts those `selections` or a whole Media ID. A Scan whose selections all name one Location reports it as the primary target, including partial ranges; mixed Library/Location or multiple-Location selections have no single primary target. Job properties still index every Location actually involved.

JobService exposes no Job retry/restart operation. Job listings filter by kind, Location and Media;
`JobFilter` has no status filter. Typed Media operations require an idle `READY` Job and follow the
[shared lifecycle](jobs.md#shared-lifecycle): pre-admission failures leave state unchanged,
admitted Scan/Index errors or operator cancellation settle as terminal `FAILED`, and only admitted
Archive/Restore Media errors or operator cancellation, including while `QUEUED`, return to pre-Media
`READY` with the prepared manifest, successful per-file/Media checkpoints and reason/timing.
Failed or cancelled Scans require a new Scan; process restart never restarts a Job or revives terminal work.

Each kind's `GetCreation` reads the existing Job configuration and priority under the Job deletion
guard, through a read-only bundle connection. It returns that kind's typed Create request and an
`unavailable_reason` when original inputs cannot be recovered completely. Imported Jobs and indexed
companion Scans can lack original selection roots; their processed manifests are not substituted
for those selections. The read does not construct a runner, change state or submit another Job.
Recovered inputs remain subject to the normal creation checks against current sources.

Registered directory selectors, including Restore targets, use complete `Files.List` reads with navigation and operations, and select directory rows locally. They validate the returned source identity and directory capabilities before enabling selection or creation. `Location.BrowsePaths` discovers unregistered administrator-authorized absolute paths only; CLI users browse registered directories with `ls`.

[HTTP resource endpoints](../../internal/apis/files.go) serve Library JSONL metadata export/import and [Preview assets](../../internal/apis/preview_service.go). They do not serve full original or archived file bytes; obtaining saved content is a Restore workflow. There is no file or Preview Download CLI command. Loopback-only upgrade endpoints report running operations and quiesce admission for preflight.

`JobService.GetLog` retains its raw forward byte read for CLI and evidence consumers.
`JobService.ListLogLines` serves the browser's bidirectional log view: no cursor starts at
the tail for older reads or the beginning for newer reads; returned byte cursors name the
next region in either direction. Each request scans at most 1 MiB and returns at most 200
lines, aligned to newlines when possible. A longer line is returned in marked fragments;
an unfinished final line can grow on a later read. Level and case-insensitive text filters
run on the server over each scanned region. A continuation whose line start lies outside
the bounded read is returned as context even when that fragment cannot confirm a filter
match. The browser labels such fragments as unverified and continues a filtered search until
it finds a confirmed line or reaches the boundary. An empty response may still move its cursor;
the caller continues until the corresponding `has_older` or `has_newer` flag is false.

Files.Get supplies organization, original navigation and applicable content references for explicit workflows without opening file bytes in the browser. User Ignore hides an entry from browsing, measurement and Location selection but does not prevent explicit admission. Physical mutations accept typed relative references, never shell commands or unrestricted absolute paths.

## Listing projection

Each visible Files panel requests one complete directory through Files.List. Fixed include
groups select attributes, status, operations and navigation; omitted groups avoid their
queries and observations. A minimal request asks for none, while `ls -l` adds
attributes and `--status` adds content availability, and UI rows request all four. Only
File ID zero denotes the virtual Library root; the reserved Trash ID -1 is looked up as
a persisted directory, and a missing ID returns NotFound rather than an empty
directory. Read commands accept that ID without relaxing mutation validation.

**One operation layer serves both sources.** A Library directory and a live Location are
two implementations of one interface, whose methods carry the `FilesService` operation
names and meanings (list, get, measure, mkdir, move, remove, update metadata). Service
handlers bind a request to one implementation, ask it for exactly the fact groups the
request asked for, and project the result; they do not branch on which source a request
came from, and neither does the client. Where the two differ is the implementation's
business: on a Location a move changes disk entries, while on the Library it changes logical
organization without moving the physical original.

**A listing is enumerated once and projected one batch at a time.** `List` is a server
stream whose batches are work units, not just transport slices: the source enumerates the
directory, sorts compact entries once, states the identity, breadcrumbs and total in the
first batch, and reads each following batch's facts and observations only once the previous
batch has been sent. The first response waits for complete enumeration and sorting plus
one batch's projection. Compact entries take memory proportional to directory size; projected
rows are bounded by the response batch. There is no fixed directory-entry cutoff. A batch's projection is the same bounded metadata round
and filesystem observation whether it holds one row or a thousand; only the number of
rounds follows the batch size. Location reads never use index fallback.

Ordinary names and paths preserve legal UTF-8 text exactly, including literal backslashes,
spaces, tabs and newlines. Slash separates components; NUL, dot/dot-dot components and root
escape are rejected at operation boundaries. The string protocol does not support arbitrary
non-UTF-8 filename bytes. Such a listed name is escaped for display, with an explicit
`FilesEntry.error` and no actionable reference. A child's attribute-read failure likewise
retains its row and reason. Unknown size, time and kind remain absent or unspecified;
enumerated totals and independent usable rows remain available. A directory-level access,
read, transport or cancellation failure still fails the request. Search, measurement and
workflow consumers retain their explicit failure/incomplete-result semantics rather than
silently skipping unreadable children.

A Location listing prepares directory authorization and checks administrator rules for every leaf.
Native identity is derived from the same stat observation; tracking never opens a file or reads
an xattr. Missing native evidence leaves identity unconfirmed rather than inventing continuity.
Mixed pages of associated and unlinked Files retain each row's observation while the
associated rows are checked with at most eight workers. A request reuses source and parent
checks and batches association lookups. Missing parents mark only their affected rows missing;
other observation failures make those rows unavailable. Request cancellation still stops the operation.

[LocationDirectoryReader](../../internal/executor/location_directory.go) owns one open directory
and sequential bounded reads for List, Search, Measure, selection, Scan and file operations.
Administrator access remains mandatory; each caller explicitly decides whether to apply user
Ignore. [FilesQuery](../../internal/library/live_query.go) compiles a request's predicates once
and reuses them across batches. Ordinary Search keeps bounded page candidates and its existing
continuation semantics; it does not retain a directory snapshot.

## Measurement

[Files.Measure](../../internal/apis/files_measure.go) answers one complete
directory/query scope, independently of Search pagination. Matching directories include
all accessible descendants without reapplying the root filter; overlapping matches
contribute once to the final total. Bounded postorder traversal streams per-root known
bytes/completeness and a final deduplicated summary. Cancellation or a failed stream
cannot produce a completed total. Unknown facts and inaccessible ranges retain known
subtotals with an incomplete result. Library measurement uses stored original attributes
or the latest saved version; Location measurement includes dot entries, omits entries
matching Location Ignore, skips linked/special-file bytes, and preserves runtime,
registration and mount boundaries. It never hashes, admits or creates a Job. Streaming
consumers render the same scope, and an incomplete result is reported as incomplete
rather than as a subtotal that stands for the whole.
