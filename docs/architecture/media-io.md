# Media Backends and File I/O

## Content stability

The [supported operating model](overview.md#supported-operating-model) owns the stability assumption.
Read each filesystem fact when the operation needs it; do not repeat a read solely to prove that the
source did not change or to recover from such a change. Behavior under external replacement is
unspecified. A mismatch with previously recorded content is reported as a content result rather than
retried or reconciled.

Authorization and path containment, refusal of an observed existing target, physical Media identity,
and transfer/integrity checks remain separate required boundaries.

## Session Boundary

The [Backend](../../internal/media/backend.go) is a stateless factory for Read/Write Sessions and
accepts only the typed physical target. It never receives a Job database. Sessions prepare physical
access, resolve content references, report capabilities and finalize; Archive, Restore and Scan retain manifest
paging, ACP orchestration, item transitions and Library publication. ReadSession uses a neutral
ReadMediaTarget with expected identity/profile; it never queries Restore `copies` or requires fake
Restore tables for Scan. LTFS expectations precede encryption/mount; tar validates its physical label
after establishing the required record mode and before content access. Volume expectations precede
path exposure.

Read Finalize also receives the caller's read/stop error. A successful raw tar read uses normal
physical release; failed or canceled work only restores owned controls and closes the descriptor.
LTFS still delegates release to its configured unmount adapter; Volume has no Tape release action.

`Media` is a zero-I/O boundary descriptor on a prepared Session, not a Library persistence row.
Read resolution receives the frozen Media path and available storage facts. A source or target
reference contains exactly one native path or ACP content endpoint; constructing it performs no
content I/O. LTFS and Volume retain native paths. Endpoint identifiers come from the caller's Media
path and are never treated as filesystem paths. Write target resolution receives the Archive row's
frozen path and size, so Volume capacity planning does not query that row. Finalize receives the
copy/stop error and returns a `WriteResult`: the selected Media descriptor, physical path prefix and,
for Tape, bounded completed path/size/storage facts after physical finalization. LTFS supplies its
captured Index after unmount; tar validates positions already retained in Archive item results. LTFS derives its
target-full behavior from the copy error. The returned finalization error describes physical completion;
the result separately retains a stop first discovered while settling buffered data or the trailer.
The runner combines that stop with the copy error before reconciliation, preserving a usable
physical result and its actual termination reason. The Archive runner reconciles that result with its
own `items` table in one transaction and then asks Library to publish it. No Media implementation imports an Archive row,
changes a Job table or owns a Library transaction.

Capabilities derive from immutable [Media profiles](../../entity/media.proto), not duplicate database flags:

| Profile | Read | Write |
| --- | --- | --- |
| HDD | Concurrent random | Concurrent random |
| HM-SMR | Concurrent random | Sequential |
| Tape | Sequential | Sequential |

Concurrent random access uses configured ACP concurrency, random access uses a single device thread, and sequential access uses ACP's linear behavior. Read access determines storage-order requirements. An [attempt lease](../../internal/executor/device.go) exclusively reserves a Tape device or Volume UUID; these locks also prevent conflicting YATM operations, not just removable-media swaps. A conflicting Job waits for the holder in arrival order and reports `QUEUED`, so a device in use never fails a Job; an unconfigured Tape device remains an explicit configuration error. Restore acquires the drive before probing its cartridge identity, and Session setup reuses that attempt's lease. The attempt retains the lease through finalization and settlement.

## Volume

[Volume Sessions](../../internal/executor/media_volume.go) manage only already-mounted filesystems. [Volume discovery](../../internal/media/volume.go) accepts configured roots or direct children, resolves the immutable `.yatm.json` UUID/profile, and rejects duplicate mounted identities, profile changes, symlink path components, and path escapes. An absent marker is skipped; a present but unreadable/invalid marker fails discovery, including checks separating originals from archive storage. YATM does not mount, unmount, or eject Volumes; current discovery validates the directory/marker rather than requiring an OS mount-point record.

[Initialize/Register](../../internal/apis/volume.go) creates a marker and Media row or re-registers an existing marker after metadata-only deletion. The read-only candidate listing reports each configured discovery candidate with its registration state, so an unreadable marker stays visible instead of failing that listing; identity discovery still rejects it. Initializing without an explicit serial number reads the block-device serial from sysfs on Linux; the Volume type is never detected and remains the caller's choice. Unknown devices, other platforms and file-backed mounts keep an empty serial instead of guessing. Initialization failure removes only a marker created by that request. Runtime mounted/available-space state is not persisted as a Library fact.

Volume admission uses filesystem free space with reserve `max(1 GiB, filesystem capacity * 1%)`. Archive selects the deterministic pending prefix that fits the remaining allowance. Actual no-space errors are normalized by the Backend; capacity prediction remains advisory, with no independent `full` flag. Media file counts and last-write information derive from indexed physical Positions rather than duplicate summary columns.

Volume Finalize returns the already-opened session result and keeps successfully copied files eligible for publication, including after an ordinary copy error or ENOSPC. It does not reread the marker, scan, roll back, or delete a directory. A prefix is an ordinary namespace, not a persistence or ownership boundary. Media Delete removes metadata only and leaves marker/files unchanged.

## ACP and Integrity

[ACP](https://github.com/samuelncui/acp) streams bounded shared buffers with backpressure. Source
reading, hashing, target writing and file completion can overlap. Linear source reads remain ordered
through EOF; linear targets retain one application Write lane in request order. The next file can
advance while the preceding file's Close and metadata work finish. Each result still waits for its
own hash, source/target closure, cache and publication dependencies. Conflicting output paths retain
their required publication order; independent paths can finish concurrently. Terminal target errors
stop new source work once observed while draining and closing bounded prefetched work. This
application ordering does not prescribe physical LTFS extent placement; its final Index remains
authoritative.

ACP's optional content endpoints use the same transfer engine and require its linear mode only in
the endpoint's direction; the native side retains its configured options. An opened source is a
standard `fs.File`. ACP observes its Stat and optional Xattrs once in the ordered lane, then copies
portable metadata before creating any target. Tar supplies its actual parsed header and raw PAX
attributes; ACP owns ordinary-xattr selection, managed-cache exclusion and snapshot limits. External
Info.Sys does not establish native ownership. Failed or panicking metadata observation fails that
item before output creation and still closes its acquired source. The codec retains its bounded
decoding and PAX-specific representation checks; ACP retains native size probes before allocation. Each provider bounds its
own allocations before ACP receives its snapshot.

Source Close must succeed before native output publication and before the next content source opens;
target Finish runs once after writes, source closure and hashing settle, retaining their causes,
before the next content target opens. A source returned with an Open error still closes once.
Restore/Scan tracking and read-window wrappers forward Stat/Xattrs and preserve observation errors;
source-boundary settlement does not replace the terminal item result. Native linear completion
keeps its existing overlap. Endpoint identifiers never enter native stat, cache, capacity or cleanup
operations. Tar uses these endpoints for Archive and Restore/Scan; native LTFS/Volume paths retain
their applicable metadata/cache/output behavior.

ACP's SourceFromFS binds a real `fs.FS` and relative name without inventing a filesystem tree for
tar. It opens once and observes that file, avoiding fs.Stat's possible extra Open/Close. Injected
filesystem paths follow io/fs rules and do not acquire native mmap or ownership guarantees. Native
paths retain their existing absolute/arbitrary-byte names and platform behavior. Context-aware tar
operations keep their command cancellation; wrapping an arbitrary fs.FS does not make blocking
Open/Read calls interruptible.

Location configuration selects ACP's source read mode for original content in Archive preparation/copying and Scan content reads; buffered is the default. Archive applies the setting of each item's current Location without reordering the Media write. Scan applies it per Location scope, including indexed original content reads. Media sources and Restore output verification keep their own read behavior. On Linux buffered ACP reads first attempt `O_NOATIME` and fall back to an ordinary open when the filesystem or permissions refuse it; mapped reads use a different open path. Avoiding access-time updates is therefore best effort, not an operation guarantee.

Callers own their items: the runner pages the Job manifest into ACP one bounded page per submission, and every accepted item is reported exactly once through one results callback, called from one goroutine. A result that carries any error reaches that callback immediately, while results without an error wait in the engine's result queue until a delivery holds the result batch, the result flush interval elapses, or the run closes. The caller's callback only starts a write of the batch it received, so ACP never waits for a database write; the caller's writer is where a persistence failure is recorded, reported back as the callback's cancellation and finally reported by the drain. A completion reports one outcome per requested target in request order, so an item whose every target failed is still a completion, and target errors keep their identity for classification. A missing or unreadable source remains an observable item failure rather than a silent omission. `Wait` returns the run's terminal error, and `Close` still flushes what its buffer holds. Page/Submit, Wait and Close failures are retained independently; a single failure is returned unchanged and multiple failures use the existing multierror aggregation, without introducing another error classification. ACP's older Job-based API is a compatibility shell over the same engine and the only path that publishes ACP's `Job` report rows; YATM's runners call the push engine directly.

Archive and Restore always hash real source bytes in ACP. Completed targets are not reread for hashing.
Ordinary targets, including LTFS files, are written to an exclusively created `.tmp_*` file beside the
final path. ACP completes metadata, signature-cache work and required closure before renaming and
reporting success against the final path. Random outputs complete metadata and Sync before Close;
linear outputs close after their data writing, then finish metadata/cache work on the owned temporary
path. Archive and Restore retain `Overwrite(false)`, so ACP never replaces an existing destination.
Archive targets that alias on the Volume filesystem compete through exclusive rename: only the
completed file is published, and the refused file remains pending for another Media choice. Restore
reserves distinct native output names during preparation, including case and Unicode aliases.
Failed copies clean up only their
owned temporary output and preserve existing destinations; cleanup failures remain alongside the copy
error without losing no-space error identities. Linear targets skip preallocation and per-file Sync;
successful LTFS unmount and the final Index still govern LTFS publication. Tar uses its physical
complete-member gate above. Staging adds no filesystem
transaction or power-loss durability guarantee.

ACP's managed signature xattr is a disposable cache keyed by size and nanosecond mtime, never an
immutable identity or transfer-integrity proof. Scan's KNOWN_ONLY uses the cache-only read without
hash fallback; FILL_MISSING hashes misses, and FORCE_READ reads every selected file. Verification
always reads uncached content against old expected facts. The managed key is excluded from ordinary
xattr copying. Native cache lookup uses the source's descriptor and indexed facts. Source Close can
finish before hashing; cache refresh then uses the source path and finishes before result delivery.
This follows the supported operating model in which selected files remain unchanged during an
operation. A failed cache write is counted, never a Job failure. Read-only/unsupported xattrs do not
fail copying. Custom content sources have no native cache reuse/refresh capability; their optional
Xattrs supplies ordinary metadata only. Tar stores neither this cache nor a checksum manifest.
Restore hashes actual bytes and recreates a supported native output's cache under its selected policy.
Applying the selected FileVersion's metadata can subsequently invalidate this disposable cache;
the [Restore contract](jobs.md#restore) owns that behavior.

The Archive runner normalizes ACP and operating-system no-space errors before Finalize, preserving the
original copy error independently. Finalize performs physical cleanup and identity validation; Archive
then applies the returned facts, publishes verified successes and returns the original/normalized error.
[Jobs](jobs.md) defines these publication checkpoints; [E2E](../development/e2e-test.md) defines physical
validation coverage.
