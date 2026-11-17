# Faster Restores with Auto Read Order

Restoring files scattered across a Tape is usually dominated by positioning, not reading. LTO
records data in wraps that run back and forth along the tape, so files read in recorded
(**Logical**) order can send the drive along the tape length again and again. **Auto** plans the
complete selection on the loaded cartridge before reading. It uses the cartridge's position map and
the device's read parameters (configured for the cartridge's density, or built-in) to choose the order with the lowest predicted positioning, reading and
completion time. When its inputs do not apply or no order is predicted to save at least one
positioning base time (`base_seconds`), it keeps Logical. The order
never changes which files are read or how they are verified.

On one HP LTO-5 drive (Z6ED firmware) reading uncompressed tar, Auto took 47–69% less time to
restore files scattered across wraps or at wrap and band boundaries, and kept Logical when the
selected members were adjacent. The
[research record](../research/tape-read-ordering.md#software-read-ordering) has the measurements, method and limits.
These results are not a guarantee on other drives, compressed media or LTFS. The project has only this
drive, so qualification on other drives, generations or LTFS depends on reports from users with that
hardware; report results or problems in [Issue #14](https://github.com/samuelncui/yatm/issues/14).
Unrecognized inputs keep Logical order rather than failing. Planning stops within
the **Read planning budget** (default 300 seconds), or earlier once it has taken longer than the
predicted time it saved, and always keeps a complete order.

## Use Auto

Choose **Read order → Auto** under **Settings → Devices**, or set `readOrder` to
`TAPE_READ_ORDER_AUTO` with `yatm-cli settings devices`. Saving performs no hardware operation. Each Job log reports the
order used and any fallback reason, and **Device details** shows the last prepared read. After a
planned read completes, the Job log repeats the predicted Logical and selected costs
(`estimated_logical_ms`, `estimated_planned_ms`) beside the measured `realized_ms`; the predictions
also include the final rewind/unload, which the measured time does not. Logical
remains the default because Auto has been measured only on the hardware above.

## How Auto Decides

**Read order** applies to each later read attempt on the selected device. **Logical** is the default
and preserves file order; eligible exact seek can still be used. **Auto** evaluates an alternative
order when eligible member seek, applicable geometry and a cost model are available. Otherwise it
keeps logical order and reports the fallback reason in the Job log. Saving Auto is allowed offline and
does not qualify the hardware. The selected READ timings can support continued reading and
record reuse in Logical mode; missing seek costs never justify reading through an avoidable gap.
Complete positioning parameters and a usable map can enable Auto. Parameters come from the device's
set for the loaded cartridge's density code first, then built-in initial references. Layout/native-rate
references depend on recorded density; the generic timing seed is borrowed from a measured HP LTO-5
configuration and is not a measurement of every device. Incomplete observations retain normal reads.
For LTFS, Auto maps partitions by the LTFS convention (`I:a,D:b` is physical partition 0 and 1); a
different mounted partition map keeps the Logical order. LTFS ends every file with a short block, so
when the drive's complete wraps hold object counts more than 0.5% apart, as wraps of many small files
do, positions cannot be interpolated and the read keeps Logical.
LTFS mounting reads the Index near the start of the tape, so Auto prices the first file from the
start of the Data partition and may read any selected file first.
Auto also considers the final position. Native tar includes its normal rewind/unload. LTFS assumes
completion does not rewrite the Index and estimates rewind/unload from the final position plus the
unmount time. This
assumption changes neither mount scripts nor write protection; an actual Index rewrite is outside
the estimate. These scores compare candidate orders rather than predict a complete ETA.
Auto retains the complete order with the lowest available predicted cost, and adopts it only when it
saves at least one `base_seconds` (never less than the built-in value) over Logical; a smaller predicted saving is within the estimate's
error, so Logical is kept and the Job log states the saving. Prediction errors remain
visible, and offline verification checks sensitivity to positioning, longitudinal movement and read
speed. These estimates do not guarantee a speedup on unmeasured hardware.

Read preparation prefers the drive's wrap report, then the cartridge-memory (CM) tape
directory. Position maps exist only for the current read and are never saved. If neither is usable,
ordinary Logical reading remains available. CM is queried once during preparation, not per file or during device-details refresh;
its source and any fallback reason appear in the Job log. It stores no per-file wrap assignment.
CM format support and physical validation have the [documented limits](../research/tape-read-ordering.md#direct-physical-coordinates).

Migrated LTFS tapes do not need a historical XML file for Auto ordering. If selected copies lack
extents, YATM can use the current Index captured by the mount adapter to fill their planning
positions. The mounted UUID and Index generation must match. Missing or conflicting Index data
keeps ordinary Logical reads; it does not alter the saved Job or Library content.

Current **Auto** ordering requires a Tape written without hardware compression. Tar always writes
with drive compression off; files already compressed before Archive are not excluded.
For LTFS, the formatting script decides: the bundled [`mkfs`](../../scripts/mkfs) formats with
`mkltfs --no-compression`. A customized script without that option writes compressed LTFS volumes,
so Auto keeps Logical on them; reformatting is the only way to change an existing volume.

## Read Planning Budget

**Read planning budget (seconds)**
defaults to 300 when blank and accepts positive whole seconds. Planning completes before content
reading; the budget bounds planning and preserves a complete Logical or improved order.
It does not include device preparation or mounting.
Changing these Device settings never changes the current attempt or a cartridge's written profile.

## Read Parameters

**Read parameters** lists the device's parameter sets, at most one per density code (the
cartridge's recorded density). A read uses the set for the loaded cartridge's density code and
otherwise the built-in values; record size and encryption take no part in that
choice. A set with `appliesTo.driveSerial` does not apply when the drive reports another serial
number; the read then uses the built-in values and **Last prepared read** says why.
The section edits the sets inline: each set is a block headed by its density code (with the LTO
generation name for the standard codes), with one field per stored value grouped as Positioning,
Reading, Completion and Geometry, each with its unit, plus the drive serial and source reference.
A new set needs a **Density code**, chosen in the add row from the standard LTO codes or entered
as another code, that no other set of the device uses; **Add read parameters** then adds it, and an
existing set keeps its density code. Times are entered in seconds, **Read rate** in MB/s and
**Streaming distance** in MB (decimal megabytes; stored as bytes per second and bytes). An empty
field stays unknown. A field you do not change keeps its stored value exactly. Invalid values show
their error beside the field at once, and page **Save** is refused until they are fixed. Edits go
straight into the page draft; page **Save** persists it and page **Cancel** discards it.
**Reset to built-in** removes that set from the draft, so after Save that density code uses the
built-in values.
After **Refresh device details**, the section also lists the built-in values, with the label
**Built-in**, for the loaded cartridge's density (or, without a cartridge, each density the drive
supports) that has no set of its own. Their **Edit** adds a set started from those values. Adding a
set for a density code likewise starts it from that code's built-in values, when it has any, so you
replace only what you measured; the source reference stays empty for you to describe your
measurement. The CLI's
`settings devices` output lists every built-in set as `builtin_read_parameters`; a replacement may
include it unchanged, and the server ignores it.
A set takes precedence even when incomplete, so a partial set can retain Logical order. Leave unknown
values empty; do not fill them with zero to make a complete set. In the stored set (and the CLI's
JSON), time values use seconds, rates use bytes per second, and `streaming_bytes` uses a decimal
string to preserve unsigned 64-bit values. The source reference is free text that describes the
measurement conditions.
The form labels these values **Fixed positioning time** (`base_seconds`), **Seek time for one
complete span** (`seek_seconds_per_wrap`), **Added time when arriving against the wrap direction**
(`approach_reversal_seconds`), **Streaming distance** (`streaming_bytes`), **Read rate**
(`bytes_per_second`), **Filemark overhead** (`filemark_overhead_seconds`), the Completion
**Fixed time**, **Full-span time** and **LTFS unmount time** (`fixed_seconds`,
`full_span_seconds`, `ltfs_unmount_seconds`) and **Nominal wraps** (`nominal_wraps`).
Positioning uses `base_seconds` (the fixed time of every LOCATE the drive does not stream through),
`approach_reversal_seconds` (the tape arrives moving against the target wrap's read direction, or
turns at a wrap end while reading) and
a seek-speed slope; every value is nonnegative. For uncompressed linear interpolation,
`seek_seconds_per_wrap` means seconds to move one complete longitudinal tape span at seek speed,
not seconds to read all data in a wrap. `streaming_bytes` is how far logically ahead the drive
reaches by continuing to read instead of positioning: a target at most that many bytes ahead costs
the skipped records at `bytes_per_second` plus one `approach_reversal_seconds` for each wrap turn
crossed; omitted or zero never applies. Reading a file through a wrap turn also pays one
`approach_reversal_seconds` per turn. A longer forward move within one half of the same wrap is a
read-speed locate:
`base_seconds` plus the skipped records at `bytes_per_second`. Every other move, including a
backward move, a forward move across the middle of the tape and every wrap change, costs
`base_seconds` plus the distance at seek speed, plus one `approach_reversal_seconds` when it arrives
against the target wrap's read direction.
Deriving geometry from a live wrap report requires
an explicit nominal wrap count and assumes four equal bands with alternating wrap directions; without
it the read keeps Logical order. Validate the applicable layout
and measurements for the actual device. The final partial wrap retains its real end of data.
The optional `completion` object describes normal unload with `full_span_seconds` and
`fixed_seconds`; LTFS completion adds `ltfs_unmount_seconds` for the unmount script and YATM's
finalization, and tar ignores it. The full span is one physical tape length, independent of the serpentine
logical block count. Omitted values remain unknown; LOCATE timings do not fill them automatically.

**From device details** on a set uses the currently displayed report, without a new
device query, to fill the set's drive serial. It clears the other
values instead of assigning old measurements to new hardware, and is unavailable when there is no
report or the report shows another density code. Supply measured timings and geometry
separately, or let a [Calibration](#calibration) measure and store a complete set. These draft actions do not access hardware or certify that optimization is available.

## Calibration

A **Calibration** Job measures a drive's positioning times on one cartridge and stores them as
that device's read parameters for the cartridge's recorded density. It names only the device; the
Job loads the cartridge. It runs on one of two cartridges:

- Any cartridge with two full wraps of data (about 37 GB on LTO-5; a failed run states the amount
  for the loaded cartridge), whether or not the Library knows it, tar or LTFS:
  `yatm-cli calibration create --device <configured device>`.
  The Job reads no files, changes no data and runs no Tape script, so the cartridge's encryption
  key is not needed. It converts wraps to bytes with the nominal native capacity of the
  cartridge's recorded density (1.5 TB over 80 wraps on LTO-5), which was 2.7% below the measured
  wrap on the LTO-5 test cartridge; the times themselves are measured. It works in the partition
  that holds the most wraps: the only partition of a tar cartridge, the data partition of an LTFS
  one, which it positions in without mounting LTFS. On the LTO-5 test drive an LTFS cartridge gave the
  same terms as a tar one, within 0.15 s per span.
- A spare cartridge: `yatm-cli calibration create --device <configured device> --write-test-data`.
  **This overwrites the cartridge from the beginning of tape. Never use it on a cartridge that holds
  data you need.** The Job does not look at what the cartridge holds; it only refuses a cartridge
  whose barcode is registered as Media in the Library, as FORMAT does; turn the option off to
  calibrate on that Media instead. The refusal comes after the
  Job has loaded the cartridge, because only a loaded cartridge reports its barcode; nothing is
  written and the cartridge stays loaded. It writes zero records with
  drive compression off and 512 KiB records until the drive reports wraps 0 and 1 complete,
  then calibrates on them. Afterwards the cartridge holds that test data and is no longer blank.
  To use it again, initialize it with FORMAT as described in
  [Load Tape for Archive](devices.md#load-tape-for-archive).

In the web interface, open **Settings → Devices**, choose the device, expand **Read parameters** and
select **Calibrate…**. The dialog identifies the loaded cartridge as **Check Tape** does. Without
**Write test data** it calibrates on whatever the cartridge holds. **Write test data** shows the overwrite warning and is unavailable for a registered cartridge. It requires typing the cartridge's barcode, as
FORMAT does, or, for a cartridge without a readable barcode such as a new one, ticking **Overwrite
the cartridge in** followed by the device name. **Start calibration** creates the Job and opens its
page, which shows the measured moves, the elapsed time and, once fitted, the parameters with a link
back to **Read parameters**; opening or cancelling the dialog creates nothing.

From the CLI, follow either with `yatm-cli job wait` and `yatm-cli job progress`. The Job waits for the drive like
any other Media step, loads the cartridge, so one an earlier step ejected need not be reinserted, and then moves the tape through a fixed sequence of about 22 LOCATEs in wraps
0 and 1. It takes about nine minutes on LTO-5, including the final unload, plus the time to write
the test data when requested.
Progress lists each measured move as it completes. At the end it fits the positioning and reading
terms, unloads the cartridge, timing the unload for the completion terms, and replaces the device's
set for that density code (or adds one) in **Read parameters**, with source reference
`Calibration Job <id>` and the drive's serial. Times are stored rounded to a hundredth of a second,
the read rate to a whole byte per second and the streaming distance to a whole byte. The geometry and LTFS unmount time come from the
built-in values for that density, so the stored set is complete on its own.

The result applies to that drive and density code only. Edit it or use **Reset to built-in** like
any other set. Deleting the Job keeps the stored set. If the moves do not fit the positioning model
the read planner assumes, the Job fails and leaves Device settings unchanged; so does any other
failure or cancellation, which also unloads the cartridge unless it was the wrong cartridge (a
different barcode, or a registered one for test data). There is no retry: create a new
Calibration.

## Last Prepared Read

Device details show **Last prepared read** with its timestamp, Job, Media, parameter/position sources,
applicability and available validation errors. It is historical preparation information, not the
currently loaded cartridge or the final read order selected by planning. It remains available while the
drive is busy; after a service restart it is absent until another read is prepared.
