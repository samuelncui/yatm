# Physical Tape E2E Test Suite

Status: Hardware release gate. See [test environment safety](testing.md); this guide does not authorize a physical run.

This suite is the release gate for behavior that the LTFS `file` backend cannot prove: drive discovery, hardware encryption, physical partition placement, tape motion, unload and reload, reads after a process restart, and a real physical full-Media boundary. Run the automated LTFS file-backend E2E first; it remains the primary coverage for injected failures, deterministic capacity exhaustion, two-Media completion, backup compatibility, and cleanup.

## Safety and Preconditions

`FORMAT` destroys every existing file on the loaded cartridge. Use one explicitly assigned scratch cartridge whose contents may be erased. A second scratch cartridge is needed only for the optional continuation case. Never run this suite against a production Library database, production Job directory, or a cartridge containing valid data.

Before starting:

1. Record the expected six-character barcode and confirm it on the physical cartridge.
2. Use an isolated YATM configuration with empty Executor and Library databases and dedicated work, source, and restore directories.
3. Confirm that the configured Tape device resolves through `sg_map` and that `mkltfs`, `ltfs`, `mt`, `stenc`, `umount`, `fuser`, `timeout`, and `yatm-lto-info` are available.
4. Confirm that the drive, cartridge generation, LTFS implementation, and encryption support are compatible.
5. Use an isolated copy of the configured format script whose `mkltfs` command includes `-r 'size=1M/name=*.txt'`. Do not change the default production policy solely for this test.
6. Confirm that the source and restore filesystems have enough real free space for the baseline fixture, one native cartridge of incompressible data, and the selected boundary-file restores.
7. Record the YATM commit, ACP commit, LTFS version, drive model and firmware, cartridge generation, native cartridge capacity, barcode, and start time.

Keep the host on stable power and reserve the drive for the complete run. Do not test process crashes or power loss as part of this suite.

## Automated Stages

Build `./e2e` as a Linux test binary and place it, `yatm-lto-info`, and isolated copies of the configured Tape scripts under the test root. Set the following environment variables for every stage:

```bash
export YATM_E2E_PHYSICAL_ROOT=/path/to/isolated-test-root
export YATM_E2E_PHYSICAL_DEVICE=/dev/nst0
export YATM_E2E_PHYSICAL_BARCODE=ABC001
export YATM_E2E_PHYSICAL_SCRIPTS="$YATM_E2E_PHYSICAL_ROOT/scripts"
```

Run the four stages as separate processes so Restore proves restart behavior and post-boundary validation can be retried without filling the Tape again:

```bash
cd "$YATM_E2E_PHYSICAL_ROOT"
YATM_E2E_PHYSICAL_STAGE=baseline ./yatm-physical-e2e.test -test.v -test.run TestPhysicalTapeBaseline -test.timeout 2h
YATM_E2E_PHYSICAL_STAGE=restore ./yatm-physical-e2e.test -test.v -test.run TestPhysicalTapeRestartRestore -test.timeout 2h
scripts/generate-physical-eom.sh source/eom 25 64
YATM_E2E_PHYSICAL_STAGE=full-write ./yatm-physical-e2e.test -test.v -test.run TestPhysicalTapeFullBoundaryWrite -test.timeout 8h
YATM_E2E_PHYSICAL_STAGE=full-verify ./yatm-physical-e2e.test -test.v -test.run TestPhysicalTapeFullBoundaryVerify -test.timeout 4h
```

The configured `readinfo` script starts every physical operation with `mt -f DEVICE load`. It then spends at most about one minute polling the explicit MAM Barcode field. The script removes an attached media-generation suffix such as `L5`; YATM validates the resulting six-character identity before encryption, formatting, or mounting.

Normal unmount keeps the Job attempt and device lease until the mount is gone, no process holds the resolved SG device, and `mt -f DEVICE status` reports `DR_OPEN`. This post-unmount wait shares a ten-minute deadline. A successful Job therefore exposes the drive only after eject completion; timeout follows the unmount-failure rules below.

The full-write stage saves its Archive Job ID before mounting the Tape and permits a write only for the Job created by that invocation. A later invocation with an existing Job validates a complete no-space checkpoint and exits without writing. If its structured checkpoint event, final Index, item prefix, or Positions are incomplete, the stage fails immediately; clean the isolated environment and start the full-Media case again. The full-verify stage never writes Archive data and may be rerun after a test assertion or restore-side failure.

An unmount failure invalidates the attempt and keeps the device unavailable for the lifetime of that YATM process. Stop YATM before cleanup, identify and leave any process holding the mount with `fuser -vm /tmp/yatm-ltfs-*`, then attempt a normal `umount`. A force or lazy unmount is only a last-resort manual cleanup after the service has stopped; discard that write result and restart YATM before using the device again. Do not enter a `/tmp/yatm-ltfs-*` mount while a physical stage is running.

## Fixture

Create deterministic source files and record their size and SHA-256 before starting:

| Path | Content | Expected LTFS partition |
| --- | --- | --- |
| `dataset/index-small.txt` | 60 KiB | Index |
| `dataset/data-small.bin` | 64 KiB | Data |
| `dataset/data-large.txt` | 2.15 MiB | Data |
| `dataset/empty.bin` | Empty | No data extent |
| `dataset/nested/payload.fixture` | 6.25 MiB supported by the test Preview generator | Data |
| `append/index-small.txt` | Different 65 KiB content | Index |
| `append/data.bin` | 2.34 MiB of non-sparse data | Data |

The placement rule uses AND semantics: a file must be no larger than 1 MiB and match `*.txt` to be placed in the index partition. Resolve the physical partition letters from the LTFS partition map; the usual mapping is index `a` and data `b`.

