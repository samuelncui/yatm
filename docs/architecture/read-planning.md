# Tape Read Planning

How Tape reads choose an order: Logical and Auto read order, the cost model and its parameter
sources, passive read learning and position maps. The
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
LOGICAL route and adopts only a lower complete-route estimate that covers every member exactly once;
it is a deterministic heuristic, not a proof of optimality. A candidate move whose cost is unknown is
skipped; LOGICAL is kept with a recorded reason when a member's range, exit state or spatial
coordinate is unknown, when the current route cannot be priced, or when no lower route is found.

The total includes the prepared initial position to the first file, complete target reads, each
later transition and the provider's terminal action from the forecast exit. Tar preparation reads
the identity label and establishes a qualified host-next address; its terminal action is
synchronous unload, priced as fixed work plus the physical longitudinal fraction times full-span
rewind time. Missing completion parameters keep LOGICAL. LTFS delegates completion to the configured
unmount adapter and prices rewind/unload from the final forecast position, assuming completion does
not rewrite the Index; an adapter-caused Index rewrite lies outside this estimate. Scores are
route-comparison estimates, not elapsed-time guarantees. A provider with an unknown initial position
fixes the first LOGICAL file and compares only the remainder; otherwise an unknown origin keeps LOGICAL.

One planning budget covers indexing and search: absent `read_planning_seconds` allows 300 seconds;
explicit values are positive. Expiry keeps the last completely verified route or the LOGICAL
manifest, never a partial selection. Cancellation, index corruption and temporary-disk failure remain
errors. Closing the index removes it; abandoned directories follow the shared seven-day cleanup
policy. After planning or its fallback, the Session records the effective order (AUTO only when a
lower route was adopted) and its reason in the Device read-optimization status, and the attempt's
read-order log records `planning_ns`.

The [cost model](../../internal/media/read_cost.go) retains all seven CERN factors: intercept,
wrap, band, landing zone, direction, same-wrap step-back and longitudinal distance. Signed terms
are regression coefficients; a negative term is not an independent mechanical benefit. Nonfinite,
negative or overflowing total predictions cannot quote elapsed time. Absolute LPOS interpolation
uses the actual interval midpoint `min + (max-min)/2`. Alternatively,
`longitudinal_seconds_per_wrap` prices one whole longitudinal tape span using normalized coordinates
0–1 and midpoint 0.5; it is mutually exclusive with seconds per LPOS. Reverse wraps reflect that
coordinate. Its estimates are not guaranteed latency bounds.

Parameter priority is manual read parameters, learned read parameters matching
all observed conditions, then source-labelled built-in initial values. Generation/density sources
supply documented layout and native-rate references. The generic seven-term timing seed comes from
the recorded HP LTO-5 experiment; it is an unmeasured borrowed prior for other readers, not an LTO
standard or portable validation result. Missing serial, firmware, density, record or written control
facts remain unknown. Partial manual input is never silently filled from learning. Hardware-compressed
written content retains ordinary reading and exact seek but is excluded from linear smart ordering.
Changing current drive compression does not change the cartridge's written policy.
The optional `completion` parameters keep full-span rewind and fixed unload work separate. Built-in
completion values borrow provisional HP references; they are not generation-wide mechanical guarantees.

Passive read learning defaults on and adds no measurement command. The native transport emits completed
scalar observations into a bounded 256-entry queue; overflow drops observations and increments a
counter without waiting. The consumer only buffers records in memory; nothing is persisted or fitted
while the record lane is active. Sequence gaps and failed commands withdraw the inferred logical
cursor, idle time and preceding action, so only actions whose endpoints were known are recorded.
LOCATE(10)/LOCATE(16), REWIND and synchronous UNLOAD each form one record. A first READ after LOCATE
and a READ that crosses a filemark together with its following data READ are single records. Other
data READs form a READ segment: the contiguous READs between any other commands, split only where
the discard label changes, never by time. Successful nonmoving restoration preserves the cursor but
ends READ continuity. SPACE is not decoded, so it withdraws nothing and records nothing; command
observations do not expose RECOVERED ERROR sense, so records carry no recovered-error count.

Each record keeps raw facts only: the logical address of both endpoints (partition, logical object
and file number when known), their wrap and longitudinal offset within the wrap's interpolation span
as resolved by the Session's position map, that map's source, observation time and LPOS bounds, the
SCSI opcode, action, preceding action, absolute start, host idle time before the action, and for
READ records the command count, bytes, device seconds, host wait, largest host gap and discard label.
Each record also names its device and applicability. Band, direction and access features are derived
at fit time from the wrap and the generation layout through the same feature function the cost model
uses. After native I/O settles and the drive is released, the attempt's records are appended to its
Job bundle's [observation file](persistence.md#job-bundle). A background refit, serialized per device
and bounded by the process lifetime, then streams the observation files of every Job in the catalog,
keeps those of the same device and applicability, and refits every action:

