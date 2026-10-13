# Physical Tape E2E Test Suite

Status: Hardware release gate. See [test environment safety](testing.md); this guide does not authorize a physical run.

This suite is a hardware gate in the [Release SOP](testing.md#release-sop). After affected source and nonphysical checks pass, it can run from a reviewed local build while release CI proceeds, following [Check Scope and Reuse](testing.md#check-scope-and-reuse). It covers behavior that the LTFS `file` backend cannot prove: drive discovery, hardware encryption, physical partition placement, tape motion, unload and reload, reads after a process restart, and a real physical full-Media boundary. Run the automated LTFS file-backend E2E first; it remains the primary coverage for injected failures, deterministic capacity exhaustion, two-Media completion, backup compatibility, and cleanup.

## Safety and Preconditions

`FORMAT` destroys every existing file on the loaded cartridge. Use one explicitly assigned scratch cartridge whose contents may be erased. A second scratch cartridge is needed only for the optional continuation case. Never run this suite against a production Library database, production Job directory, or a cartridge containing valid data.

Before starting:

1. Record the expected six-character barcode and confirm it on the physical cartridge.
2. Use an isolated YATM configuration with empty Executor and Library databases and dedicated work, source, and restore directories.
3. Confirm that the configured Tape device resolves to its SCSI generic device through Linux sysfs. The host needs Bash, Python 3, Node.js, tmux, systemd, the official LTFS programs, `mt`, `stenc`, `umount`, `fuser`, `timeout`, `openssl`, `sqlite3`, `findmnt`, and the package controller's ordinary installation/xattr tools. The main package supplies `yatm-lto-info`.
4. Confirm that the drive, cartridge generation, LTFS implementation, and encryption support are compatible.
5. Baseline preparation adds `-r 'size=1M'` only to the isolated format adapter. Capacity preparation uses ordinary packaged placement; use a separate fresh installation and state for that sequence.
6. Baseline preparation retains the package controller's 4 GiB free-space prerequisite. Capacity requires more than twice the selected payload plus 8 GiB, or payload plus the largest file plus 8 GiB with `--capacity-source-reflink`; see [capacity measurement](#continuous-boundary-capture). Budget full published Restore output before writing.
7. Record the YATM commit, ACP commit, LTFS version, drive model and firmware, cartridge generation, native cartridge capacity, barcode, and start time.

Keep the host on stable power and reserve the drive for the complete run. Do not test process crashes or power loss as part of this suite.

## Automated Stages

### Prepare the Artifacts and Host Context

Use checksum-verified Linux amd64 main and matching Preview packages from the reviewed source.
Local builds use `build/release/build.sh` and `previewworker/build/build.sh`. Reviewed locally
compiled binaries can begin affected hardware checks once their source and nonphysical checks
pass, independently of CI's final release-byte gate. This installer-based procedure uses packages
so that installation and runtime adapters are also exercised. Final release-byte acceptance and
the evidence comparison in [Check Scope and Reuse](testing.md#check-scope-and-reuse) still apply.

On the authorized Linux hardware host, use an authorized root shell outside the agent sandbox.
Prefer a host-local executor with `--host local`; SSH transfers inputs and retrieves results.
Read applicable host operator notes first. Allocate an existing spacious, xattr-capable filesystem
for the isolated installation, fixtures and private `TMPDIR`. Keep controller inputs, `state.json`
and evidence outside the installation root that cleanup removes.

Transfer the main archive, Preview helper archive, matching
`yatm-preview-source-linux-amd64-<version>.tar.gz` and their `.sha256` companions.
Keep the corresponding-source archive beside the Preview helper; the package validator requires it
before installation. Transfer the reviewed controller's
`e2e/physical_package_acceptance.py` and `e2e/package*.py`, and these validators, preserving their
repository-relative layout under `CONTROLLER_ROOT`:

- `build/release/check-release.mjs`, `archive-members.mjs` and `package-assets.mjs`;
- `build/release/check-candidate-set.mjs` and `check-preview-sources.mjs`.

Include the operational dependencies of every selected check, beyond its entrypoint. For the
Linux virtual-unmount regression, transfer `e2e/test_package_tape_scripts.py` and
`e2e/ltfs-file-backend/umount` plus `get_device`. Broader Tape-script unit selections additionally
need `scripts/mkfs` and `scripts/get_device`. Source-based LTFS E2E uses `e2e/testdata` adapters;
its `umount-file-failures.sh` calls `umount-file.sh`, which delegates to the same
`e2e/ltfs-file-backend/umount` and `get_device`. Keep those relative paths and executable modes.
Source-based Go E2E remains a local/CI check under the [E2E guide](e2e-test.md), separate from
the host's installed-package procedure.

Supply absolute host-local values for `CONTROLLER_ROOT`, `TEST_PARENT`, `EVIDENCE_ROOT`,
`HOST_TMP`, `MAIN_ARCHIVE`, `PREVIEW_ARCHIVE`, `STATE` and the official LTFS executables.
Set `STATE` to the new run's `state.json`. `VERSION` and `COMMIT` identify the archives;
the controller's reviewed source identity is recorded separately. Supply `SCRATCH_DEVICE` and
`SCRATCH_BARCODE` from the explicit assignment. Keep actual host, device, barcode and local path
values in the private handoff and evidence.

Run from the absolute controller directory in every new host shell:

```bash
set -euo pipefail
cd -- "$CONTROLLER_ROOT"
umask 077
mkdir -p -- "$EVIDENCE_ROOT" "$HOST_TMP"
export TMPDIR="$HOST_TMP"
sha256sum -- "$MAIN_ARCHIVE" "$PREVIEW_ARCHIVE" > "$EVIDENCE_ROOT/packages.sha256"
sha256sum e2e/physical_package_acceptance.py e2e/package*.py \
  build/release/check-release.mjs build/release/archive-members.mjs \
  build/release/package-assets.mjs build/release/check-candidate-set.mjs \
  build/release/check-preview-sources.mjs > "$EVIDENCE_ROOT/controller.sha256"
uname -a > "$EVIDENCE_ROOT/kernel.txt"
id > "$EVIDENCE_ROOT/executor.txt"
findmnt -T "$TEST_PARENT" > "$EVIDENCE_ROOT/filesystem.txt"
df -Pk "$TEST_PARENT" > "$EVIDENCE_ROOT/space.txt"
```

Compare these hashes with the reviewed transfer manifest. Record the YATM and ACP source
identities, any reviewed working-tree changes, toolchains/build flags, extracted program hashes,
LTFS build/version, adapter hashes and any transferred regression inputs. The private host record
also identifies drive model/firmware,
cartridge generation/capacity, assigned identity, resolved Tape/SG mapping, drive reservation,
start time, and the isolated service/configuration. Package version labels alone do not establish
the executed bytes. The controller checks archive companions, package members and offline program
identity before installation.

### Resolve LTFS Before Starting systemd

Resolve the official LTFS installation in the host shell and supply its absolute executable paths:

```bash
MKLTFS_BIN=$(realpath -- "$(command -v "$MKLTFS_COMMAND")")
LTFS_BIN=$(realpath -- "$(command -v "$LTFS_COMMAND")")
test -x "$MKLTFS_BIN"
test -x "$LTFS_BIN"
```

`MKLTFS_COMMAND` and `LTFS_COMMAND` are the reviewed executable names or absolute paths from
the handoff. Match the mount template to that LTFS distribution:

| LTFS distribution | Mount template | Index capture arguments |
| --- | --- | --- |
| HPE or older capture-enabled builds using the bare option | `scripts/mount` | `-o work_directory="${TAPE_DIR}" -o capture_index` |
| Current official LTFS using a capture directory | `scripts/mount.openltfs` | `-o work_directory="${TAPE_DIR}" -o capture_index="${TAPE_DIR}"` |

Both templates create `TAPE_DIR` and resolve it to an absolute directory before launching LTFS.
Normal unmount must finish the LTFS process and leave the final current-cartridge
`TAPE_DIR/<barcode>.schema`. The [Tape adapter contract](migration.md#tape-script-adaptation)
owns these inputs and output.

The physical controller below selects the openltfs template. Supply the corresponding official
LTFS build; an HPE run requires reviewed controller adapter selection matching the HPE template.
During `prepare`, `--mkltfs "$MKLTFS_BIN"` and `--ltfs-binary "$LTFS_BIN"` put absolute commands
into the isolated adapters, and the format adapter adds `-r 'size=1M'`. This happens
before the installer starts systemd. Inspect and hash the resulting adapter copies before
`baseline`. A login shell's `PATH` is insufficient evidence of the systemd service's commands.

### Run the Scoped File-Backend Precheck

Before the first physical operation, run the same package/controller pair through the existing
official LTFS file-backend format/append/Restore/Verify case. Use openltfs with its `file` backend
and the matching directory-valued capture option. The virtual unmount adapter waits for the
exact mount-owning LTFS process, identified by its Linux `/proc` command line, to exit as well as
for FUSE to detach. That boundary makes the final Index available before publication checks.

Choose the exact checks and verify their transferred inputs before a tmux launch. For a changed
virtual-unmount adapter, the focused Linux regression selector is:

```bash
cd -- "$CONTROLLER_ROOT"
python3 -m unittest -v \
  e2e.test_package_tape_scripts.VirtualUnmountTests.test_waits_for_final_index_after_fuse_detaches
```

This check uses a simulated LTFS process and ordinary Linux tools. Require an executed pass,
including its delayed final Index assertion. Record the selector, dependencies and result in
the handoff. The real file-backend check below selects one exact package case and its declared
prerequisite; retain that selection in its launch command and report.

Put the resolved executables on the host shell's
`PATH`; the controller resolves them into its isolated virtual adapters before service installation:

```bash
cd -- "$CONTROLLER_ROOT"
export TMPDIR="$HOST_TMP"
export PATH="$(dirname -- "$MKLTFS_BIN"):$(dirname -- "$LTFS_BIN"):$PATH"
test "$(realpath -- "$(command -v mkltfs)")" = "$MKLTFS_BIN"
test "$(realpath -- "$(command -v ltfs)")" = "$LTFS_BIN"
python3 e2e/package_acceptance.py \
  --host local --test-parent "$TEST_PARENT" \
  --archive "$MAIN_ARCHIVE" --preview-archive "$PREVIEW_ARCHIVE" \
  --version "$VERSION" --commit "$COMMIT" --ltfs \
  --case ltfs-format-append-restore-verify \
  --keep-root --out "$PRECHECK_EVIDENCE"
```

`PRECHECK_EVIDENCE` must be a new directory. The selected case includes its fresh-installation
prerequisite and creates only owned virtual-cartridge directories. Allow at least the controller's
16 GiB free-space prerequisite. `--keep-root` retains its stopped installation for review.

Require a passed report for the selected case, with both FORMAT and APPEND partition checks:

1. FORMAT writes a small file and a file larger than 1 MiB; APPEND adds different small content
   to the same Media. This precheck and the physical fixture use the same size-only 1 MiB
   placement rule, which remains effective for ACP's temporary write filenames.
2. After normal unmount, parse each Job's actual final captured `.schema`; obtain partition roles
   independently from the FORMAT log. Compare exported Library Positions with actual Media paths,
   sizes, complete extents and 17-byte partition/block/offset storage order. Both partition letters
   must occur in the fixture.
3. The final APPEND Index must retain every earlier file and its extents on both partitions.
   Earlier Positions and Media ID remain unchanged; new Positions match the final Index.
4. Restore all selected files and compare actual output hashes and sizes with the sources.
   Verify reads every recorded copy and publishes matching findings and healthy observations.

Preview acceptance resolves the PNG through its frozen Archive content signature. An Archive
selection can have a saved version without a known current Location signature; the saved identity
is the source of truth for that derivative check.

Review `report.json`, including `case_scope`, `partition_checks`, command exits and the retained
Job artifacts. A completed copy without the final Index/Position comparison is incomplete
acceptance. Retain a failure and resolve its first unmet check before advancing to hardware.
The separate file-backend capacity case remains part of full nonphysical acceptance; this scoped
precheck provides a quick check of the relevant publication and readback path.

### Launch One Durable Host-Local Stage

Stages share one isolated installation and the controller's existing state:

| Stage | Work and completion boundary |
| --- | --- |
| `prepare` | Fresh installation, deterministic sources, prepared Archive/Preview Jobs and byte-preserving Preview evidence; no Tape operation |
| `baseline` | PT-01..05: inspection, FORMAT, reload, barcode refusal, APPEND and final Index/Position/Preview checks |
| `restore` | PT-06: start the same stopped installation, restore both Archives, verify bytes and storage order |
| `performance-prepare` | Fresh separate installation, ordinary-placement setup Archive and capacity/performance observation helpers; no Tape operation |
| `performance-format` | FORMAT the setup Archive, eject and validate its final Index and Positions |
| `capacity-write` | PT-07: generate the frozen ordinary-file fixture, submit one Archive APPEND and preserve settlement, publication and failure evidence |
| `capacity-verify` | PT-07: Restore every published capacity file, verify bytes, signatures and storage order; no Archive write |
| `cleanup` | PT-08: seal and verify evidence after baseline Restore or capacity readback, delete idle test Jobs and remove the owned installation after review |

Use `prepare → baseline → restore → cleanup` for the baseline and a separate
`performance-prepare → performance-format → capacity-write → capacity-verify → cleanup`
for capacity. Both preparation stages require absent state. Later invocations require the
preserved state, matching package identity, host, device, barcode and LTFS paths. Capacity
verification can follow a failed write only after observed settlement and device release;
cleanup requires successful full published readback. A baseline run does not prove capacity acceptance. Each successful
non-cleanup stage stops its isolated service. Give every invocation a new `--out` directory and
unique log/exit filenames. Keep `STATE` and the preparation stage's shared physical evidence
directory for the entire run.

In a host-local Bash shell outside the sandbox, set `STAGE` to the next authorized stage and
`STAGE_OUT`, `STAGE_LOG`, `STAGE_EXIT` and `SESSION` to new paths/a unique tmux name from the
handoff. Put the log and exit file in an existing evidence directory. Recheck live drive ownership
and the assigned cartridge before physical use. Launch one stage, then review its result before
launching the next:

```bash
cd -- "$CONTROLLER_ROOT"
sha256sum -c "$EVIDENCE_ROOT/packages.sha256"
sha256sum -c "$EVIDENCE_ROOT/controller.sha256"
common=(
  --host local --test-parent "$TEST_PARENT"
  --archive "$MAIN_ARCHIVE" --preview-archive "$PREVIEW_ARCHIVE"
  --version "$VERSION" --commit "$COMMIT"
  --physical-device "$SCRATCH_DEVICE" --physical-barcode "$SCRATCH_BARCODE"
  --mkltfs "$MKLTFS_BIN" --ltfs-binary "$LTFS_BIN" --state "$STATE"
)
test ! -e "$STAGE_OUT"
test ! -e "$STAGE_LOG"
test ! -e "$STAGE_EXIT"
case "$STAGE" in
  prepare|baseline|restore|cleanup) ;;
  *) echo 'Select one documented physical stage' >&2; exit 1 ;;
esac
cleanup_options=()
if [[ "$STAGE" == cleanup ]]; then
  # Baseline without a syscall observer; instrumented runs supply exact trace paths below.
  cleanup_options=(--no-syscall-trace)
fi
printf -v stage_command '%q ' python3 e2e/physical_package_acceptance.py \
  "${common[@]}" "${cleanup_options[@]}" --physical-stage "$STAGE" --out "$STAGE_OUT"
printf -v stage_shell \
  'cd -- %q || exit; export TMPDIR=%q; %s >%q 2>&1; stage_status=$?; printf "%%s\\n" "$stage_status" >%q; exit "$stage_status"' \
  "$CONTROLLER_ROOT" "$HOST_TMP" "$stage_command" "$STAGE_LOG" "$STAGE_EXIT"
printf -v tmux_command '%q ' bash -c "$stage_shell"
tmux new-session -d -s "$SESSION" "$tmux_command"
```

The host's tmux process keeps the stage alive when SSH or an agent turn ends. Read status on
that host without submitting another operation:

```bash
if tmux has-session -t "$SESSION"; then
  tmux display-message -p -t "$SESSION" '#{session_name}: #{pane_current_command}'
fi
tail -n 60 -- "$STAGE_LOG"
test ! -f "$STAGE_EXIT" || cat -- "$STAGE_EXIT"
test ! -f "$STAGE_OUT/report.json" || \
  jq '{status, error, phases, cases, cleanup}' "$STAGE_OUT/report.json"
test ! -f "$STATE" || \
  jq '{completed_stages, service, install, url, physical_evidence_dir, cleaned}' "$STATE"
```

An ended session normally makes `tmux has-session` exit nonzero. A stage passes only when its
exit file is `0`, its report is passed, its expected evidence assertions pass, and state records
its completed checkpoint. A missing exit file or a running/failed report requires inspection of
the existing session, service, Job phase and owned mounts. Reconnect to these records after a
disconnect; the absence of a connection does not imply the stage stopped.

### Resume, Handoff and Review

Advance from completed checkpoints with the same state and a new output directory. Recovery
of an interrupted invocation uses the controller's existing guards:

- `baseline` can reuse a normally completed FORMAT only while no APPEND Job has been created.
  It rechecks Media identity, encryption and FORMAT evidence before continuing. A saved APPEND
  Job or incomplete/failed FORMAT requires operator diagnosis rather than another baseline write.
- `restore` can collect/check an already submitted Job after it settles
  normally; it does not submit that saved Job's Media operation again.
- `capacity-write` and `capacity-verify` each persist an admission marker before work and refuse
  another invocation after that marker. Read the retained result and Job state; never clear the
  marker or replay Media to repair evidence. A failed write may proceed to its first explicit
  `capacity-verify` after settlement and device release.
- `cleanup` resumes deletion only against the sealed evidence and unchanged remaining Job
  catalog. It verifies every preserved checksum again; an active Job, uncertain lease, remaining
  mount, changed catalog or changed evidence stops deletion. Preserve state, reports and shared
  evidence outside the installation; host-local cleanup rejects paths that it would remove.

For a collection-only failure, retain the failed report, review/check the collector repair,
record its changed hash and resume only where the guard above permits it. State records remain
the evidence of what happened. An unresolved extent or Index/Position mismatch remains an
unmet acceptance check even when the write or unmount completed.

A fresh executor's private handoff includes:

- authorized host/drive/cartridge and destructive scope, drive reservation and operator notes;
- exact source, package, program, controller and adapter hashes, LTFS selection, tools and host context;
- absolute controller/input/state/evidence/installation/TMPDIR paths, service name and CLI URL;
- exact selected checks/stage and their operational dependencies, the variables and complete
  launch/status commands above, current tmux/log/exit/report locations;
- completed checkpoints and Job IDs, first unfinished check, pass criteria and failure stop condition;
- executor, result reviewer and cleanup owner, including the coordinator's decision before cleanup.

One executor owns the drive. The coordinator reviews stage reports and compact evidence before
authorizing the separate `cleanup` invocation. Cleanup seals and verifies the shared Job evidence
before deletion, preserves state and stage reports outside the removed installation, and leaves
the cartridge ejected. After review, remove only the owned precheck/fixture resources and apply
the agreed evidence retention policy. Failed hardware remains available for diagnosis; cleanup
does not establish a pass for its failed write.

Evidence collection copies the settled Job's original `job.log` and verifies its checksum, like
the other bundle artifacts. The [CLI output contract](../architecture/cli.md) exposes text for
inspection, not a byte-preserving log archive. Preparation exercises the real packaged Job and
collector together; parser unit tests alone do not verify that boundary.

The configured `readinfo` script starts every physical operation with `mt -f DEVICE load`. It then spends at most about one minute polling the explicit MAM Barcode field. The script removes an attached media-generation suffix such as `L5`. A successful, explicitly empty field allows FORMAT with the assigned scratch barcode; formatting writes that identity. APPEND and Restore require the existing device identity. A failed probe or unreadable field stops the operation. The [Tape contract](../architecture/media-io.md#tape) owns these identity rules.

Normal unmount keeps the Job attempt and device lease until the mount is gone, no process holds the resolved SG device, and `mt -f DEVICE status` reports `DR_OPEN`. This post-unmount wait shares a ten-minute deadline. A successful Job therefore exposes the drive only after eject completion; timeout follows the unmount-failure rules below.

Capacity acceptance uses ordinary Archive sources and a complete Restore of its published
selection. FORMAT, APPEND and Restore remain separate explicit Media submissions.

An unmount failure invalidates the attempt and keeps the device unavailable for the lifetime of that YATM process. Stop the isolated YATM service before manual cleanup, identify processes holding its recorded owned mount with `fuser -vm MOUNT_POINT`, and have them release the mount before attempting a normal `umount`. A force or lazy unmount is only a last-resort manual cleanup after the service has stopped; discard that write result and restart YATM before using the device again. Do not enter an owned LTFS mount while a physical stage is running.

Stopping a systemd unit can also terminate its LTFS child. If the mount has already disappeared,
verify that no process holds the assigned SG device. When a normal eject reports "Medium removal
prevented", release that failed session's lock with `mt -f DEVICE unlock`, then use
`mt -f DEVICE offline` and confirm `DR_OPEN`. This cleanup never makes the failed write acceptable.

## Fixture

Create deterministic source files and record their size and SHA-256 before starting:

| Path | Content | Expected LTFS partition |
| --- | --- | --- |
| `dataset/index-small.txt` | 60 KiB | Index |
| `dataset/data-small.bin` | 64 KiB | Index |
| `dataset/data-large.txt` | 2.15 MiB | Data |
| `dataset/empty.bin` | Empty | No data extent |
| `dataset/nested/payload.png` | Small PNG supported by the packaged native Preview helper | Index |
| `append/index-small.txt` | Different 65 KiB content | Index |
| `append/data.bin` | 2.34 MiB of non-sparse data | Data |

The placement rule depends only on size: non-empty files no larger than 1 MiB go to the Index
partition, and larger files go to Data. It applies to text, binary and PNG content and to ACP's
temporary write filenames. Empty files have no data extent. Resolve the partition letters from
the FORMAT log's declared roles and compare them with the captured final Index.

The mandatory full-Media case must reach a real physical capacity boundary with incompressible data and exercise ordinary Archive source files. Retain the path, size and SHA-256 manifest for files selected for Archive and Restore. Sparse files, zero-filled files and repeating byte patterns do not establish physical Tape consumption because filesystem holes and drive compression can reduce the bytes written.

## Required Cases

Run the cases in order because they share one cartridge and one isolated YATM installation.

### PT-01: Device and Barcode Preflight

1. Start YATM with the isolated configuration and load the scratch cartridge.
2. Inspect the configured Tape device through the packaged CLI.
3. Compare a non-empty returned barcode with the assigned scratch barcode. If the electronic barcode
   is empty, inspect again with `--identity BARCODE` to check Library registration for the explicitly
   assigned identity before FORMAT.

Expected results:

- Inspection completes without a device-busy or device-discovery error.
- A reported electronic barcode matches the assigned scratch barcode. A successfully read empty
  barcode is accepted only before the initial FORMAT, which writes the assigned identity.
- The isolated Library has no Tape Media with that barcode.
- No format, mount, or Library mutation occurs during inspection.

### PT-02: FORMAT Archive and Preview

1. Create an Archive Job for `dataset` with Preview generation enabled.
2. Wait until indexing is complete and the Job is waiting for Media.
3. Write the Job with Tape mode `FORMAT`, the expected barcode, and a test name.
4. Wait for Archive and its follow-up preview Scan to complete and for the cartridge to eject.

Expected results:

- Hardware encryption is configured before formatting and the key remains in the Tape Media profile.
- LTFS formatting, mounting, ordered ACP writes, unmounting, and ejecting succeed.
- Formatting passes the assigned barcode to `mkltfs`; the subsequent reload/inspection must return it.
- Every non-empty source file has the expected size and SHA-256 in the Archive result.
- Every Archive item is `SUBMITTED`; the Library contains one `ltfs_v1` Tape Media and one Position per source file.
- The captured LTFS Index contains valid extents for every non-empty file. Each persisted Position has the same first logical extent encoded as partition, block, and byte offset in its 17-byte storage order.
- `index-small.txt`, `data-small.bin` and `payload.png` are on the index partition.
  `data-large.txt` is on the data partition; `empty.bin` has no extent.
- The Job Tape directory contains the LTFS log, captured Index, Archive report, and manifest.
- The Preview bundle and its HTTP asset are readable.

### PT-03: Reload and Inspect the Durable Tape

1. Reload the ejected cartridge with `mt -f DEVICE load` without changing the databases. A standalone drive may take about one minute to become ready.
2. Inspect it through the packaged CLI.
3. Leave it loaded for the next Archive operation; inspection does not mount or change its contents.

Expected results:

- The physical barcode resolves to the Media created by PT-02.
- Media ID, name, format, file count, and written bytes match the committed Library state.
- Inspection does not format, mount, or modify the Tape or Library.

### PT-04: Reject a Different Requested Barcode

1. Reload the same cartridge.
2. Create a second Archive Job for `append` and wait until it is ready for Media.
3. Request a different barcode through the packaged CLI and record its preflight refusal against the loaded physical cartridge.

Expected results:

- The CLI reports the requested/device barcode mismatch. An unavailable MAM barcode is not a pass.
- CLI preflight refuses the request before admitting an Archive Media operation. The prepared Job, existing Tape Media and Positions remain unchanged; no encryption, formatting, mounting or copying occurs.
- Reloading and inspecting the cartridge still returns the original barcode.

Reuse `TestTapeSessionsRejectUnverifiedIdentityBeforeMutation` for the backend's independent rejection before encryption, formatting or mounting, and `TestArchiveFailedMediaAttemptSettlesPhaseAndReopens` for a failed Media operation returning the Job to its pre-Media `READY` state. These are local/CI checks at separate boundaries; physical CLI refusal does not prove an admitted runner mismatch through RPC.

### PT-05: Append to the Existing Tape

1. Submit the prepared Archive Job from PT-04 against the same cartridge with mode `APPEND` and the original barcode.
2. Wait for completion and eject.

Expected results:

- Append reuses the original Media ID and encryption key.
- Existing files and Positions remain unchanged.
- Appended files use a collision-resistant Media path prefix and become `SUBMITTED`.
- The format-time placement policy remains effective: appended `index-small.txt` is on the index partition and `data.bin` is on the data partition.
- The final captured Index contains both Archive Jobs with valid extents and storage order.
- Within each partition, physical storage order matches the deterministic Archive item order even when source preparation completes out of order.
- Job completion and device availability occur only after the cartridge has ejected.

### PT-06: Restart and Restore

1. Stop YATM normally after PT-05 and start it again with the same isolated databases and work directory.
2. Create one Restore Job containing files from both Archive Jobs.
3. Sort the selected Positions by their persisted storage order and record the expected Media-path sequence.
4. Reload the cartridge and restore through the physical Tape device.
5. Wait for completion and eject.

Expected results:

- The restarted process reads the durable Media profile and reconfigures the drive with the stored encryption key.
- Restore reads files from both the index and data partitions.
- Position storage order is propagated unchanged into Restore Copies. The sequential-source unit test verifies that requests follow ascending partition, block, and byte offset rather than logical path order; the physical run verifies that the resulting files are restored correctly.
- Every selected file completes, including the zero-byte file.
- Restored paths, sizes, bytes, and SHA-256 values match the source fixture.
- Restored ACP signature xattrs contain the expected size and SHA-256.
- Restore logs are stored under the Restore Job's `tapes/<barcode>/` directory.
- Job completion and device availability occur only after the cartridge has ejected.

### PT-07: Physical Full-Media Boundary

Use the separate [capacity sequence](#continuous-boundary-capture) with its frozen input sizes
and disk budget. Choose enough ordinary source content to reach the measured cartridge boundary;
a completed Archive whose input fits does not establish capacity exhaustion.

1. Run `performance-prepare` and `performance-format` with `--capacity-observe` to obtain the
   setup Archive and its saved before-copy capacity preflight.
2. Run `capacity-write` once. It hashes all source files, saves the immutable size/hash manifest,
   prepares one ordinary Archive and submits one APPEND. Keep continuous samples through settlement.
3. Review `capacity/write-result.json`, item order/states, progress, Library export, final Index,
   Archive report and Job/LTFS logs, including before-copy and before-unmount observations.
4. Once the Job is observed idle and the device is released/ejected, run `capacity-verify` once.
   It restores **every published file**, up to the 100-file bound, and verifies actual output
   size/SHA-256, ACP signatures and persisted Position storage order. An empty selection fails.

Expected results and interpretation:

- Software refusal before creating the next file is an expected capacity boundary. Keep its
  complete error chain and pending suffix. `capacity-write` still returns nonzero for a Job error;
  this is evidence to review, not permission to replay the write or an automatic hardware pass.
- A late write, Close or finalization failure is a distinct failure finding. `no_space` alone
  cannot distinguish pre-write refusal from partial output. Compare the logs, temporary-file
  observations, final Index and publication; successful readback does not erase the write failure.
- Published items form a continuous prefix on the assigned Media. Final Index paths, sizes,
  extents and storage order match their Positions. The setup Archive remains present. Pending
  items have no Media/path publication; no item remains staged. Report counts match published
  files and bytes. Missing or contradictory finalization evidence remains a failed check.
- Device availability and eject are checked after settlement. Uncertain ownership retains the
  running service and all resources for the lifecycle owner; no device status query is made while
  the Job or lease is active. No automatic unmount, eject, reformat or Media retry follows failure.
- Full published readback is mandatory. A first/last sample cannot establish this result.

### PT-08: Cleanup and Evidence

1. Require completed baseline Restore or capacity readback. Check the entire isolated Job catalog is idle, the device lease is released, the cartridge is ejected and no owned mounts remain. Export the Library metadata backup into preserved evidence.
2. Before deleting anything, capture each Job's catalog and bundle metadata, complete Job log, LTFS log, Archive report, captured Index, before-copy/before-unmount snapshots, and the exact `sg-trace.jsonl` and `sg-trace.jsonl.error` files when present. Capture verifies each transfer against the host SHA-256; unrelated Tape files are not swept into evidence.
3. Require the source manifests and original stage timing reports described below. Finalize every supplied syscall trace and checksum-copy it into preserved evidence. Generate a path-sorted SHA-256 manifest covering these files and all Job evidence. Missing prerequisites, active observers, capture failures or checksum mismatches stop cleanup before Job deletion.
4. Recheck preserved checksums, the saved manifest digest and unchanged Job catalog, then delete only those idle test Jobs through the API. A resumed cleanup also checks the remaining catalog against the sealed inventory and compares live Tape artifacts with their preserved checksums. An old seal that omitted an SG trace or its error file cannot authorize deletion.
5. Check mounts again, stop the owned service and remove only its installation, source files and Restore output. Keep state, stage reports, sealed evidence and the written cartridge. Cleanup issues no load, format, mount, unmount, eject or Media submission.

Expected results:

- The export contains the Tape profile, Files, and physical Positions needed to describe the completed run.
- The evidence checksum manifest verifies every preserved Job artifact before Job deletion.
- Job deletion removes the corresponding local Job directories.
- Cleanup does not modify or erase the physical cartridge.
- The cartridge is ejected and clearly marked as scratch or test media.

### Cleanup Evidence Prerequisites

Use the existing shared `physical_evidence_dir` in `STATE`. Before invoking `cleanup`, supply
the reviewed source manifests at these exact paths; do not reconstruct report timings or
substitute current source hashes for those used by an earlier stage:

| Preserved path | Required evidence |
| --- | --- |
| `source/packages.sha256` | Original package checksum record; its main-package digest must match the preparation report |
| `source/controller.sha256` | Executed controller source hashes, including `physical_package_acceptance.py`; an instrumented SG run also includes `physical_sg_trace.c` and the executed `physical_sg_trace.so` |
| `stages/<stage>/report.json` | Original completed invocation report, matching commit, version and installation, with finite nonnegative duration and the matching physical case result |

The required baseline reports are `prepare`, `baseline` and `restore`. Capacity requires
`performance-prepare`, `performance-format`, `capacity-write` and `capacity-verify`, including
the original failed `capacity-write` report for an expected software refusal. Other required
stage reports must have passed. The current cleanup report stays in its separate `--out`
directory; it cannot be a completed prerequisite of its own invocation.

After each entered non-cleanup stage, the controller copies the exact bytes from its final
`--out/report.json` save to `stages/<stage>/report.json` under the common evidence directory.
This includes failed capacity writes, their failure reason, retention decision and final duration.
Early validation failures that never enter a stage remain only in their invocation's `--out` and
do not replace its preserved report. Keep source manifest selection explicit and reviewed:

```bash
PHYSICAL_EVIDENCE=$(jq -er '.physical_evidence_dir' "$STATE")
mkdir -p -- "$PHYSICAL_EVIDENCE/source"
for name in packages.sha256 controller.sha256; do
  cp -- "$EVIDENCE_ROOT/$name" "$PHYSICAL_EVIDENCE/source/$name"
  cmp -- "$EVIDENCE_ROOT/$name" "$PHYSICAL_EVIDENCE/source/$name"
done
```

The same source-manifest step applies to baseline. With an SSH controller, preserved files and
stage reports are controller-local; `--syscall-trace` paths below refer to the hardware host.
The controller validates the fixed
prerequisites before sealing. Source review still establishes that the recorded hashes identify
the executed source and observer; checksum syntax alone cannot prove that history.

Cleanup requires either repeated `--syscall-trace /absolute/host/file` arguments for **every**
separately collected syscall trace, including each `strace -ff` output, or an explicit
`--no-syscall-trace` declaration when that observer was not used. Supply exact files, not directory
or wildcard arguments. Stop/detach the observer after normal Media settlement and ensure starting
the idle service for cleanup will not restart it. Cleanup refuses symlink trace files, duplicate
filenames, open traces or an uncertain `fuser` result. It copies raw bytes into
`observers/syscall/<filename>` using the existing checksum transfer helper, whether the source
was inside or outside the owned root; the evidence seal covers every copy. An observer outside
Jobs is not discovered automatically: the cleanup owner must supply its complete file set.

For a capacity run whose Archive and Restore used the SG observer, add `--require-sg-trace`.
This requires nonempty `jobs/<id>/tapes/<barcode>/sg-trace.jsonl` for both Jobs and the recorded
observer source/binary hashes. Capture preserves any accompanying `.error` even without that
flag. An error marker remains a failed observer finding for review; preserving it does not
establish complete SG measurement or a successful write. Cleanup performs no observer or
hardware operation to replace missing evidence.

For example, pass the following options to the separate authorized cleanup invocation:

```bash
cleanup_options=(--require-sg-trace)
for trace in "${SYSCALL_TRACES[@]}"; do
  cleanup_options+=(--syscall-trace "$trace")
done
```

`SYSCALL_TRACES` is the explicit nonempty list of finalized host files from the handoff. If no
syscall observer was used, use `cleanup_options=(--require-sg-trace --no-syscall-trace)` for an
SG-instrumented run. Omit `--require-sg-trace` only for an uninstrumented run. Preserve source,
reports and traces before the first seal; a failed or incomplete old seal is retained for review,
not silently replaced to permit deletion.

## Optional Physical Cases

The first physical full-Media boundary is mandatory. The cases below either require a second cartridge or repeat destructive formatting, so they are optional unless the affected behavior changed.

### PT-X1: Continue the Full Job on a Second Tape

Continue the pending PT-07 Archive Job on a second scratch cartridge:

1. Load the second scratch cartridge and write the remaining items in `FORMAT` mode with its own barcode.
2. Verify that the Archive Job completes and that every item is submitted to exactly one of the two Tape Media.
3. Restore the complete capacity logical set by loading both cartridges as requested.
4. Compare every restored size and SHA-256 with the generation manifest.

Do not add a synthetic FUSE source or streaming-file abstraction solely for this case. The physical test should exercise ordinary source files through the production I/O path.

### PT-X2: Explicit Delete and Reformat

After preserving the original evidence, delete the Tape Media metadata through YATM, create a new Archive Job, and format the same physical barcode again. Verify that FORMAT is rejected before metadata deletion, succeeds after deletion, creates a new Media ID, and does not delete the old logical Files.

## Pass Criteria and Evidence

The release gate requires the baseline and capacity sequences plus their evidence cleanup, without manual database edits or changes to archived Tape files. A reviewed expected software refusal and successful full published readback can satisfy the capacity boundary; a late write/finalization failure remains a separate finding. PT-X1 and PT-X2 are recorded separately when run. Retain:

- the version and hardware record;
- source and restored SHA-256 manifests;
- packaged CLI request results and the reused local/CI Session identity and failed-Media lifecycle evidence;
- every Job log and report;
- every Job's catalog and bundle metadata plus the sorted evidence SHA-256 manifest;
- captured LTFS Index files from FORMAT and APPEND;
- the immutable capacity source manifest, continuous Archive/Restore samples and write/readback results;
- all raw Job Tape SG traces and optional error markers, plus every separate syscall trace when used;
- the Library JSONL metadata backup;
- a concise result for each case, including elapsed Archive, full-Media boundary, and Restore time.

Each invocation's `report.json` records total `duration_seconds`, per-phase setup timing,
per-case timing and command durations, including failed stages. Retain separate timings for
preparation, FORMAT/baseline, Archive, complete Restore/verification and cleanup, plus wall-clock
handoff/queue/transfer time. Capacity samples mark Media admission and settlement; write-result
timestamps include fixture generation and Archive work. Post-Restore output hashing remains part
of overall stage time.

Reuse the already accepted ordinary-file full-capacity run when review confirms unchanged
relevant production source, dependencies, build/runtime configuration and fixture semantics.
A controller/SOP cleanup does not alone require another full Tape write. Keep the original
source/package/controller hashes and result with the reviewed diff; run affected local collector
and cleanup regressions. New release archives still need their own identity/content checks and
relevant package smoke acceptance under [Check Scope and Reuse](testing.md#check-scope-and-reuse).

Record a failure at the first unmet expected result. Preserve the cartridge and local evidence for diagnosis instead of reformatting and retrying immediately.

## Bounded Performance Comparison

Use `performance-prepare`, `performance-format`, and `performance-run` with a new
state and isolated installation for each compared build. Capacity uses a fresh formatted
installation without performance-case writes. Preview is optional. Preparation creates a deterministic
64 KiB setup Archive; FORMAT uses ordinary packaged data-partition placement,
without `-r size=1M`. Measured cases APPEND, normally unmount, validate the captured
final Index and Library publication, then Restore and verify actual output bytes.

Transfer `e2e/physical_performance_*.py` with the controller and `e2e/package*.py` modules.
`make test-package-tools` runs their local regressions with the package-controller tests and
mocked device observations, without Tape access. SVG rendering additionally needs `matplotlib`;
its rendering test reports a skip when that optional dependency is absent. Measurement parsing,
fixture checks and verification tests still run.

Supply the explicit package, host, device, barcode and evidence arguments. Use
`--performance-source-read-mode buffered` or `mapped` consistently across stages;
mapped uses the existing Location `--use-mmap` setting. Select cases with repeated
`--performance-case`. `--performance-size-gib` accepts 4 (default), 32 or 64 GiB per
case, saved in state. The fixed order and file sizes are:

| Case | File size | Content |
| --- | ---: | --- |
| `small-random` | 256 KiB | Deterministic SHAKE-256 bytes |
| `small-compressible` | 256 KiB | Adjacent duplicated 4 KiB random blocks |
| `large-random` | 1 GiB | Deterministic SHAKE-256 bytes |
| `large-compressible` | 1 GiB | Adjacent duplicated 4 KiB random blocks |

At 32 GiB, a small case has 131,072 files and a large case has 32. Generation emits
an external JSON Lines SHA-256/size manifest. CLI manifest pages contain at most
1,000 rows; a private validation database checks selection, duplicates, content,
previous Positions, final Index extents and Restore order without whole-manifest
maps. Index parsing and artifact transfer stream; actual restored-byte checks use
batches of at most 64. Source/Restore fixtures are removed only after complete
verification; Job bundles and evidence remain. Interrupted cases are preserved
for explicit diagnosis. Each scene requires twice its logical size plus 8 GiB
of free evidence headroom.

Media timing includes admission through normal finalization and publication.
Fixture generation, preparation and external content checks are outside it. Keep
input bytes/order, source mode, compression/encryption, LTFS settings and physical
layout equal. Source generation and Archive preparation affect page cache; record
cache preparation and host load rather than claiming a cold-cache measurement.

Independent bounded pollers record application progress about every two seconds,
drive metrics about every five seconds and execution-host resources about every
five seconds. One writer streams JSONL, with request wall/monotonic start and end,
errors and timeouts. Drive reads cannot block the application polling channel.
Use `--no-performance-driver-sampling` consistently across stages for collector
calibration. The sampler reads xattrs only through the LTFS process whose exact
`work_directory` identifies the admitted Job; it never opens a competing SG reader.
Timeout cleanup kills only the sampler subprocess group.

The controller retries only read-only Job waits when Get reports
a SQLite lock, within the original deadline and with every failed transcript kept.
Delays grow from 2 to 4, 8 and 16 seconds, then remain capped at 30 seconds;
the remaining observation budget bounds the final delay. Attempts, lock errors
and chosen delays remain in the report. There is no separate attempt-count limit.
It never repeats a Create or Media action; durable failure and other errors stop
the stage for inspection.

Application `copied_bytes` during COPYING describes ACP source/handoff progress,
not physical commitment or final Library publication. ACP `copied_file_count`
increments when work enters content processing, so its curve is labeled file-work
entries rather than completed files. Final Index/Library and restored-byte checks
establish completed content separately. Stage changes, resets and missing samples
break interval arithmetic. Root xattrs `user.ltfs.vendor.IBM.logPage.34.00`
and `.0C.00` retain their raw bytes and request times. Binary page framing, required
widths and duplicate parameters are checked before decoding. Page 0C provides
Sequential Access byte proxies; page 34 parameter 0 reports repositions per 100 MB,
not cumulative events. Equal values do not prove freshness or its absence.

Analysis emits observations, intervals, resources, fixed 10/30-second bins,
all candidate windows, auxiliary drive samples, controller milestones, file-work
entry rates, COPY phase-edge brackets and summary CSV/JSON plus a standalone SVG.
Phase durations retain request-boundary uncertainty; unknown edges stay absent.
The observed byte-endpoint subset rate is distinct from a full successful COPY rate. Matplotlib is needed on the controller only for
plot generation. Counters produce rates over actual sample intervals; UI smoothed
speed is not averaged again. Invalid gaps remain gaps, and zero increments mean
only that no progress was observed in that interval.

The first observed positive ACP increment anchors candidate windows every ten
seconds. Each 180-second window has six 30-second bins. A qualifying window needs
at least 90% valid time coverage per bin, no gap longer than twice the nominal
sample period, rate coefficient of variation at most 15%, and first/last 60-second
mean drift at most 10% of the window mean. Only valid adjacent increments are
allocated by time overlap. Rates with incomplete coverage describe covered time.
These fixed engineering thresholds classify application averages; they are not
vendor guarantees, physical-streaming proof or optimization acceptance thresholds.
The first qualifying window and every later candidate remain visible, including
periodic no-progress intervals that stable averages can hide.

Use the complete curve to determine whether a longer fixture is necessary. Fix
sampling gaps before extending duration; persistent low speed and repeated pauses
are results to diagnose. Compare one matched baseline/candidate pair first. When
noise prevents a decision, use at most one reversed affected-case pair. Keep a
change only when throughput or reposition behavior demonstrably improves without
a confirmed regression in the other measure, correctness or bounded resources.
A short successful run or failed candidate alone does not establish a hardware
limit. This flow does not claim capacity-exhaustion or release acceptance.

## Capacity Measurement

### Continuous Boundary Capture

Use one planned Archive run to compare the capacity sources on the same timeline. Reuse
`performance-prepare` and `performance-format` for a fresh isolated installation, then select
`capacity-write` and `capacity-verify`. Transfer `e2e/physical_capacity_*.py` and
`e2e/physical_performance_*.py` with the controller and `e2e/package*.py` modules.
This is the PT-07 ordinary Archive and full published Restore path.

Choose the cartridge capacity and input size before starting. A separately authorized hardware
capacity reduction can shorten an experiment: HP LTO-5 documents SET CAPACITY `0x0FB3`, roughly
92 GB, in its [integration guide, page 15](https://docs.oracle.com/cd/E21419_04/en/LTO5_Vol2_E3a/LTO5_Vol2_E3a.pdf#page=15).
It erases the cartridge. Verify actual drive support, beginning-of-medium position and measured
capacity. Keep that reduction during FORMAT with the implementation's `mkltfs --keep-capacity`
option; ordinary formatting can restore full capacity. Record partition sizes after formatting.
Restore full capacity only after the experiment's evidence and readback are preserved. Reduced
media establish that configuration's boundary behavior, not a full-length capacity guarantee.

Pass `--capacity-observe` during `performance-prepare` and repeat it for each later stage.
The controller installs `physical_capacity_boundary.py` in the isolated adapters after successful
mount and before returning to YATM. Pass `--work-directory "$TAPE_DIR" --mount "$MOUNT_POINT"`,
`--phase before-copy` and `--output "$TAPE_DIR/capacity-before-copy.json"`, using the reviewed
absolute helper path. It verifies required probes before copying. At the start of normal unmount,
capture `--phase before-unmount` into `capacity-before-unmount.json`; retain a failed observation
and proceed with normal finalization. These passive hooks do not synchronize or pause an active
copy. Unload clears some HP error counters, so the final mounted snapshot is part of the evidence.

Pass the FORMAT Job's saved `capacity-before-copy.json` as `--capacity-preflight` to
`capacity-write`. It checks the owning installation/Job and usable starting page 31, page 0C
and `statvfs` before generating payload. The live before-copy hook additionally requires raw page 17
before permitting payload writes. Each LOG page uses one Linux xattr read with a 64 KiB buffer;
this avoids LTFS's oversized-reply error for a small caller buffer and double reads of read-cleared
counters. The default is 24 ordinary 4 GiB files, with disjoint
deterministic AES streams and an immutable SHA-256/size manifest; `--capacity-files` selects
1–100. Repeated `--capacity-file-mib` instead provides up to 100 ordered sizes, allowing large
files for the main transfer and smaller files approaching the measured boundary. Freeze this
selection before the run. Reserve twice the payload size plus 8 GiB on the source/Restore filesystem.
For a filesystem supporting Linux FICLONE, `--capacity-source-reflink` instead generates one
pseudorandom base, clones ordinary files and gives each a distinct pseudorandom prefix. Each
file receives a full size/hash manifest before Archive. This saves source-disk space; every byte
still passes through Archive and the drive. It requires the payload size plus the largest file
plus 8 GiB for source and Restore space. Check this generator on the selected filesystem with
a small fixture first; unsupported cloning fails explicitly. Repeat the flag on later stages.

Use the stage launcher above with a separate `STATE` and new outputs for each capacity stage.
Its baseline-only stage selector must be replaced by the following selector. Keep the `common`
package/identity arguments and construct capacity options from the frozen workload before launch:

```bash
case "$STAGE" in
  performance-prepare|performance-format|capacity-write|capacity-verify|cleanup) ;;
  *) echo 'Select one documented capacity stage' >&2; exit 1 ;;
esac
capacity_options=(--capacity-observe)
# Optional: add once at preparation and repeat for every subsequent invocation.
# capacity_options+=(--capacity-source-reflink)
# Set FILE_MIB to the reviewed ordered list (1–100 files, 1–65536 MiB each).
for size in "${FILE_MIB[@]}"; do
  capacity_options+=(--capacity-file-mib "$size")
done
if [[ "$STAGE" == capacity-write ]]; then
  capacity_options+=(--capacity-preflight "$FORMAT_PREFLIGHT")
fi
# For cleanup, set cleanup_options using Cleanup Evidence Prerequisites above;
# for other stages, use cleanup_options=().
printf -v stage_command '%q ' python3 e2e/physical_package_acceptance.py \
  "${common[@]}" "${capacity_options[@]}" "${cleanup_options[@]}" \
  --physical-stage "$STAGE" --out "$STAGE_OUT"
```

`FORMAT_PREFLIGHT` is the saved FORMAT Job's `capacity-before-copy.json` from this installation;
it is not a fresh device query. `FILE_MIB` is a Bash array populated from the approved workload.
Alternatively use `--capacity-files N` for N ordinary 4 GiB files. Repeat the source mode, fixture
sizes, observation and reflink options on subsequent stages. Preview is optional for this sequence,
including `cleanup`. Preserve the written cartridge after cleanup.

The capacity sampler records application progress and owned LTFS readings every second. Its raw
pages are 31, 0C, 17, 02 and 34, alongside `statvfs`, bounded file metadata including temporary
outputs, and resource observations. All channels use the host's wall/monotonic request times;
each sequential device query retains its own start/end time. Preserve gaps, unsupported fields,
counter resets and observation delays. Job byte progress can include rejected work; it is not
actual Tape throughput or a buffer-occupancy measurement. File-work entries and file size changes
do not establish successful Close, final publication or readable content.

Read TapeAlert through `user.ltfs.mediaStorageAlert`, which lets LTFS retain flags in its own
cache. Reading raw page 2E independently can consume read-cleared flags before LTFS observes them.
Keep the returned bitmask and query times; this observation must not hide a warning from its owner.

To distinguish the copyer's actual capacity reads from the independent sampler, an isolated
service can run under `strace -D -f --seccomp-bpf`, selecting `statfs` and the required file
lifecycle calls. Verify that the service's main PID remains the HTTP listener and that syscall
filtering works before admitting Media. Keep raw syscall arguments, results, durations and
process identity; a capacity reading alone does not identify its caller. Go's direct `statfs`
syscall is not intercepted by a libc preload wrapper. Keep this optional observer with the
isolated service until normal settlement, and record its potential measurement cost. Before
cleanup, finalize and explicitly supply every raw output under
[Cleanup Evidence Prerequisites](#cleanup-evidence-prerequisites); files outside Job bundles
are not captured by the Job collector.

A capacity error returns nonzero after retaining observations, item rows, Job/LTFS logs, final
Index and Library results. Inspect those results before running `capacity-verify`; the latter
requires observed settlement and device release, then restores every published file and verifies
its bytes. Restore retains continuous samples and allows eight hours for settlement; an empty
published selection cannot pass readback acceptance. No stage automatically repeats a Media operation. A software refusal before creating
the next file, a partial-write failure, and successful readback are separate findings.

Analyze the saved stream without accessing hardware:

```sh
python3 -B e2e/physical_capacity_analysis.py /private/evidence/archive-samples.jsonl \
  --job-log /private/evidence/job.log --output /private/evidence/capacity
```

The analyzer writes observation/interval/page/event CSVs, a JSON summary and, with matplotlib,
aligned SVG/PNG curves. The optional same-host Job log adds its first error as a one-second
interval and a separate plot of the 15 seconds on either side. Request bounds remain visible;
resets, gaps and partition-coordinate changes break
curves. Compare remaining-to-EW, remaining-to-partition-end and filesystem availability
with byte counters and error events. Their overlap does not alone establish causation or an
exact physical byte position. Page 17 remains available as raw parameter data for vendor-specific
interpretation; an unsupported field is not synthesized from another source.

### Continue to the Drive's EW Boundary

Use [physical_ew_run.py](../../e2e/physical_ew_run.py) for an explicitly authorized
scratch-cartridge experiment that must continue beyond application admission. This is an
LTFS/drive measurement; ordinary Archive acceptance remains the workflow above. The runner
verifies the cartridge, formats once, mounts the installed LTFS and generates an incompressible
AES stream directly into ordinary files. Source data need not occupy another filesystem.
It stops new payload at the first write/Close failure, observation failure or frozen workload
bound, records partial-file cleanup, then attempts normal Index finalization and eject once.
The written cartridge and evidence remain available for review.

Run it in the host-local tmux owner using the quoted command/exit-file construction under
[Launch One Durable Host-Local Stage](#launch-one-durable-host-local-stage). Preserve the numeric controller exit
status with a single `printf '%s\n' "$stage_status"` command; verify that the exit file contains
one integer before interpreting it. An empty file is missing evidence, not exit zero. Keep the
controller's structured lifecycle, LTFS log and captured Index as independent completion evidence.
Supply a new output directory, the verified device aliases,
installed LTFS tools, the `yatm-lto-info` executable and the explicitly authorized barcode.
Choose `--max-bytes` above the measured native capacity for this incompressible workload;
reaching that bound is a stopped experiment, not an observed EW. The optional
`--expected-native-min-bytes` catches an unintentionally shortened cartridge before format.
The CLI requires `--authorize-erase` to repeat the scratch barcode. Existing run directories
cannot replay the destructive sequence.

Build the [test-only SG observer](../../e2e/physical_sg_trace.c) on the Linux host and run
its fake-device tests before using it:

```sh
python3 -B -m unittest discover -s e2e -p 'test_physical_sg_trace.py' -v
cc -std=c11 -O2 -Wall -Wextra -Werror -shared -fPIC \
  e2e/physical_sg_trace.c -o /private/evidence/physical_sg_trace.so -ldl -pthread
```

The runner loads this library only into its foreground LTFS process. It records SG_IO command
times, raw CDBs, status, residual and sense, with bounded LOG SENSE and READ POSITION responses.
File payload is excluded. Optional short READ POSITION queries execute after clean page 31
queries on the same device descriptor, before the owning call releases LTFS's device lock,
at most once per second. Verify this locking path in the installed LTFS build. The preflight
requires usable filesystem/page readings, initialized tracing and a successful position reply.

A position query can receive a deferred write error. Its raw response is retained separately;
an injected error disables further injection, creates a trace error marker and stops new payload.
Such a result establishes observer interaction, not the unmodified LTFS error path. Every command
keeps its own timing, so query cost and sample gaps remain measurable. Passive snapshots use the
retained TapeAlert cache and continue briefly after payload failure; command tracing remains
active through the normal unmount. Evidence contains per-write application returns, file hashes,
Close/rename/delete results, mounted attributes, LTFS logs and captured Indexes.

Analyze the complete or still-growing capture without accessing the drive:

```sh
python3 -B e2e/physical_ew_analysis.py \
  --sg /private/evidence/ew-run/sg.jsonl \
  --samples /private/evidence/ew-run/capture/samples.jsonl \
  --events /private/evidence/ew-run/capture/events.jsonl \
  --metadata /private/evidence/ew-run/capture/metadata.json \
  --output /private/evidence/ew-analysis
```

The analyzer preserves partition attribution, unknown position/buffer fields, query intervals
and current/deferred sense. With optional Matplotlib installed it writes full-run curves and,
when EW is observed, a boundary enlargement from 120 seconds before to 30 seconds after EW.
Successful EW responses have a separate command-warning count from command errors. Compare
observations immediately before and after actual EW with
their different endpoints: page 31 remaining partition capacity, page 0C EOD-to-EW distance,
LTFS availability and logical READ POSITION. A filesystem error or EOP flag alone does not prove
a successful WRITE's EW response, and logical blocks are not physical tape length. Report what
the tested workload establishes separately from a general prediction or complete-file guarantee.

### Interpretation and Additional Observations

Use the [Tape capacity study](../research/tape-capacity.md#evidence-required-for-a-solution) to
choose the question and success criteria before a capacity experiment. Assess pre-write refusal,
retained file readability, cleanup, Index finalization and accurate Library publication separately.
Screen candidate policies against the [continuous data-supply constraint](../architecture/media-io.md#tape-capacity)
before hardware work. Record observation-induced stalls and reposition activity as well as throughput;
a diagnostic synchronization experiment does not qualify recurring forced drains for production.
PT-07 establishes the observed publication and readback of the tested run; it does not guarantee
that every admitted file will fit on other cartridges or workloads.

Use one host-local hardware executor for both capacity and performance experiments. Record the
actual LTFS executable and build, backend, mount options, drive firmware and cartridge identity
with each run. Read drive information through the mounted LTFS implementation's supported
interface; its device lock coordinates the observation with active I/O. An unavailable counter
is an explicit gap, not zero and not permission for another process to access the SG device.

For a capability investigation on an idle, unmounted Linux drive, use the maintained collector
after confirming completed Jobs, device ownership and the assigned cartridge:

```sh
python3 -B e2e/physical_tape_observe.py --device /dev/sgN --output /private/evidence/new-observation
```

Substitute the resolved SG device and a new host-local evidence directory. Run under the existing
host-local executor; load and final eject remain explicit operator steps. The collector checks all
Tape aliases before each query, requests complete raw LOG SENSE and READ POSITION responses, and
records timings and errors. Ownership checks do not reserve the device: keep other users and Jobs
out for the entire experiment. Queries with unsupported fields remain recorded failures, not zeros.
The collector never positions or writes the Tape. Separately approved read-only positioning can
compare the same fields at different locations; record its exact commands and completion status.
`--cartridge-memory` additionally reads HP's documented CM buffer up to its advertised size, capped
at 8 KiB. Keep all raw output private because device pages and cartridge memory can contain
identifying information. Publish only the relevant findings in the capacity study. Fast queries
on an idle drive do not establish acceptable polling cost during Archive.

For a physical-position comparison, add `--position-probe --cartridge-memory` immediately after
each completed authorized position change. This captures full fixed/descriptor REQUEST SENSE
replies and tests the vendor READ POSITION form before the normal observations. REQUEST SENSE can
consume pending sense information, so this mode belongs only to the isolated idle-drive experiment.
Compare beginning, an existing intermediate logical block and recorded EOD in the same partition;
confirm actual logical positions before interpreting any changing field. Preserve response lengths
and unsupported-command errors. Compare CM pages separately from live replies: unchanged stored
EOD information does not describe current head movement.

Capture timestamped raw capacity replies and the `statvfs` block counts and units alongside
logical bytes submitted, file completion, explicit synchronization and final Index results.
Keep request start/end times: separate device queries are not one atomic observation. Interpret
partition numbers using the FORMAT log and label, and record LTFS capacity adjustments before
comparing raw drive units with filesystem available bytes. A passive sample does not synchronize
the filesystem. Synchronization experiments record the requested operation, duration and error
separately from the resulting capacity sample.

When diagnosing an authorized capacity run, preserve its available write-error, corrected-rewrite
and TapeAlert observations before unloading. On HP LTO-5, LOG page `0x02` is specific to the current Tape,
updates on physical data-set writes and clears on unload. Its parameters count different units
(including data sets and CCQ sets), so a retry count is not a byte count. Treat reload, reset,
unsupported fields and counter decreases explicitly instead of subtracting across them. Establish
the effects of head or media degradation from the drive's documented write/rewrite mechanism and
capacity guarantees. A run on one cartridge of unknown condition cannot prove those general
properties; it supplies observations of that run only.

Compare native capacity consumed with both logical content and corrected-write activity for
compressible and incompressible input. The [capacity study](../research/tape-capacity.md#compression-and-rewrites)
explains why a prior compression ratio or retry allowance does not prove that the next complete
file will fit. Record successful admission, rejection before output creation, and failure after
partial output as different results. End-of-media alone is not a passing pre-write refusal test.