For the mandatory full-Media case, create an `eom` directory containing deterministic high-entropy, non-sparse files in lexical path order. Use independently checksummed 64 or 256 GiB chunks whose total size exceeds the cartridge's recorded native capacity by at least one complete chunk. Use one fixed test seed with a distinct stream or IV per file, and retain the path, size, stream identifier, and SHA-256 manifest. Do not use `truncate`, sparse files, zero-filled files, or repeating byte patterns: filesystem holes and drive compression would make logical size an invalid proxy for physical Tape consumption.

## Required Cases

Run the cases in order because they share one cartridge and one isolated YATM installation.

### PT-01: Device and Barcode Preflight

1. Start YATM with the isolated configuration and load the scratch cartridge.
2. Inspect the configured Tape device through the Media API.
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
- `index-small.txt` is on the index partition. `data-small.bin`, `data-large.txt`, and `payload.fixture` are on the data partition. `empty.bin` has no extent.
- The Job Tape directory contains the LTFS log, captured Index, Archive report, and manifest.
- The Preview bundle and its HTTP asset are readable.

### PT-03: Reload and Inspect the Durable Tape

1. Reload the ejected cartridge with `mt -f DEVICE load` without changing the databases. A standalone drive may take about one minute to become ready.
2. Inspect it through the Media API.
3. Leave it loaded for the next Archive operation; inspection does not mount or change its contents.

Expected results:

- The physical barcode resolves to the Media created by PT-02.
- Media ID, name, format, file count, and written bytes match the committed Library state.
- Inspection does not format, mount, or modify the Tape or Library.

### PT-04: Reject a Different Requested Barcode

1. Reload the same cartridge.
2. Create a second Archive Job for `append` and wait until it is ready for Media.
3. Attempt its Archive write using a different requested barcode.

Expected results:

- The Job log contains an exact mismatch with both requested and device barcodes. A MAM-unavailable result is not a pass; the automated case may retry it once and must then observe the mismatch.
- YATM rejects the operation before creating the requested Tape work directory, configuring encryption, formatting, mounting, or copying.
- The Archive Job remains retryable and the existing Tape Media and Positions are unchanged.
- Reloading and inspecting the cartridge still returns the original barcode.

### PT-05: Append to the Existing Tape

1. Retry the pending Archive Job from PT-04 against the same cartridge with mode `APPEND` and the original barcode.
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

1. In the full-write stage, create and checkpoint a new Archive Job for the ordered `eom` fixture, then wait until it is ready for Media.
2. Reload the original cartridge and write the Job in `APPEND` mode.
3. Wait until the mounted Tape has insufficient capacity for the next complete file or the device reports end-of-media, and YATM has unmounted, captured the final Index, and ejected the cartridge.
4. In the full-verify stage, compare the Archive item states, Tape Media, Positions, final captured Index, Job progress, and Archive report.
5. Reload the first cartridge and restore the first and last submitted `eom` files to an empty directory.
6. Compare both restored sizes and SHA-256 values with the generation manifest, then eject the cartridge.

Expected results:

- A conservative pre-write boundary or device write/close no-space error is normalized to the same portable `no_space` result. The Archive Job returns to `PENDING` instead of becoming failed or completed.
- After the usable final Index and Library commit are durable, the Job log contains `event=archive_media_checkpoint`, `reason=no_space`, `media_id`, `files`, `bytes`, and the complete diagnostic `error` field. Test control flow reads these stable fields rather than human-readable error text.
- At least one item is `SUBMITTED` and at least one item is `PENDING`. Submitted items form one continuous prefix of the deterministic Archive order.
- Every successfully finalized prefix item has the first Tape's Media ID and one matching Library Position. No pending item has a Media ID or Library Position, including the first item rejected at the capacity boundary and every later prefetched item.
- No Archive item remains `STAGED`. If the device reached end-of-media during a write, a partial physical file may be present in the LTFS Index, but it is never published as a valid Position unless its path, size, and extents match the completed ACP result.
- The original Tape Media remains committed as `ltfs_v1`; its final captured Index and typed Position metadata retain valid extents and storage order for every submitted non-empty file.
- Within each partition, the submitted physical files retain the deterministic Archive prefix order.
- Job progress and the Archive report count only submitted files and bytes; they do not count a partial or unverified suffix.
- The first and last submitted boundary files remain readable from the full Tape and match their original SHA-256 values.
- While finalization is active, the drive remains unavailable and Job deletion is rejected; completion occurs only after eject.

### PT-08: Cleanup and Evidence

1. Export the Library using the Library metadata-backup format and retain it with the test evidence.
2. Before deleting anything, copy each Job's catalog and bundle metadata, complete Job log, LTFS log, Archive report, and captured Index into the evidence directory.
3. Generate a path-sorted SHA-256 manifest for the preserved Job evidence. Any capture or checksum failure stops cleanup and leaves every Job intact.
4. Delete the completed and pending test Jobs through the API.
5. Stop the isolated YATM instance and remove its temporary databases, Job directories, mount points, large EOM fixture, and restored output after collecting the compact evidence.

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

The release gate passes only when PT-01 through PT-08 complete without manual database edits or direct modifications to mounted Tape files. PT-X1 and PT-X2 are recorded separately when run. Retain:

- the version and hardware record;
- source and restored SHA-256 manifests;
- gRPC request results or equivalent UI operation records;
- every Job log and report;
- every Job's catalog and bundle metadata plus the sorted evidence SHA-256 manifest;
- captured LTFS Index files from FORMAT and APPEND;
- the Library JSONL metadata backup;
- a concise result for each case, including elapsed Archive, full-Media boundary, and Restore time.

Record a failure at the first unmet expected result. Preserve the cartridge and local evidence for diagnosis instead of reformatting and retrying immediately.
