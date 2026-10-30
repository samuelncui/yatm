# E2E Testing

Status: Maintained validation for the current v1 development line. See the [development checks](testing.md) before selecting a host or fixture. Actual release results belong to the release workflow, not this procedure.

Primary business acceptance uses actual `yatm-cli` subprocesses and production HTTP/gRPC-Web transport. The [complete installation workflows](../../e2e/workflows_test.go) and [indexing recovery workflows](../../e2e/indexing_cli_test.go) also start the actual `yatm-httpd` binary. Development runs build both binaries from the checkout; release acceptance sets `YATM_E2E_BIN_DIR` to the extracted candidate directory and uses those programs unchanged. Each test owns isolated configuration, databases, source files, output directories and loopback listeners. No production installation or Tape device is used by local tests.

Local/CI Mounted Volume, Preview-policy and LTFS fixtures use the same CLI process adapter, with an in-process server only where generator configuration, finalization faults or detailed checkpoint assertions require it. The adapter has explicit command mappings and **no direct-RPC fallback**. Direct filesystem writes represent external changes, not hidden catalog setup. Public registration, scanning, selection expansion, archive, restore, annotation, import/export and Job mutations go through CLI commands. Remote package acceptance instead uses only packaged application binaries and [local SSH orchestration](../release/package-acceptance.md#remote-packaged-binary-acceptance).

New primary capabilities require a CLI command, a repeatable CLI E2E scenario and an entry in the matrix below. Move existing CLI-expressible business steps to subprocesses instead of keeping a parallel private-service workflow. Command availability and RPC coverage tests do not prove end-to-end acceptance. Retain unit/integration tests for bounded traversal, failed Library/Job commits and supported recovery boundaries; CLI smoke tests cannot replace those checks.

## CLI Acceptance Matrix

The scenario names below are executable tests, not a release claim. Record actual run results during delivery; Linux-only LTFS and explicitly assigned physical scratch cartridges remain separate gates.

| Capability | CLI commands | Scenario | Key assertions | Platform |
| --- | --- | --- | --- | --- |
| Service and access configuration | `status`, `settings access/browse`, `location create/get/update/list` | `TestCLIRegisteredLocationWorkflows`, `TestCLIIndexFailureCancellationAndVisibility` | Actual binaries, authorized roots, recommended/non-recommended targets, verbatim Ignore hiding browsed entries while explicit access remains, revisions | macOS/Linux |
| Catalog search | `location list --query --after-id`, `media list --query --after-id` | `TestCLICatalogSearch` | Actual server/CLI binaries, literal case-insensitive name/path/identity search, kind/preference filters before keyset pagination, missing identities and no matches | macOS/Linux |
| Live Location admission and analysis | `ls`, `analyze create/progress/entries`, `settings library` | `TestCLILiveAdmissionAnalyzeAndArchive`, `TestLocationAnalyzeReadArchive` | Unadmitted real entries, explicit/basic collection, stable identity, optional Preview isolation; browsing, registration and settings create no collection Job | macOS/Linux |
| Failed/cancelled analysis | `analyze create --mode force/basic --path`, `job cancel/wait` | `TestCLIIndexFailureCancellationAndVisibility`, `TestCLILiveAdmissionAnalyzeAndArchive` | Unavailable roots return browsing errors but retain Library records; real sparse-file cancellation does not publish; earlier completed Locations survive a later Location failure; Scan failure/cancellation is terminal `FAILED` and a new Job rescans the whole selection | macOS/Linux |
| Library visibility and organization | `settings library`, `ls`, `files get/metadata`, `mkdir/mv` | `TestCLIRegisteredLocationWorkflows`, `TestCLIIndexFailureCancellationAndVisibility` | Explicit false preference, all/saved/default scopes, independent logical rename/move, tags/Note retained, direct-ID access unaffected | macOS/Linux |
| Explicit Data Usage | `du --file-id/--location-id --query`, `du --recursive` | `TestMeasureThroughCLIProcessAndRealFilesService`; `TestMeasure*` API tests; `file-measure.test.tsx` | Real CLI subprocess and Files service; all-page roots/full directory contents, overlap deduplication, stored/latest-version fallback, Location Ignore exclusion and dot files, link exclusion, bounded query cost, partial-result exit, icon-only toolbar, cancellation and refresh invalidation | macOS/Linux |
| Preview preferences | `settings preview --preview-json` | `TestCLILibraryPreviewSettings` | Actual server/CLI, complete defaults, persisted typed generator settings and independent preference edits | macOS/Linux |
| Job execution preferences | `settings job --execution-json` | `TestCLIJobExecutionSettings`; `TestSettingsJobExecutionDefaultsRoundTripAndRejections` | CLI JSON flag wiring; default batch/buffer/write-batch/flush limits for an absent group; a persisted group survives an unrelated edit; malformed JSON and rejected limits, including a write batch above its buffer | macOS/Linux |
| Duplicate lookup | `files duplicates` | `TestCLIRegisteredLocationWorkflows` | Signature lookup returns independently organized Files without merging organization, unaffected by the saved-only preference | macOS/Linux |
| Identical-file review and cleanup | `identical find/groups/members/rows/positions/close/merge/keep`, `files remove-version` | `TestCLIIdenticalHistoryAndLocationKeep` | Historical A{x,y}, B{y,z}, C{z} form one complete group; an explicit Find result can be paged and queried by file position after Merge without rerunning Find; current Location groups retain one explicit survivor and Trash the others; removing a version leaves archived bytes untouched | macOS/Linux |
| Shared Files query and Scan policies | `ls`, `files get/metadata`, `mkdir/mv`, `scan create/results/progress`, `job log/log-lines/progress` | `TestCLISharedFilesQueriesAndMerges` | Same tag/Note query in both views, pure live pagination, invalid query rejection, both directory merge adapters, known-only miss and fill-missing read, raw and filtered Scan log lines, frozen completed elapsed time | macOS/Linux |
| Shared Library/Location organization | `ls`, `mkdir/mv/rm`, `files metadata/locate-original`, `job list`, `settings library` | `TestCLIPhysicalLocationOperations` | Named operations and JSON Lines results; logical edits leave disk unchanged, physical rename retains logical path/Note, logical Trash preserves originals, physical Trash retains bytes and File metadata, relink continuity and zero Jobs | macOS/Linux |
| Selection review and cross-directory Archive | `archive estimate/create` | `TestCLIRegisteredLocationWorkflows`, `TestCLILiveAdmissionAnalyzeAndArchive` | Real unadmitted files and edited Library originals need no Analyze prerequisite; all selected paths precede identity fallback, mixed roots, case-sensitive counts, overlap deduplication and frozen logical targets | macOS/Linux |
| Volume candidates and archive | `volume candidates/initialize/register`, `archive write volume/files`, `files get/versions/copies` | Complete workflow, Volume and Location tests | Candidate states and probed serial, verified bytes, independent same-content Files, shared saved content, new versions only after changed content is saved | macOS/Linux |
| Cross-directory and multi-version Restore | `restore create/run volume/files/media` | `TestCLIRegisteredLocationWorkflows`, `TestVolumeArchiveRestoreScan`, Location test | Explicit Location/subdirectory, two versions of one File, version suffixes, equal content adopted after verification, independent output association without replacing originals | macOS/Linux |
| Recorded-time Restore selection | `restore estimate --before`, `restore create --before --skip-unmatched-versions`, `restore run volume/files` | `TestCLIRestoreRecordedTimePolicy` | Real A/B/A/C/A archived saves retain the middle A save, inclusive cutoff, directory overlap, nested custom version overrides its directory's automatic choice, later-only File remains unmatched until explicit skip, frozen manifest and actual restored bytes | macOS/Linux |
| Integrity observations | `verify create/run/entries`, `job wait/progress`, `files copies` | `TestCLIIntegrityVerification` | Actual reads, healthy/tampered/missing results, health publication, unchanged expected facts, and manifest paging forward by cursor, backward by descending cursor and from an explicit offset with a requested result-set total | macOS/Linux |
| Damaged-copy recovery | `restore estimate --allow-damaged-copies`, `restore create --allow-damaged-copies`, `restore run volume/files` | `TestCLIDamagedCopyRecovery` | Default rejection, explicit salvage retains mismatched complete bytes, damaged result is not verified or linked | macOS/Linux |
| Automatic Media inventory | `scan media/results`, `job wait/progress` | `TestVolumeArchiveRestoreScan` | Added/changed/removed inventory publishes without Apply; force-read detects metadata-preserving changes without relabeling unchanged siblings | macOS/Linux |
| Inventory admission and retention | `files import-positions`, `library trim`, `media delete`, `volume register` | `TestVolumeArchiveRestoreScan` | Explicit saved-File admission, archived retention, metadata-only deletion, unchanged marker and original bytes | macOS/Linux |
| HTTP resource boundary | Preview asset reads, Library JSONL export/import and removed file-byte routes | API and complete-installation tests | Preview and metadata backup remain accessible; original, Location and Position content routes return 404 for GET/HEAD | macOS/Linux |
| Preview generation/update | `preview capabilities`, `preview create`, `job wait`; `archive create --preview-policy`; Preview HTTP reads | `TestPreviewGenerationPolicy`, LTFS archive test | Real native helper capability/admission, missing-only preservation and regenerate-all replacement; Archive's companion uses frozen SCAN input; generated asset is readable | macOS/Linux; LTFS Linux |
| Export/import | `library export/import` | Complete-installation tests; LTFS archive test | Version 1 metadata without Settings despite view filtering, tags/Note, imported roots immediately usable, legacy whole-object import | macOS/Linux; LTFS Linux |
| Job navigation data and lifecycle | `job list/changes/get/log/wait/delete` | Complete-installation tests and Volume test | Location/Media resource history, kind and target filters, machine-readable results, independent Scan Jobs, explicit deletion | macOS/Linux |
| LTFS append/finalization/capacity/integrity | `archive write tape format/append`, `restore run tape`, `verify create/run/entries`, `media inspect/delete` | `TestLTFSArchiveRestore`, `TestLTFSFullTapeSpansMediaAndRestores` | Final-Index/unmount boundaries, continuous prefix, two-Media restore, storage order, actual Tape reads and healthy observations | Linux, official file backend only |
| Physical Tape release gates | Same CLI commands, separately documented explicit fault probes | [Physical Tape E2E](../release/physical-tape-e2e.md) | Hardware identity, append, restart restore, end-of-Media evidence | Explicitly assigned scratch hardware only |

Preview assets and Library JSONL backup use HTTP; full original or archived file bytes do not. Business setup remains CLI-driven. Physical barcode-mismatch acceptance checks the public CLI refusal; backend Session tests separately check rejection before encryption, formatting or mounting. The [physical suite](../release/physical-tape-e2e.md#pt-04-reject-a-different-requested-barcode) records that coverage boundary.

Run the full suite with Tape opt-ins unset for ordinary local acceptance:

```bash
go test -tags=e2e -count=1 -v ./e2e
```

The CLI contract checks are separate and inexpensive:

```bash
go test ./cmd/yatm-cli
go vet ./cmd/yatm-cli
go test -race ./cmd/yatm-cli
CGO_ENABLED=0 go test ./cmd/yatm-cli
go vet -tags=e2e ./e2e
```

File-operation transport tests additionally cover partial-result exit codes, absent final summaries and request-deadline cancellation through real gRPC-Web streams. Backend semantic tests retain frozen-delete manifests, path/identity conflicts, protected roots, bounded preparation, cancellation and physical-success/publication-failure boundaries. No private service call substitutes for the public CLI workflow above.

`job wait` returns the last observed Job as JSON on stdout, including when a later network poll fails or times out. It succeeds when the durable status is completed and the runner phase is completed or unspecified after detachment; a required Media or other operator decision exits nonzero with `action_required` on stderr. A bounded wait timeout uses `deadline_exceeded`; no successful observation means no Job JSON. It never selects Media, starts another attempt, formats or cancels automatically. Per-request `--timeout` and total `--wait-timeout` are independent.

The [integrity CLI tests](../../e2e/integrity_cli_test.go) supplement semantic tests for Media finalization failure, missing baselines, cancellation, unreadable entries, guarded health publication and explicit Restore publication failures. Verify and Restore must not fabricate successful catalog observations when only physical bytes were produced. Resource-filter departures are covered by catalog change-feed tests; UI tests cover restoring filters and navigation.

## Mounted Volume

The Volume test uses an ordinary temporary directory as an already-mounted offline disk. It runs the real ACP copy flow and verifies:

1. HDD Volume initialization, marker identity, and capabilities.
2. Archive to the Volume and publication of physical files and Library Positions.
3. Restore from the Volume with byte-for-byte and SHA-256 verification.
4. Automatically published cached Scan differences for added, changed, and removed files.
5. Force-rehash Scan detection of a content change whose size, mode, and modification time are unchanged.
6. Metadata-only Media deletion without changing the marker or physical files.
7. Register of the unchanged marker followed by an explicit Scan that rebuilds Positions.

Run it on macOS or Linux with the matching optional helper extracted alongside its private libraries:

```bash
go test -tags=e2e -v ./e2e -run TestVolumeArchiveRestoreScan
```

All files, databases, and Job directories live under the test temporary directory and are removed after the test.

## Preview Update Policy

The Preview policy test runs the optional SCAN Preview stage against one shared content-addressed bundle. Missing-only preserves an existing bundle even when generator settings change; regenerate-all replaces it. Manager tests additionally prove regeneration with identical settings and replacement of damaged derivatives. Analyze, Preview and Verify CLI commands are convenience presets for ScanJobService, not separate Job kinds or execution paths.

Run it on macOS or Linux with:

```bash
YATM_TEST_PREVIEW_HELPER=/absolute/path/to/package/yatm-preview \
  go test -tags=e2e -v ./e2e -run TestPreviewGenerationPolicy
```

Without a helper this native integration test explicitly skips; a skip is not release acceptance. The test enables generation through the public Settings CLI before creating its Scan. Manager tests cover malformed helper output, live enablement, bounded concurrency, cancellation and publication failures. Nested `previewworker` tests cover sparse keyframe timestamps, deduplication, geometry and supported encoders. Native runtime/benchmark and packaged installation remain separate evidence from these local fault tests.

## Locations and File Versions

The isolated [Location E2E](../../e2e/location_test.go) registers a root, scans through CLI subprocesses, creates Library-selected Archive input, and writes/verifies a real mounted Volume. It checks independent same-content Files, shared-copy publication creating each observed File's saved version, later original edits retaining that version, Restore using the second File's own path/mode/mtime, live pagination and metadata-only unregistration. Preview failure isolation has separate Scan/API/Archive semantic coverage.

The [live workflow](../../e2e/live_analysis_cli_test.go) starts the real server and archives an unadmitted Location file without prior Scan. It checks path-first continuity across live selection roots, complete selected-Location publication, fail-fast behavior and a new full-selection Scan after failure, and absence of collection on settings changes. Pure browsing remains unadmitted until explicit Scan or an authorized annotation/Archive operation. The [complete workflow](../../e2e/workflows_test.go) also archives edited Library originals without preparatory analysis. [Queued CLI acceptance](../../e2e/queued_cli_test.go) uses a controlled resource holder to check real CLI waiting and cancellation without copying. Acceptance requires admitted Archive Media cancellation to return to pre-Media `READY` with its reason and prepared manifest, followed by another explicit Media choice after the resource becomes available. This continues Media work without restarting the Job. [Resource queueing](../../internal/executor/resource_queue_test.go) separately covers FIFO handover and cancellation at the lease boundary.

```bash
go test -tags=e2e -v ./e2e -run 'Test(LocationAnalyzeReadArchive|CLILiveAdmissionAnalyzeAndArchive|CLIQueuedMediaCancellationAndExplicitRestart)$'
```

Library, Scan, Archive, API, CLI, and Demo unit tests cover the larger failure/compatibility matrix. Shared engine contract tests cover object-store prefixes, partial outcomes and no native rename; Scan tests cover cache-only zero-read, Media finalization, frozen verification baselines and safe Preview handling of damaged bytes. Local tests do not replace LTFS regression when physical I/O changes.

## LTFS File Backend

The LTFS test treats a directory as a virtual tape. It does not require a physical LTO device, but it runs the real `mkltfs`, LTFS/FUSE mounts, ACP copies and hashes, SCAN Preview outcomes, Job DB checkpoints, Library commits, and CLI business operations. The PNG fixture uses the current typed Preview Settings and real native helper, supplied with `YATM_TEST_PREVIEW_HELPER`.

Select the installed LTFS capture syntax before launching native tests or the package controller. The shared adapter defaults
to `capture_index=<directory>`; older capture-enabled builds use
`YATM_E2E_LTFS_CAPTURE_INDEX=bare`. The
[file-backend adapter guide](../../e2e/ltfs-file-backend/README.md) owns that test-only choice.

A second test sets the official file backend `capacity_mb` cartridge property to 3072 MiB and writes three 1200 MiB files. LTFS exposes the configured remaining capacity through the mounted filesystem, so ACP's pre-write `statfs` guard stops the first virtual Tape before the next complete file. Acceptance requires the failed Archive Media operation to return the Job to its pre-Media `READY` state with its failure reason/timing and no live phase, continuous-prefix publication, pending item suffix, exact Position set, progress and report totals, completion on a second Media, and Restore across both Media. The prepared manifest and submitted prefix remain for another explicit load-Media operation. Every failure before attempt admission leaves the existing state unchanged; admitted Scan/Index errors or cancellation settle as terminal `FAILED`, while admitted Archive/Restore Media errors or operator cancellation, including while `QUEUED`, return to pre-Media `READY` with successful per-file/Media checkpoints preserved. The [Job lifecycle](../architecture/jobs.md#shared-lifecycle) owns these boundaries.

### Flow

1. Create isolated Executor and Library SQLite databases, work, source, restore, and virtual-tape directories.
2. Register and analyze a Location, then create an Archive Job through CLI with Preview generation enabled and files that span multiple LTFS records.
3. Explicitly format the virtual tape with `mkltfs -e file -r 'size=512k'`, mount it, and let ACP write files sequentially. The small PNG goes to the Index partition; binary files above 512 KiB go to the Data partition. Size-only placement also applies to ACP's temporary output names.
4. Inject one reported unmount error after LTFS has stopped. Verify that the Archive Media operation returns the Job to pre-Media `READY` with its failure reason/timing and no live phase, retains its prepared manifest with every item `PENDING`, the Library has no Tape checkpoint, and the uncertain virtual device remains unavailable for that Executor lifetime.
5. Load Media for the same Job and barcode on a fresh virtual device, corrupt the post-unmount Index, and verify that the failed operation again returns the Job to pre-Media `READY` with its failure reason/timing and no live phase, retaining its prepared manifest with every item `PENDING` and no Library checkpoint while the successfully unmounted device remains usable.
6. Run the Media step once more, verify that the stale Index is cleared, then verify index- and data-partition placement, the 17-byte partition/block/offset storage order persisted on each Position, the Preview-enabled SCAN, content-addressed ZIP, HTTP-served asset, Archive manifest, `tapes/<barcode>/`, canonical signature xattrs after remount, and Library Media.
7. Inspect the inserted Tape through the Media API, then create a second Archive Job and append it to the same cartridge while retaining the Media ID.
8. Export and re-import the Library using the [Library JSONL format](../architecture/persistence.md#published-data-formats), including FileVersion content, tags and notes; legacy whole-object backups remain readable through their explicit adapter.
9. Import the same Library into an isolated compatibility database in the legacy whole-object JSON format and verify the rebuilt Position directory index.
10. Create a Restore Job through CLI with a registered restore target and restore both Archive Jobs from the same virtual tape.
11. Verify every restored file and SHA-256, run Check integrity against the same virtual Tape through CLI, inspect all healthy findings and their catalog health observations, then explicitly delete the old Media metadata and format the barcode for a third Archive Job. Logical Files remain.
12. Delete all Jobs, including source analysis, through CLI.

Virtual tapes, databases, and Job directories live under the test temporary directory and are removed after the test.

### Environment

The test requires Linux, FUSE 2, and an LTFS build with the `file` backend:

```bash
command -v mkltfs ltfs fusermount
ls -l /dev/fuse
```

Run this source-based test locally or in an authorized Linux CI harness environment:

```bash
YATM_E2E_LTFS=1 YATM_TEST_PREVIEW_HELPER="$PREVIEW_HELPER" \
  go test -tags=e2e -v ./e2e -run 'TestLTFS(ArchiveRestore|FullTapeSpansMediaAndRestores)'
```

The command above is not the remote package-acceptance procedure. On the isolated host, replay its public CLI scenarios using [packaged binaries and local orchestration](../release/package-acceptance.md#remote-packaged-binary-acceptance); retain internal checkpoint/fault assertions in the local/CI tests.

Regular `go test ./...` runs do not include the tagged E2E suite. Physical-media behavior remains covered by the separate physical Tape E2E suite before release.

Storage-order coverage is intentionally split at stable boundaries: the LTFS parser tests the encoded partition, block, and offset; the real file-backend E2E tests the captured Index, persisted Position, and request-ordered physical layout within each partition; and the Restore tests verify exact Position-to-Copy propagation plus sequential request order across index and data partitions. This avoids adding a test-only hook to the production ACP stream.

Full-Media handling is mandatory at three levels. ACP's Linux `/dev/full` integration test verifies an actual write-time `ENOSPC`, while its disk-usage test verifies the conservative pre-write boundary. Archive tests verify pending item state. The LTFS capacity E2E then verifies the complete YATM finalization and publication flow, including the stable `archive_media_checkpoint`/`no_space` Job-log event, using the official file backend. The physical suite additionally requires one real scratch cartridge to reach its full-Media boundary and validates the finalized prefix and pending suffix; continuing that Job on a second physical cartridge is optional.
