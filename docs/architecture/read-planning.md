# Tape Read Planning

How Tape reads choose an order: Logical and Auto read order, the cost model and its parameter
sources and position maps. The
[Tape contract](tape.md) owns cartridge identity, formats and Sessions.

## Software Read Ordering

The Device's LOGICAL/AUTO preference and parameters are frozen at admission. LOGICAL retains the
manifest order, eligible member seek and the same per-member cost decisions as AUTO. AUTO plans
all selected pending reads on the loaded Media after identification and Session preparation, before
feeding ACP. Verified local Restore outputs are excluded first. Other cartridges and explicit Media
attempts get their own plan; Library edits do not retarget a Job's frozen content or locators.
Scan with `VERIFY_COPIES` plans its complete frozen baseline without a fresh Inventory traversal.
Other Scan policies retain their enumeration/read flow. `read_batch` controls delivery to ACP,
not optimization scope. Native source paths remain native ACP inputs.
Native paths can still supply Media extents for planning; endpoint close tracking does not determine
whether those extents are usable.

LTFS prefers the extents already frozen with the selection. When they are missing, including a
migrated `ltfs_v0` Tape without an old XML capture, the same planning database can fill only the
missing ranges from this mount's captured Index. The parser verifies the mounted volume UUID and
Index generation, scans once, and supplies path/size/extent entries. Bounded batch joins retain
only selected matches; duplicate paths, size differences and incomplete parses keep the complete
LOGICAL route. Existing valid ranges are not replaced. No Job or Library locator is rewritten,
and ACP still receives the original native paths. The whole preparation shares the planning
budget and disposable index; a fully located selection does not open the captured XML.
Extents must fit the declared file length; sparse holes remain valid. Exceeding the bounded Index
parser's limits retains complete LOGICAL reading and does not restrict ordinary inventory parsing.
Native Scan sources retain their Media path and size for this lookup independently of ACP paths.

The [planner](../../internal/media/readplan/plan.go) orders one complete selection in a disposable
SQLite index under `Work/temporary`; Job and Library locators are never rewritten and ACP receives the
original sources. Providers must quote every member transition, a certified incoming-independent exit
state, spatial neighbourhood coordinates and the terminal action. The search starts from the complete
LOGICAL route and keeps only a lower complete-route estimate that covers every member exactly once;
it is a heuristic, not a proof of optimality, and its steps are deterministic, but where it stops
depends on elapsed time (the payback stop and the budget below), so the same selection can end on a
different route on a faster or slower host. The best route is adopted only when its
predicted saving over LOGICAL is at least one `base_seconds` of the applied parameters, the fixed
cost every LOCATE pays, and never less than the built-in `base_seconds`, so a configured
value of zero cannot remove the floor: a smaller saving removes less than one LOCATE and lies within
the route estimate's accumulated per-move error, so LOGICAL is kept and the reason states both values. A
candidate move whose cost is unknown is
skipped; LOGICAL is kept with a recorded reason when a member's range, exit state or spatial
coordinate is unknown, when the current route cannot be priced, or when no lower route is found.

The total includes the prepared initial position to the first file, complete target reads, each
later transition and the provider's terminal action from the forecast exit. Tar preparation reads
the identity label and establishes a qualified host-next address; its terminal action is
synchronous unload, priced as fixed work plus the physical longitudinal fraction times full-span
rewind time. Missing completion parameters keep LOGICAL. LTFS delegates completion to the configured
unmount adapter and prices the same rewind/unload from the final forecast position plus
`ltfs_unmount_seconds` (absent is zero) for the adapter and Session finalization, assuming
completion does not rewrite the Index; an adapter-caused Index rewrite lies outside this estimate. Scores are
route-comparison estimates, not elapsed-time guarantees. A provider with an unknown initial position
fixes the first LOGICAL file and compares only the remainder; otherwise an unknown origin keeps LOGICAL.

