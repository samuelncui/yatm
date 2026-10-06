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
5. Use an isolated copy of the configured format script whose `mkltfs` command includes `-r 'size=1M'`. Keep this acceptance policy in the test adapter copy.
6. Reserve about 20 GiB for three 4 GiB ordinary boundary source files and up to two restored files, plus headroom for the baseline fixture, databases and evidence. The bulk filler streams directly to Tape and requires no cartridge-sized local file.
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
| HPE | `scripts/mount` | `-o work_directory="${TAPE_DIR}" -o capture_index` |
| openltfs | `scripts/mount.openltfs` | `-o work_directory="${TAPE_DIR}" -o capture_index="${TAPE_DIR}"` |

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
| `full-write` | PT-07: stopped-service prefill and one ordinary Archive write to its durable `no_space` checkpoint |
| `full-verify` | PT-07: recheck that checkpoint and restore the submitted boundary files |
| `cleanup` | PT-08: preserve/seal evidence, delete test Jobs, stop/remove the owned installation after coordinator review |

`prepare` requires absent state. Later invocations require that same state, matching package
hashes, host, device, barcode and LTFS paths, and the completed preceding stage. Each successful
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
  prepare|baseline|restore|full-write|full-verify|cleanup) ;;
  *) echo 'Select one documented physical stage' >&2; exit 1 ;;
esac
printf -v stage_command '%q ' python3 e2e/physical_package_acceptance.py \
  "${common[@]}" --physical-stage "$STAGE" --out "$STAGE_OUT"
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
- `restore` and boundary Restore can collect/check an already submitted Job after it settles
  normally; they do not submit that saved Job's Media operation again.
- `full-write` with an existing Archive Job only validates its complete `no_space` checkpoint.
  Interrupted prefill, fixture generation or an incomplete checkpoint remains failed work.
  `full-verify` examines the settled write and performs readback without another Archive write.

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

The configured `readinfo` script starts every physical operation with `mt -f DEVICE load`. It then spends at most about one minute polling the explicit MAM Barcode field. The script removes an attached media-generation suffix such as `L5`; YATM validates the resulting six-character identity before encryption, formatting, or mounting.

Normal unmount keeps the Job attempt and device lease until the mount is gone, no process holds the resolved SG device, and `mt -f DEVICE status` reports `DR_OPEN`. This post-unmount wait shares a ten-minute deadline. A successful Job therefore exposes the drive only after eject completion; timeout follows the unmount-failure rules below.

The full-write stage first fills most of the cartridge with the isolated service stopped, streaming incompressible data directly to an owned LTFS mount and leaving about 8 GiB available. It unmounts and ejects normally, retains the final Index, then restarts the service. The filler is outside Library inventory. Three ordinary 4 GiB source files exercise the actual Archive capacity boundary through the packaged CLI.

The stage saves its Archive Job ID before submitting the Archive write and permits that write only for the Job created by that invocation. A later invocation with an existing Job validates its complete no-space checkpoint and exits without repeating the write. An incomplete prefill, fixture generation or checkpoint stops the stage and retains evidence for diagnosis. The full-verify stage never writes Archive data.

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
3. Compare the returned barcode with the recorded barcode.

Expected results:

- Inspection completes without a device-busy or device-discovery error.
- The barcode is non-empty and matches the cartridge label.
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

1. In the full-write stage, stop the isolated service, verify the cartridge identity and stream the filler to Tape. Preserve the filler manifest and final Index after normal unmount/eject, then restart the service. Create the three-file ordered `eom` fixture and checkpoint a new Archive Job, then wait until it is ready for Media.
2. Reload the original cartridge and write the Job in `APPEND` mode.
3. Wait until the mounted Tape has insufficient capacity for the next complete file or the device reports end-of-media, and YATM has unmounted, captured the final Index, and ejected the cartridge.
4. In the full-verify stage, compare the Archive item states, Tape Media, Positions, final captured Index, Job progress, and Archive report.
5. Reload the first cartridge and restore the first and last submitted `eom` files to an empty directory.
6. Compare both restored sizes and SHA-256 values with the generation manifest, then eject the cartridge.

Expected results:

