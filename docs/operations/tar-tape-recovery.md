# Recover Tar Tape with Public Tools

This guide describes independent content extraction for `tar_pax_v1` Tape without a YATM executable,
service, catalog, Job bundle, content index or saved file locators. Keep this guide and the cartridge's
record parameters outside the YATM installation. The raw backend currently uses fixed or variable
512-byte-aligned records, ordinary PAX tar metadata and xattrs, with optional hardware compression
and optional hardware encryption. Encrypted recovery requires its independently retained key and
a compatible public device-tool unlock procedure. For normal
Archive, Restore and Scan, use the [Device guide](devices.md).

## Tested Reader and Required Parameters

Physical recovery has been checked with Linux st, mt-st 1.8, GNU dd and GNU tar 1.35 for variable
and fixed records, hardware compression On and Off, and script-configured encryption on the writing
drive. That does not establish another reader or qualify every encryption adapter. The
[recovery checks](../research/tar-tape.md#public-tool-recovery-checks) record what was tested and
its limits.
Other readers must establish their own record, close/reopen, positioning and EOF/EOD/EOT behavior
before using the Tape-copy procedure below. Passive diagnostics do not supply that evidence.

Retain the barcode, initialization UUID, record mode and record bytes from the published Media
profile. Keys use the existing Media/Job profile; runtime script key files are temporary, and
FORMAT does not create a second persistent recovery descriptor or key copy. This guide covers
completed Archive content and controlled EW results, not interrupted-FORMAT recovery.

A retained Library JSONL export also contains the same Media profile. Before losing the installation,
use `yatm-cli library export --output library.jsonl`, or the browser's Library export. Export backs
up metadata, not Tape content or Settings. With public `jq`, select exactly one matching Media row:

```shell
umask 077
jq -s -e --arg barcode ABC123 '
  [.[] | select(.type == "media" and .data.identity == $barcode) | .data]
  | if length == 1 then .[0] else error("expected exactly one Media") end
  | select(.profile.tape.format == "tar_pax_v1")
  | {identity, partition: 0, profile}
' library.jsonl > recovery.json
jq -e '.identity, .partition, .profile.tape.tar' recovery.json
```

The Library export retains the adapter key in `profile.tape.encryption`; protect that backup.
The optional observed `encryption_algorithm` is diagnostic metadata: its absence does not prove
plaintext. Use the configuration and qualified behavior of the adapter that wrote the cartridge
to establish whether decryption is needed. A saved key alone does not show which encryption mode
the adapter applied. Do not print a key or place it in command-line arguments.

The descriptor uses snake-case fields such as `record_mode`, `record_bytes` and `initialization_id`;
record mode is `TAPE_RECORD_MODE_FIXED` or `TAPE_RECORD_MODE_VARIABLE`. Do not guess missing record
parameters from a cartridge generation, current drive mode or sample values. On-Tape file 0 is an
identity PAX archive containing `YATM/identity.json` and `YATM/README.txt`; it is hardware-encrypted
when the profile requires it. It is useful
for checking the recorded identity after copying, but does not replace the parameters needed to
read it with the qualified reader.

If `hardware_compression` is true, use a reader compatible with the cartridge's hardware compression
format. Its hardware decoder must return the original PAX bytes through Linux `st`; no software
decompression command is involved. Check the reader's documented decompression setting before
copying. Typical LTO readers decode automatically; a disabled decoder requires a qualified public
device-tool procedure to enable it, verify the effective state and restore any changed session
control. The write compression setting DCE does not control read decompression DDE, so setting
`mt compression` does not establish decoder readiness. A reported current algorithm identifier
alone does not prove that another cartridge's compressed content is readable.

## Unlock an Encrypted Cartridge

Skip this section when the writing adapter and reader establish that decryption is unnecessary.
The tested public-reader example uses `AES-256-GCM-128` with a 32-byte key and stenc 2.0.1;
it is not a native YATM algorithm selector or a promise for every configured adapter. Establish its actual
device/driver path, read modes and clearing behavior on each reader before recovery; the physical
check above covers only its stated combination. Public-tool source findings are in the
[tar study](../research/tar-tape.md#hardware-encryption-controls).
Check `stenc --version` before querying the drive. Older interfaces exposing only `-e` cannot
request these independent encryption/decryption modes, and their status command may read records.

For explicit independent recovery, produce `recovery.json` from the protected Library export
as above, then create a protected key file with public `jq`. This is an operator export, not an
automatic runtime copy. It writes the key directly to the file without printing it or passing it
as an argument:

```shell
set -eu
umask 077
KEY_FILE=recovery.key
(set -C; jq -er '
  .profile.tape.encryption
  | if type == "string" and test("^v1:[0-9a-fA-F]{64}$")
    then .[3:] else error("invalid retained encryption key") end
' recovery.json > "$KEY_FILE")
```

Stop all other drive owners and write-protect the cartridge. Query the reader with
`stenc -f "$TAPE_DEVICE"`. It must support the public algorithm, the 32-byte key and decrypt-only
mode, with application-managed control and a known empty initial key state. Do not replace a
foreign key or take over external key management. Select the algorithm index from this reader's
reported mapping; an index saved or observed on the writer is not portable.

After configuring the record policy and positioning for the selected Tape file, unlock before
`dd`. Replace `READER_INDEX` with the confirmed index. Specify both modes: `-d on` alone can also
enable write encryption in the studied stenc version.

```shell
READER_INDEX=confirmed-index # Replace with this reader's reported AES-256-GCM-128 index.
stenc -f "$TAPE_DEVICE" -e off -d on -k "$KEY_FILE" -a "$READER_INDEX"
stenc -f "$TAPE_DEVICE"
```

stenc 2.0.1 writes its normal settings-change and success messages to stderr. Check the exit
status and effective encryption off/decryption on; an empty stderr is not a success requirement.
An accepted key-load command does not validate the key: a wrong, correctly sized key can load
successfully and fail only on the encrypted read. Missing/wrong keys are not evidence of damaged
tar bytes. Recheck key persistence after positioning, close/reopen or other reader-specific events;
if an event clears it, stop and establish the reader's required unlock sequence before another read. Stop on ambiguous
ownership or state.

After copying, explicitly clear the key through the qualified public-tool release procedure and
verify its non-secret state. For the studied stenc version, both modes off send an empty key:

```shell
stenc -f "$TAPE_DEVICE" -e off -d off -a "$READER_INDEX"
stenc -f "$TAPE_DEVICE"
```

Successful descriptor close is not proof of key clearing. The reader qualification must establish
that this command releases its loaded key and leaves both modes off; preserve errors and stop using
the drive if clearing cannot be confirmed. Keep the independent key file for future recovery.

## Copy One Physical Tape File

Use a qualified Linux non-rewinding `st` device and public `mt`, GNU `dd`, GNU tar and `jq` tools.
Stop YATM and other users of the drive first. Physically write-protect the recovery cartridge
before loading it. Confirm its identity and select the recorded
partition using the qualified reader's documented partition procedure; this format uses partition
zero. Do not run format, erase, write, filemark or unload commands during recovery. Required
public-tool read unlocking and clearing follow the scoped procedure above.
Provide enough disk space for ordinary copies and extracted files.

Read the descriptor explicitly extracted from the Library export and configure the recorded record policy. The following commands are
operator examples, not a hardware qualification test. Replace the device and descriptor paths with
the confirmed values:

```shell
set -eu
export LC_ALL=C
umask 077
TAPE_DEVICE=/dev/nst0
PARAMETERS=recovery.json
RECOVERY_DIR=recovered-tape
RECORD_MODE=$(jq -er '.profile.tape.tar.record_mode' "$PARAMETERS")
RECORD_BYTES=$(jq -er '.profile.tape.tar.record_bytes' "$PARAMETERS")
mkdir -m 700 "$RECOVERY_DIR"
case "$RECORD_MODE" in
  TAPE_RECORD_MODE_VARIABLE) mt -f "$TAPE_DEVICE" setblk 0 ;;
  TAPE_RECORD_MODE_FIXED) mt -f "$TAPE_DEVICE" setblk "$RECORD_BYTES" ;;
  *) echo 'Unknown record mode' >&2; exit 1 ;;
esac
```

For variable records, the `dd` read buffer must be at least the stored maximum record size; for
fixed records it must obey the driver's qualified fixed-block read rules. Use the recorded byte
size and verify the reader's bounds. `iflag=fullblock` can combine separate record reads across a
filemark, so it is excluded. Never add `conv=noerror`, `conv=sync` or other read-error padding.

Copy each physical Tape file separately. File 0 is identity; file 1 and later files are independent
content archives. Always rewind and skip from BOT for each copy so the previous command's implicit
close positioning cannot choose the next file. Choose a new `N` and filename each time:

```shell
N=0
mt -f "$TAPE_DEVICE" rewind
if [ "$N" -gt 0 ]; then mt -f "$TAPE_DEVICE" fsf "$N"; fi
# For an encrypted cartridge, perform and verify the qualified read-only unlock here.
DD_STATUS=0
dd if="$TAPE_DEVICE" of="$RECOVERY_DIR/archive-$N.tar" \
  bs="$RECORD_BYTES" conv=excl status=progress \
  2>"$RECOVERY_DIR/archive-$N.dd.log" || DD_STATUS=$?
MT_STATUS=0
mt -f "$TAPE_DEVICE" status >"$RECOVERY_DIR/archive-$N.status" \
  2>"$RECOVERY_DIR/archive-$N.status.log" || MT_STATUS=$?
printf 'dd=%s mt-status=%s\n' "$DD_STATUS" "$MT_STATUS" \
  >"$RECOVERY_DIR/archive-$N.result"
```

A positioning failure stops before copying. A read or status error stops enumeration; retain the error and any partial disk copy for diagnosis.
This procedure does not salvage an interrupted Archive or repair its Tape tail. Do not pipe Tape input into tar or hide
command exit statuses. `conv=excl` prevents overwriting a retained copy.

Interpret the fresh post-read status only according to the qualified driver/drive mapping, including
its behavior across `dd` close and the status command's reopen:

- Successful copy plus a confirmed filemark/EOF without conflicting EOD/EOT or errors completes one
  physical Tape file. Even an empty Tape file is a boundary; continue with `N + 1` from BOT.
- Confirmed recorded EOD without conflicting EOT or errors ends enumeration. Bytes reaching EOD
  without a terminating filemark are an unterminated tail; stop without declaring that Archive recovered.
- EOT, unknown, stale or conflicting flags, a failed status query, or unqualified EOF/EOD semantics
  mean incomplete enumeration. Stop and qualify the reader; do not infer EOD from zero bytes, a
  successful `dd`, a tar terminator, or an empty file.

## Extract the Disk Copies

Run GNU tar against each retained ordinary disk copy in a fresh directory. First inspect file 0 and
compare `YATM/identity.json` with the retained barcode, initialization UUID and record policy. It
uses a label-local `profile` with the same snake-case record field names. Extract content archives
individually; their member paths preserve the Archive's Tape path prefix. This is a separate
invocation using an existing disk copy; inspecting it does not require another Tape read.

```shell
set -eu
export LC_ALL=C
RECOVERY_DIR=recovered-tape
N=1
EXTRACT_DIR="$RECOVERY_DIR/extracted-$N"
mkdir -m 700 "$EXTRACT_DIR"
LIST_STATUS=0
tar --list --verbose --file="$RECOVERY_DIR/archive-$N.tar" \
  --xattrs --warning=missing-zero-blocks --quoting-style=shell-escape-always \
  >"$RECOVERY_DIR/archive-$N.list" 2>"$RECOVERY_DIR/archive-$N.list.log" || LIST_STATUS=$?
TAR_STATUS=0
tar --extract --file="$RECOVERY_DIR/archive-$N.tar" \
  --directory="$EXTRACT_DIR" --same-permissions --no-same-owner \
  --xattrs --warning=missing-zero-blocks \
  2>"$RECOVERY_DIR/archive-$N.tar.log" || TAR_STATUS=$?
printf 'list=%s extract=%s\n' "$LIST_STATUS" "$TAR_STATUS" \
  >"$RECOVERY_DIR/archive-$N.tar-result"
```

GNU tar's default xattr extraction selection covers `user.*`; other namespaces need an explicit
compatible-system selection such as `--xattrs-include='*'` and suitable privileges. Check extracted
paths, contents and required metadata on the destination filesystem. Preserve tar's diagnostics.
Do not use `--ignore-zeros` or `--multi-volume` to conceal incomplete framing.

Missing tar end blocks can produce a warning even when every member payload is complete, and GNU
tar may still exit successfully. The explicit missing-zero-block warning reveals that incomplete
terminator; it does not prove physical Tape finalization. Do not discard the last complete file
merely because a warning exists.

A nonzero extraction result is not a successful whole-archive recovery. Preserve the disk copy and
diagnostics. For a separately established normal EW boundary, check the published complete members
against retained sizes and hashes; an unpublished partial member is not restored content. Other
truncated headers, payloads or padding remain errors outside this procedure. Tar header checksums
do not verify payload contents.
Independent extraction recovers ordinary files and represented metadata, not Library organization,
Job state or proof of every expected archived copy.