- LOCATE uses the seven-term model in the base parameters' longitudinal unit.
- READ uses seconds = bytes / rate, plus the first-READ or filemark overhead for those records.
- UNLOAD uses fixed work plus the physical fraction of one full-span rewind; it never relabels LOCATE
  coefficients as rewind time.

The fit minimizes weighted absolute deviations. Each record weighs one over its Job's record count for
that action, so every Job carries equal weight and one abnormal Job among N moves the result by about
1/N. The solve uses iteratively reweighted least squares with a fixed iteration bound and a
deterministic stopping rule; the same observations always give the same parameters. A coefficient is updated
only when its weighted feature column is independent under the scaled/reorthogonalized rank check,
and only when the result is a valid parameter (positive rate and full span, nonnegative overheads,
fixed work and longitudinal slope); otherwise it keeps the base value. The published model records
the observation count and the unweighted mean and maximum absolute residual over those observations.
Isolated Device Settings writes publish it for subsequent tasks, replacing the stored model read at
the start of that refit. Deleting a Job removes its observations from later refits; there is no other
retention. A write or refit failure only logs a warning; manual preferences remain unchanged. Turning
learning off records and publishes nothing; existing observations remain with their Jobs.

## LTFS Reads

LTFS uses the same planner with its existing file extents and native ACP paths. Optional device
facts are acquired under the existing owner before mount; that raw descriptor closes before LTFS
owns physical I/O. AUTO preparation can read each physical partition's VOL1/filemark/XML Label/
filemark prefix using qualified CP LOCATE and variable READ, without changing controls or the
adapter-installed key. Two complete, agreeing Labels establish the symbol-to-physical mapping;
the bounded reader does not scan file contents. Its two partition-start seeks have separate
preparation cost, recorded in the Job log. The XML cap is 512 KiB per Label, not a format limit.
Commands use a twenty-minute native timeout and retain ownership until settlement. Unavailable
host buffers, unsupported forms or unusable complete Labels withdraw this optional source;
cancellation, native I/O faults and uncertain completion remain errors. Cleanup must finish before mount.

Mounted LTFS supplies the written block size, compression policy, volume UUID and partition roles,
which must agree with the pre-mount Labels. Its current index generation binds retained geometry;
Labels contain no index generation. Mount can reposition, so its initial cursor remains unknown.
The read-only `indexLocation` must place the current Index on the Index partition for this normal
completion comparison. A missing/invalid pointer, unknown matching data-partition EOD or incomplete
completion parameters withdraw optimization. The old Index start is never substituted for EOD.
`ltfs.sync` is not a probe: reading it can write an Index and is not used.
When the raw source is unavailable, unambiguous mounted MAM
selectors remain usable. Contradictory sources never substitute for one another. Partition letters
and capacity ratios alone do not establish addresses. Missing or conflicting facts retain LOGICAL
without changing native reads. No raw command is issued after LTFS mounts.

The LTFS evaluator visits a file's extents in file-offset order, retaining sparse gaps and partial
block uncertainty. It prices the demonstrated last-block cache, adjacent reads and ordinary seeks;
it grants no unimplemented gap-discard benefit. The mounted initial cursor is unknown, so every
route keeps the same complete first file. This terminal model only prices data-partition content;
other or cross-partition selections retain LOGICAL. Matching native learned read parameters can supply mechanical priors, but do not validate LTFS
whole-file latency. LTFS has no whole-file passive read learning and no ACP source-timing
extension. Finalization
invalidates the Session's model before another attempt can use it.

## Position Maps

Position maps belong to Media and contain spatial facts, source, binding and uncertainty. A live
complete REOWP report takes priority. Read preparation tries cartridge memory once on the owned
HP LTO-5 descriptor if REOWP has no complete directory; ordinary device Inspect does not read CM.
The READ BUFFER descriptor bounds the image to 8 KiB and each transfer to 4 KiB. Unsupported,
malformed or inconsistent CM leaves matching retained geometry available; cancellation, uncertain
completion and medium changes remain preparation errors. No failure triggers LOAD or another motion.
LTFS obtains the same facts before mounting and closes the raw owner before native reads.

