# Physical Tape Host Runbook

How to run the [physical Tape E2E](physical-tape-e2e.md) and [tar acceptance](physical-tar-acceptance.md)
stages on the assigned Linux host. The gate documents own cases and pass criteria; the safety
preconditions in the E2E suite apply to every step here.

## Automated Stages

### Prepare the Artifacts and Host Context

Use checksum-verified Linux amd64 main and matching Preview packages from the reviewed source.
Local builds use `build/release/build.sh` and `previewworker/build/build.sh`. Reviewed locally
compiled binaries can begin affected hardware checks once their source and nonphysical checks
pass, independently of CI's final release-byte gate. This installer-based procedure uses packages
so that installation and runtime adapters are also exercised. Final release-byte acceptance and
the evidence comparison in [Evidence Reuse](release.md#evidence-reuse) still apply.

On the authorized Linux hardware host, use a root shell.
Prefer a host-local executor with `--host local`; SSH transfers inputs and retrieves results.
Allocate an existing spacious, xattr-capable filesystem
for the isolated installation, fixtures and private `TMPDIR`. Keep controller inputs, `state.json`
and evidence outside the installation root that cleanup removes.

Transfer the main archive, Preview helper archive, matching
`yatm-preview-source-linux-amd64-<version>.tar.gz` and their `.sha256` companions.
Keep the corresponding-source archive beside the Preview helper; the package validator requires it
before installation. Transfer the reviewed controller's
`e2e/physical_package_acceptance.py`, `e2e/physical_tar_recovery.py` and `e2e/package*.py`, and these validators, preserving their
repository-relative layout under `CONTROLLER_ROOT`:

- `build/release/check-release.mjs`, `archive-members.mjs` and `package-assets.mjs`;
- `build/release/check-candidate-set.mjs` and `check-preview-sources.mjs`.

Include the operational dependencies of every selected check, beyond its entrypoint. For the
Linux virtual-unmount regression, transfer `e2e/test_package_tape_scripts.py` and
`e2e/ltfs-file-backend/umount` plus `get_device`. Broader Tape-script unit selections additionally
need `scripts/mkfs` and `scripts/get_device`. Source-based LTFS E2E uses `e2e/testdata` adapters;
its `umount-file-failures.sh` calls `umount-file.sh`, which delegates to the same
`e2e/ltfs-file-backend/umount` and `get_device`. Keep those relative paths and executable modes.
Source-based Go E2E remains a local/CI check under the [E2E guide](../development/e2e-test.md), separate from
the host's installed-package procedure.

Supply absolute host-local values for `CONTROLLER_ROOT`, `TEST_PARENT`, `EVIDENCE_ROOT`,
`HOST_TMP`, `MAIN_ARCHIVE`, `PREVIEW_ARCHIVE`, `STATE` and the official LTFS executables.
Set `STATE` to the new run's `state.json`. `VERSION` and `COMMIT` identify the archives;
the controller's reviewed source identity is recorded separately. Supply `SCRATCH_DEVICE` and
`SCRATCH_BARCODE` from the explicit assignment. Keep actual host, device, barcode and local path
values in the private stage record and evidence.

Run from the absolute controller directory in every new host shell:

```bash
set -euo pipefail
cd -- "$CONTROLLER_ROOT"
umask 077
mkdir -p -- "$EVIDENCE_ROOT" "$HOST_TMP"
export TMPDIR="$HOST_TMP"
sha256sum -- "$MAIN_ARCHIVE" "$PREVIEW_ARCHIVE" > "$EVIDENCE_ROOT/packages.sha256"
sha256sum e2e/physical_package_acceptance.py e2e/physical_tar_recovery.py e2e/package*.py \
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
the stage record. Choose the mount template for that LTFS distribution as the
[Tape scripts guide](../operations/tape-scripts.md#mount-and-unmount) describes. Normal unmount must
finish the LTFS process and leave the final current-cartridge `TAPE_DIR/<barcode>.schema`.

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
the stage record. The real file-backend check below selects one exact package case and its declared
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

In a host-local root Bash shell, set `STAGE` to the next authorized stage and
`STAGE_OUT`, `STAGE_LOG`, `STAGE_EXIT` and `SESSION` to new paths/a unique tmux name from the
stage record. Put the log and exit file in an existing evidence directory. Recheck live drive ownership
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

The host's tmux process keeps the stage alive when the SSH session ends. Read status on
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

### Stage Continuation and Review

Advance only from completed stages with the same state and a new output directory. Reconnecting
to an existing executor or collecting its completed evidence never resumes a Tape write:

- `baseline` can reuse a normally completed FORMAT and its prepared APPEND Job only while the APPEND write has not been admitted.
  It rechecks Media identity, encryption and FORMAT evidence before continuing. An admitted APPEND
  write or incomplete/failed FORMAT requires operator diagnosis rather than another baseline write.
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
record its changed hash and collect the existing result only where the guards above permit it. State records remain
the evidence of what happened. An unresolved extent or Index/Position mismatch remains an
unmet acceptance check even when the write or unmount completed.

Before each stage, record the authorized host, drive, cartridge and destructive scope; the exact
source, package, program, controller and adapter hashes, LTFS selection, tools and host context; the
absolute controller, input, state, evidence, installation and TMPDIR paths, service name and CLI
URL; the selected stage, its launch and status commands and log, exit and report locations; and
completed checkpoints and Job IDs, the first unfinished check, pass criteria and stop condition.

Only one executor uses the drive. Review stage reports and compact evidence before running the
separate `cleanup` invocation. Cleanup seals and verifies the shared Job evidence
before deletion, preserves state and stage reports outside the removed installation, and leaves
the cartridge ejected. After review, remove only the owned precheck/fixture resources and apply
the agreed evidence retention policy. Failed hardware remains available for diagnosis; cleanup
does not establish a pass for its failed write.

Evidence collection copies the settled Job's original `job.log` and verifies its checksum, like
the other bundle artifacts. The [CLI output contract](../operations/cli.md) exposes text for
inspection, not a byte-preserving log archive. Preparation exercises the real packaged Job and
collector together; parser unit tests alone do not verify that boundary.

The configured `readinfo` script starts every physical operation with `mt -f DEVICE load`. It then spends at most about one minute polling the explicit MAM Barcode field. The script removes an attached media-generation suffix such as `L5`. A successful, explicitly empty field allows FORMAT with the assigned scratch barcode; formatting writes that identity. APPEND and Restore require the existing device identity. A failed probe or unreadable field stops the operation. The [Tape contract](../architecture/tape.md#tape) owns these identity rules.

Normal unmount keeps the Job attempt and device lease until the mount is gone, no process holds the resolved SG device, and `mt -f DEVICE status` reports `DR_OPEN`. This post-unmount wait shares a ten-minute deadline. A successful Job therefore exposes the drive only after eject completion; timeout follows the unmount-failure rules below.

Capacity acceptance uses ordinary Archive sources and a complete Restore of its published
selection. FORMAT, APPEND and Restore remain separate explicit Media submissions.

An unmount failure invalidates the attempt and keeps the device unavailable for the lifetime of that YATM process. Stop the isolated YATM service before manual cleanup, identify processes holding its recorded owned mount with `fuser -vm MOUNT_POINT`, and have them release the mount before attempting a normal `umount`. A force or lazy unmount is only a last-resort manual cleanup after the service has stopped; discard that write result and restart YATM before using the device again. Do not enter an owned LTFS mount while a physical stage is running.

Stopping a systemd unit can also terminate its LTFS child. If the mount has already disappeared,
verify that no process holds the assigned SG device. When a normal eject reports "Medium removal
prevented", release that failed session's lock with `mt -f DEVICE unlock`, then use
`mt -f DEVICE offline` and confirm `DR_OPEN`. This cleanup never makes the failed write acceptable.
