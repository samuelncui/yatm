# Faster Restores with Auto Read Order

Restoring files scattered across a Tape is usually dominated by positioning, not reading. LTO
records data in wraps that run back and forth along the tape, so files read in recorded
(**Logical**) order can send the drive along the tape length again and again. **Auto** plans the
complete selection on the loaded cartridge before reading. It uses the cartridge's position map and
the device's read parameters (manual, learned from
earlier Jobs, or built-in) to choose the order with the lowest predicted positioning, reading and
completion time. When its inputs do not apply or no order beats Logical, it keeps Logical. The order
never changes which files are read or how they are verified.

On one HP LTO-5 drive (Z6ED firmware) reading uncompressed tar, Auto took 47–69% less time to
restore files scattered across wraps or at wrap and band boundaries, and kept Logical when the
selected members were adjacent. The
[research record](../research/tape-read-ordering.md#software-read-ordering) has the measurements, method and limits.
These results are not a guarantee on other drives, compressed media or LTFS. Planning stops within
the **Read planning budget** (default 300 seconds) and always keeps a complete order.

## Use Auto

Choose **Read order → Auto** under **Settings → Devices**, or set `readOrder` to
`TAPE_READ_ORDER_AUTO` with `yatm-cli settings devices`. Saving performs no hardware operation, and
**Passive read learning** refines the parameters from ordinary Restores. Each Job log reports the
order used and any fallback reason, and **Device details** shows the last prepared read. Logical
remains the default because Auto has been measured only on the hardware above.

## How Auto Decides

**Read order** applies to each later read attempt on the selected device. **Logical** is the default
and preserves file order; eligible exact seek can still be used. **Auto** evaluates an alternative
order when eligible member seek, applicable geometry and a cost model are available. Otherwise it
keeps logical order and reports the fallback reason in the Job log. Saving Auto is allowed offline and
does not qualify the hardware. Matching saved READ timings can support continued reading and
record reuse in Logical mode; missing seek costs never justify reading through an avoidable gap.
Applicable positioning parameters and a usable map can enable Auto. Parameters come from manual read
parameters first, then learned read parameters, then built-in initial references. Layout/native-rate
references depend on recorded density; the generic timing seed is borrowed from a measured HP LTO-5
configuration and is not a measurement of every device. Incomplete observations retain normal reads.
For LTFS, Auto can read partition labels before mounting. This preparation takes time even when
the final read order remains Logical; the Job log records the label-reading time separately.
Auto also considers the final position. Native tar includes its normal rewind/unload. LTFS assumes
completion does not rewrite the Index and estimates rewind/unload from the final position. This
assumption changes neither mount scripts nor write protection; an actual Index rewrite is outside
the estimate. These scores compare candidate orders rather than predict a complete ETA.
Auto retains the complete order with the lowest available predicted cost. Prediction errors remain
visible, and offline verification checks sensitivity to positioning, longitudinal movement and read
speed. These estimates do not guarantee a speedup on unmeasured hardware.

Read preparation prefers the drive's wrap report, then the supported HP LTO-5 cartridge-memory
directory, then a matching saved position map. If none is usable, ordinary Logical reading remains
available. CM is queried once during preparation, not per file or during device-details refresh;
its source and any fallback reason appear in the Job log. It stores no per-file wrap assignment.
CM format support and physical validation have the [documented limits](../research/tape-read-ordering.md#direct-physical-coordinates).

Migrated LTFS tapes do not need a historical XML file for Auto ordering. If selected copies lack
extents, YATM can use the current Index captured by the mount adapter to fill their planning
positions. The mounted UUID and Index generation must match. Missing or conflicting Index data
keeps ordinary Logical reads; it does not alter the saved Job or Library content.

Current **Auto** ordering requires a Tape written with hardware compression **Off**.
Hardware-compressed tapes retain ordinary Logical reads and eligible exact seek. Changing the
drive's current compression setting does not change the cartridge's written profile. Files already
compressed before Archive are not excluded when Tape hardware compression is Off.

## Passive Read Learning

**Passive read learning** is On by default for native tar reads. It records completed ordinary I/O
without additional measurement commands in each Job's bundle, and after each Job refits the device's parameters
in the background from the observations of every existing Job, giving each Job equal weight so one
unusual Job has little effect. Deleting a Job removes its observations from later refits. The refit
is available to subsequent Jobs. Manual read parameters take priority and are never overwritten. Turning
learning Off stops recording; observations already recorded stay with their Jobs. LTFS can use
matching native learned read parameters as mechanical priors; separate LTFS whole-file learning is not
implemented. 

**Read planning budget (seconds)**
defaults to 300 when blank and accepts positive whole seconds. Planning completes before content
reading; the budget bounds planning and preserves a complete Logical or improved order.
It does not include device preparation or mounting.
Changing these Device settings never changes the current attempt or a cartridge's written profile.

## Manual Read Parameters

**Read parameters JSON** accepts measured inputs. They apply only when the actual drive identity,
firmware and density, and the cartridge's written record/compression/encryption conditions match.
Unknown conditions leave estimates unavailable. The manual-parameter dialog starts from a copy of
the page draft. **Apply** validates the JSON and changes that draft; page **Save** persists it.
Cancel leaves the page unchanged and confirms discarding edited JSON. **Use automatic parameters** clears the manual draft and
returns to automatic parameter selection after Save; it does not erase server-owned learned read parameters.
Manual inputs take precedence even when incomplete, so a partial manual model can retain Logical order. Unknown values may be omitted; do not fill them with zero to make a complete
document. Time values use seconds, rates use bytes per second, and LPOS bounds use decimal strings
to preserve unsigned 64-bit values. The source reference describes the measurement conditions.
For uncompressed linear interpolation, `longitudinal_seconds_per_wrap` means seconds to move one
complete longitudinal tape span, not seconds to read all data in a wrap. Supply it instead of
`longitudinal_seconds_per_lpos`; the two fields cannot coexist, even with a zero value.
Normalized inputs need no absolute LPOS bounds. Deriving geometry from a live wrap report requires
an explicit nominal wrap count and assumes four equal bands with alternating wrap directions. A
matching saved position map can instead supply its recorded layout. Validate the applicable layout
and measurements for the actual device. The final partial wrap retains its real end of data.
The optional `completion` object describes normal unload with `full_span_seconds` and
`fixed_seconds`. The full span is one physical tape length, independent of the serpentine
logical block count. Omitted values remain unknown; LOCATE timings do not fill them automatically.

**From device details** uses the currently displayed report, without a new
device query, to create a parameter draft from observed identity, density and record controls. It replaces any old parameter
draft instead of assigning old measurements to new hardware. Supply applicable measured timings,
geometry and the written compression/encryption conditions separately. Current variable mode
reports no maximum record size, and current write controls do not identify a cartridge's written
profile. These draft actions do not access hardware or certify that optimization is available.

## Last Prepared Read

Device details show **Last prepared read** with its timestamp, Job, Media, parameter/position sources,
applicability and available validation errors. It is historical preparation information, not the
currently loaded cartridge or the final read order selected by planning. It remains available while the
drive is busy; after a service restart it is absent until another read is prepared.