The [CM decoder](../../internal/media/tape/cm_directory.go) accepts the studied version-4 directory
and version-2 EOD pages for one or two partitions. Checksums protect the tables, directory header,
each entry and each EOD page. The device's confirmed partition count selects active EOD pages;
an unused second page can remain after single-partition FORMAT and does not create a partition.
Tar reuses its configured actual count; pre-mount CM fallback otherwise reads the bounded MAM
partition list once. Unknown partition identity withdraws CM. Complete valid runs must each match exactly one completed EOD's
terminal dataset/write pass and separate record/filemark totals. Partition starts come from these
runs, never cartridge identity or nominal capacity. Unexplained extended counts and other formats
withdraw this source. The [research](../research/tape-read-ordering.md#direct-physical-coordinates)
states its empirical scope and remaining limits. CM cumulative object counts use exclusive bounds.
The paired HP Ultrium 5-SCSI/Z6ED density-0x58 REOWP response uses the same qualified count convention;
other readers retain their existing reported-object interpretation. Earlier inclusive REOWP maps
from that HP5 configuration require a new observation before reuse. Source selection never changes a saved
file's logical locator. Accepted derived geometry uses the existing Media map and session lifetime;
no raw CM image or per-file wrap is persisted. A new read after APPEND observes the current directory.

When timing parameters omit nominal layout,
a self-described tar map remains usable only if current full boundaries agree and current EOD covers
its saved prefix; confirmation never extends retained coverage. Tar maps bind to initialization ID; LTFS maps
bind to volume UUID and index generation. Each map owns its explicit endpoint convention. Missing
or ambiguous coverage refuses cost estimates without changing exact logical seek addresses. Session
invalidation withdraws live estimates. The four-band, alternating-wrap interpolation assumption uses
a sourced nominal wrap count; complete intervals use their own boundaries. A partial final interval
uses the greater of its observed length and typical complete interval length for scale, while actual
EOD still bounds readable data. Locations whose wrap classification is ambiguous remain unknown.

Tar WRITE geometry inference uses existing start/final position checks and completed WRITE facts,
with no new command. The consumer bins native time by cumulative confirmed bytes, separately retains
host-supply gaps, checks first-half frequency, recurrence, phase and harmonics, and rejects starved,
constant-slow or continuously backhitching signals. Fixed bin storage is bounded; raw observations
are discarded after deriving the map. A successful APPEND preserves the complete prefix and replaces
only the affected tail; insufficient evidence retains old coverage without claiming the new tail.
Rejected inputs allocate no byte-bin storage.
Unknown acceptance never becomes inferred written bytes. Map publication follows physical cleanup,
and failure to save optional geometry cannot fail content publication. The [research evidence](../research/tar-tape.md)
separates the same-cartridge blind replay from prospective hardware qualification.

Tar planning and execution share one full-range evaluator for retained-record reuse, continued READ
with gap discard/filemarks, and LOCATE plus target reads. Headers, padding, cross-record ends and
actual host-next/cache state are included. Missing seek costs never justify reading an avoidable
gap; immediately adjacent or retained records can still be reused. Actual reads validate header,
path, content length and padding. A safe state mismatch withdraws further estimates; physical I/O
failure retains the existing Tape error/cleanup boundary. A planned sequence remains a fixed estimate,
not a physical cursor observation. The optimizer is a deterministic heuristic, not a global optimum.

Passive REOWP inspection scopes the A3/1F/45 maintenance CDB family to IBM and HP/HPE Ultrium
devices, using independently documented IBM and HPE response framing. Standard RSOC reports retain
exact service-action and field-mask checks. A complete vendor-specific support-5 report for HP/HPE
Ultrium permits one bounded report-all query on loaded, ready media; rejection never triggers a
second form. Other vendor/product families remain unknown without a vendor query. A successful
query supplies OBSERVED evidence, with physical validation UNVALIDATED. Typed observations expose the reported
long command form and source, with count only after an interpretable response; these do not prove
full-wrap coverage. Successful querying
alone leaves freshness unknown. The live reported observation preserves gaps, pair identities and
endpoint semantics, and becomes unusable after its owner writes, initializes, loses certainty or
closes. Direction, band, full-wrap normalization and empirical costs require separate applicable
evidence.

## Position-Map Refresh

Explicit position-map refresh precedes read settings/key application and label validation under the
attempt lease. Applicable documented HOLD/LOAD semantics, positively reported exact synchronous
forms, a ready cartridge, usable all-wrap reporting and logical navigation establish eligibility;
physical validation remains separate. The provider requires encryption/decryption off without
foreign control before motion. It invalidates old geometry/navigation, performs one non-ejecting
HOLD, confirms the held/unthreaded state and performs one LOAD. It then reobserves drive identity,
readiness, navigation and the bounded map on the same descriptor. The factory cross-checks the
cartridge identity again before applying the frozen read policy/key and validating the label.
Only that post-HOLD readiness check accepts the exact current held-state response; it does not
close shared command admission. Ordinary readiness loss, uncertain completion and cancellation
still prevent further motion.
Original non-secret controls are captured before motion and retained through ordinary cleanup.
Cancellation or failure never schedules a compensating LOAD, retry or eject; failed restoration
keeps the existing device-unavailability boundary.

The replacement map is a newly acquired reported observation. The cycle does not establish map
reconstruction, full physical coverage or `Fresh=true`, and supplies no timing model or AUTO
recommendation. AUTO never adds a reload. Restore preserves the explicit action through frozen
selection; Scan accepts it only for frozen tar VERIFY_COPIES.
