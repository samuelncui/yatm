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
Device details report observed identity, readiness, record limits, the densities the drive
supports, current encryption state when known, and feature support, availability and validation
separately. Missing values
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
for tar records, capacity polling and the planning budget.
**Default Tape format** offers LTFS and Tar, and **Record mode** offers Fixed and Variable; an unset
value shows as the one used when unset (Tar, Variable), and choosing an entry stores it. After
**Device details** report that the drive supports no LTO-5 or later density, the format select offers
only Tar, and a saved LTFS default shows as unsupported.
**Read parameters** has its own collapsed section: a table with one set per density code, plus the
built-in values for the reported densities without a set, edited in a form, and, for a device in service, **Calibrate…**, which starts a [Calibration](read-order.md#calibration). Save, Cancel and **Refresh devices**
are below the fields; Refresh devices updates device availability without changing the saved
Device settings or draft. **Device details** follows in a separate collapsed section. Saving changes
only persisted Device settings; it performs no hardware operation. Entries removed from service configuration remain editable as inactive Device settings and do not authorize
access to those devices. Validation errors retain the edited values and reveal the affected fields.

Read order, the planning budget and read parameters are explained in
[Faster Restores with Auto read order](read-order.md), which also describes how a
[Calibration](read-order.md#calibration) measures and stores a drive's read parameters.

The CLI reads or replaces the complete DeviceSettings group using protobuf JSON. Read the current
group before editing; replacement must retain every entry you want to keep:

```shell
yatm-cli settings devices
yatm-cli settings devices --device-json device-settings.json
```

For example, a saved tar preference for one configured entry is:

```json
{"devices":[{"device":"/dev/nst0","tape":{"defaultFormat":"tar_pax_v1","tarWriteOptions":{"recordMode":"TAPE_RECORD_MODE_VARIABLE","recordBytes":524288},"readOrder":"TAPE_READ_ORDER_LOGICAL"}}]}
```

The example size is an explicit preference. Omitted options remain
absent. `readOrder` accepts `TAPE_READ_ORDER_LOGICAL`
or `TAPE_READ_ORDER_AUTO`; omission/unspecified uses Logical when the attempt is admitted.
The optional `capacityPollIntervalSeconds` uses Auto when omitted, preserves explicit `0` as Off,
and accepts custom unsigned 32-bit values of at least `5` seconds.

## Tar Write Options

The available formats are `ltfs_v1` and `tar_pax_v1`. Without a saved format, FORMAT uses Tar.
LTFS needs a drive of LTO-5 or later. When the drive reports its supported densities and none is
LTO-5 or later, the FORMAT dialog offers only Tar and an LTFS FORMAT is refused before any script
runs or the Tape moves. A drive that does not report its densities keeps both formats.
Tar accepts fixed or variable records. FORMAT uses 512 KiB (524288 bytes) unless a saved device size
or explicit FORMAT override is supplied; APPEND and reads keep the cartridge's recorded size.
Record bytes must be positive, fit an unsigned 32-bit integer,
and be aligned to 512 bytes. Saving checks this shape; the operation checks the current device,
driver and transfer limits before allocating buffers or writing. An incompatible size fails rather
than being silently reduced. For variable records, record bytes is the maximum physical record size;
for fixed records, it is the size of every physical record.

Tar always writes with drive compression off: FORMAT and APPEND turn the drive's compression off
for the attempt, verify it, and restore the previous setting afterwards. There is no compression
setting. Reads leave the drive's compression controls untouched.

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
when admitted; it is independent of the stored record profile. File admission uses cached results, and writing
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
attempt and withhold all of its Archive results. A write already admitted before a terminal failure may
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
before writing data. A failed write or readback stops FORMAT; it is not retried. A FORMAT that
ends without publishing any file, for example when it is canceled or reaches early warning before
the first file completes, registers no Media for tar or LTFS; FORMAT the cartridge again, with the
same barcode, for the remaining files.
LTFS retains its cartridge checks, initialization, mount and normal unmount flow.

```shell
yatm-cli archive write tape format JOB_ID --device /dev/nst0 \
  --barcode ABC123 --name 'Archive Tape' --confirm-format ABC123 \
  --format tar_pax_v1 --record-mode variable --record-bytes 524288
```

Omit `--format` to use the selected device's saved format (Tar when none is saved), or supply `--format ltfs_v1` for an
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
before publishing completed copies. Every tar Archive, Restore, Scan and Calibration step first
loads the cartridge, so one an earlier step ejected but left in the drive is used without
reinserting it, and an empty drive fails with a missing-medium reason. Each step ends by restoring
owned controls and unloading the cartridge, including its rewind and eject, whether it succeeded,
failed, including an encryption script failure, or was canceled. A cartridge with a different
barcode stays loaded, and a drive whose command outcome became unknown is left untouched and
unavailable. LTFS follows the configured unmount adapter; the bundled adapter ejects. An LTFS operation that
fails or is canceled after its cartridge check but before mounting, such as an encryption or
format script failure, unloads the cartridge through the same raw access tar uses; without that
access the cartridge stays loaded and the Job log says why. Early warning is handled as a normal capacity boundary only
when the provider supplies validated accepted-count evidence. A canceled tar Archive, or one that
fails for a reason outside the drive such as an unreadable source file, also closes its Tape file
with a filemark, unloads and publishes the files written completely before it stopped; the
interrupted file and any still buffered stay pending for the next Media choice. A write, hardware
or unknown command error, or a failed filemark or unload, publishes nothing. Completion errors remain
visible; accepted bytes alone do not establish a completed copy.

Tar can use a cached preparation estimate to refuse the next complete file before its header and
close a normal archive. File admission does not wait for a space query or pause to synchronize each
file. Unknown capacity permits baseline writing; actual EW still stops data writing and may leave
an incomplete final member. That member stays pending for another explicitly selected cartridge.

## Load Tape for Restore or Scan

Restore and Media Scan offer automatic identification and a manual Media selection from the Job's
prepared candidates. Start with automatic identification. For tar it loads the cartridge, as LTFS
identification does, so one an earlier step ejected but left in the drive is identified without
reinserting it, then reads the cartridge memory barcode; an empty drive fails with a missing-medium
reason. If the server requires a hint, retain the
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
Use `yatm-cli job log-lines JOB_ID` or the Job's log to inspect requested/effective read order and its
reason. Geometry diagnostics retain unknown freshness; a reported all-wrap command form is not
proof of a fresh complete map or an ordering recommendation. A reported-wrap count requires a
decoded response; command-form support alone leaves that count unknown.

LTFS Auto preparation reads the drive's geometry before mounting and maps the LTFS partitions by
convention (`I:a,D:b` is physical 0 and 1), checked against the mounted partition map; LTFS owns all
subsequent reads. Missing partition, geometry or timing evidence retains the normal logical order.

Media details display the stored format and record profile. Retain the independent
[tar recovery parameters and guide](tar-tape-recovery.md) before relying on a cartridge.
