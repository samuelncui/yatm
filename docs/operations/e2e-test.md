# E2E Testing

Status: Maintained validation for v1 Alpha and subsequent development. See [test environment safety](testing.md) before selecting a host or fixture and [Alpha acceptance](../releases/v1.0.0-alpha.1.md) for the recorded release scope.

Primary business acceptance uses actual `yatm-cli` subprocesses and production HTTP/gRPC-Web transport. The [complete installation workflows](../../e2e/workflows_test.go) and [indexing recovery workflows](../../e2e/indexing_cli_test.go) also start the actual `yatm-httpd` binary. Development runs build both binaries from the checkout; release acceptance sets `YATM_E2E_BIN_DIR` to the extracted candidate directory and uses those programs unchanged. Each test owns isolated configuration, databases, source files, output directories and loopback listeners. No production installation or Tape device is used by local tests.

Mounted Volume, Preview-policy and LTFS fixtures use the same CLI process adapter, with an in-process server only where generator configuration, finalization faults or detailed checkpoint assertions require it. The adapter has explicit command mappings and **no direct-RPC fallback**. Direct filesystem writes represent external changes, not hidden catalog setup. Public registration, scanning, selection expansion, archive, restore, annotation, import/export and Job mutations go through CLI commands.

New primary capabilities require a CLI command, a repeatable CLI E2E scenario and an entry in the matrix below. Move existing CLI-expressible business steps to subprocesses instead of keeping a parallel private-service workflow. Command availability and RPC coverage tests do not prove end-to-end acceptance. Retain unit/integration tests for concurrency, bounded traversal, failed Library/Job commits and crash/retry boundaries; CLI smoke tests cannot replace those checks.

## CLI Acceptance Matrix

The scenario names below are executable tests, not a release claim. Record actual run results during delivery; Linux-only LTFS and explicitly assigned physical scratch cartridges remain separate gates.

