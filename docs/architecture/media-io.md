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
access, resolve paths, report capabilities and finalize; Archive, Restore and Scan retain manifest
paging, ACP orchestration, item transitions and Library publication. ReadSession uses a neutral
ReadMediaTarget with expected identity/profile; it never queries Restore `copies` or requires fake
Restore tables for Scan. Tape expectations are checked before encryption/mount; Volume expectations
precede path exposure.

`Media` is a zero-I/O boundary descriptor on a prepared Session, not a Library persistence row.
Write target resolution receives the Archive row's frozen path and size, so Volume capacity planning
does not query that row. Finalize receives only whether the copy stopped at a target-full boundary and
returns a `WriteResult`: the selected Media descriptor, physical path prefix and, for Tape, a bounded
post-unmount inventory source. The Archive runner reconciles that result with its own `items` table in
one transaction and then asks Library to publish it. No Media implementation imports an Archive row,
changes a Job table or owns a Library transaction.

Capabilities derive from immutable [Media profiles](../../entity/media.proto), not duplicate database flags:

| Profile | Read | Write |
| --- | --- | --- |
| HDD | Concurrent random | Concurrent random |
| HM-SMR | Concurrent random | Sequential |
| Tape | Sequential | Sequential |

Concurrent random access uses configured ACP concurrency, random access uses a single device thread, and sequential access uses ACP's linear behavior. Read access determines storage-order requirements. An [attempt lease](../../internal/executor/device.go) exclusively reserves a Tape device or Volume UUID; these locks also prevent conflicting YATM operations, not just removable-media swaps. A conflicting Job waits for the holder in arrival order and reports `QUEUED`, so a device in use never fails a Job; an unconfigured Tape device remains an explicit configuration error. Restore acquires the drive before probing its cartridge identity, and Session setup reuses that attempt's lease. The attempt retains the lease through finalization and settlement.

## Tape Script Compatibility

