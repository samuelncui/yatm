# Physical Tape E2E Test Suite

Status: Hardware release gate. See the [development checks](../development/testing.md); this guide does not authorize a physical run.

This suite is a hardware gate in the [Release SOP](release.md#release-sop). After affected source and nonphysical checks pass, it can run from a reviewed local build while release CI proceeds, following [Evidence Reuse](release.md#evidence-reuse). It covers behavior that the LTFS `file` backend cannot prove: drive discovery, hardware encryption, physical partition placement, tape motion, unload and reload, reads after a process restart, and a real physical full-Media boundary. Run the automated LTFS file-backend E2E first; it remains the primary coverage for injected failures, deterministic capacity exhaustion, two-Media completion, backup compatibility, and cleanup.

## Safety and Preconditions

`FORMAT` destroys every existing file on the loaded cartridge. Use one explicitly assigned scratch cartridge whose contents may be erased. A second scratch cartridge is needed only for the optional continuation case. Never run this suite against a production Library database, production Job directory, or a cartridge containing valid data.

Before starting:

1. Record the expected six-character barcode and confirm it on the physical cartridge.
2. Use an isolated YATM configuration with empty Executor and Library databases and dedicated work, source, and restore directories.
3. Confirm that the configured Tape device resolves to its SCSI generic device through Linux sysfs. The host needs Bash, Python 3, Node.js, tmux, systemd, the official LTFS programs, `mt`, `stenc`, `umount`, `fuser`, `timeout`, `openssl`, `sqlite3`, `findmnt`, and the package controller's ordinary installation/xattr tools. The main package supplies `yatm-lto-info`.
4. Confirm that the drive, cartridge generation, LTFS implementation, and encryption support are compatible.
5. Baseline preparation adds `-r 'size=1M'` only to the isolated format adapter. Capacity preparation uses ordinary packaged placement; use a separate fresh installation and state for that sequence.
6. Baseline preparation retains the package controller's 4 GiB free-space prerequisite. Capacity requires more than twice the selected payload plus 8 GiB, or payload plus the largest file plus 8 GiB with `--capacity-source-reflink`; see [capacity measurement](physical-measurements.md#continuous-boundary-capture). Budget full published Restore output before writing.
7. Record the YATM commit, ACP commit, LTFS version, drive model and firmware, cartridge generation, native cartridge capacity, barcode, and start time.

Keep the host on stable power and reserve the drive for the complete run. Do not test process crashes or power loss as part of this suite.

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
3. Compare a non-empty returned barcode with the assigned scratch barcode. If the cartridge memory barcode
   is empty, inspect again with `--identity BARCODE` to check Library registration for the explicitly
   assigned identity before FORMAT.

Expected results:

- Inspection completes without a device-busy or device-discovery error.
- A reported cartridge memory barcode matches the assigned scratch barcode. A successfully read empty
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
3. Request FORMAT and then APPEND with a different, unregistered barcode through the packaged CLI and record both preflight refusals against the loaded physical cartridge.

Expected results:

- FORMAT reports the requested/device barcode mismatch; APPEND reports that the requested barcode has no registered Media. An unavailable cartridge memory barcode is not a pass.
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

Use the separate [capacity sequence](physical-measurements.md#continuous-boundary-capture) with its frozen input sizes
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

The required baseline reports are `prepare`, `baseline` and `restore`; tar also requires
`tar-clear-barcode` and `tar-fill` when they completed.
Capacity requires `performance-prepare`, `performance-format`, `capacity-write` and `capacity-verify`, including
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

`SYSCALL_TRACES` is the explicit nonempty list of finalized host files from the stage record. If no
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
relevant package smoke acceptance under [Evidence Reuse](release.md#evidence-reuse).

Record a failure at the first unmet expected result. Preserve the cartridge and local evidence for diagnosis instead of reformatting and retrying immediately.