| Capability | CLI commands | Scenario | Key assertions | Platform |
| --- | --- | --- | --- | --- |
| Service and access configuration | `status`, `settings access/browse`, `location create/get/update/list` | `TestCLIRegisteredLocationWorkflows`, `TestCLIIndexFailureCancellationAndVisibility` | Actual binaries, authorized roots, recommended/non-recommended targets, verbatim Ignore, revisions | macOS/Linux |
| Catalog search | `location list --query --after-id`, `media list --query --after-id` | `TestCLICatalogSearch` | Actual server/CLI binaries, literal case-insensitive name/path/identity search, kind/preference filters before keyset pagination, missing identities and no matches | macOS/Linux |
| Live Location admission and analysis | `location entries`, `analyze create/progress/entries`, `settings library --auto-collect` | `TestCLILiveAdmissionAnalyzeAndBackup`, `TestLocationAnalyzeReadArchive` | Unadmitted real entries, explicit/basic collection, stable identity, optional Preview isolation and collection toggle Jobs | macOS/Linux |
| Failed/cancelled analysis | `analyze create --mode force/basic --path`, `job cancel/retry-index/wait` | `TestCLIIndexFailureCancellationAndVisibility`, `TestCLILiveAdmissionAnalyzeAndBackup` | Unavailable roots return browsing errors but retain Library records; real sparse-file cancellation does not publish; successful selected scopes survive another scope's failure | macOS/Linux |
| Library visibility and organization | `settings library`, `file search/get/mkdir/edit/metadata` | `TestCLIRegisteredLocationWorkflows`, `TestCLIIndexFailureCancellationAndVisibility` | Explicit false preference, all/saved/default scopes, independent logical rename/move, tags/Note retained, direct-ID access unaffected | macOS/Linux |
| Duplicate content | `file duplicate-groups/duplicate-members` | `TestCLIRegisteredLocationWorkflows` | Separate content groups and independent member cursors, unaffected by saved-only preference | macOS/Linux |
| Shared Files query and Scan policies | `files list/get/metadata`, `fileops run`, `scan create/results`; guarded Open HTTP request | `TestCLISharedFilesQueriesAndMerges` | Same tag/Note query in both views, pure live pagination, invalid query rejection, guarded content access, both directory merge adapters, known-only miss and fill-missing read | macOS/Linux |
| Shared Library/Location organization | `location entry/admit`, `fileops run`, `file mkdir/edit/delete/metadata/locate-original`, `job list`, `settings library` | `TestCLIPhysicalLocationOperations` | Shared Execute JSON Lines and resulting File IDs; logical edits leave disk unchanged, physical rename retains logical path/Note, logical Trash preserves originals, physical deletion retains File metadata, external-copy independence, explicit deletion confirmation, relink continuity and zero Jobs | macOS/Linux |
| Selection review and cross-directory Backup | `file inspect-selection`, `archive create` | `TestCLIRegisteredLocationWorkflows`, `TestCLILiveAdmissionAnalyzeAndBackup` | Real unadmitted files and edited Library originals need no Analyze prerequisite; all selected paths precede identity fallback, mixed roots, case-sensitive counts, overlap deduplication and frozen logical targets | macOS/Linux |
| Volume archive and versions | `volume initialize`, `archive write volume/files`, `file state/versions/copies` | Complete workflow, Volume and online tests | Verified bytes, independent same-content Files, shared saved content, new versions only after changed content is saved | macOS/Linux |
| Cross-directory and multi-version Restore | `restore create/run volume/files/media` | `TestCLIRegisteredLocationWorkflows`, `TestVolumeArchiveRestoreScan`, online test | Explicit Location/subdirectory, two versions of one File, version suffixes, equal content adopted after verification, independent output association without replacing originals | macOS/Linux |
| Recorded-time Restore selection | `file inspect-selection --restore --before`, `restore create --before --skip-unmatched-versions`, `restore run volume/files` | `TestCLIRestoreRecordedTimePolicy` | Real A/B/A/C/A backups retain the middle A save, inclusive cutoff, directory overlap, nested custom version overrides its directory's automatic choice, later-only File remains unmatched until explicit skip, frozen manifest and actual restored bytes | macOS/Linux |
| Integrity observations | `verify create/run/entries`, `job wait/progress`, `file copies` | `TestCLIIntegrityVerification` | Actual reads, healthy/tampered/missing results, health publication, unchanged expected facts | macOS/Linux |
| Damaged-copy recovery | `file inspect-selection --allow-damaged-copies`, `restore create --allow-damaged-copies`, `restore run volume/files` | `TestCLIDamagedCopyRecovery` | Default rejection, explicit salvage retains mismatched complete bytes, damaged result is not verified or linked | macOS/Linux |
| Automatic Media inventory | `scan media/results`, `job wait/progress` | `TestVolumeArchiveRestoreScan` | Added/changed/removed inventory publishes without Apply; force-read detects metadata-preserving changes without relabeling unchanged siblings | macOS/Linux |
| Inventory admission and retention | `file import-positions`, `library trim`, `media delete`, `volume register` | `TestVolumeArchiveRestoreScan` | Explicit saved-File admission, archived retention, metadata-only deletion, unchanged marker and original bytes | macOS/Linux |
| Content access | Guarded Open HTTP GET/HEAD/Range requests | Online, shared Files and Volume tests | Expected identity/revision and safe response types; original and mounted-copy bytes without Restore; no file Download CLI command | macOS/Linux |
| Preview generation/update | `scan create --preview-policy`, `preview create`, `job wait`; `archive create --preview-policy`; Preview HTTP reads | `TestPreviewGenerationPolicy`, LTFS archive test | Missing-only preserves existing bundles; regenerate-all replaces them; Archive's companion uses a frozen SCAN input; generated asset is readable | macOS/Linux; LTFS Linux |
| Export/import | `library export/import`, `location confirm` | Complete-installation tests; LTFS archive test | Full metadata despite view filtering, tags/Note, unconfirmed imported roots, legacy whole-object import | macOS/Linux; LTFS Linux |
| Job navigation data and lifecycle | `job list/changes/get/log/wait/delete` | Complete-installation tests and Volume test | Location/Media resource history, type/state filters, machine-readable outcomes, independent Scan Jobs, explicit deletion | macOS/Linux |
| LTFS append/finalization/capacity/integrity | `archive write tape format/append`, `restore run tape`, `verify create/run/entries`, `media inspect/delete` | `TestLTFSArchiveRestore`, `TestLTFSFullTapeSpansMediaAndRestores` | Final-Index/unmount boundaries, continuous prefix, two-Media restore, storage order, actual Tape reads and healthy observations | Linux, official file backend only |
| Physical Tape release gates | Same CLI commands, separately documented explicit fault probes | [Physical Tape E2E](physical-tape-e2e.md) | Hardware identity, append, restart restore, end-of-Media evidence | Explicitly assigned scratch hardware only |

Open and Preview use HTTP in the browser, with no separate Download command. Their content bytes, Range and response headers are tested through those endpoints; business setup remains CLI-driven. The physical barcode-mismatch probe deliberately sends the typed RPC after the CLI preflight boundary to test the runner's independent identity check. These are explicit boundary tests, not alternative business entry points.

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

`job wait` returns the last observed Job as JSON on stdout, including when a later network poll fails or times out. Both durable status and runner phase must be completed before it exits successfully; a retry/media decision exits nonzero with `action_required` on stderr. A bounded wait timeout uses `deadline_exceeded`; no successful observation means no Job JSON. It never selects Media, retries, formats or cancels automatically. Per-request `--timeout` and total `--wait-timeout` are independent.