One planning budget covers indexing and search: absent `read_planning_seconds` allows 300 seconds;
explicit values are positive. Within it, planning also has to pay for itself: planning precedes
every read, and search returns diminish, so after each search round the planner stops once the time
spent since planning began exceeds the predicted saving achieved so far and keeps that route. Expiry keeps the last completely verified route or the LOGICAL
manifest, never a partial selection. Cancellation, index corruption and temporary-disk failure remain
errors. A candidate route whose changed-edge estimate disagrees with its complete score, a
current route that loses a priced edge and a route that no longer covers every member exactly once
each break a planner invariant; because ordering is optional, the attempt then keeps LOGICAL and
logs that reason as a warning instead of failing. Closing the index removes it; abandoned directories follow the shared seven-day cleanup
policy. After planning or its fallback, the Session records the effective order (AUTO only when a
lower route was adopted) and its reason in the Device read-optimization status, and the attempt's
read-order log records `planning_ns` and, when the LOGICAL route could be priced, the estimated
LOGICAL and selected route costs (`estimated_logical_ms`, `estimated_planned_ms`). When every read
of such an attempt settles without error, a second log line repeats both estimates with
`realized_ms`, the time from the end of planning until the last read settled, so a run can be
compared with its prediction; the estimates also include the terminal action, which follows later.
The order is chosen from the difference between two routes over the same files, so a cost every
order pays alike, such as the drive regaining speed after each positioning, cancels and is not
modelled; it makes both absolute estimates slightly low without changing the choice.

The cost model applies only to uncompressed content (every tar Media, and LTFS volumes whose label
records compression off) and has no compression term; its read rate is the density's native rate (or the configured one), because gap reads are discarded on the host and member reads cost the same in every order.

The [cost model](../../internal/media/read_cost.go) prices a LOCATE from a to b in one partition with
nonnegative terms, choosing the first of three ways the drive can arrive:

- Streaming: b is logically ahead of a (its logical object at or after a's) by at most
  `streaming_bytes` (the streaming distance) divided by the applicable record bytes, in any wrap, because the drive keeps
  reading instead of positioning. It costs the skipped objects at reading speed (their record bytes
  divided by `bytes_per_second`) plus one `approach_reversal_seconds` for each wrap turn crossed,
  and no other positioning term. Absent or zero `streaming_bytes`, or an unknown record size, never
  streams.
- A read-speed locate: b is in the same wrap interval and half as a and does not lie behind it (b
  at or after a in the wrap's read direction). It costs `base_seconds` plus the objects between
  them at reading speed, so reading speed has no separate parameter.
- A seek, for every other move (a same-wrap move behind the head, a forward move across the
  midpoint and every wrap change): `base_seconds` plus the physical longitudinal distance at seek
  speed (`seek_seconds_per_wrap`), plus one whole `approach_reversal_seconds` when the signed
  longitudinal distance of the move along b's wrap read direction is negative, that is, when the
  tape arrives moving against that direction. Changing wrap or band adds no term of its own.

Halves split the wrap at its physical
midpoint, which belongs to the second half. This follows the CERN observation that drives position
at reading speed unless they cross a high-speed positioning zone, rather than a constant midpoint
crossing term; the [research](../research/tape-read-ordering.md) records the evidence.
`seek_seconds_per_wrap` prices one whole longitudinal tape span using normalized coordinates 0–1
and midpoint 0.5. Reverse wraps reflect that coordinate. Nonfinite or overflowing predictions cannot quote elapsed time, and the estimates are
not guaranteed latency bounds.

Reading follows the same rule: continuous reading from one logical object to a later one in the same
partition costs its objects at reading speed plus one `approach_reversal_seconds` for each wrap turn
crossed between them, because logical order runs through a partition's wraps in increasing number
and the drive reverses at each wrap end. A read is priced up to its host-next position, so one that
ends with a wrap's last object pays that turn and the following move, which starts at host-next in
the next wrap, does not pay it again. LTFS extent reads and tar transfers with a position map are
priced this way, and an endpoint the map does not resolve leaves the quote unknown. A tar transfer
without a position map prices its records at the read rate alone.

Each configured Tape device holds at most one set of read parameters per recorded density code in
its Device settings, entered by the operator or stored by a [Calibration](#calibrated-parameters). A read uses the set whose density code equals the loaded cartridge's recorded
density; a set that names a `drive_serial` other than the one the drive reports does not apply, and
the Prepared read's applicability reason says so. Otherwise the read uses the
[built-in initial values](../../internal/executor/media_tar_defaults.go): positioning and reading
terms from a fixed calibration sequence on one HP LTO-5 drive, completion timing measured on HP
LTO-5 and per-density wraps, native rate and rewind. The Prepared read reports the source as
`configured` or `builtin`; Settings Get returns the built-in set for every density with a known
layout, built by the same function. Missing values in a configured set stay unknown and are never filled from
the built-in values, so the affected estimates are unknown and the read keeps LOGICAL. Record size
and encryption take no part in selection: the time to pass one object at reading speed
and the streaming distance use the Session's actual record size (the tar Media profile's record
bytes, or the LTFS volume block size). Tar content with an unknown record policy, and LTFS volumes
whose label records compression on or unknown, retain ordinary reading and exact seek but are
excluded from linear smart ordering. Changing current drive compression does not change an LTFS
volume's written policy.
The optional `completion` parameters keep full-span rewind, fixed unload work and the additional
LTFS unmount time separate.

## Calibrated Parameters

A [Calibration Job](jobs.md#calibration) measures the set for one drive and recorded density. It
times at least 21 fitted LOCATEs in wraps 0 and 1 of a tar Media or written test data: same-half forward moves,
backward moves, forward probes that bisect the streaming distance and wrap changes, the fixed
sequence the [research](../research/tape-read-ordering.md#locate-time-shape-and-a-fixed-calibration-sequence)
measured. A least-squares fit yields the base, seek and approach-reversal terms, the reading time
of one span and the streaming distance. The fit fails, and Device settings stay unchanged, when no
move streamed, when fewer than two same-half LOCATEs were observed, when their slope differs from
the streaming reading speed by more than 10%, when a term is negative or unidentified, or when any
move misses the fit by more than 3 seconds.

The stored set applies to the drive's reported serial and the cartridge's recorded density. Its
positioning terms come from the fit; `streaming_bytes` is the streaming distance times the bytes in
wrap 0 (its logical objects times the Media's record bytes) and `bytes_per_second` is those bytes
over the reading time of one span. `completion.full_span_seconds` is the fitted seek time of one
span, and `completion.fixed_seconds` is the timed unload minus that seek time times the final span
fraction, not below zero. Geometry and `ltfs_unmount_seconds` are copied from the built-in values
for the density, so the set is complete on its own. Seconds are rounded to 0.01 and bytes to whole
numbers; `source_reference` names the Job.

## LTFS Reads

LTFS uses the same planner with its existing file extents and native ACP paths. Optional device
facts are acquired under the existing owner before mount; that raw descriptor closes before LTFS
owns physical I/O. That pre-mount step reads only the geometry facts; it does not read the
LTFS Labels. A LOGICAL Device does not take it.

Mounted LTFS supplies the written block size, compression policy, volume UUID and partition map.
The map must be exactly `I:a,D:b`: every known formatter (IBM/OpenLTFS and HPE `mkltfs`) places the
Index partition `a` on physical partition 0 and the data partition `b` on physical partition 1, so
that convention fixes the mapping `{a: 0, b: 1}`. Any other map keeps LOGICAL with its reason. A wrong
convention can only leave extents outside the map, which are unknown and keep LOGICAL, never change data.
Mount reads the current Index from the Index partition near BOT, so the post-mount position is
assumed, not observed, to be the Data partition's first object.
The read-only `indexLocation` must place the current Index on the Index partition for this normal
completion comparison. A missing/invalid pointer, unknown matching data-partition EOD or incomplete
completion parameters withdraw optimization. The old Index start is never substituted for EOD.
`ltfs.sync` is not a probe: reading it can write an Index and is not used.
Missing or conflicting facts retain LOGICAL
without changing native reads. No raw command is issued after LTFS mounts.

The LTFS evaluator visits a file's extents in file-offset order, retaining sparse gaps and partial
block uncertainty. It prices the demonstrated last-block cache, adjacent reads and ordinary seeks;
it grants no unimplemented gap-discard benefit. The first file is priced as an ordinary move from
the assumed post-mount position, without a cached block or streaming (mount leaves the
head on the Index partition, so the drive is not reading ahead in the data partition), so any member can be read first; when the
position map cannot resolve that object (its first wrap has no endpoint), every route keeps the same complete, unquoted first file. This terminal model only prices data-partition content;
other or cross-partition selections retain LOGICAL. It selects parameters as tar does; native Tape timing estimates do not validate LTFS whole-file latency. Finalization invalidates the Session's model before another attempt can use it.

## Position Maps

Position maps contain spatial facts and their source. They exist only for the current read
Session and are never written to the Catalog. A live complete REOWP report takes priority. Read preparation reads cartridge memory once on the owned
descriptor of any drive if REOWP has no complete directory; ordinary device Inspect does not read CM.
READ BUFFER tries buffer `10h`, which HP documents for Ultrium 2 to 6, and then `05h`, which IBM
and HPE Ultrium 7 and later document; HP's `20h` main-buffer segments are never read. The
descriptor bounds the image to 32 KiB and each transfer to 4 KiB. An empty, unsupported or
undecodable buffer moves to the next one. Unsupported, malformed or inconsistent CM leaves the
Session without a position map, so reading keeps LOGICAL order; cancellation, uncertain completion
and medium changes remain preparation errors. No failure triggers LOAD or another motion.
LTFS obtains the same facts before mounting and closes the raw owner before native reads.

The [CM decoder](../../internal/media/tape/cm_directory.go) selects the tape directory layout of
the cartridge generation, because interchange makes the format a property of the cartridge rather
than of the drive vendor: Ultrium-1's ECMA-319 half-wrap entries, Ultrium-2's 28-byte entries,
Ultrium-3 to 5's 16-byte header and Ultrium-6 to 9's 48-byte header, each with its wrap count.
A directory is accepted only when its exact length and every checksum fit one layout; the version
nibble is also required where hardware showed it (4 for Ultrium-5, 5 for Ultrium-6). EOD pages
`0x104`, `0x10e`, `0x10f` and `0x110` serve partitions 0 to 3. Ultrium-5 and Ultrium-6 are
checked against hardware images; the other generations are supported but unverified. Checksums
protect the tables, directory header, each entry and each EOD page. The device's confirmed
partition count selects active EOD pages; unused pages can remain after a FORMAT with fewer
partitions and do not create a partition.
Tar reuses its configured actual count; pre-mount CM fallback otherwise reads the bounded MAM
partition list once. Unknown partition identity withdraws CM. Complete valid runs must each match exactly one completed EOD's
terminal dataset/write pass and separate record/filemark totals. Partition starts come from these
runs, never cartridge identity or nominal capacity. Unexplained extended counts and other formats
withdraw this source. The [research](../research/tape-read-ordering.md#direct-physical-coordinates)
states its empirical scope and remaining limits.

Every wrap endpoint, from REOWP or CM, is exclusive: the first logical object after
that wrap, or actual EOD for the final partial wrap. This is the HPE-documented cumulative count; CM
cumulative counts already equal it. A drive whose completed-wrap value names the last object on the
wrap shifts that boundary by one object, which is accepted as estimation error. The
[endpoint research](../research/tape-read-ordering.md#reowp-endpoint-definition) records the vendor
definitions. Source selection never changes a saved file's logical locator. Accepted derived geometry
has the Session's lifetime; no position map, raw CM image or per-file wrap is persisted. Every read,
including one after APPEND, observes the current directory.

Without a current report or with timing parameters that omit the nominal layout, the Session has no
position map and keeps LOGICAL order. Missing or ambiguous coverage refuses cost estimates without changing exact logical seek addresses. Session
invalidation withdraws live estimates. The four-band, alternating-wrap interpolation assumption uses
a sourced nominal wrap count; complete intervals use their own boundaries. A partial final interval
uses the greater of its observed length and typical complete interval length for scale, while actual
EOD still bounds readable data. Locations whose wrap classification is ambiguous remain unknown.
Interpolation places objects by count, and LTFS ends every file with a short block, so a wrap of many
small files holds more objects and misplaces them. An LTFS Session therefore has no position map when
a full wrap's object count differs from its partition's median by more than 0.5%: that keeps the
misplacement below 0.5% of a span, 0.48 seconds at the built-in full-span seek and about the held-out
LTFS positioning error, while recorded HP LTO-5 maps deviate by at most 0.06%. The read keeps LOGICAL.

Tar planning and execution share one full-range evaluator for retained-record reuse, continued READ
with gap discard/filemarks, and LOCATE plus target reads. Headers, padding, cross-record ends and
actual host-next/cache state are included. Missing seek costs never justify reading an avoidable
gap; immediately adjacent or retained records can still be reused. Actual reads validate header,
path, content length and padding. A safe state mismatch withdraws further estimates; physical I/O
failure retains the existing Tape error/cleanup boundary. A planned sequence remains a fixed estimate,
not a physical cursor observation. The optimizer is a heuristic, not a global optimum.

Passive REOWP inspection scopes the A3/1F/45 maintenance CDB family to IBM and HP/HPE Ultrium
devices, using independently documented IBM and HPE response framing. Standard RSOC reports retain
exact service-action and field-mask checks. A complete vendor-specific support-5 report for HP/HPE
Ultrium permits one bounded report-all query on loaded, ready media; rejection never triggers a
second form. Other vendor/product families remain unknown without a vendor query. A successful
query supplies OBSERVED evidence. Typed observations expose the reported
long command form and source, with count only after an interpretable response; these do not prove
full-wrap coverage. Successful querying
alone leaves freshness unknown. The live reported observation preserves gaps, pair identities and
reported endpoints, and becomes unusable after its owner writes, initializes, loses certainty or
closes. Direction, band, full-wrap normalization and empirical costs require separate applicable
evidence.

