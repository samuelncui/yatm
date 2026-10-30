# Devices and Tape Operations

Configure Tape entries under `tape_devices` in the service configuration described in the
[installation guide](install.md). Device names refer to entries on the server, not paths on the
browser or CLI client. LTFS operations retain the configured scripts and their local helpers;
[upgrades](migration.md#tape-script-adaptation) preserve those adapters.

## Device Requirements

Device diagnostics and raw tar need Linux SG_IO
access to the configured entry's resolved generic device, read/write permission for the service
account, and `fuser` from psmisc with visibility of host process holders; **Device details**
reports permission, missing-tool, busy or unknown-ownership reasons when that access is missing.
Listing devices sends no hardware commands.

Raw tar additionally needs a standard sequential-access drive (SPC-3 with SSC-2/3 command
behavior), supported record operations and an observed partition layout. Reads and APPEND require
the initialized single data partition; FORMAT also requires supported destructive initialization,
including any transition from an LTFS layout. A missing or uncertain required fact fails admission
with a reason, and a model name alone establishes no qualification. LTFS workflows instead rely on
the configured [Tape scripts](tape-scripts.md) and compatible LTFS software.

## Inspect a Device

Open **Settings → Devices**, select a device, and explicitly request its **Device details**. Refreshing
the device list reports configured entries and busy reasons without sending SCSI commands.
Device details report observed identity, readiness, record limits, current compression/encryption
state when known, and feature support, availability and validation separately. Missing values
remain unknown. A passive report neither positions the Tape nor proves that writing, seeking or
independent recovery works on that hardware.

Capacity diagnostics, when available, show native bytes estimated to remain before early warning,
the observed partition and observation start/end times. Unknown error/buffer-lag allowances remain unknown.
An estimate is not a guarantee that a file fits. Counter support and measured performance remain
separate observations; enabling polling does not mark a device as physically validated.

```shell
yatm-cli tape device list
yatm-cli tape device inspect --device /dev/nst0
```

Raw Tape access requires Linux, the configured device's SCSI generic mapping, suitable permissions,
and visibility of host device holders. A busy or unknown ownership result must be resolved before
raw access. `media inspect tape` is a separate LTFS cartridge action that can load the cartridge;
passive diagnostics do not replace it for LTFS.

## Save Device Settings

In **Settings → Devices**, choose **Default Tape format** and **Read order**. Expand **Advanced**
for tar records, hardware compression, capacity polling, planning budget and Tar passive read learning.
**Manual read parameters** has its own collapsed section. Save, Cancel and **Refresh devices**
are below the fields; Refresh devices updates device availability without changing the saved
Device settings or draft. **Device details** follows in a separate collapsed section. Saving changes
only persisted Device settings; it performs no hardware operation. Entries removed from service configuration remain editable as inactive Device settings and do not authorize
access to those devices. Validation errors retain the edited values and reveal the affected fields.

Read order, passive read learning, the planning budget and manual read parameters are explained in
[Faster Restores with Auto read order](read-order.md).

The CLI reads or replaces the complete DeviceSettings group using protobuf JSON. Read the current
group before editing; replacement must retain every entry you want to keep:

```shell
yatm-cli settings devices
yatm-cli settings devices --device-json device-settings.json
```

For example, a saved tar preference for one configured entry is:

```json
{"devices":[{"device":"/dev/nst0","tape":{"defaultFormat":"tar_pax_v1","tarWriteOptions":{"recordMode":"TAPE_RECORD_MODE_VARIABLE","recordBytes":262144,"hardwareCompression":false},"readOrder":"TAPE_READ_ORDER_LOGICAL"}}]}
```

The example size is an explicit preference, not a hardware recommendation. Omitted options remain
absent; explicit `false` remains an explicit choice. `readOrder` accepts `TAPE_READ_ORDER_LOGICAL`
or `TAPE_READ_ORDER_AUTO`; omission/unspecified uses Logical when the attempt is admitted.
The optional `capacityPollIntervalSeconds` uses Auto when omitted, preserves explicit `0` as Off,
and accepts custom unsigned 32-bit values of at least `5` seconds.

## Tar Write Options

The available formats are `ltfs_v1` and `tar_pax_v1`. Without a saved format, FORMAT uses LTFS.
Tar accepts fixed or variable records. FORMAT uses 512 KiB (524288 bytes) unless a saved device size
or explicit FORMAT override is supplied; APPEND and reads keep the cartridge's recorded size.
Record bytes must be positive, fit an unsigned 32-bit integer,
and be aligned to 512 bytes. Saving checks this shape; the operation checks the current device,
driver and transfer limits before allocating buffers or writing. An incompatible size fails rather
than being silently reduced. For variable records, record bytes is the maximum physical record size;
for fixed records, it is the size of every physical record.

Tar hardware compression can be explicitly On or Off. The leased operation checks current hardware
support, required control changes and readback; a saved preference alone cannot establish support.
APPEND uses the cartridge's saved choice after verifying its label and recorded tail. Reads require
compatible hardware decoding for compressed content and preserve the drive's write-compression
controls. Diagnostics show observed algorithm identifiers and known or unknown control changeability,
separately from physical validation. No physically qualified compression recommendation is supplied
by passive inspection, and expected compression savings are not a space guarantee.

## Encryption

Encryption for tar and LTFS follows the configured `scripts.encrypt` adapter. There is no separate
FORMAT encryption switch or algorithm selector. FORMAT generates the existing key representation;
APPEND/read uses the key saved in the Media profile. A temporary 0600 file passes it to the adapter
and is removed during cleanup. Changing formats does not replace a customized encryption script.

Published keys remain in the existing Media/Job profiles and Library exports. No extra persistent
JSON or `.key` copy is created. If independent recovery is needed, retain a protected Library export
and use the [public-tool guide](tar-tape-recovery.md). Media deletion removes its profile and key;
Job deletion removes the Job's attachments. Diagnostics expose only non-secret facts; an unknown
algorithm is not evidence of plaintext and a key counter is not key presence.

## Capacity Polling

Tar capacity polling defaults to **Auto (5 seconds)** unless the device reports that the
required query is unsupported. Leave the interval empty for Auto, enter `0` for Off, or enter a whole
number of at least `5` seconds for a custom cadence. FORMAT and APPEND freeze this preference
when admitted; it is independent of the stored record/compression profile. File admission uses cached results, and writing
does not wait for the query to return.
Unsupported counters use EW/error handling and any available preparation estimate. A missing counter value
remains unknown, and neither periodic estimates nor the selected interval guarantee that a file fits.
Writing deducts space from the cached estimate; periodic capacity samples only correct it. A settled
query timeout, busy response, unreadable payload or missing value keeps the cached debits and
ordinary polling cadence. Repeated misses do not stop Archive or disable later capacity polling.
Failed capacity samples appear as warnings in the Job log, including the error and consecutive failure
count; the first successful value after failures logs recovery. Investigate repeated warnings using
the device and host logs. Skipped busy polls and ordinary shutdown do not generate failure warnings.
Actual write errors, medium loss, device resets or unknown native command outcomes stop the
attempt and withhold its Archive results. A write already admitted before a terminal failure may
finish; a failed optional capacity sample alone does not establish such a failure.
LTFS and Volume retain their existing capacity paths.

## Load Tape for Archive

Create and prepare the Archive Job with the normal file selection. Format selection belongs to
**Choose archive storage → Storage type → Tape**. Select a device and **FORMAT**, review its saved defaults,
and override them for this operation when needed. Explicit operation values take precedence over
saved Device settings and the [defaults](#tar-write-options). Validated
passive recommendations can supply other remaining choices. Review the reported limits and any
admission error.

FORMAT is destructive even when the cartridge memory barcode is empty. Enter the six-character
alphanumeric barcode and Tape name, then confirm the exact barcode before submission. A reported
different barcode or an existing Library registration stops FORMAT. To reinitialize registered
Media, explicitly delete its metadata first; metadata deletion alone never erases Tape contents.
Raw tar FORMAT uses passive diagnostics and does not require the LTFS cartridge inspection script.
Like `mkltfs`, it writes the confirmed barcode into an empty cartridge memory barcode and verifies it
before writing data. A failed write or readback stops FORMAT; it is not retried.
LTFS retains its cartridge checks, initialization, mount and normal unmount flow.

```shell
yatm-cli archive write tape format JOB_ID --device /dev/nst0 \
  --barcode ABC123 --name 'Archive Tape' --confirm-format ABC123 \
  --format tar_pax_v1 --record-mode variable --record-bytes 262144
```

Omit `--format` to use the selected device's saved format, or supply `--format ltfs_v1` for an
explicit LTFS override. Tar-only record options are invalid for LTFS. An edited operation choice
is retained while reviewing the form; selecting another device does not erase explicit overrides.

Choose **APPEND** explicitly for an existing registered Tape. Its stored format and tar record
profile are immutable: current Device write-profile defaults do not apply. The separate capacity
polling preference still applies to the new attempt. APPEND verifies the loaded identity;
tar additionally verifies its label/profile and recorded tail before adding an independent archive.
A mismatching override fails instead of rewriting the profile.

```shell
yatm-cli archive write tape append JOB_ID --device /dev/nst0 --barcode ABC123
```

Successful raw Archive completion writes a standard tar terminator and a confirmed Tape filemark
before publishing completed copies. Normal tar Archive, Restore and Scan completion restores owned
controls and unloads the cartridge, including its rewind and eject. Reinsert the Tape for another
operation. Failed or canceled raw operations use their existing cleanup path instead. LTFS follows
the configured unmount adapter; the bundled adapter ejects. Early warning is handled as a normal capacity boundary only
when the provider supplies validated accepted-count evidence. Completion errors remain visible;
accepted bytes alone do not establish a completed copy.

Tar can use a cached preparation estimate to refuse the next complete file before its header and
close a normal archive. File admission does not wait for a space query or pause to synchronize each
file. Unknown capacity permits baseline writing; actual EW still stops data writing and may leave
an incomplete final member. That member stays pending for another explicitly selected cartridge.

## Load Tape for Restore or Scan

Restore and Media Scan offer automatic identification and a manual Media selection from the Job's
prepared candidates. Start with automatic identification. If the server requires a hint, retain the
form, select the expected Media, review it, and submit again. A manual selection supplies expected
identity and the stored profile; the server still verifies the physical cartridge. It does not
permit reading an unrelated Tape or bypass a label mismatch.

```shell
yatm-cli restore run tape JOB_ID --device /dev/nst0
yatm-cli restore run tape JOB_ID --device /dev/nst0 --media-id MEDIA_ID
yatm-cli verify run JOB_ID --device /dev/nst0 --media-id MEDIA_ID
```

Tar reads use the stored record policy and sequential physical filemarks; they do not use a
current FORMAT default or require LTFS `readinfo`. LTFS keeps its configured adapter, identity,
key, mount and normal unmount path. Exact member seek requires stored physical anchors and an
applicable standard READ POSITION/LOCATE pairing in the prepared record/partition envelope.
Missing anchors or optional command support retain sequential reads. Device details reports
physical validation separately from protocol support. A real
positioning or record-read failure stops that attempt rather than silently retrying by another path.
For tar Restore and recorded-copy verification, **Refresh position map before reading** is an
explicit preparation action, not a saved Device setting. Select it in the read dialog, or pass
`--refresh-position-map` to `restore run tape`, Tape `verify run` or `scan run` for a frozen
VERIFY_COPIES Job. The drive unloads without ejecting, loads once and captures a new position map
before reading; **Device details** shows whether the drive is eligible. The
[refresh contract](../architecture/read-planning.md#position-map-refresh) owns eligibility, identity
checks and encryption handling. A refreshed map does not qualify Auto read order.

Ordinary reads remain available with the action omitted. A failed or canceled cycle can leave the
cartridge unthreaded: inspect the device and explicitly load it before another read if required.
YATM does not retry the cycle or perform recovery motion automatically.
Auto read order never triggers a reload. LTFS, Volume, inventory and other Media Scan policies do not
accept this action. A missing Scan policy hides only refresh; it does not block normal reading.

Use `yatm-cli job log-lines JOB_ID` or the Job's log to inspect requested/effective read order and its
reason. Geometry diagnostics retain unknown freshness; a reported all-wrap command form is not
proof of a fresh complete map or an ordering recommendation. A reported-wrap count requires a
decoded response; command-form support alone leaves that count unknown.

LTFS Auto preparation can read both partition Labels through SG before mounting. This adds two
partition-start seeks; the Job log records that preparation time separately from planning.
YATM checks the Labels against mounted volume facts and lets LTFS own all subsequent reads.
Missing partition, geometry or timing evidence retains the normal logical order.

Media details display the stored format and record profile. Retain the independent
[tar recovery parameters and guide](tar-tape-recovery.md) before relying on a cartridge.
