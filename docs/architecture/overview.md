# Architecture Overview

Status: Current v1 development architecture.

The [glossary](../../CONTEXT.md) owns domain definitions. YATM organizes ongoing Files with at most
one original, saved FileVersions, and signature-linked independent Tape/Volume copies.

## Ownership

| Module | Responsibility | Interface |
| --- | --- | --- |
| Library | Files, annotations, versions, Media/Positions and health, Locations, originals and tracking | Metadata transactions; no physical copy orchestration |
| Settings | Typed Library, Preview, Job and Device settings | Complete independently stored groups; not part of Library metadata backup |
| Executor | Job catalog projections, runner lifetime, cancellation, Job locking, accessible paths and device allocation | One execution owner per Job and attempt-scoped Media leases |
| Job runner | Typed specification, durable manifest, live phase and progress | Archive, Restore and Scan own their operation-specific execution |
| Media Backend/Session | Physical preparation, safe paths, capabilities and finalization | Stateless factory and short-lived read/write sessions |
| Preview manager | Generators and content-addressed derivative bundles | Independent of Media identity and device leases |
| gRPC/HTTP, frontend and CLI adapters | Typed operations, browsing, Preview/metadata resources and polling | No second execution state machine in clients |

Application entry and configuration are in the [HTTP service](../../cmd/httpd/main.go),
[configuration](../../internal/config/config.go), and [example configuration](../../config.example.yaml).
[Executor construction](../../internal/executor/executor.go), [Job registration](../../internal/executor/job_type.go),
and [API wiring](../../internal/apis/api.go) connect the modules. Source ownership and build entrypoints
follow the [repository layout](../../AGENTS.md#repository-layout).

## Supported operating model

YATM is one local service for one operator, not a multi-user or distributed service. It does not
coordinate independent users or another service instance sharing its Library or Locations. The
operator does not issue conflicting mutations against the same catalog objects or paths and does not
alter selected files, referenced configuration or mounted Media while an operation uses them.
Behavior outside this model is unspecified. Internal pipeline parallelism and independent Jobs remain
supported only where a workflow requires them.

The expected deployment is local or on a low-latency LAN. Ordinary file browsers receive a
complete immediate directory through streamed batches, then sort/filter and virtualize it in
the client; they do not impose a silent row cutoff or require manual page loading. This allows
memory proportional to the directory, while metadata queries, projection and rendered DOM
remain bounded by batches or the visible window. Recursive Search and retained global
Identical results have their own query/paging contracts. Performance decisions use this
deployment model and measured large-directory workloads rather than Internet latency assumptions.

Required guards are limited to administrator authorization and path containment, refusing a target
observed to exist, physical Media/device identity and lifecycle, atomicity of one metadata mutation,
bounded resource use, and integrity of the bytes actually transferred or verified. Do not add
filesystem or catalog locks, revalidation, change detection, snapshot or token protocols, automatic
repair, replay, or cross-store transactions for unsupported concurrent change. No-overwrite does not
promise race-proof behavior against an external writer. Catalog import is a quiesced operator action
and assumes no Job is running.

An explicit Find can retain its read-only result for bounded paging; that result is not a guard for
concurrent mutations. Keep and Merge validate their current connected component before acting.

## Main Flows

- Archive: filesystem selection -> durable manifest -> ACP copy -> backend validation -> Library publication -> submitted Job items.
- Restore: explicit FileVersion selection -> signature-matched candidates -> ordered Media reads -> verified output -> Library association publication -> Job checkpoint.
- Scan: recorded Location/Library/Media scope -> bounded enumeration -> policy-controlled facts/reads -> optional comparison and Preview -> selected publication -> Job checkpoint.
- Files browsing: actual Location directory or logical Library directory -> requested projections and visible observations; reads never admit Files or fall back to cached physical rows.
- File operations: shared bounded planner -> guarded Library/filesystem primitives -> exact successful association publication and per-item results; no Job.
- Library-selected Archive: frozen File identities and logical targets -> usable originals -> verified ACP transfer -> ordinary Media publication.

[Jobs](jobs.md) owns lifecycle and attempt semantics; [Media and file I/O](media-io.md) owns
physical guarantees; [Library](library.md) owns organization, versions and inventory;
[Persistence](persistence.md) owns durable stores and recovery. Accessibility, copy health, Job state,
live phase and UI summaries are separate facts. Library organization never renames or removes physical
Media files. LTFS/filesystem files and tar members use ordinary public formats;
[Media/I/O](media-io.md) and the [recovery guide](../operations/tar-tape-recovery.md) own their
physical recovery requirements.

## Source Modules and Dependency Direction

A Go package is an ownership and behavior boundary, not a folder for one technical kind of type.
The source tree therefore follows the business modules above:

| Source | Owns | May depend on |
| --- | --- | --- |
| `entity/` | Protobuf sources, generated wire types and encoding helpers | Go standard library and wire-format dependencies; no implementation package |
| `internal/library/` | Library catalog rows, metadata transactions and Library projections | `entity` and focused infrastructure helpers |
| `internal/settings/` | Typed Settings groups, validation and persistence | `entity` and its Settings store |
| `internal/executor/` | Job catalog, execution admission, resources and composition of runners | Library, Settings, Media contracts and operation modules |
| `internal/executor/archive/`, `restore/`, `scan/` | One Job kind's bundle rows, runtime pipeline and results | Executor capabilities plus the Library and Media operations that Job kind uses |
| `internal/media/` | Backend and session contracts, Tape records and tar encoding for physical Media behavior | `entity`, ACP content endpoints; never a Library row or Job database |
| `internal/media/readplan/` | Software read ordering of one frozen selection in a disposable index | `entity`, temporary resources; never `internal/media`, Tape I/O or locators |
| `internal/apis/`, `cmd/` | Transport and process composition | Public module interfaces; no duplicate domain state |

Dependencies point toward contracts and owning modules. A physical Media session receives the facts
needed for I/O and returns physical results; the Archive runner alone changes Archive rows, and the
Library alone publishes catalog rows. Likewise, continuity matching uses an explicit observation-store
interface: Archive observation rows and Scan entry rows each map their own schema to that interface.
Extract shared behavior when it materially removes duplication between current callers or establishes
one independently testable ownership or invariant boundary. Similar structs, a hypothetical future
caller, or a generic `models`, `services` or `repositories` layer are not sufficient.
