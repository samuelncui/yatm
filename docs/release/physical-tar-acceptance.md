# Physical Tar Acceptance

The tar stage sequence of the physical Tape gate, run through the installed service and CLI.
The [physical Tape E2E suite](physical-tape-e2e.md) owns safety preconditions and evidence rules, and
the [host runbook](physical-tape-runbook.md) owns stage launch.

## Tar Acceptance

The [LTFS cases](physical-tape-e2e.md#required-cases) exercise LTFS; their Index, partition-placement, Preview and eject
assertions do not establish tar acceptance. Tar acceptance is a separate stage sequence of the same
[controller](../../e2e/physical_package_acceptance.py) with `--physical-format tar_pax_v1`. Like
PT-01..PT-08, it drives only the installed service and `yatm-cli`, so it exercises the business
logic users run; no source-level hardware test participates. Every stage uses the configured
`encrypt` adapter, because tar has no plaintext path, and passive read learning, which `prepare`
enables explicitly in Device settings. The [public-tool reader](../../e2e/physical_tar_recovery.py)
also generates and checks the nine-file fixture oracle; the [recovery guide](../operations/tar-tape-recovery.md)
owns the operator commands.

Assign an erasable cartridge, its exact barcode, the configured Tape device and the SCSI generic
path that sysfs resolves for it. Besides Python 3, systemd, tmux and the installer tools, the host
needs sg3_utils (`sg_raw`, `sg_read_attr`), `journalctl`, mt-st 1.8, coreutils `dd`, GNU `tar`,
`fuser`, `findmnt`, `sqlite3` and the tools the encryption adapter calls, such as `stenc`; `prepare`
checks the commands before installing. Choose explicit `fixed` or `variable` records and a
512-byte-aligned size within the observed device limits; an unknown limit or refused preparation is
an unresolved result, not permission to guess. Transfer `e2e/physical_package_acceptance.py`,
`e2e/physical_tar_recovery.py` and `e2e/package*.py`, plus `e2e/physical_performance_*.py` for
`tar-fill` and `tar-ew`. Record the package, program and tool identities, drive/firmware, driver/HBA and
cartridge density, and retain evidence outside the installation.

Launch each stage in the host-local tmux owner described under
[Launch One Durable Host-Local Stage](physical-tape-runbook.md#launch-one-durable-host-local-stage), replacing its argument
and selector block with:

```bash
tar_common=(
  --host local --test-parent "$TEST_PARENT"
  --archive "$MAIN_ARCHIVE" --version "$VERSION" --commit "$COMMIT"
  --physical-format tar_pax_v1 --physical-device "$SCRATCH_DEVICE" --sg-device "$SCRATCH_SG"
  --physical-barcode "$SCRATCH_BARCODE" --record-mode "$RECORD_MODE" --record-bytes "$RECORD_BYTES"
  --state "$STATE"
)
case "$STAGE" in
  prepare|baseline|restore) stage_options=() ;;
  tar-clear-barcode) stage_options=(--confirm-clear-barcode
    "CLEAR BARCODE $SCRATCH_DEVICE $SCRATCH_SG $SCRATCH_BARCODE") ;;
  tar-fill|tar-ew) stage_options=()
    for size in "${FILE_MIB[@]}"; do stage_options+=(--capacity-file-mib "$size"); done ;;
  tar-read-order) stage_options=() ;;
  cleanup) stage_options=(--no-syscall-trace) ;;
  *) echo 'Select one documented tar stage' >&2; exit 1 ;;
esac
printf -v stage_command '%q ' python3 e2e/physical_package_acceptance.py \
  "${tar_common[@]}" "${stage_options[@]}" --physical-stage "$STAGE" --out "$STAGE_OUT"
```

The order is `prepare → [tar-clear-barcode] → baseline → restore → [tar-fill → [tar-ew → [tar-read-order]]] → cleanup`. Each
stage requires its predecessor in the preserved state, saves an admission marker before its first
device or Job operation and is never replayed. Every assertion retains stdout, stderr, exit status,
Job settlement and hashes in the evidence directory; keys are redacted from transcripts and
evidence. `tape device inspect` defines no barcode field, so the controller reads MAM attribute
0806h with `sg_read_attr --raw` on the confirmed SG path and only records the inspection.

| Stage | Pass criteria |
| --- | --- |
| `prepare` | Fresh installation configured with only the `encrypt` adapter, fixture oracle, source and Restore Locations, Device settings with `passive_read_learning: true` and LOGICAL read order, and READY FORMAT, APPEND and mismatch Archive Jobs. No Tape access. |
| `tar-clear-barcode` | Optional preparation before FORMAT, for a cartridge whose 0806h is not empty. Requires the unregistered barcode and no holder of any Tape/SG alias. Sends WRITE ATTRIBUTE `sg_raw -s 9 -i PARAMS SG 8D 00 00 00 00 00 00 00 00 00 00 00 00 09 00 00` with the parameter list `00 00 00 05 08 06 01 00 00` (0806h, ASCII, length 0 clears it). `sg_read_attr` must then report no barcode, and `tape device inspect` none. A refusal is recorded in `mam/clear-write.json` and stops the stage; there is no fallback. |
| `baseline` | 0806h is empty before FORMAT. FORMAT settles with the requested tar profile; after reload, 0806h holds the barcode as a 32-byte space-padded value. FORMAT and then APPEND of the mismatch Job with a different barcode are refused by the CLI or by the leased Session (idle READY with its error), publish nothing and leave Media, Positions and 0806h unchanged. After reload, APPEND keeps the Media, profile and earlier Positions, adds exactly its members, logs `tar APPEND tail verified`, and neither its Job log nor the service journal mentions a sequential scan. Settled Item rows match the published locators and have cleared provisional results. |
| `restore` | After the restart, full and selected Restores without `--media-id` identify the cartridge by cartridge memory barcode; output bytes, paths, permissions, subsecond times and xattrs match the oracle. Inventory retains every member without a content finding, and Verify matches every copy. Each Restore and Scan bundle has `tapes/BARCODE/read-observations.jsonl` with a parseable line, and a learned model for the device appears in `settings devices` within 600 seconds after release. LOGICAL and AUTO Restores of the full selection record their duration, progress `elapsed_ms`, Job-log read-order decision and inspected `prepared_read`; the comparison and the model change after the second Restore are report only, with no threshold. `job delete` of the selected Restore removes its bundle. Public-tool recovery passes. |
| `tar-fill` | The tar equivalent of PT-07. Requires the reviewed `FILE_MIB` plan and optional `--capacity-source-reflink`; free space must cover the sources (the largest file with reflink), three sampled members and 8 GiB; when inspection reports remaining capacity, the payload must exceed it. One Archive APPEND writes through the normal path until capacity admission or EW stops it and settles idle READY with its capacity-stop error, complete published members and a PENDING suffix without staged storage. Earlier Positions are unchanged. A Restore of the first, middle and last published members matches their bytes; Verify matches every copy on the Media and public-tool recovery hashes every published member while streaming, so the full payload is never restored to disk. |
| `tar-ew` | Continues a filled cartridge to the drive's own EW. Requires the reviewed `FILE_MIB` plan of small members whose total exceeds the reported remaining capacity, and free space for the sources and every published member. The device's `capacity_poll_interval_seconds` is set to 0 for the write, so admission keeps only its preparation sample, and is removed again before readback. One Archive APPEND settles like `tar-fill`; earlier Positions are unchanged and every published member restores with matching bytes. The stage passes only when the Job error reports `Tape early warning`; an admission stop is recorded in `ew/write-result.json` and fails it. |
| `tar-read-order` | One LOGICAL and one AUTO Restore of the same selection on the filled cartridge: the fixture members near BOT and every other `tar-ew` member. Each records what the `restore` comparison records, in `read-order-full-comparison.json`; report only, no threshold. |
| `cleanup` | [PT-08](physical-tape-e2e.md#pt-08-cleanup-and-evidence) rules. Requires completed `restore` recovery and, once admitted, completed `tar-fill`, `tar-ew` and `tar-read-order`; the reports of every completed tar stage are required prerequisites. |

Public-tool recovery stops the service and runs the reader with the saved key in a temporary 0600
file and the configured `encrypt` adapter. Using only `mt`, `dd` and GNU `tar`, it extracts the
label, FORMAT and APPEND Tape files, each ending at a FILEMARK, and compares them with the oracle.
After `tar-fill` it streams the fourth Tape file through `tar --to-command` hashing: every published
member must match its size and SHA-256 exactly once, and other members may only form an unpublished
tail. Recorded EOD follows the last FILEMARK. Payloads stay on the host; only bounded receipts are
copied. Retain the unpublished tail and the reader's tar status separately from the published
members.

Before an operation the controller requires an idle device in `tape device list` and observes
`mt status`; when it reports `DR_OPEN`, it issues one synchronous `mt load`, then verifies `ONLINE`.
No timeout wraps that load or the public reader, and a failure stops without retry. Normal Archive,
Restore and Scan unload the cartridge. `tar-fill` waits for each Job within an explicit budget of
one hour plus the payload at a conservative 35 MB/s, about 13 hours per Job for 1.5 TB (LTO-5
nominally streams about 140 MB/s);
exceeding it stops observation and retains the active owner. Failed or interrupted operations stop
and report their errors; this suite does not salvage tails, resume interrupted writes or test
crashes. Disk-only GNU checks establish framing and metadata behavior, not drive completion.
Reload-stable seek, capacity polling cost and geometry need their own measurements; record
unknown, unsupported and refused cases explicitly. The [tar study](../research/tar-tape.md) and
[Tape contract](../architecture/tape.md) own the remaining questions and publication rules.

### Recovery Reader and Assertion Scope

Preparation freezes the fixture's SHA-256, modes, nanosecond times and empty/binary user xattrs.
Baseline checks FORMAT/APPEND, immutable identity/profile, exported copy hashes and unchanged
earlier Positions. After a service restart, full and selected Restores use automatic Media identification and
compare actual bytes and metadata.
Known-only inventory must remain unchecked; FORCE_READ Verify must report checked matches.
Saved physical-anchor coverage does not establish reload or seek qualification.

Media replies carry only `encryption_key_saved`. Where the controllers need the saved key, they
read it from the matching `media` record of an in-memory Library JSONL export whose transcript is
redacted; FORMAT and APPEND/restart compare its SHA-256. For the explicitly requested public
recovery, the controller obtains the existing completed Media key this way before stopping only
its owned service. It passes record settings and the
fixture oracle to the [public reader](../../e2e/physical_tar_recovery.py), which also generated it,
with a temporary 0600 key file, adapter path, barcode, Tape name and original service directory.
The reader checks device users and mounts, then runs that adapter with the existing script inputs.
Key bytes are redacted from receipts and the caller removes the temporary file after the reader
settles. The reader requires no YATM executable, Catalog, Job recovery artifact or saved locator. `mt`/`dd` copy label, FORMAT and APPEND Tape files; GNU tar
lists/extracts them, and the disk oracle checks every fixture member. A nonempty copy requires
exclusive EOF at the expected file/block/partition after close/reopen. A separate next read
requires zero bytes and exclusive driver-reported EOD; zero bytes alone never pass. Unknown
syntax, diagnostics or boundary facts fail and retain the stopped owner and partial evidence.
The original driver block setting is restored if changed after completed read commands or failed
checks. Each attempted command is recorded before execution. If execution raises before normal
completion, its receipt remains unsettled and no restoration or status query follows.
The host-local owner waits for each public device command to return; no subprocess watchdog
kills an in-flight read or motion. An interrupted wait attempts to receive completion without
killing the child and preserves the unsettled failure. Retain and investigate that owner before further
device access.

Encryption adapter inputs are optional as one complete set: `--encrypt-script`, `--key-file`,
`--barcode`, `--tape-name` and `--script-directory`. They do not enable a hardware compression decoder.

These boundary assertions are scoped to Linux st's observed mt-st 1.8 status syntax and the
recorded kernel/GNU tool versions. They cover normal recorded end only, not empty physical Tape
files, physical EOT, reload, seek, EW/capacity cost measurement or arbitrary faults. Local stub checks
establish only the script-managed encryption adapter's invocation and cleanup. Other tool/driver semantics require reviewed oracles. Stages admitted to device work cannot be replayed;
cleanup requires application readback, independent recovery and sealed evidence before deleting
idle owned Jobs and the installation. It does not eject the cartridge. Keep state and evidence
outside the owned installation root. `make test-package-tools` checks this controller locally
with device/service/process boundaries stubbed; it does not run physical acceptance.

The sampled-counter plotter's finite limits cover 24 hours at two-second app and five-second
driver cadence with ten page 0C and five page 34 parameters: 120,000 raw intervals, 45,000
file-work intervals and 300,000 driver rows. The existing 20,000-bin and 500,000-resource-row
limits remain; larger input shapes require their own review. Plots retain observation waits and
unknown gaps without interpolation.
