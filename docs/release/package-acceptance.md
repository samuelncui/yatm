# Package Acceptance

Acceptance of exact release packages: remote packaged binaries, browser, installer and legacy upgrade.

## Release Candidates and Upgrades

Follow the [Release SOP](release.md#release-sop) for source preflight, candidate assembly, acceptance and publication. [Release Packages](release.md#release-packages) owns the required archive set, [Release Backend Builds](release.md#release-backend-builds) owns toolchains and the [candidate and publication procedure](release.md#candidate-and-publication) owns the accepted-candidate upload. Hosted CI skips opt-in LTFS and physical Tape tests. Remote release acceptance replays the public CLI workflows with the official LTFS file backend, using packaged application binaries. Physical Tape acceptance follows the affected source and nonphysical checks and may use a reviewed local build while CI proceeds; [Evidence Reuse](release.md#evidence-reuse) owns final-artifact checks and evidence reuse.

For local/CI harness runs, set `YATM_E2E_BIN_DIR` to an extracted candidate and run the full suite without a `-run` filter. Set `YATM_E2E_LTFS=1` only in a Linux harness environment with the official file backend. Physical Tape uses the separate [package controller](physical-tape-runbook.md#automated-stages) and explicitly assigned scratch hardware. Do not move this harness or source tree to the remote package-acceptance host. Record every pass, failure and skipped test with its reason. Use a temporary filesystem supporting user xattrs; the full-capacity LTFS fixture needs at least 12 GiB of free space for its source, virtual Media and restored bytes.

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

Run the maintained controller over SSH or directly on the explicitly selected test host with `--host local`. Verify and extract the reviewed release archive into an isolated attempt directory; invoke only its shipped YATM programs. Read their offline `--version` output and compare the commit/version and archive checksum before starting. For host-local execution, transfer the controller and its package validators with the artifacts and operating instructions under [Remote Candidate Acceptance](#remote-candidate-acceptance). Do not deploy an application checkout or separately built harness.

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

The [Tape device browser fixture](../../frontend/e2e/tape-device.spec.ts) intercepts service reads
with generated gRPC messages and rejects cartridge inspection or unexpected operations. It checks
passive refresh, busy choices, diagnostic distinctions and desktop/narrow dialog layout. This is
mocked browser-consumer coverage; it does not qualify raw hardware or prove device-server behavior.

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

Skill acceptance uses an isolated ordinary-user home and the pinned public Skills CLI from [installation](../operations/install.md#optional-agent-skill). Test the real interactive picker and existing-target cancel/replace, repeat through sudo from a root working directory, and exercise missing dependencies, non-TTY input and source-read failure. Compare installed copy hashes and ownership; cancellation must preserve existing content and optional failure must leave YATM installed.

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

## Remote Candidate Acceptance

On the explicitly selected isolated acceptance host, package acceptance uses the corresponding checksum-verified release package, built locally or by CI. Earlier hardware diagnosis may use locally compiled application binaries under [Evidence Reuse](release.md#evidence-reuse). The maintained controller can invoke the server/CLI and installer through SSH or run on the authorized test host with `--host local`. For host-local execution, transfer the controller and its package validators alongside the reviewed archives, checksums, runtime adapters, fixture data and operating instructions. Application execution still uses packaged programs; do not deploy the application checkout, separately compiled test binaries such as `e2e.test`, or an in-process test server. Existing systemd, LTFS/FUSE and other host dependencies remain environmental prerequisites.

Choose an existing, spacious, xattr-capable mounted filesystem and create a uniquely scoped test directory beneath it. Place packages, installation copies, backups, Restore outputs and a private `TMPDIR` inside that root; export `TMPDIR` for remote installer and migration invocations as well as the controller. Check available space before extraction and each capacity fixture; the test owns only that directory and its explicitly registered test resources. A temporary pathname does not imply a disposable filesystem or sufficient capacity.

Local development and CI may compile and run Go test harnesses. Those results are separate from remote packaged-binary acceptance. If a fault-injection scenario requires private functions or a custom in-process server, run it locally/CI and record that limit; do not describe it as remote candidate acceptance. [Remote Packaged-Binary Acceptance](#remote-packaged-binary-acceptance) owns the remote procedure.

For official LTFS file-backend acceptance, explicitly configure the optional `templates/testing/ltfs-file-backend` adapters from the package. They operate only on scoped virtual-cartridge directories and are not default installation scripts; their README describes identity and capacity fixtures. Physical Tape remains a separate acceptance boundary.