The [integrity CLI tests](../../e2e/integrity_cli_test.go) supplement semantic tests for final Media identity failure, missing baselines, cancellation, unreadable entries, guarded health publication and Restore receipts. Verify and Restore must not fabricate successful catalog observations when only physical bytes were produced. Resource-filter departures are covered by catalog change-feed tests; UI tests cover restoring filters and navigation.

## Release Candidates and Upgrades

Builds use Go 1.26.8, Node 24 and the committed pnpm lockfile. The [candidate workflow](../../.github/workflows/build.yml) requires all ten targets, validates the combined archive set, runs offline identity checks and the entire E2E package against the extracted Linux programs, and uploads only accepted bytes to the matching Release. Hosted CI skips opt-in LTFS and physical Tape tests; release acceptance additionally runs the full suite on an isolated Linux host with the official LTFS file backend enabled.

```bash
node --test build_documents.test.mjs check_release.test.mjs
go test ./cmd/migrate ./internal/dataformat ./migrate/legacy
bash -n install-release.sh build.sh build_backend.sh build_frontend.sh
```

Set `YATM_E2E_BIN_DIR` to an extracted candidate to run the full suite above without rebuilding its programs. Run it without a `-run` filter on each supported acceptance host, with `YATM_E2E_LTFS=1` on the LTFS host and physical Tape opt-ins unset unless scratch hardware is assigned. Record every pass, failure and skipped test with its reason. Use a temporary filesystem supporting user xattrs; the full-capacity LTFS fixture needs at least 12 GiB of free space for its source, virtual Media and restored bytes.

A missing or non-executable candidate fails explicitly. Package checks reject altered checksums, missing programs, mixed identities, active configuration, development dependencies and hidden filesystem metadata. Packaged guides retain local links; source references resolve at the matching public tag.

Installer semantic tests cover read-only checks, version ordering, cancellation, file ownership and failure boundaries. Actual systemd acceptance uses unique `--install-dir` and `--service` values on an isolated Linux host with candidate `--archive` and `--checksum` inputs. Exercise fresh install, same-version rerun, compatible update, public legacy migration, declined upgrade/report, readiness failure and complete-backup recovery. Compare config/script/unit hashes, catalog contents and original service state at each boundary; remove only the test's own unit registration afterward.

