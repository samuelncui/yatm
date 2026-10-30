# APIs and services

Status: Current v1 development architecture.

The published programmatic surface comprises gRPC services, HTTP resource endpoints,
and their contracts. [User interface](ui.md) owns the browser behavior built on them,
the [command-line interface](../operations/cli.md) owns the scripted surface that calls the same
methods, and [Interface contracts](contracts.md) owns the rules shared across surfaces.

| Service | Responsibility |
| --- | --- |
| [LibraryService](../../entity/library_service.proto) | Tags and Library maintenance |
| [MediaService](../../entity/media_service.proto) | Media, inventory, Volume candidate discovery, Volume initialize/register and device discovery |
| [PreviewService](../../entity/preview.proto) | Existing current-content or saved-version Preview metadata and role-bound resource URLs; independent optional-helper capabilities |
| [JobService](../../entity/job.proto) | Common catalog, get/list, delete/cancel and logs |
| [ArchiveJobService](../../entity/job_archive.proto) | Estimate, Create, GetCreation, WriteMedia, progress, manifest pages |
| [RestoreJobService](../../entity/job_restore.proto) | Estimate, Create, GetCreation, RestoreMedia, progress, Media/candidate pages |
| [FilesService](../../entity/files.proto) | Projected List, explicit Measure, basic Get, annotations, Mkdir/Move/Remove, versions, copies, single-signature duplicates, retained identical Find results with bounded rows, [Library-defined sorting](library.md#identical-file-tools) and group/member pages, Keep/Merge, inventory admission and original relocation |
| [ScanJobService](../../entity/job_scan.proto) | Configurable source/content/result policies, GetCreation, ReadMedia, progress and paged entries |
| [LocationService](../../entity/location.proto) | Register/list/get/update/delete, access summary and administrator discovery of unregistered paths |
| [SettingsService](../../entity/settings.proto) | Typed Get/Update for Library visibility/removal confirmation, Preview generation, Job pipeline limits and Device settings |

Archive/Restore use typed Tape/Volume operation targets. Archive Tape FORMAT carries optional
format/write choices; APPEND uses the registered immutable profile. Read Tape targets carry an
optional Media hint. Restore resolves it against pending frozen candidates; Scan requires its
configured Media. Their existing WriteMedia, RestoreMedia and ReadMedia envelopes remain the
operation boundary, with no separate Media-selection RPC. [Interface contracts](contracts.md#tape-options-and-selection)
owns option resolution and [Jobs](jobs.md) owns attempt admission/publication.

A kind's service is registered with its runner; Job listings include kind and optional target identity/name for routing and filtered history. New input browsing reads registered Location directories or administrator-authorized paths. Archive accepts Library/Location `selections`; Scan accepts those `selections` or a whole Media ID. A Scan whose selections all name one Location reports it as the primary target, including partial ranges; mixed Library/Location or multiple-Location selections have no single primary target. Job properties still index every Location actually involved.

JobService exposes no Job retry/restart operation. Job listings filter by kind, Location and Media;
`JobFilter` has no status filter. Typed Media operations require an idle `READY` Job; the
[shared lifecycle](jobs.md#shared-lifecycle) owns their admission and settlement.

Each kind's `GetCreation` reads the existing Job configuration and priority under the Job deletion
guard, through a read-only bundle connection. It returns that kind's typed Create request and an
`unavailable_reason` when original inputs cannot be recovered completely. Imported Jobs and indexed
companion Scans can lack original selection roots; their processed manifests are not substituted
for those selections. The read does not construct a runner, change state or submit another Job.
Recovered inputs remain subject to the normal creation checks against current sources.

Registered directory selectors, including Restore targets, use complete `Files.List` reads with navigation and operations, and select directory rows locally. They validate the returned source identity and directory capabilities before enabling selection or creation. `Location.BrowsePaths` discovers unregistered administrator-authorized absolute paths only; CLI users browse registered directories with `ls`.

[HTTP resource endpoints](../../internal/apis/files.go) serve Library JSONL metadata export/import and [Preview assets](../../internal/apis/preview_service.go). They do not serve full original or archived file bytes; obtaining saved content is a Restore workflow. There is no file or Preview Download CLI command. Loopback-only upgrade endpoints report running operations and quiesce admission for preflight.

Library export streams through HTTP/1.0, HTTP/1.1 and HTTP/2 without staging a complete snapshot.
A failure before response commitment returns an error without attachment headers; a later failure
aborts the response without appending JSON. The single HTTP recovery boundary preserves that abort.
HTTP/1.1 framing and HTTP/2 stream resets expose interrupted transfers. Close-delimited HTTP/1.0
cannot distinguish interruption from normal EOF; download success alone does not validate a backup.
The existing JSONL `end` record identifies completed snapshot records, and import rejects a missing
end record while rolling back all changes. The CLI checks transport errors, not JSONL completeness.

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

## Device Discovery

MediaService.ListDevices returns all configured Tape entries as DeviceSummary values with the
configured device spelling, kind, busy status and detail. It reports configuration and lease state
without issuing SCSI commands. Busy entries remain visible; Job resource admission retains its
existing queued behavior.

MediaService.InspectDevice accepts one configured device and returns transient passive observations.
Busy devices return a typed reason without hardware queries.
The optional `prepared_read` is separately timestamped last-prepared-read information from this
process, including Job/Media, requested/prepared order, parameter and position sources, applicability,
coverage and the selected learned read parameters' mean and maximum error. It can accompany a busy response; it never identifies the
currently loaded cartridge or claims the final execution route. It adds no hardware query. Optional identity/mode/limit facts
remain absent when unknown; feature support, current availability and validation are separate.
Capacity limits report optional native bytes before EW, partition, observation timestamps and error/
unreflected-write allowances, with estimate/lower-bound quality. Existing feature fields distinguish
support, availability and measured validation.
They are transient diagnostics, not a per-file fit guarantee or persisted FORMAT preference.
Compression/decompression limits expose an optional current numeric algorithm and enable-bit
changeability. Current-enabled observations remain in TapeDeviceReport. Missing changeability
stays unknown, and an algorithm observation is not a supported-algorithm list or physical validation.
Encryption limits expose public algorithm names, device-local indexes and key sizes, plus optional
current mode, scope, parameter-control and key-instance observations. Indexes are resolved on each
reader and are not FORMAT preferences. Encryption setup belongs to the configured Tape script;
these observations do not add native key management. A key counter is not evidence that a key is present or correct;
neither keys nor key-associated data enter inspection responses.
Geometry limits expose the reported all-wrap command form and source, with count only after an
interpretable response and optional freshness. Command-form support is not proof of full coverage;
passive queries leave freshness unknown. Software ordering and non-ejecting reload retain separate
qualification and availability.
Permission, missing tools, no medium, clean unsupported forms, malformed responses and unknown
ownership have distinct reasons. No capability report is saved in the Catalog. The
[Tape device contract](tape.md#device-observation-and-raw-transport) defines physical access and cleanup.
InspectDevice can include supported baseline tar write recommendations; absent recommendations
remain unknown and never certify physical writes. The existing MediaService.Inspect retains its
cartridge-oriented LTFS/Volume behavior. Tar label validation occurs under the operation lease.
Settings Get/Update stores typed Device settings without issuing hardware commands or granting
access to an unconfigured device. Media List exposes tar profiles without the saved key; Media responses
report its presence only through `encryption_key_saved`; existing ScanEntry storage and
Library JSONL backups carry the locators. There is no separate recovery export.
Device Settings also carries the optional tar capacity-polling interval. The admitted Device
snapshot owns its effective value for both FORMAT and APPEND; the [tar capacity admission contract](tape.md#tar-capacity-admission)
owns its defaults, query-failure handling and confirmed-WRITE accounting.
The optional `TapeDeviceSettings.read_parameters` uses this same replacement and snapshot path.
It contains measured applicability conditions, nominal geometry, seven positioning terms,
READ-rate/overhead estimates, separate normal-unload `completion` timings and descriptive provenance.
Completion has optional `full_span_seconds` and `fixed_seconds`; supplied
full-span time must be positive and fixed time nonnegative. Supplied values are type/range checked;
partial measurements retain absent fields, separately from explicit zero, false and plaintext.
The longitudinal coefficient uses either seconds per absolute LPOS unit or seconds per complete
longitudinal span (`longitudinal_seconds_per_wrap`), mutually exclusive by field presence.
Explicit zero positioning terms are valid; omitted measurements stay unknown. Saving input does not establish its
applicability or accuracy; the prepared tar factory checks actual conditions and map coverage.
There is no measurement RPC, registry, cartridge-position table or new tar metadata.
The same Device group carries optional `passive_read_learning` (omitted means enabled) and
`read_planning_seconds` (omitted means 300; explicit values must be positive). Read-only
`learned_read_parameters` are server-owned: Settings Update replaces user-owned Device settings while retaining
the latest learned read parameters, including when a client submits an older snapshot. The observations behind
them have no API. Media responses and backups
include the optional derived `tape_position_map`; the [persistence owner](persistence.md#derived-tape-data)
defines its initialization binding and separate update boundary.

## Browser Endpoint Addresses

`listen` selects the bound service address. `domain` independently selects the browser's
API base as `<domain>/services`, and the frontend derives Library import/export URLs as
`<domain>/files`. An explicit value is the public URL, including scheme and external port,
without a trailing slash. An empty or omitted value yields `/services` and `/files`, using
the page's current origin. Preview resource URLs are always root-relative `/files/preview`
URLs; CLI consumers select their own server address.

The service does not infer this address from Host or forwarded headers. The
[reverse-proxy guide](../operations/install.md#reverse-proxy) owns operator configuration and
deployment limits.

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