- A conservative pre-write boundary or device write/close no-space error is normalized to the same portable `no_space` result. The failed Archive Media operation returns the Job to its pre-Media `READY` state with its failure reason/timing and no live phase. Its prepared manifest and successfully submitted prefix remain for another explicit load-Media operation; pending items remain unprocessed.
- After the usable final Index and Library commit are durable, the Job log contains `event=archive_media_checkpoint`, `reason=no_space`, `media_id`, `files`, `bytes`, and the complete diagnostic `error` field. Test control flow reads these stable fields rather than human-readable error text.
- At least one item is `SUBMITTED` and at least one item is `PENDING`. Submitted items form one continuous prefix of the deterministic Archive order.
- Every successfully finalized prefix item has the first Tape's Media ID and one matching Library Position. No pending item has a Media ID or Library Position, including the first item rejected at the capacity boundary and every later prefetched item.
- No Archive item remains `STAGED`. If the device reached end-of-media during a write, a partial physical file may be present in the LTFS Index, but it is never published as a valid Position unless its path, size, and extents match the completed ACP result.
- The original Tape Media remains committed as `ltfs_v1`; its final captured Index and typed Position metadata retain valid extents and storage order for every submitted non-empty file.
- The filler remains in the final Index with its complete size and data-partition extents; it never appears as a Library Position. Both earlier Archives remain intact.
- Within each partition, the submitted physical files retain the deterministic Archive prefix order.
- Job progress and the Archive report count only submitted files and bytes; they do not count a partial or unverified suffix.
- The first and last submitted boundary files remain readable from the full Tape and match their original SHA-256 values.
- While finalization is active, the drive remains unavailable and Job deletion is rejected; completion occurs only after eject.

### PT-08: Cleanup and Evidence

1. Export the Library using the Library metadata-backup format and retain it with the test evidence.
2. Before deleting anything, copy each Job's catalog and bundle metadata, complete Job log, LTFS log, Archive report, and captured Index into the evidence directory.
3. Generate a path-sorted SHA-256 manifest for the preserved Job evidence. Any capture or checksum failure stops cleanup and leaves every Job intact.
4. Delete the completed and pending test Jobs through the API.
5. Stop the isolated YATM instance and remove its temporary databases, Job directories, mount points, ordinary boundary source files, and restored output after collecting the compact evidence.

Expected results:

- The export contains the Tape profile, Files, and physical Positions needed to describe the completed run.
- The evidence checksum manifest verifies every preserved Job artifact before Job deletion.
- Job deletion removes the corresponding local Job directories.
- Cleanup does not modify or erase the physical cartridge.
- The cartridge is ejected and clearly marked as scratch or test media.

## Optional Physical Cases

The first physical full-Media boundary is mandatory. The cases below either require a second cartridge or repeat destructive formatting, so they are optional unless the affected behavior changed.

### PT-X1: Continue the Full Job on a Second Tape

Continue the pending PT-07 Archive Job on a second scratch cartridge:

1. Load the second scratch cartridge and write the remaining items in `FORMAT` mode with its own barcode.
2. Verify that the Archive Job completes and that every item is submitted to exactly one of the two Tape Media.
3. Restore the complete `eom` logical set by loading both cartridges as requested.
4. Compare every restored size and SHA-256 with the generation manifest.

Do not add a synthetic FUSE source or streaming-file abstraction solely for this case. The physical test should exercise ordinary source files through the production I/O path.

### PT-X2: Explicit Delete and Reformat

After preserving the original evidence, delete the Tape Media metadata through YATM, create a new Archive Job, and format the same physical barcode again. Verify that FORMAT is rejected before metadata deletion, succeeds after deletion, creates a new Media ID, and does not delete the old logical Files.

## Pass Criteria and Evidence

The release gate passes only when PT-01 through PT-08 complete without manual database edits or changes to archived Tape files. The controlled stopped-service prefill creates only its separate filler file. PT-X1 and PT-X2 are recorded separately when run. Retain:

- the version and hardware record;
- source and restored SHA-256 manifests;
- packaged CLI request results and the reused local/CI Session identity and failed-Media lifecycle evidence;
- every Job log and report;
- every Job's catalog and bundle metadata plus the sorted evidence SHA-256 manifest;
- captured LTFS Index files from FORMAT and APPEND;
- the prefill manifest, LTFS log and captured Index;
- the Library JSONL metadata backup;
- a concise result for each case, including elapsed Archive, full-Media boundary, and Restore time.

Record a failure at the first unmet expected result. Preserve the cartridge and local evidence for diagnosis instead of reformatting and retrying immediately.