Skill acceptance uses an isolated ordinary-user home and the pinned public Skills CLI from [installation](install.md#optional-agent-skill). Test the real interactive picker and existing-target cancel/replace, repeat through sudo from a root working directory, and exercise missing dependencies, non-TTY input and source-read failure. Compare installed copy hashes and ownership; cancellation must preserve existing content and optional failure must leave YATM installed.

Real legacy archive acceptance uses an approved timestamped copy, input checksums, independent Library/Job expectations and full CLI import/export roundtrip. Preserve the phase reports and every historical-item comparison privately. Record absent fixture categories explicitly instead of inferring coverage from aggregate counts.

## Mounted Volume

The Volume test uses an ordinary temporary directory as an already-mounted offline disk. It runs the real ACP copy flow and verifies:

1. HDD Volume initialization, marker identity, and capabilities.
2. Archive to the Volume and publication of physical files and Library Positions.
3. Online HTTP Range reads directly from a published mounted Position.
4. Restore from the Volume with byte-for-byte and SHA-256 verification.
5. Automatically published cached Scan differences for added, changed, and removed files.
6. Force-rehash Scan detection of a content change whose size, mode, and modification time are unchanged.
7. Metadata-only Media deletion without changing the marker or physical files.
8. Register of the unchanged marker followed by an explicit Scan that rebuilds Positions.

Run it on macOS or Linux with:

```bash
go test -tags=e2e -v ./e2e -run TestVolumeArchiveRestoreScan
```

All files, databases, and Job directories live under the test temporary directory and are removed after the test.

## Preview Update Policy

The Preview policy test runs the optional SCAN Preview stage against one shared content-addressed bundle. Missing-only preserves an existing bundle even when generator settings change; regenerate-all replaces it. Manager tests additionally prove regeneration with identical settings and replacement of damaged derivatives. Analyze, Preview and Verify CLI commands are convenience presets for ScanJobService, not separate Job kinds or execution paths.

Run it on macOS or Linux with:

```bash
go test -tags=e2e -v ./e2e -run TestPreviewGenerationPolicy
```

## Locations and File Versions

The isolated [online E2E](../../e2e/online_test.go) registers a root, scans through CLI subprocesses, reads original content with HTTP Range, creates Library-selected Archive input, and writes/verifies a real mounted Volume. It checks independent same-content Files, shared-copy publication creating each observed File's saved version, later original edits retaining that version, Restore using the second File's own path/mode/mtime, live pagination and metadata-only unregistration. Preview failure isolation, stale-content rejection and rebind races have separate Scan/API/Archive semantic coverage.

The [live workflow](../../e2e/live_analysis_cli_test.go) starts the real server and backs up an unadmitted Location file without prior Scan. It checks path-first continuity across live selection roots, basic selected-scope publication, partial failure/retry, and auto-collection on settings changes. Pure browsing remains unadmitted until explicit Files.Collect. Ordinary fixtures disable auto-collection through CLI to avoid racing their explicit scans. The [complete workflow](../../e2e/workflows_test.go) also backs up edited Library originals without preparatory analysis.

```bash
go test -tags=e2e -v ./e2e -run 'Test(LocationAnalyzeReadArchive|CLILiveAdmissionAnalyzeAndBackup)$'
```

Library, Scan, Archive, API, CLI, and Demo unit tests cover the larger failure/compatibility matrix. Shared engine contract tests cover object-store prefixes, partial outcomes and no native rename; Scan tests cover cache-only zero-read, final Media identity, frozen verification baselines and safe Preview handling of damaged bytes. Local tests do not replace LTFS regression when physical I/O changes.

## LTFS File Backend

The LTFS test treats a directory as a virtual tape. It does not require a physical LTO device, but it runs the real `mkltfs`, LTFS/FUSE mounts, ACP copies and hashes, SCAN Preview outcomes, Job DB checkpoints, Library commits, and CLI business operations. Built-in ffmpeg generators have a separate integration test.

A second test sets the official file backend `capacity_mb` cartridge property to 3072 MiB and writes three 1200 MiB files. LTFS exposes the configured remaining capacity through the mounted filesystem, so ACP's pre-write `statfs` guard stops the first virtual Tape before the next complete file. The test verifies the retryable Job phase, continuous-prefix publication, pending suffix, exact Position set, progress and report totals, completion on a second Media, and Restore across both Media.

### Flow

1. Create isolated Executor and Library SQLite databases, work, source, restore, and virtual-tape directories.
2. Register and analyze a Location, then create an Archive Job through CLI with Preview generation enabled and files that span multiple LTFS records.
3. Explicitly format the virtual tape with `mkltfs -e file` and a placement rule for small `*.fixture` files, mount it, and let ACP write files sequentially.
4. Inject one reported unmount error after LTFS has stopped. Verify that the Archive Job and every item return to `PENDING`, the Library has no Tape checkpoint, and the uncertain virtual device remains unavailable for that Executor lifetime.
5. Retry the same Job and barcode on a fresh virtual device, corrupt the post-unmount Index, and verify that every item again returns to `PENDING` without a Library checkpoint while the successfully unmounted device remains usable.
6. Retry once more, verify that the stale Index is cleared, then verify index- and data-partition placement, the 17-byte partition/block/offset storage order persisted on each Position, the Preview-enabled SCAN, content-addressed ZIP, HTTP-served asset, Archive manifest, `tapes/<barcode>/`, canonical signature xattrs after remount, and Library Media.
7. Inspect the inserted Tape through the Media API, then create a second Archive Job and append it to the same cartridge while retaining the Media ID.
8. Export and re-import the Library using the [published v1 JSONL format](../architecture/persistence.md#published-data-formats), including FileVersion content, tags and notes; legacy whole-object backups remain readable through their explicit adapter.
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

Run it with:

```bash
YATM_E2E_LTFS=1 go test -tags=e2e -v ./e2e -run 'TestLTFS(ArchiveRestore|FullTapeSpansMediaAndRestores)'
```

For an isolated source snapshot without `.git`, set `GOFLAGS=-buildvcs=false` for this command and its child binary builds. This only disables VCS stamping; it does not alter the tested source or runtime behavior.

Regular `go test ./...` runs do not include the tagged E2E suite. Physical-media behavior remains covered by the separate physical Tape E2E suite before release.

Storage-order coverage is intentionally split at stable boundaries: the LTFS parser tests the encoded partition, block, and offset; the real file-backend E2E tests the captured Index, persisted Position, and request-ordered physical layout within each partition; and the Restore tests verify exact Position-to-Copy propagation plus sequential request order across index and data partitions. This avoids adding a test-only hook to the production ACP stream.

Full-Media handling is mandatory at three levels. ACP's Linux `/dev/full` integration test verifies an actual write-time `ENOSPC`, while its disk-usage test verifies the conservative pre-write boundary. Archive tests verify retryable item state. The LTFS capacity E2E then verifies the complete YATM finalization and publication flow, including the stable `archive_media_checkpoint`/`no_space` Job-log event, using the official file backend. The physical suite additionally requires one real scratch cartridge to reach its full-Media boundary and validates the finalized prefix and pending suffix; continuing that Job on a second physical cartridge is optional.
