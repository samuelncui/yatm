# E2E Testing

Status: Maintained validation for the current v1 development line. See [test environment safety](testing.md) before selecting a host or fixture. Actual release results belong to the release workflow, not this procedure.

Primary business acceptance uses actual `yatm-cli` subprocesses and production HTTP/gRPC-Web transport. The [complete installation workflows](../../e2e/workflows_test.go) and [indexing recovery workflows](../../e2e/indexing_cli_test.go) also start the actual `yatm-httpd` binary. Development runs build both binaries from the checkout; release acceptance sets `YATM_E2E_BIN_DIR` to the extracted candidate directory and uses those programs unchanged. Each test owns isolated configuration, databases, source files, output directories and loopback listeners. No production installation or Tape device is used by local tests.

Local/CI Mounted Volume, Preview-policy and LTFS fixtures use the same CLI process adapter, with an in-process server only where generator configuration, finalization faults or detailed checkpoint assertions require it. The adapter has explicit command mappings and **no direct-RPC fallback**. Direct filesystem writes represent external changes, not hidden catalog setup. Public registration, scanning, selection expansion, archive, restore, annotation, import/export and Job mutations go through CLI commands. Remote package acceptance instead uses only packaged application binaries and [local SSH orchestration](#remote-packaged-binary-acceptance).

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
| Physical Tape release gates | Same CLI commands, separately documented explicit fault probes | [Physical Tape E2E](physical-tape-e2e.md) | Hardware identity, append, restart restore, end-of-Media evidence | Explicitly assigned scratch hardware only |

Preview assets and Library JSONL backup use HTTP; full original or archived file bytes do not. Business setup remains CLI-driven. Physical barcode-mismatch acceptance checks the public CLI refusal; backend Session tests separately check rejection before encryption, formatting or mounting. The [physical suite](physical-tape-e2e.md#pt-04-reject-a-different-requested-barcode) records that coverage boundary.

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

## Release Candidates and Upgrades

Follow the [Release SOP](testing.md#release-sop) for source preflight, candidate assembly, acceptance and publication. The [release build guide](testing.md#release-backend-builds) owns toolchains, the required archive set and the separate accepted-candidate upload procedure. Hosted CI skips opt-in LTFS and physical Tape tests. Remote release acceptance replays the public CLI workflows with the official LTFS file backend, using packaged application binaries. Physical Tape acceptance follows the affected source and nonphysical checks and may use a reviewed local build while CI proceeds; [Check Scope and Reuse](testing.md#check-scope-and-reuse) owns final-artifact checks and evidence reuse.

For local/CI harness runs, set `YATM_E2E_BIN_DIR` to an extracted candidate and run the full suite without a `-run` filter. Set `YATM_E2E_LTFS=1` only in a Linux harness environment with the official file backend. Physical Tape uses the separate [package controller](physical-tape-e2e.md#automated-stages) and explicitly assigned scratch hardware. Do not move this harness or source tree to the remote package-acceptance host. Record every pass, failure and skipped test with its reason. Use a temporary filesystem supporting user xattrs; the full-capacity LTFS fixture needs at least 12 GiB of free space for its source, virtual Media and restored bytes.

A missing or non-executable candidate fails explicitly. Package checks reject altered checksums, missing programs, mixed identities, active configuration, development dependencies and hidden filesystem metadata. Packaged guides retain local links; source references resolve at the matching public tag.

### Remote Packaged-Binary Acceptance

Use repeatable `--case NAME` to select exact existing case names. The controller automatically
runs their required cases in normal order and always allocates a fresh isolated environment.
Omit `--case` for the complete selected profile. The report's `case_scope` distinguishes requested
cases, prerequisites, executed cases and skipped cases with reasons; a passing subset does not
establish full acceptance. The legacy profile supports the same selection: upgrade is required
before historical repair or JSONL roundtrip, while review/decline/abort checks do not run those actions.

Reports use monotonic elapsed times for preparation, transfer, installation, workflows and cleanup,
plus individual commands, cases and the whole run. Independent read-only host, tool and identity
checks run in batches of at most four; each retains its result and exit status even if another fails.
Nested or concurrent durations overlap and must not be added to claim total elapsed time.

Run the maintained controller over SSH or directly on the explicitly selected test host with `--host local`. Verify and extract the reviewed release archive into an isolated attempt directory; invoke only its shipped YATM programs. Read their offline `--version` output and compare the commit/version and archive checksum before starting. For host-local execution, transfer the controller and its package validators with the artifacts and operating instructions under [Remote Candidate Acceptance](testing.md#remote-candidate-acceptance). Do not deploy an application checkout or separately built harness.

The [package controller](../../e2e/package_acceptance.py) requires Python 3.9 or newer and Node.js for
the existing package validators; SSH mode also requires OpenSSH. The explicitly selected
Linux amd64 test session must permit an isolated systemd installation and provide the installer's
dependencies plus `timeout`, `ss`, `setfattr` and `getfattr`. It does not install host dependencies.

```bash
python3 e2e/package_acceptance.py \
  --host isolated-test-host --test-parent /srv/yatm-tests \
  --archive "$MAIN_ARCHIVE" --preview-archive "$PREVIEW_ARCHIVE" \
  --version "$VERSION" --commit "$COMMIT" --out "$NEW_PRIVATE_REPORT_DIRECTORY" --ltfs
```

Checksum companions must be beside each archive. Preview validation also requires its matching
corresponding-source archive beside the controller's Preview package; it is validated without being
installed. The controller checks package identities before preparing the host, allocates a unique
root and service name, uses real systemd and the package installer, and records CLI exits, JSON and
output-byte assertions in its evidence directory.
Its workflows cover literal UTF-8 and invalid-byte filenames, exact signed file times, Scan's
all-source preparation barrier and overlapping multi-Location selections, read-only Job creation
inputs, explicit new submission, recorded/live Search, dry run and recoverable Delete, Volume
Archive/Restore/Scan, explicit zero/pre-epoch Restore cutoffs, JSONL export/import with rejected
timestamp shapes preserving existing data, and Verify findings
for healthy, damaged, missing and explicitly repaired copies, including result paging and health.
Volume inventory checks added/changed/removed files, a stat-preserving edit under force-read,
explicit and repeated inventory import, trim, metadata-only deletion, marker registration and
reconstruction through another Scan.
With a Preview archive, it also generates an image with a literal backslash in its name and the
licensed five-minute Demo video, checks HTTP asset bytes and thirty 320x180 timeline frames, and
compares missing-only preservation with explicit regeneration. `--ltfs` adds official file-backend
format/append, finalized-prefix/pending-suffix behavior at full capacity, explicit second-Media
selection, Restore and Verify. It requires the optional LTFS dependencies, `findmnt`, `truncate`
and 16 GiB of free space including the installation backups (4 GiB without LTFS). Its cartridge paths
are newly allocated beneath the attempt root;
physical device paths are not accepted. Omitting this flag records the missing acceptance explicitly.
SQLite acceptance enables WAL on the stopped test service, writes through the CLI, restarts with
WAL disabled and compares committed Catalog data. Two same-version replacements check decline/EOF,
whole-directory backup contents, hidden files,
links, permissions, user attributes, customization and removal of obsolete managed files. A
same-root recovery retains the displaced tree and restores Catalog/Job identities without overlaying
new-only files. These cases do not simulate every installer failure stage or a cross-version upgrade.
Browser form interaction, legacy migration and physical Tape retain their separate acceptance
requirements below. Helper-only RAW/codec acceptance remains separate.

Cleanup verifies service ownership and stops only this attempt's service. Failed runs retain their
root and transcripts; successful runs remove the root unless `--keep-root` is selected. The controller
stops the test service and unmounts any remaining owned FUSE paths before removing successful
fixtures. Unexpected mounts or unverifiable ownership retain the root and fail cleanup.
`make test-package-tools`, also included in `make check`, checks argument quoting, fixture encoding,
exit/timeout handling and cleanup without connecting to a host.
The report covers its named scenarios; it is not an overall release approval.

`YATM_E2E_BIN_DIR` remains a local/CI selector. Some detailed Go fixtures start the checkout server
in-process even when their CLI comes from that directory. Those tests retain their source-level
coverage and do not substitute for this remote whole-package boundary.

Select an already-mounted, xattr-capable filesystem with sufficient free space, then create one owned test root for the extracted package, fixtures, isolated installation and results. Use the archive's `install-release.sh` for remote installation acceptance; the README's exact-tag download remains the normal user bootstrap. Check free space for the complete fixture, including archive and Restore outputs, rather than relying on the system temporary directory's capacity.

Use the same public CLI steps and observable assertions as the coverage matrix: registration, live Files operations, Scan policies, archive/restore, versions, copy verification, import/export and Job lifecycle. Compare exit codes, JSON, Job terminal results and actual output bytes from the local controller. Exercise real systemd installation/update and the official LTFS file backend. Retain invocation transcripts and artifact hashes privately.

Low-level fault injection requiring a custom server remains local/CI coverage. Record any remote scenario that cannot be expressed using the shipped tools as unrun, with its exact reason; previous source-based or separately compiled harness runs do not satisfy this gate. Physical Tape remains a distinct explicitly authorized test.

### Packaged browser acceptance

The maintained Playwright suite in `frontend/e2e/` operates on disposable Demo data
and deletes two files. Create a fresh Demo root locally from the candidate source,
with `go run ./cmd/demo -root /tmp/yatm-demo-browser-UNIQUE -reset -listen 127.0.0.1:18080
-identical-files 1001`. Then start the extracted candidate's `yatm-httpd` from its
package directory with `-config /tmp/yatm-demo-browser-UNIQUE/config.yaml`; this loads
the packaged frontend and server while all data stays inside the Demo root. The Demo
generator prepares fixtures only. Never use a regular installation or physical Tape.

Run `YATM_BROWSER_URL=http://127.0.0.1:18080 YATM_BROWSER_COMMIT=FULL_COMMIT
pnpm --dir frontend test:e2e` after installing Chromium as described in the
[frontend guide](../../frontend/README.md#packaged-browser-acceptance). Record the main
archive checksum alongside the output. Stop the owned server after the run. CI runs
this same suite on the extracted Linux package. Browser acceptance covers actual
rendering and interaction; backend large-data tests own million-entry completion.

### Installer acceptance

Installer semantic tests cover read-only checks, version ordering, cancellation, file ownership and failure boundaries. Actual systemd acceptance uses unique `--install-dir` and `--service` values on an isolated Linux host with candidate `--archive` and `--checksum` inputs. The cases below are required acceptance, not claims that the candidate has passed them:

| Workflow | Public command/entry point | Required assertions |
| --- | --- | --- |
| Fresh installation | Exact-version `install-release.sh`, `--config` | Shipped binaries, real systemd/API/frontend readiness, Volume-only configuration, template ownership and optional Skill. |
| Read-only review | Installer `--check`; `yatm-migrate -phase preflight` | No persistent attempt, database write, quiesce or service interruption; pass/manual/blocker distinctions; Tape scripts never run. |
| Pre-stop rejection | Installer confirmation/package/version checks | Cancel, EOF, unknown version, missing packaged migration guide, wrong checksum and Busy preserve service PID/state and active file hashes. |
| Legacy activation | Installer; `yatm-migrate` Prepare/Commit/validate/cleanup | Guide text comes from the verified archive; two explicit approvals; retained report on Prepare failure/cancel; item-by-item Library/Job/log validation before cleanup. |
| Configuration review | `yatm-migrate` config-plan/config-check/config-apply; installer | Unified YAML diff and Settings summary before stop; stale inputs rejected; explicit access preserved, legacy authorization materialized; saved/deleted registrations and Preview preferences preserved; idempotent conversion. |
| Repeated upgrade | Two successive installer updates, same-version replacement and unchanged rerun | Whole-installation tar includes hidden files, hard/symbolic links, permissions and supported xattrs; only `.backup/` excluded; archive/source verified; old checksums unchanged; successful scratch removed; unchanged rerun offers Skill without another archive. |
| Customization | Installer update | Reviewed config changes only; scripts/helpers, unit, permissions, original working-directory path meanings unchanged; complete replacement removes obsolete managed resources. |
| Failure and rollback | Installer faults plus documented in-root recovery | Backup/Prepare/Commit/config-apply/cleanup/replacement/readiness failure retains reports; uncertain mutation stays stopped; failed tree saved before whole-archive restore; `.backup/` retained. |
| Historical repair | `yatm-migrate -phase repair-job -backup-root ...` | Operates after active legacy tables are removed; every repaired item/log matches backup evidence; already frozen Restore destination survives config changes. |
| Old installation entry | `main` branch's legacy `install-release.sh` | Full parameter validation and cross-major rejection; `v0.1.x`-to-`v0.1.x` update remains available. |

Compare configuration/script/helper/unit hashes, Catalog contents and original service state at each boundary. Inject faults only in the isolated test's candidate/environment; remove only its own service registration afterward. Persist exact candidate commit/archive hashes, stdout/stderr and each pass/fail/skip reason outside the public repository.

Skill acceptance uses an isolated ordinary-user home and the pinned public Skills CLI from [installation](install.md#optional-agent-skill). Test the real interactive picker and existing-target cancel/replace, repeat through sudo from a root working directory, and exercise missing dependencies, non-TTY input and source-read failure. Compare installed copy hashes and ownership; cancellation must preserve existing content and optional failure must leave YATM installed.

Real legacy archive acceptance uses an approved timestamped copy, input checksums, independent Library/Job expectations and a full CLI import/export roundtrip. Run Prepare → Abort → Prepare → Commit → validate → cleanup, repeat idempotent phases, and repair a historical Job from the unchanged backup. Compare every historical item, frozen Restore path and archived log, rather than only Catalog counts. Preserve reports privately and record absent fixture categories explicitly. Production updating follows the public guide only after candidate acceptance; it is not a migration test fixture.

The same package controller automates this flow for a copied `v0.1.x` SQLite metadata fixture:

```bash
python3 e2e/package_acceptance.py \
  --host isolated-test-host --test-parent /srv/yatm-tests \
  --archive "$MAIN_ARCHIVE" --version "$VERSION" --commit "$COMMIT" \
  --legacy-package "$PUBLISHED_LEGACY_LINUX_ARCHIVE" \
  --legacy-fixture "$APPROVED_METADATA_ARCHIVE" --out "$NEW_PRIVATE_REPORT_DIRECTORY"
```

The metadata tar contains root-level `tapes.db`, its retained WAL if present, and installation-relative
`captured_indices`, `job-logs`, `write-reports` or transitional `jobs` evidence as applicable.
The controller copies only those data members. It rejects links and escaping paths, excludes saved
configuration and scripts, and supplies an isolated configuration with no Tape devices or live
Locations. The old server never starts. The exact candidate installer exercises declined Prepare,
Abort, activation, validation and cleanup; the candidate CLI performs a complete JSONL roundtrip.
The stopped installation then exercises repeated cleanup and historical Job repair.

[Independent metadata checks](../../e2e/legacy_metadata.py) compare copied legacy facts with the
migrated SQLite rows and Job files, including nanosecond instants and archived logs. This checker
supports completed Archive history with submitted inline sources and uniquely matched logged Media.
It rejects other retained Job kinds/states and transitional Job databases; the report records missing
Restore and other-state coverage. Compressed metadata needs `zstd` on the local `PATH`.
Run Python without optimization because these checks use assertions. A missing fixture category is
not acceptance. Real metadata, extracted copies and detailed transcripts remain in the private
report directory and must not be uploaded as public CI artifacts. Unit tests generate synthetic
inputs. The original copied archive is read-only and its checksum is rechecked after acceptance.

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
YATM_E2E_LTFS=1 go test -tags=e2e -v ./e2e -run 'TestLTFS(ArchiveRestore|FullTapeSpansMediaAndRestores)'
```

The command above is not the remote package-acceptance procedure. On the isolated host, replay its public CLI scenarios using [packaged binaries and local orchestration](#remote-packaged-binary-acceptance); retain internal checkpoint/fault assertions in the local/CI tests.

Regular `go test ./...` runs do not include the tagged E2E suite. Physical-media behavior remains covered by the separate physical Tape E2E suite before release.

Storage-order coverage is intentionally split at stable boundaries: the LTFS parser tests the encoded partition, block, and offset; the real file-backend E2E tests the captured Index, persisted Position, and request-ordered physical layout within each partition; and the Restore tests verify exact Position-to-Copy propagation plus sequential request order across index and data partitions. This avoids adding a test-only hook to the production ACP stream.

Full-Media handling is mandatory at three levels. ACP's Linux `/dev/full` integration test verifies an actual write-time `ENOSPC`, while its disk-usage test verifies the conservative pre-write boundary. Archive tests verify pending item state. The LTFS capacity E2E then verifies the complete YATM finalization and publication flow, including the stable `archive_media_checkpoint`/`no_space` Job-log event, using the official file backend. The physical suite additionally requires one real scratch cartridge to reach its full-Media boundary and validates the finalized prefix and pending suffix; continuing that Job on a second physical cartridge is optional.