Configured scripts are a long-lived user interface. Preserve previously supported adapters wherever
possible, including their helpers, input variables, output format, working directory and exit-status
meaning. LTFS distribution and hardware differences remain inside those adapters. A necessary
incompatible change requires explicit developer approval, an actionable installer notice before
upgrade and regression checks of the changed boundary. Installation preserves customized adapters
and their execution context instead of replacing them automatically. This contract applies
independently of the pre-stable API/data policy and preserves the [Tape lifecycle](#tape-lifecycle).

Script changes require automated checks of the published interface and representative unchanged
historical adapters. Physical acceptance checks the selected host adapter separately; it does not
replace the historical compatibility checks.

Archive requires a completed per-Job captured Index; historical adapters without that output need
manual adaptation before Tape operations. This required incompatibility is described in the
[upgrade notice](../operations/migration.md#tape-script-adaptation) displayed by the installer.

## Tape

Configured scripts are the anti-corruption boundary for environment-specific executable paths, device mapping, vendor options and helper logic. The service supplies operation inputs and validates identity, content and final Index output; it does not absorb those local decisions into configuration switches. The [migration guide](../operations/migration.md#tape-script-adaptation) owns input/output contracts and upgrade examples. Installer preflight never invokes these scripts.

The format script passes the resolved device, barcode and complete Tape name as separate literal
arguments to `mkltfs`; spaces and shell pattern characters in the name do not split or expand it.
Regression checks execute the shipped script with a stub LTFS command, while physical acceptance
uses the packaged script with only the host executable path and test placement rule adapted.

The Linux `get_device` adapter resolves the Tape node through the kernel's sysfs mapping to its
SCSI generic node. This lookup does not open the drive, so it still works while LTFS owns the
device and during unmount; an absent or ambiguous mapping fails explicitly.

Bundled mount scripts resolve the Job's Tape artifact directory to an absolute path before passing it to LTFS. LTFS can change its working directory when it runs in the background; captured indexes must still land in the supplied Job directory when `paths.work` is relative.

The official LTFS file-backend adapters select a supported capture option at setup: an absolute
directory value, or bare `capture_index` with an absolute work directory for older builds. They wait
for their mount's LTFS process to exit after FUSE detaches. FUSE detachment alone can precede the final Index
write; the test adapter therefore supplies the same completed-finalization boundary as physical
device release.

The bundled encryption script loads the drive and retries `stenc` at most 60 times. If
every attempt fails, it exits nonzero so the Tape Session cannot proceed to formatting or
mounting. Its `stenc` arguments remain part of the environment-specific script contract.

[Tape Sessions](../../internal/executor/media_tape.go) identify the cartridge, configure encryption, format when requested, mount LTFS, and unmount/finalize. FORMAT accepts a successful probe reporting an empty electronic barcode and uses the explicit user-supplied six-character barcode; the format script passes that identity to `mkltfs` to write it during initialization. A non-empty device barcode must match the requested identity, and probe failures stop the Session before encryption or formatting. An empty barcode does not establish that the cartridge contains no data: FORMAT remains an explicit destructive operation. FORMAT rejects a barcode already in the Library; explicit metadata deletion is required before creating a new Media identity.

APPEND and Restore require a non-empty valid device barcode before encryption or mounting; APPEND also requires an exact match with the requested identity. Device inspection may use its documented read-only manual identity fallback when automatic MAM lookup is unavailable, but that fallback does not authorize a write or Restore. APPEND requires compatible `ltfs_v1`, preserves Media ID/key/Positions, and chooses a short base36 timestamp prefix only to avoid path collisions.

### Tape Lifecycle

1. For an uninitialized cartridge, the operator chooses its six-character barcode and explicitly
   submits FORMAT. The electronic barcode may be empty; the formatting script writes the supplied
   identity and name while initializing LTFS. An empty barcode does not establish empty content.
2. Archive writes files, normally unmounts and waits for device release, then validates the final
   captured Index and publishes Media and Positions. The Library records the Tape's identity and key.
3. On later loads, APPEND verifies the existing barcode and compatible Library Media, reuses its key
   and adds files without replacing earlier Positions. Restore verifies the loaded Tape against its
   recorded identity, uses its key and reads the selected saved content.
4. Metadata deletion leaves the cartridge's bytes intact. Reinitialization requires explicit FORMAT
   after deleting the registered Media; formatting erases the old physical contents.

Script and caller changes must keep every step available, including FORMAT before an electronic
barcode exists. Per-operation identity and finalization rules remain defined above and below.

The final captured LTFS Index is the physical fact source. [LTFS processing](../../internal/media) retains backend extents in the typed storage-metadata envelope and stores opaque binary order from partition/block/offset. `ltfs_v0` is path-order compatibility; `ltfs_v1` has validated LTFS order/extents and supports append. Versioning belongs to the storage format, not a second independent version field.

After physical Finalize, Archive matches staged files against the returned Index by actual Media path,
size and valid extents. On normal completion every staged result must match. On target-full it submits
only the continuous verified prefix and resets the later suffix to PENDING. A successful normal unmount
includes waiting up to ten minutes for the LTFS process to release the SG device and for `mt status` to
report `DR_OPEN`; the Job attempt, device lease and Job bundle remain owned throughout this wait. An
unmount timeout or invalid/missing final Index publishes no unverified result. An uncertain device
remains unavailable for that Executor lifetime. Force and lazy unmount are manual stopped-service
recovery tools whose write result must be discarded. Ordinary LTFS files stay readable without a
YATM-specific segmented format.

Read Sessions validate the expected Tape before mounting and keep the attempt's device lease
through normal unmount. Finalize does not run `readinfo` again: that script loads the cartridge,
which must not occur while LTFS owns it. Successful reads become publishable only after normal
unmount succeeds; a failed unmount still withholds publication and keeps the device unavailable.

## Volume

[Volume Sessions](../../internal/executor/media_volume.go) manage only already-mounted filesystems. [Volume discovery](../../internal/media/volume.go) accepts configured roots or direct children, resolves the immutable `.yatm.json` UUID/profile, and rejects duplicate mounted identities, profile changes, symlink path components, and path escapes. An absent marker is skipped; a present but unreadable/invalid marker fails discovery, including checks separating originals from archive storage. YATM does not mount, unmount, or eject Volumes; current discovery validates the directory/marker rather than requiring an OS mount-point record.

[Initialize/Register](../../internal/apis/volume.go) creates a marker and Media row or re-registers an existing marker after metadata-only deletion. The read-only candidate listing reports each configured discovery candidate with its registration state, so an unreadable marker stays visible instead of failing that listing; identity discovery still rejects it. Initializing without an explicit serial number reads the block-device serial from sysfs on Linux; the Volume type is never detected and remains the caller's choice. Unknown devices, other platforms and file-backed mounts keep an empty serial instead of guessing. Initialization failure removes only a marker created by that request. Runtime mounted/available-space state is not persisted as a Library fact.

Volume admission uses filesystem free space with reserve `max(1 GiB, filesystem capacity * 1%)`. Archive selects the deterministic pending prefix that fits the remaining allowance. Actual no-space errors are normalized by the Backend; capacity prediction remains advisory, with no independent `full` flag. Media file counts and last-write information derive from indexed physical Positions rather than duplicate summary columns.

Volume Finalize returns the already-opened session result and keeps successfully copied files eligible for publication, including after an ordinary copy error or ENOSPC. It does not reread the marker, scan, roll back, or delete a directory. A prefix is an ordinary namespace, not a persistence or ownership boundary. Media Delete removes metadata only and leaves marker/files unchanged.

## ACP and Integrity

[ACP](https://github.com/samuelncui/acp) streams bounded buffers with backpressure. Bounded concurrent source preparation feeds one writer in original request order for linear targets; preparation failures and skipped requests advance the same ordering cursor. Random-access targets retain completion-order concurrency. Terminal target errors stop new source work while draining and closing bounded prefetched work.

Location configuration selects ACP's source read mode for original content in Archive preparation/copying and Scan content reads; buffered is the default. Archive applies the setting of each item's current Location without reordering the Media write. Scan applies it per Location scope, including indexed original content reads. Media sources and Restore output verification keep their own read behavior. On Linux buffered ACP reads first attempt `O_NOATIME` and fall back to an ordinary open when the filesystem or permissions refuse it; mapped reads use a different open path. Avoiding access-time updates is therefore best effort, not an operation guarantee.

Callers own their items: the runner pages the Job manifest into ACP one bounded page per submission, and every accepted item is reported exactly once through one results callback, called from one goroutine. A result that carries any error reaches that callback immediately, while results without an error wait in the engine's result queue until a delivery holds the result batch, the result flush interval elapses, or the run closes. The caller's callback only starts a write of the batch it received, so ACP never waits for a database write; the caller's writer is where a persistence failure is recorded, reported back as the callback's cancellation and finally reported by the drain. A completion reports one outcome per requested target in request order, so an item whose every target failed is still a completion, and target errors keep their identity for classification. A missing or unreadable source remains an observable item failure rather than a silent omission. `Wait` returns the run's terminal error, and `Close` still flushes what its buffer holds. The `af05f05c` surface is a compatibility shell over the same engine and the only path that publishes ACP's `Job` report rows; YATM's runners call the push engine directly.

Archive and Restore always hash real source bytes in ACP. Completed targets are not reread for hashing.
Ordinary targets, including LTFS files, are written to an exclusively created `.tmp_*` file beside the
final path. ACP completes metadata and signature-cache work, applies the target's sync policy, closes
the output, and renames it before reporting success against the final path. Restore transfers retain
`Overwrite(false)`, so ACP never replaces an existing Restore target. Failed copies clean up only their
owned temporary output and preserve existing destinations; cleanup failures remain alongside the copy
error without losing no-space error identities. Linear targets skip preallocation and per-file Sync;
successful LTFS unmount and the final Index still govern Tape publication. Staging adds no filesystem
transaction or power-loss durability guarantee.

ACP's managed signature xattr is a disposable cache keyed by size and nanosecond mtime, never an immutable identity or transfer-integrity proof. Scan's KNOWN_ONLY uses the cache-only read without hash fallback; FILL_MISSING hashes misses, and FORCE_READ reads every selected file. Verification always reads uncached content against old expected facts. The managed key is excluded from ordinary xattr copying. One descriptor serves an item for its whole lifecycle: the stored hash is read through the descriptor that reads the content, and a refresh is written through that same descriptor before it closes and before the result is delivered, which is why no asynchronous cache writer pool or queue exists. A failed cache write is counted, never a Job failure. Read-only/unsupported xattrs do not fail copying.

The Archive runner normalizes ACP and operating-system no-space errors before Finalize, preserving the
original copy error independently. Finalize performs physical cleanup and identity validation; Archive
then applies the returned facts, publishes verified successes and returns the original/normalized error.
[Jobs](jobs.md) defines these publication checkpoints; [E2E](../operations/e2e-test.md) defines physical
validation coverage.

## Copy Health

Position stores the latest health observation, check time and local checking Job ID. Detailed findings belong to Verify or Restore. Verification reads against the Job's frozen expected content, and [publication](../../internal/library/position_health.go) records the completed check after successful Session Finalize on the Media validated before access.

| Health | Evidence | Restore and subsequent changes |
| --- | --- | --- |
| Unknown | No usable completed verification baseline | Candidate by default, but actual transfer must verify; never counted as healthy |
| Healthy | Actual hash/size matched and Session finalized successfully | Candidate; a newer complete check can supersede the observation |
| Damaged | Complete read mismatched frozen expected content | Excluded by default; explicit salvage may retain complete mismatched output |
| Missing | A specific recorded path was absent during valid Media access | Not a candidate; expected content and version history remain |
| Unreadable | A specific path could not be read | Excluded by default; explicit salvage permits another read attempt |

Successful Archive writing does not manufacture a readback check or check timestamp. Inventory publication preserves health only for unchanged content and never accepts damaged bytes as a replacement baseline. Only actual successful reads on the expected Media after successful finalization can clear bad observations. Checks have no automatic expiry and do not claim continuous health. Whole-Media unavailability and canceled/unattempted entries do not mark individual copies bad. Export/import preserves historical observations tied to the same expected content; installation-specific Job links do not survive import.

Media access (unchecked/reachable/unavailable), lease state (free/owned/uncertain), and Position health are independent. Inspection observes Media identity/capacity, not bytes. Preparation acquires a lease; the attempt releases it after physical finalization and settlement. Uncertain device cleanup preserves the existing lifetime-level unavailable guard; it is not a Position corruption finding. No health transition deletes physical bytes or saved history.
