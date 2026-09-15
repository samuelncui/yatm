# Browsing, Restore Association and Copy Integrity

Status: Superseded design and acceptance record, not a released v1 compatibility baseline. Maintained contracts belong to the [architecture](../architecture/overview.md) and [state reference](../architecture/states.md); current development acceptance is recorded in [Shared Files and Scan](files-and-scan.md).

## Interaction

Each browser root has a home action and a separate source menu. Compact single-line breadcrumbs fold intermediate ancestors into an accessible menu. Shared behavior belongs in the source-built Chonky extension. Backup and Restore use a Chonky selection list with cross-directory accumulation, selection-only drag/drop and automatic bounded, deduplicated metadata estimates. Unknown sizes are distinct from unknown signatures.

Settings owns Library visibility. Every Location can index originals; the preferred Restore destination flag only filters the chooser. Related Jobs use compact tables and canonical detail links. Catalog queries support kind, durable status and many-to-many Location/Media resource associations, with filtering before pagination and removals when a changed Job leaves a filter.

Media actions are contextual Properties, Volume Scan, selected-position admission, Check integrity and related Jobs. Properties and Scan do not imply content verification.

## Restore Association

Successful verified outputs automatically associate with the original File only when it has no FileLocation and the item is its frozen designated candidate. Among selected versions the candidate is the most recently archived version, then highest ID, with unknown dates last. Other items become independent Files; an unavailable or stale FileLocation still counts as an existing original. Candidate failure never promotes an older version implicitly.

Independent Files use the frozen original logical parent and `name.restored.<base36-version-id>.ext`, with numeric collision suffixes. They keep only the restored version's evidenced content and dates, not copied tags, notes or the entire source history. Physical outputs use the original name when free and the version suffix on conflict. Unowned equal content may be verified and adopted. Owned paths are never stolen; frozen-path conflicts require intervention.

Ignore controls indexing, not write authorization. Excluded outputs are restored without association and disclosed before copying. Interrupted reads use ACP failure cleanup. Fully read mismatched outputs may be retained only under the explicit damaged-copy option; they are not verified success or an association to the expected FileVersion.

Location binding tokens invalidate observations on root, Ignore, import and rebinding changes. A confirmed Location can publish an individually verified restored FileLocation without claiming a complete scan or changing its last full-scan time. File reads require a current per-file token and ordinary boundary/fact checks. Imported numeric IDs cannot retarget an old Job.

Library Restore results are keyed by operation UUID and item ID. The resulting File, FileLocation, ancestor index and successful business result publish in one metadata transaction before the Job checkpoint. Repeated checkpoints reuse that result. File I/O remains outside Library transactions; failed linking preserves bytes. The target Location operation gate covers each actual attempt, not waiting for Media. Legacy legacy jobs without a trusted registered target do not guess original associations.

## Verify and Damaged Recovery

An independent Verify Job freezes one Media's identity/profile and expected ordinary Positions in a bounded Job manifest. Volume and Tape sessions retain their identity checks, leases, storage order and finalization. ACP hashes actual source bytes without a signature-cache shortcut. Read Sessions do not depend on Restore-specific tables.

Findings distinguish healthy, damaged, missing, unreadable and unavailable verification baselines. A whole-Media access failure is not evidence that every copy is damaged. Finalized observations publish only against unchanged expected Position facts. Expected hashes are never replaced by damaged observations. Detailed findings belong to the Job; the latest copy-health observation belongs to Library.

Restore normally permits healthy or unchecked candidates, validating actual bytes. The frozen advanced `allow_damaged_copies` option additionally permits deliberate damaged/unreadable-copy recovery, preferring normal candidates. Complete mismatched outputs are retained and labeled `Recovered with damage`; interrupted prefixes are not retained. No ACP extension, bad-block skipping, repair, formatting or physical deletion is included.

## Acceptance

- Shared root navigation, long paths, responsive spacing, waitlist multi-selection and deduplicated estimates.
- New Location partial restored observations, binding invalidation, Ignore output and legacy destination boundaries.
- Existing/stale originals, multiple versions, stable logical/physical names, path ownership and post-Library checkpoint retry.
- Healthy/mismatched/missing/unreadable/unverifiable copies, cancellation, changed Media and stale inventory publication.
- Real CLI business E2E, fresh reset Demo, Go/vet/race and both SQLite drivers, generated-client coverage and frontend checks.
- Isolated Volume/Preview and isolated Linux LTFS file-backend only; no physical Tape tests. legacy validation uses copies of the approved source backups, never production or source backups.

Only legacy compatibility is required during the temporary v1 Draft period. No commits, pushes or releases are automatic. Runtime behavior, state tables and operational commands move to their owning architecture and operations documents after verification.

### Verification Status

- Passed: full Go tests/vet, affected race checks, both SQLite drivers, repeatable IDL generation, public CLI coverage and the seven local CLI E2E scenarios.
- Passed: frontend format/lint/type/build and interaction tests, explicit-reset Demo preparation and read-only CLI smoke checks, including partial restored observations, Verify results and duplicate groups.
- Passed: official LTFS file-backend archive/append/capacity/Restore/Verify on Linux; no physical Tape used.
- legacy prepare-stage copies were audited item-by-item, including every non-empty historical Job. Two size-conflicting candidates were explicitly excluded with alternate copies retained. Activation and actual historical-content reads are separate gates, not implied by metadata comparison.
- Pending: unlocked-browser screenshots and responsive visual acceptance. Keep this acceptance record Draft until that gate is completed; then archive it and retain navigation to the owning contracts.
