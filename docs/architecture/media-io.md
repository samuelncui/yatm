# Media Backends and File I/O

## Session Boundary

The [Backend](../../media/backend.go) is a stateless factory for Read/Write Sessions, accepting the Job `*gorm.DB` and typed protobuf target directly. Sessions prepare physical access, resolve paths, report capabilities, and finalize. Archive, Restore and Verify retain paging, ACP orchestration, item transitions and Library publication. ReadSession uses a neutral ReadMediaTarget with expected identity/profile; it never queries Restore `copies` or requires fake Restore tables for Verify. Tape expectations are checked before encryption/mount; Volume expectations precede path exposure.

`Inspect` is a zero-I/O snapshot getter on a prepared Session. Capabilities derive from immutable [Media profiles](../../entity/media.proto), not duplicate database flags:

| Profile | Read | Write |
| --- | --- | --- |
| HDD | Concurrent random | Concurrent random |
| HM-SMR | Concurrent random | Sequential |
| Tape | Sequential | Sequential |

Concurrent random access uses configured ACP concurrency, random access uses a single device thread, and sequential access uses ACP's linear behavior. Read access alone determines storage-order requirements and direct online-read eligibility. An [attempt lease](../../executor/device.go) exclusively reserves a Tape device or Volume UUID; these locks also prevent conflicting YATM operations, not just removable-media swaps.

## Tape

[Tape Sessions](../../executor/media_tape.go) identify the cartridge, configure encryption, format when requested, mount LTFS, and unmount/finalize. Every FORMAT, APPEND, and Restore Session requires a non-empty valid device barcode before encryption, formatting, or mounting; FORMAT and APPEND also require an exact match with the requested barcode. Device inspection may use its documented read-only manual identity fallback when automatic MAM lookup is unavailable, but a read/write Session never trusts that fallback. FORMAT rejects a barcode already in the Library; explicit metadata deletion is required before creating a new Media identity. APPEND requires compatible `ltfs_v1`, preserves Media ID/key/Positions, and chooses a short base36 timestamp prefix only to avoid path collisions.

The final captured LTFS Index is the physical fact source. [LTFS processing](../../media) retains backend extents in the typed storage-metadata envelope and stores opaque binary order from partition/block/offset. `ltfs_v0` is path-order compatibility; `ltfs_v1` has validated LTFS order/extents and supports append. Versioning belongs to the storage format, not a second independent version field.

Write Finalize matches staged files by actual Media path, size, and valid extents. On normal completion every staged result must match. On target-full it submits only the continuous verified prefix and resets the later suffix to PENDING. A successful normal unmount includes waiting up to ten minutes for the LTFS process to release the SG device and for `mt status` to report `DR_OPEN`; the Job attempt, device lease, and Job bundle remain owned throughout this wait. An unmount timeout or invalid/missing final Index publishes no unverified result. An uncertain device remains unavailable for that Executor lifetime. Force and lazy unmount are manual stopped-service recovery tools whose write result must be discarded. Ordinary LTFS files stay readable without a YATM-specific segmented format.

## Volume

[Volume Sessions](../../executor/media_volume.go) manage only already-mounted filesystems. [Volume discovery](../../media/volume.go) accepts configured roots or direct children, resolves the immutable `.yatm.json` UUID/profile, and rejects duplicate mounted identities, profile changes, symlink path components, and path escapes. An absent marker is skipped; a present but unreadable/invalid marker fails discovery, including checks separating originals from archive storage. YATM does not mount, unmount, or eject Volumes; current discovery validates the directory/marker rather than requiring an OS mount-point record.

[Initialize/Register](../../apis/volume.go) creates a marker and Media row or re-registers an existing marker after metadata-only deletion. Initialization failure removes only a marker created by that request. Runtime mounted/available-space state is not persisted as a Library fact.

Volume admission uses filesystem free space with reserve `max(1 GiB, filesystem capacity * 1%)`. Archive selects the deterministic pending prefix that fits the remaining allowance. Actual no-space errors are normalized by the Backend; capacity prediction remains advisory, with no independent `full` flag. Media file counts and last-write information derive from indexed physical Positions rather than duplicate summary columns.

Volume Finalize revalidates the marker and keeps successfully copied files eligible for publication, including after an ordinary copy error or ENOSPC. It never scans, rolls back, or deletes a directory. A prefix is an ordinary namespace, not a persistence or ownership boundary. Media Delete removes metadata only and leaves marker/files unchanged.

## ACP and Integrity

[ACP](https://github.com/samuelncui/acp) streams bounded buffers with backpressure. Bounded concurrent source preparation feeds one writer in original request order for linear targets; preparation failures and skipped requests advance the same ordering cursor. Random-access targets retain completion-order concurrency. Terminal target errors stop new source work while draining and closing bounded prefetched work.

Archive and Restore always hash real source bytes in ACP. Completed targets are not reread for hashing. Copying uses final target paths directly, without rename-based publication; linear LTFS targets skip per-file Sync because successful unmount and the final Index establish durability. ACP attempts cleanup of the explicitly failed target, not its containing directory, and preserves no-space errors even when partial-target cleanup also fails.

ACP's managed signature xattr is a disposable cache keyed by size and nanosecond mtime, never an immutable identity or transfer-integrity proof. Scan's KNOWN_ONLY uses the cache-only read without hash fallback; FILL_MISSING hashes misses, and FORCE_READ reads every selected file. Verification always reads uncached content against old expected facts. The managed key is excluded from ordinary xattr copying; asynchronous cache writes drain before ACP returns and their failures are warnings. Read-only/unsupported xattrs do not fail copying.

Write Finalize consumes the copy error to choose backend behavior, but returns backend cleanup, validation, and normalized capacity errors. The runner preserves the original copy error and publishes verified successes before returning it. [Jobs](jobs.md) defines these publication checkpoints; [E2E](../operations/e2e-test.md) defines physical validation coverage.

## Copy Health

Position stores the latest health observation, check time and local checking Job ID. Detailed findings belong to Verify or Restore; [guarded publication](../../library/position_health.go) checks unchanged expected content facts and prevents an older observation from replacing a newer one.

| Health | Evidence | Restore and subsequent changes |
| --- | --- | --- |
| Unknown | No usable completed verification baseline | Candidate by default, but actual transfer must verify; never counted as healthy |
| Healthy | Actual hash/size matched and Session identity finalized | Candidate; a newer complete check can supersede the observation |
| Damaged | Complete read mismatched frozen expected content | Excluded by default; explicit salvage may retain complete mismatched output |
| Missing | A specific recorded path was absent during valid Media access | Not a candidate; expected content and version history remain |
| Unreadable | A specific path could not be read | Excluded by default; explicit salvage permits another read attempt |

Successful Archive writing does not manufacture a readback check or check timestamp. Inventory publication preserves health only for unchanged content and never accepts damaged bytes as a replacement baseline. Only actual successful reads with final Media identity verification can clear bad observations. Checks have no automatic expiry and do not claim continuous health. Whole-Media unavailability and canceled/unattempted entries do not mark individual copies bad. Export/import preserves historical observations tied to the same expected content; installation-specific Job links do not survive import.

Media access (unchecked/reachable/unavailable), lease state (free/owned/uncertain), and Position health are independent. Inspection observes Media identity/capacity, not bytes. Preparation acquires a lease; normal Finalize releases it. Uncertain device cleanup preserves the existing lifetime-level unavailable guard; it is not a Position corruption finding. No health transition deletes physical bytes or saved history.
