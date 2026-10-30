# Tape Read Ordering

Status: Research reviewed on 2026-10-29 for [Issue #14](https://github.com/samuelncui/yatm/issues/14).
The [read planning contract](../architecture/read-planning.md) owns current behavior; this study
supplies evidence, method and remaining questions, not a hardware-support guarantee. Physical
measurements apply only to their stated device, firmware, format and workload.

## Objective

Determine when reordering a Tape read selection shortens Restore and Verify, which position and
timing inputs are trustworthy enough to plan with, and how to measure the result without
overstating it for other drives, compressed media or LTFS.

## Software Read Ordering

CERN's 2018 presentation distinguishes estimated layout from observed physical coordinates.
Its first approximation divides total logical blocks by the nominal wrap count on an assumed
full cartridge; the presentation explicitly warns about partial occupancy, reserved wraps,
compression variation, rate adaptation and bad areas. Its second approach samples vendor-specific
REQUEST SENSE wrap/LPOS fields during reading or writing and associates observations with segment
boundaries. This observes a current position; it is not a command mapping an arbitrary logical
block to coordinates. Buffering and the command's position definition still matter when associating
that observation with a file.
[CERN LTO study, slides 9–13](https://indico.cern.ch/event/730908/contributions/3153156/attachments/1732268/2800425/LTO-CERN-HEPiX-Oct-2018-germancancio.pdf).

CERN's February 2021 CTA investigation estimates logical-block placement within wraps from
READ END OF WRAP POSITION (REOWP) data. Its comparison used LTO-8 cartridges, identical selected
file lists, BOT starting conditions and total seek/read time. The presentation reports benefits
for both incompressible and highly compressible fixtures, while noting interpolation's suitability
for incompressible data and the need to improve non-BOT starts.
These measurements do not establish a portable timing model or a YATM speedup guarantee.
[CERN CTA RAO investigation, slides 5–10](https://indico.cern.ch/event/876801/contributions/4196223/subcontributions/325951/attachments/2186748/3694990/CERNCTARAO-preGDB-9Feb.pdf).

CERN CTA at `7949198909ffa0d0817aee26215f28aec79c9319` selects the next file using estimated
access from the preceding file's end. Its shortest-locate-time-first algorithm is a greedy
heuristic, not an exact solution for the minimum-cost complete route.
[CTA algorithm](https://github.com/cern-cta/CTA/blob/7949198909ffa0d0817aee26215f28aec79c9319/taped/rao/SLTFRAOAlgorithm.cpp),
[cost features](https://github.com/cern-cta/CTA/blob/7949198909ffa0d0817aee26215f28aec79c9319/taped/rao/CTACostHeuristic.cpp).
The implementation evaluates every remaining file at each step: quadratic cost evaluations and
linear retained file/candidate state, rather than an exact permutation search or a retained
all-pairs matrix. SQL paging can bound memory while preserving the same complete eligible set.

The seven-term prediction is `4.29 + 6.69*wrap + 3.2*band - 6.04*landingZone +
5.22*direction + 11.32*stepBack + 0.0006192*distance`. Its indicators compare the preceding end
with the next beginning; step-back is direction-dependent and applies within the same wrap.
The 2018 presentation labels the six setup coefficients in seconds and the distance coefficient
in seconds per LPOS unit. These are time-regression coefficients fitted from LTO-7M observations,
not measurements of another drive.
[CERN LTO study, slides 14–16 and 23](https://indico.cern.ch/event/730908/contributions/3153156/attachments/1732268/2800425/LTO-CERN-HEPiX-Oct-2018-germancancio.pdf).
Calculated counterexample: a forward same-wrap transition across the landing-zone boundary with
distance 1,000 predicts `4.29 - 6.04 + 0.6192 = -1.1308` seconds. This is an invalid physical
duration, not evidence that crossing the boundary takes negative time. The presentation itself
notes that short-distance and seek-to-read behavior need better modeling. Retaining the signed
prediction for candidate ranking, constraining/refitting it or adding a measured latency floor
are different choices that require review before integration. Treating it as a nonnegative
elapsed duration or silently omitting the two troublesome terms is not justified.

With all seven terms, same-wrap step-back can be more expensive than changing wrap;
an own-wrap neighbour can hide a cheaper candidate in an aggregated band/direction group.
Inference for the published coefficients: partitioning by wrap, landing zone and side of the origin
makes the non-distance terms constant, allowing indexed neighbours to recover a minimum signed
prediction. Fewer searches are possible by aggregating different-band/direction groups, while
excluding the origin wrap from its own band's direction group. Assigning wraps local ordinal bits
provides that exclusion: every other wrap differs in at least one bit, and the union of opposite-bit
groups contains all other wraps but not the origin. Search the origin wrap separately. This trades
additional disk memberships for fewer queries. The parameterized greedy reference used for the
measurements below uses this property with all seven terms. The [full-cartridge measurement](#uncompressed-full-cartridge-measurement)
evaluates one explicit parameter set; it does not establish portable physical inputs or defaults.
It is not an index proof for full target-transfer costs. Exact candidate search still does not make the greedy route globally
optimal. Whole-route comparison must include actual
initial position, internal extent transitions and uncertainty, and must not advance through
outputs needing no read.

LPOS units and usable bounds are separate facts. IBM's description of the LTO-1 through LTO-5
servo format gives one LPOS increment per 7.2 mm; that does not supply a cartridge's usable endpoints.
[IBM patent US8576510B2, Background](https://patents.google.com/patent/US8576510B2/en#description).
Mathematically, with `LPOS = min + span*u`, the continuous distance term is
`c_lpos*span*abs(u1-u2)`. A directly measured coefficient in seconds per full normalized span
therefore avoids inventing absolute LPOS bounds, while retaining the seven feature terms. Full-wrap
endpoints, direction, bands and landing-zone classification still need evidence. This is not
identical to the pinned implementation: it truncates interpolated LPOS to an integer, and its
landing-zone helper compares absolute LPOS with `(max-min)/2`, without adding `min`. Continuous
coordinates or a midpoint of `u=0.5` can change ties and classifications. The approved normalized
path uses that midpoint; [read planning contract](../architecture/read-planning.md#software-read-ordering) owns its
current implementation. Measured timing accuracy and its limits appear in the
[full-cartridge measurement](#uncompressed-full-cartridge-measurement); direct physical-coordinate
accuracy remains unmeasured.
[CTA interpolation](https://github.com/cern-cta/CTA/blob/7949198909ffa0d0817aee26215f28aec79c9319/taped/rao/InterpolationFilePositionEstimator.cpp#L75-L91),
[landing-zone calculation](https://github.com/cern-cta/CTA/blob/7949198909ffa0d0817aee26215f28aec79c9319/taped/rao/RAOHelpers.cpp#L40-L42).

The distinction is observable in CTA's own nonzero-origin test fixture: minimum 2,696 and maximum
171,097 yield a coded threshold of 84,200, whereas the interval midpoint is 86,896. The test
explicitly expects the coded threshold to belong to zone one; it does not establish the physical
location of a drive's speed-transition boundary. Thus the coordinate inconsistency is definite
if the intended feature is crossing the supplied interval's midpoint, but source inspection alone
does not prove that the arithmetic midpoint is the best measured physical boundary. Changing this
classification changes a tested model input and requires evaluation, not an unannounced cleanup.
The zero-origin example in the 2018 presentation does not exhibit this offset.
[CTA fixture and landing-zone test](https://github.com/cern-cta/CTA/blob/7949198909ffa0d0817aee26215f28aec79c9319/taped/rao/RAOTest.cpp#L184-L195).

### Standard Geometry and Timing Priors

LTO has shared format specifications. The alliance's January 2010 announcement identifies the
generation-5 format and mechanism/cartridge interchange specifications; the current licensing
process supplies the Format Specification through a documentation package after a confidentiality
agreement. This study has not obtained the complete U-516 text and revision, so it cannot certify
its performance clauses or conclude that no such clauses exist.
[LTO generation-5 announcement](https://www.lto.org/wp-content/uploads/2015/06/LTO-Gen_5_Spec_Release.pdf),
[LTO licensing](https://www.lto.org/licensing/licensing-process/).

Public ECMA-319, first edition June 2001, covers Ultrium-1. It specifies tape length, LPOS units,
four data bands, serpentine wrap numbering/direction and longitudinal regions in sections 9.2.1,
11.3.2, 12.8 and 15.1. Section 15.3 explicitly allows a variable number of datasets per wrap.
Its section 10.2 speed is a magnetic-test condition, not a universal seek/rewind rate. Applying
these numerical values to LTO-5 requires generation-specific evidence.
[ECMA-319](https://ecma-international.org/wp-content/uploads/ECMA-319_1st_edition_june_2001.pdf).

HP's LTO-5 specification supplies 80 wraps, four bands, 846 m total tape, 808 m data region and
9 m/s rewind speed; full EOT-to-BOT rewind is listed as 96 seconds with a TBC qualifier. The same
reader lists 93 seconds for LTO-4 and 78 for LTO-3, also TBC, and unload excluding rewind below
19 seconds for standard cartridges. These
give useful nominal geometry and mechanical timing priors, not actual per-cartridge block boundaries.
[HP Volume 4, June 2010, pages 31–32 and 40–41](https://docs.oracle.com/cd/E21419_04/en/LTO5_Vol4_E4/LTO5_Vol4_E4.pdf).

The existing HP5 LOCATE regression yields 92.690 seconds per longitudinal span; this is not a
directly measured full-length REWIND duration. A post-hoc sensitivity
comparison changed only this coefficient, retaining all six other fitted terms and all 48 observations:

| Longitudinal coefficient source | Seconds per span | RMS error on the existing 16 held-out observations |
| --- | ---: | ---: |
| Frozen measured regression | 92.690 | 3.552 s |
| HP data-region length / rewind speed | 89.778 | 4.028 s |
| HP total length / rewind speed | 94.000 | 3.394 s |
| HP full rewind specification, marked TBC | 96.000 | 3.238 s |

The mechanical estimates are within about 3.6% of the fitted coefficient. Their similar scale
supports using specifications as initial estimates and consistency checks. A pure transport time,
complete REWIND duration and LOCATE regression slope have different timing boundaries; selecting
the lowest error after observing these results would not be independent validation. No production
coefficient was changed. Geometry sources and timing sources should remain separate inputs.

The [read planning contract](../architecture/read-planning.md#software-read-ordering) defines the
current parameter sources. Automatic initial values are not calibrated measurements of the current
reader. Match the drive and loaded medium, and
distinguish average from full-span rewind. One full-span timing cannot identify setup, direction or
short-movement costs. Passive updates can reuse settled operations with known physical spans and
timing boundaries; an EOD-to-BOT
operation need not traverse a full longitudinal span on serpentine media. Exclude flush, unload,
queueing and recovery time before fitting motion, and validate updated estimates on separate
observations rather than repeatedly fitting and judging the same samples.

Progressive LOCATE fitting can learn operational latency without absolute LPOS, using normalized
layout and qualified logical endpoints. It cannot identify physical position or winding speed from
timing alone: density error and a distance coefficient can compensate for one another. Collect
bounded observations during ordinary operations, separate native LOCATE from subsequent READ, and
check support for short movement, backward movement and boundaries. AUTO preferentially observes
its chosen transitions, so low residuals on those routes do not validate alternatives it never
executes. Evaluate proposed updates on later observations and apply them to subsequent attempts;
keep an admitted attempt's parameters frozen. The current native passive-learning path instead
records observations in each Job bundle and refits with equal Job weight after each Job; the coverage and
physical-identifiability limits above still apply. Separate LTFS whole-file learning is not implemented.

### Position Sources and Coverage

The pinned CTA interpolator finds the first reported endpoint at or beyond a file's block ID.
For adjacent endpoints `P` and `E`, it estimates progress as `(block-P)/(E-P)`, reflecting that
progress for odd-numbered wraps. Configured minimum/maximum LPOS supply the longitudinal scale;
the nominal total wrap count supplies four equal band groups. This assumes dense ordered wrap IDs,
one relevant partition, the declared alternating orientation and approximately uniform object
density within each full wrap. REOWP alone supplies none of the LPOS scale or timing coefficients.
YATM's recorded member anchors can supply real logical starts/ends instead of CTA's
`fileSize/262144` end-block estimate.
[CTA position estimator](https://github.com/cern-cta/CTA/blob/7949198909ffa0d0817aee26215f28aec79c9319/taped/rao/InterpolationFilePositionEstimator.cpp),
[layout helpers](https://github.com/cern-cta/CTA/blob/7949198909ffa0d0817aee26215f28aec79c9319/taped/rao/RAOHelpers.cpp).

Inference for review: with compression disabled and nearly uniform record lengths, logical-object
progress is a stronger approximation of longitudinal progress. Prefer interpolation between each
wrap's observed boundaries to repeatedly adding one nominal blocks-per-wrap constant; the latter
accumulates boundary error. Variable record mode does not prevent uniform submitted record lengths,
but arbitrary record sizes and filemarks prevent equating every logical object with the same byte
count. Compression variation can distort progress inside a wrap even when both endpoints are known.
The February 2021 investigation explicitly describes interpolation as suitable for incompressible
files; it is not a compression-specific workaround.

A retrospective HP LTO-5/Z6ED observation supports approximate uniformity but not a universal
constant: in the prior compression-off, 256 KiB full-write run, 77 of 78 interior adjacent REOWP
endpoint differences were 73,543–73,647 objects; the difference ending at reported wrap 32 was
37,993. This excludes the initial interval and final EOD interval. The shorter interval's physical
cause is unresolved; it must not be silently removed from the map or assumed to span the same
physical length. These historical observations do not qualify intra-wrap interpolation, the current
cartridge initialization, or the failed Archive's publication.

The last reported endpoint may be EOD partway through a wrap. Stretching that interval across the
whole configured LPOS span is unjustified. CTA's last-wrap correction is also heuristic: for
`N` descriptors it replaces the last endpoint with `E[N-2] + (E[N-1]-E[0])/N`, using integer
division. The calculated example `[1000,1500]` becomes `[1000,1250]`, causing a formerly covered
block 1400 to exceed the corrected table. A single descriptor receives no correction and is treated
as wrap zero. These are source-level counterexamples, not hardware measurements. A replacement
must distinguish reported boundaries from inferred full-wrap spans and retain the actual logical
locator regardless of estimation quality.
[CTA factory](https://github.com/cern-cta/CTA/blob/7949198909ffa0d0817aee26215f28aec79c9319/taped/rao/FilePositionEstimatorFactory.cpp),
[last-wrap correction](https://github.com/cern-cta/CTA/blob/7949198909ffa0d0817aee26215f28aec79c9319/taped/rao/RAOHelpers.cpp#L12-L24).

The production factory applies this correction before constructing the estimator. Its existing
test only checks that a short final interval is extended: endpoints
`[208310,416271,624562,633521]` become `[208310,416271,624562,730864]`. It does not test preservation
of all observed addresses. The sum omits the first wrap's length, includes the final partial
interval and divides `N-1` differences by `N`. For equal full-wrap lengths `W` and a last partial
length `p`, the inferred final length is `floor(((N-2)*W+p)/N)`, which can be smaller than `p`.
Correcting an average would still leave compression and variable object density as estimation
limitations. An estimated full-wrap length must not replace the reported EOD as an address-validity
limit; rejecting that replacement does not require rejecting all partial-wrap estimation methods.
This distinction applies with compression disabled too. A partial interval containing half the
typical complete-wrap record count should not be normalized to a complete traversal merely because
it ends at recorded EOD. Applicable complete intervals could supply an estimated denominator, with
explicit uncertainty. The current normalized adapter uses the median complete interval length,
bounded below by the observed partial length. The full-cartridge measurement includes partial-wrap
targets, but does not independently measure their physical longitudinal coordinates.
[CTA tail test](https://github.com/cern-cta/CTA/blob/7949198909ffa0d0817aee26215f28aec79c9319/taped/rao/RAOTest.cpp#L167-L173).

Support is vendor/model/firmware-specific, not an LTO-generation switch. The reviewed documents
establish the following evidence, rather than a promise about every installed firmware:

| Drive family | Direct wrap/LPOS through REQUEST SENSE | REOWP endpoint table |
| --- | --- | --- |
| IBM LTO-1–4 | Defined in the 2008 reference; actual sense length must cover the fields | Not established by that reference |
| IBM LTO-5–6 | Defined in the current IBM reference | Explicitly unsupported in its generation table |
| IBM LTO-7–8 | Defined | Supported in the generation table |
| IBM LTO-9–10 | Defined with an additional wrap high bit | Supported in the generation table |
| HP LTO-5 / Z6ED | The reviewed guide and tested responses provide no wrap/LPOS pair | Short and long forms observed on this firmware, although absent from the reviewed 2011 command list |
| HPE LTO-7–9 | Defined in the August 2026 guides; LTO-9 adds a wrap high field | Defined in those guides; not physically tested by this study |

IBM sources: [2008 REQUEST SENSE, pages 194–198](https://publibfi.boulder.ibm.com/epubs/pdf/a3204509.pdf#page=212),
[2026 physical fields, section 5.2.31](https://www.ibm.com/support/pages/system/files/inline-files/LTO%20SCSI%20Reference%20GA32-0928-08%20(EXTERNAL).pdf#page=191),
[generation table A.2](https://www.ibm.com/support/pages/system/files/inline-files/LTO%20SCSI%20Reference%20GA32-0928-08%20(EXTERNAL).pdf#page=509).
HPE REQUEST SENSE definitions:
[LTO-7 edition 5](https://support.hpe.com/hpesc/public/docDisplay?docId=sd00006909en_us&page=GUID-D7147C7F-2016-0901-0922-0000000006B7.html),
[LTO-8 edition 5](https://support.hpe.com/hpesc/public/docDisplay?docId=sd00006916en_us&page=GUID-D7147C7F-2016-0901-0922-0000000006B7.html),
[LTO-9 edition 3](https://support.hpe.com/hpesc/public/docDisplay?docId=sd00001234en_us&page=GUID-D7147C7F-2016-0901-0922-0000000006B7.html).
HP5 direct-coordinate observations and alternative interfaces belong to
[Direct Physical Coordinates](#direct-physical-coordinates).

These sources are complementary. REOWP supports whole-selection interpolation; direct coordinates
can check its assumptions or provide sampled anchors. A direct current coordinate alone does not
locate every selected file, and moving to every file merely to discover coordinates can erase the
benefit of planning. Media specifications and user-provided parameters can supply applicable
geometry and costs, but not an unobserved cartridge's actual wrap boundaries. Standard READ POSITION
supplies logical/buffer state, not a replacement physical-coordinate pair. Physical samples, host-next
state and retained application bytes must keep their distinct meanings.

IBM GA32-0928-08 §5.2.20 documents the A3h/1Fh/45h command, short/long response framing and
populated-wrap endpoints. The long form is a reporting form, not proof that every physical wrap
has usable geometry. The documented table describes load-time state and can become stale after
writes; successful querying does not establish freshness for an already-loaded cartridge.
[IBM LTO SCSI Reference, section 5.2.20](https://www.ibm.com/support/pages/system/files/inline-files/LTO%20SCSI%20Reference%20GA32-0928-08%20(EXTERNAL).pdf).

HPE's [LTO-7 Host Interface Guide](https://support.hpe.com/hpesc/public/docDisplay?docId=sd00006909en_us),
part 20-STG-L7-TRMV3-ED5, edition 5 (August 2026), documents the same maintenance CDB family
and a separate response contract:

| Form | Byte layout |
| --- | --- |
| Short, 10 bytes | 0–1: response data length `0008h`; 2–3: reserved zero; 4–9: 48-bit BLOCK NUMBER |
| Long | 0–1: response data length, excluding those two bytes; 2–3: reserved zero; 12-byte wrap descriptors from byte 4 |
| Each wrap descriptor | 0–1: WRAP NUMBER; 2–3: PARTITION; 4–5: reserved zero; 6–11: 48-bit BLOCK NUMBER |

The short-form endpoint is the cumulative record and filemark count within the specified
partition through and including the specified wrap, not a byte offset. The short response has
no partition/wrap identifiers. A complete long response's length yields its descriptor count;
that count does not prove full physical-wrap coverage. The studied pages do not specify descriptor
ordering. HPE warns that data may be invalid while writing and primarily intends queries when not
writing, such as after loading; this does not establish a complete freshness lifecycle or make
IBM's write-invalidation rule an HPE guarantee.
[HPE command](https://support.hpe.com/hpesc/public/docDisplay?docId=sd00006909en_us&docLocale=en_US&page=GUID-D7147C7F-2016-0901-0922-000000000684.html),
[short response](https://support.hpe.com/hpesc/public/docDisplay?docId=sd00006909en_us&page=GUID-D7147C7F-2016-0901-0922-00000000067B.html),
[long response and descriptor](https://support.hpe.com/hpesc/public/docDisplay?docId=sd00006909en_us&page=GUID-D7147C7F-2016-0901-0922-00000000067E.html).

A controlled 2026-10-16 observation on HP Ultrium5-SCSI firmware Z6ED accepted both short and
report-all CDBs with GOOD status, returning 10 and 16 bytes respectively. The raw shapes match
the HPE framing, with one long-form descriptor and an endpoint consistent with the known logical
EOD. Standard long READ POSITION was unchanged afterward; ownership, production-state and control
guards passed. This establishes acceptance and response shape for that tested firmware, not an
LTO-5 portability guarantee, full-map coverage or freshness qualification.
Unknown vendor families also need their own applicable command/response evidence.

### Passive Write Timing

Host WRITE timing is a possible auxiliary source of turn-around candidates. It needs no extra
position command when the existing writer already records issue/completion and supplied bytes.
It cannot by itself distinguish turn-around from buffer backpressure, supply starvation, speed
changes or error recovery. Buffering can also hide a turn-around or shift its visible pause.
HP's Data Rate Matching explicitly adjusts tape speed to host supply; the distinction between
host rate and physical media motion is therefore material.
[HPE Data Rate Matching](https://www.hpe.com/emea_europe/en/collaterals/collateral.c04123270.html),
[IBM speed matching and backhitching](https://www.ibm.com/docs/en/ts2900tapeautoloader?topic=features-speed-matching).

HP5's Performance Data page `34h`, parameter zero, reports repositions per 100 MB and updates after
each 100 MB is physically written. It is a normalized activity measure, not a cumulative wrap-change
counter or current wrap ID. Other page fields describe buffer/native data rates, not a longitudinal
coordinate.
[HP5 Host Interface Guide, page 81](https://docs.oracle.com/cd/E21419_04/en/LTO5_Vol3_E5b/LTO5_Vol3_E5b.pdf#page=81).

Evaluation should compare passive timing candidates with independently reported endpoints or
documented coordinate observations, retaining write-buffer lag, missed events and unrelated stalls.
Use distinct streaming, supply-limited, compressible and APPEND workloads before assessing a
fallback estimator. A repeated pattern in one continuous write is useful evidence of correlation;
it does not identify physical coordinates universally. Monitoring must not introduce periodic
flushes, synchronous per-file position queries or a dependency from writing to metric persistence.
Missing telemetry remains missing and never changes the recorded logical seek address.

An offline comparison reused the uncompressed full-cartridge experiment below. Two fixed uniform
models were compared with its reported wrap boundaries. Advertised native capacity divided by
80 wraps and record size assigned 73.56% of covered logical addresses to a different wrap and
covered only 98.12% of recorded addresses. CERN's 2018 total-blocks/nominal-wraps approximation,
using the independently observed final READ POSITION rather than REOWP as input, covered all
addresses but assigned 30.29% to a different wrap. The final partial wrap and a short full interval
explain why full-cartridge occupancy does not imply equal intervals. These are logical-address
fractions on this cartridge, not an error rate measured across user files.

A fixed detector using one-second successful WRITE-completion bins found 80 low-throughput events
against 79 internal wrap boundaries. Its threshold was half the median byte rate, with at least
two low bins, gaps of at most one second joined, and five-second edge exclusions. No REOWP boundary
was used to fit those choices. At a 2,048-object matching tolerance, all 79 boundaries matched and
one extra event remained; at 1,024 objects, 76 matched, with four extras and three misses.
Matched event midpoints lagged the boundaries by 156–1,556 records, averaging about 216 MB of
host-acknowledged data. This is consistent with buffering and one-second binning, not a measured
constant buffer size. Threshold crossings supply candidates, not wrap IDs: an estimator must find
recurring sequences and distinguish unrelated stalls, rather than count every dip.

Recurrence should be analyzed against cumulative acknowledged WRITE bytes, not a fixed elapsed-time
period. A byte-domain study redistributed each one-second bin's elapsed time across 64 MiB byte
bins while conserving duration, retaining zero-progress time at its byte coordinate. The signal
was elapsed seconds/GiB during the observed write phase, not summed command latency. It used mean
removal, a Hann window and FFT before filtering events. LTO-5's nominal
1.5 TB and 80 wraps imply a 17.462 GiB mean as a harmonic-selection prior, not a required size
for every wrap. The selected recurrence was approximately 17.97 GiB in the whole stream and both
byte-halves; 128 MiB bins and half-bin origin shifts gave the same selected peak. The globally
strongest spectral peak was approximately 8.99 GiB, a second harmonic: taking the maximum blindly
would double the inferred transition rate. Autocorrelation and observed event intervals support
the approximately 18 GiB recurrence rather than a second event every 9 GiB.

Native capacity is [nominal rather than a hard upper bound](tape-capacity.md#capacity-is-not-one-interchangeable-number).
This uncompressed trace contains about 1.5286 TB of GOOD-acknowledged WRITE bytes, including tar
framing, about 1.9% above the 1.5 TB label. The typical observed interval is about 2.9% above the
nominal per-wrap mean. Neither percentage is a portable tolerance. A spectral prior must allow
deviations on both sides, then use local observations to refine intervals without equating every
wrap to the cartridge mean.

A local interval check used the first byte-half's spectral period without elapsed-time spacing or
REOWP input. For adjacent candidates less than half a period apart, it compared retaining either
against the two neighboring byte intervals, requiring each interval to support one cycle
(0.5–1.5 periods). Unsupported or tied cases remained unresolved; this is a study heuristic, not a
standard tolerance. It removed one extra event and retained 79 candidates, whose median gap was
17.974 GiB. All matched the 79 reported boundaries within the existing 512 MiB tolerance. The
remaining host-visible lag was 39–389 MiB, median 202.75 MiB; no buffer correction was fitted.
With the known initial wrap and ordered matches, the estimated boundaries misclassified about
1.12% of this cartridge's logical address length, versus 30.29% for the uniform total-blocks model.
Neither result measures errors across user files or independently observed physical LPOS.

These rules were developed after inspecting the cartridge, so split consistency and grid checks
are post-hoc evidence, not independent qualification. The 64 MiB grid oversamples the retained
one-second observations; zero padding cannot improve independent frequency resolution. Byte
recurrence must adapt to data density: compression changes host bytes per wrap. Missed cycles,
short intervals, split events and phase changes need explicit treatment. A periodic host stall
can also create this signal; spectral and interval agreement alone cannot exclude that cause.
Applicable layout and a known starting position can constrain an estimate, but must not force an
event count or turn ambiguous evidence into a reported physical endpoint.

A subsequent native-command replay separated completed WRITE durations from host supply gaps,
with a fixed 65,536-bin memory bound. It froze the inferred map before comparing the retained
REOWP reference: all 79 boundaries matched, with maximum displacement 923 records (230.75 MiB
at this cartridge's 256 KiB record setting). The minimum empirical endpoint allowance was 3,687
records; excluding ambiguous boundary neighborhoods retained stable wrap classification over
90.009% of the logical address range. These are same-cartridge post-hoc results, not independent
qualification or a specified physical tolerance. A periodic supply bottleneck or continuously
slow/backhitching source can leave no usable signal, and must not produce invented endpoints.
The [read planning contract](../architecture/read-planning.md#software-read-ordering) describes current production
observation, Media map persistence and uncertainty propagation. Estimated maps affect costs;
the recorded logical locators still determine actual reads.

### Reading Through Small Gaps

The [tar reader](../../internal/media/tar_read.go) already reuses a retained containing record,
can read the next record without LOCATE, and has a sequential path that reads and discards
intervening members. Eligible seeks otherwise use LOCATE. These source findings establish available
mechanisms, not a measured crossover between them.

Physical inference: a small forward gap can cost less to read through than to reposition and restart
reading. Compare the remaining gap bytes at an applicable sequential transfer rate, including
startup and wrap turnarounds, with LOCATE plus target-read startup. Credit only bytes actually
retained in the application's record buffer; nominal drive buffer capacity does not establish its
read-ahead contents. Continuing across APPEND boundaries requires known filemark counts and their
measured cost; absent boundary timing cannot justify a read-through estimate.
Arbitrarily divided variable records cannot be converted exactly using
`object difference × maximum record size`. Compression,
record mode, transfer size and the current streaming state affect the comparison; no reviewed source
establishes a universal byte cutoff.
[HP LTO-5 integration, pages 23–25](https://docs.oracle.com/cd/E21419_04/en/LTO5_Vol2_E3a/LTO5_Vol2_E3a.pdf#page=23),
[performance specifications, pages 39–41](https://docs.oracle.com/cd/E21419_04/en/LTO5_Vol4_E4/LTO5_Vol4_E4.pdf#page=39).
An ordering model must cost the access method execution actually chooses. Discarded gaps are not
verified selected content, and writable-capacity/EW admission rules do not decide read movement.
Measurement boundaries must also match: an Open-to-header-available observation already includes
the header READ. Treating it as seek-only time and then adding all target READ bytes counts that
READ twice. Separate settled LOCATE time from READ startup and transfer, or explicitly account
for the included work before composing a complete transition.
Likewise, a first READ's completion time includes its entire record transfer; it is not a standalone
startup constant. A continuous-read sample supplies conditional rate observations, not by itself
a reusable post-LOCATE overhead. Preserve command issue/completion times and host gaps when
testing that decomposition. A rate measured over elapsed time already includes those gaps.

Model inference: evaluate each next access as the cheaper eligible action, continued reading or
seek followed by read startup. Continued reading includes exact extra bytes, applicable transfer
rate and nonnegative startup/turn costs; retained bytes require no additional device READ.
Closely spaced selections can then favor continuous reading through the same cost comparison,
without a selected-file percentage or an independent gap-size cutoff. A crossover distance is a
consequence of measured costs, not a universal constant. Preserve the full logical-order route as
a candidate evaluated under the same action model; nearest-next greediness alone cannot establish
the best complete route.

This changes the model's inputs. The current `AccessCost(from, to)` receives logical positions,
not Tape-file identity or retained-record state. Arbitrarily divided variable records and PAX chains
would prevent recovering absolute member ends from object counts and payload size. That parser
capability is not itself a requirement to import external archives. The
[current writer](../../internal/media/tar_write.go) submits full records across members and only
emits a final short variable record after the normal trailer. A controlled EW stop discards the
unsubmitted buffer; an interrupted operation adds no completion record. APPEND opens another Tape file.

Under this writer's layout, paired physical anchors give the member end as
`headerOffset-headerInRecord+(endObject-headerObject)*R+endInRecord`, where `R` is the configured
record size, using checked arithmetic within one Tape file. The containing final record's exact
length remains unknown before reading, but pricing unread records at `R` overestimates each
same-Tape-file read leg by less than one record. That byte allowance does not cover positioning
or timing-model error. Cache eligibility can use the retained record's identity; it must not treat
an estimated length as actual buffered bytes. Runtime already owns the real record length.
These source findings support evaluating a model using existing locators, without making two new
persistent endpoint fields a prerequisite. Execution must use the same action policy with actual
settled state; no action-selection API is established by this research.

Dense whole-Tape selection can span multiple APPEND archives. The current writer terminates each
archive with one filemark, and the reader advances one logical object for each data record or
filemark. Given these qualified semantics, current Tape file `f` and host-next object `P`, a forward
target in Tape file `g` ending at exclusive record object `q` needs `g-f` filemark READs and
`q-P-(g-f)` data-record READs. A nominal-size estimate can then overestimate bytes by less than
`(g-f+1)*R`, since multiple archives can end in short records. Filemarks have zero payload but
nonzero command/transition time; their effective cost and error need observations distinct from
post-LOCATE overhead. Reuse actual boundary handling without an inventory scan. Keeping a
same-Tape-file restriction would force seeks between archives even for dense selection.

Order selection and read-action selection are distinct. Keeping LOGICAL file order can still
benefit from the same cost-based read-through decisions; reverting to unconditional LOCATE when a
reordered route is rejected would prevent that benefit. Existing retained-record reuse does not
need a timing-model switch.

An indexed candidate search needs a proof for the cost it actually minimizes. At a fixed applicable
transfer rate, forward read-through cost is monotone in the target's exclusive physical-record end.
Complete tar members within one Tape file are disjoint or duplicate the same stored member, so
their header order also orders those ends; the first eligible successor remains sufficient for
that branch under nonnegative cumulative startup/turn costs. Seek followed by a complete target
read is different: variable target lengths can make the nearest header more expensive than a
farther one. A retained record can serve an earlier header, but only its actual retained bytes
avoid device reads. Exact minimum selection under this richer cost needs an appropriate index
or a justified search bound. Keeping CERN's
access heuristic as a candidate generator and separately pricing complete routes is another
possible extension; it must not be described as exact greedy minimization of full read time.
Neither approach establishes globally optimal scheduling.

The positioning-score greedy baseline used in the original comparison can split a dense selection
into separate visits even when continuing through its small gaps would be cheaper. Comparing that
route with LOGICAL alone does not search a hybrid route. The current
[planner](../architecture/read-planning.md#software-read-ordering) instead improves directed continuous
runs from LOGICAL with complete action costs. This remains a bounded heuristic, with exact small
instances and mixed-group counterexamples for verification; it does not establish a global optimum.

For equivalent target-read work, a cost-derived continuation boundary satisfies
`gap bytes / read rate + boundary costs < LOCATE cost + post-LOCATE overhead`. LOCATE itself
depends on the gap and layout, so even this simplified boundary is not `rate * mean seek time`.
Full action quotes remain necessary when cache or final state differs. At the measured 144.18 MB/s,
the held-out 2.86-second mean absolute positioning error equals about 412 MB of sequential reading;
the 9.12-second maximum equals about 1.32 GB. These equivalents are not error bounds on a fitted
gap threshold. Near the crossover, validation must measure action-choice mistakes and their time
penalty, not only positioning RMS error.

A cheap forward link does not prove global adjacency: an incoming cursor already at the second
file can make a forced visit to the first file worse. Complete stateful quotes must include reading,
cache state and required completion; reversal is never free. The
[read planning contract](../architecture/read-planning.md#software-read-ordering) defines the implemented heuristic
and its planning budget; neither local improvement nor a separated estimate proves a global
optimum or physical speedup.

LTFS at `4c0e6faae661e0bfd50bc9707f59ecd88c90b7eb` reuses its last containing record when cached,
issues READ directly when the target is its current next record, and otherwise calls its seek path.
The studied Linux SG backend submits LOCATE(16); a forward gap is not automatically read and
discarded. The drive may satisfy a LOCATE through firmware/cache behavior, but command submission
does not establish which mechanical movement occurred. The studied implementation has no
cost-based small-gap discard choice.
LTFS must own any additional read-through operation; an application cannot obtain that benefit by
issuing competing raw SG commands while LTFS is mounted.
[LTFS raw reads](https://github.com/LinearTapeFileSystem/ltfs/blob/4c0e6faae661e0bfd50bc9707f59ecd88c90b7eb/src/libltfs/ltfs_fsops_raw.c#L630).
Its `ltfs.indexLocation` attribute reports the stored Index start, not the current head or logical
cursor. The studied xattr handlers expose block size, partition roles and medium identity, but no
live cursor or wrap geometry. They therefore cannot supply a mounted initial-position observation.
[LTFS xattrs](https://github.com/LinearTapeFileSystem/ltfs/blob/4c0e6faae661e0bfd50bc9707f59ecd88c90b7eb/src/libltfs/xattr.c#L709),
[IBM Storage Archive SDE 2.4.8 virtual attributes](https://www.ibm.com/docs/en/storage-archive-sde/2.4.8?topic=attributes-virtual-extended).

That limitation does not prevent application-level file ordering. YATM already freezes every LTFS
extent and has a complete-selection planner that accepts native-path sources. Its LTFS provider
forecasts each file's extents in logical file-offset order, generates a derived execution order and
submits those same native paths to ACP; LTFS performs ordinary reads. Neither a new ACP scheduler
nor an LTFS sorting implementation is required. The mounted `ltfs.volumeBlocksize` attribute supplies
block size, while `ltfs.partitionMap` supplies index/data roles, not the complete numerical physical
partition mapping or wrap layout. A geometry source must keep that distinction.

An unknown mounted cursor need not prevent comparing routes: retain the same first file in both,
cancel its common initial access/read cost, and compare the remaining routes from its forecast
final extent. This is an inferred planning state, not an observed head or cache; the existing
observed-state interface must not silently claim otherwise. Fragmented files retain all their
internal transitions. Cache or read-through actions specific to the raw tar reader cannot be
credited to ordinary LTFS reads without corresponding behavior. The current provider normalizes
extents, requires compatible partition geometry and fixes that common first file. Pre-mount paired
Labels can establish physical partition mapping; mounted role letters alone cannot. The
[read planning contract](../architecture/read-planning.md#software-read-ordering) defines those boundaries.

HPE's LTO-7 unload description specifies rewind, unthread and eject. A shared destination is not
evidence of equal travel time from different final physical positions. The studied LTFS version's
`ltfs_unmount` can also write an Index when dirty or when its current Index is not on the Index
partition; it skips that write for a clean Index already there. Consequently, a terminal estimate
must follow the actual adapter and reader policy, rather than treating every completion as an
identical LOCATE to block zero. These sources establish lifecycle behavior, not a measured terminal
cost function. Native tar now unloads after successful normal Archive, Restore and Scan completion,
and its planner includes a separate fixed-plus-physical-distance terminal estimate. Built-in values
borrow the provisional HP references above; they are not measured bounds. Successful ordinary UNLOAD
commands supply separate passive learning observations. The current LTFS read model assumes no Index rewrite and prices only the
direct rewind/unload from the final forecast position. That assumption matches reading a cartridge
kept read-only after writing; it is not inferred from a mounted dirty flag or a drive write-protect
observation. Writable or unknown mounts use the same assumption. Source-cache refresh may change
LTFS metadata, and configured adapters retain their own completion behavior; a resulting Index
rewrite is outside this estimate. Actual data-partition EOD remains a coverage boundary, and its
previous DP Index start is not the append/EOD address. No read of `ltfs.sync` is used, because
the studied GET handler itself syncs the Index. Source analysis and software tests establish these
comparison boundaries; the retained HPE 3.5 controls do not certify the assumption for every mount
or prove a physical benefit from the later implementation.
[HPE LTO-7 unload scenarios](https://support.hpe.com/hpesc/public/docDisplay?docId=sd00006907en_us&docLocale=en_US&page=GUID-108EFC4D-4940-4DC7-9FEB-DBE3C0A0BC86.html),
[LTFS unmount](https://github.com/LinearTapeFileSystem/ltfs/blob/4c0e6faae661e0bfd50bc9707f59ecd88c90b7eb/src/libltfs/ltfs.c#L1938-L2036).
[DP-first Index writing](https://github.com/LinearTapeFileSystem/ltfs/blob/4c0e6faae661e0bfd50bc9707f59ecd88c90b7eb/src/libltfs/ltfs.c#L2522-L2536),
[append-position handling](https://github.com/LinearTapeFileSystem/ltfs/blob/4c0e6faae661e0bfd50bc9707f59ecd88c90b7eb/src/libltfs/tape.c#L794-L865),
[Index pointers](https://github.com/LinearTapeFileSystem/ltfs/blob/4c0e6faae661e0bfd50bc9707f59ecd88c90b7eb/src/libltfs/xattr.c#L728-L731),
[sync GET](https://github.com/LinearTapeFileSystem/ltfs/blob/4c0e6faae661e0bfd50bc9707f59ecd88c90b7eb/src/libltfs/xattr.c#L883-L885).

Physical evaluation compares complete matched Restore selections using observed initial logical
positions and actual READ/LOCATE choices. The uncompressed measurement below includes gaps and wrap
crossings; compression differences and other devices remain unqualified. Applicable measured
transfer and seek/startup intervals supply the crossover; a guessed threshold is not acceptance
evidence.

Logical addresses remain authoritative seek inputs. Wrap direction, band membership, endpoint
conventions, full-wrap coverage and motion costs need separate evidence. A non-ejecting reload and
new reported-map observation do not establish physical freshness. A performance estimate must
preserve that limitation without substituting estimated coordinates for authoritative logical
addresses. Session ownership and invalidation must preserve the distinction between an observed
logical map and qualified physical geometry. A borrowed timing prior permits comparison but does not establish physical qualification. Complete routes
must account for uncertainty and actual source boundaries; nominal capacity and guessed parity
are insufficient. The [read planning contract](../architecture/read-planning.md#software-read-ordering)
defines the current parameter admission, scheduler and fallback behavior.

### Search Objectives and Established Optimization Methods

This review studies Helsgaun's 2009 LKH paper, Cook/Held/Helsgaun's LKH-AMZ work, Bertsimas/Sim
(2004), Ganesh/Maggs/Panigrahi (2020), and the optimization-benchmarking survey v2 (2020).
Sources were checked on 2026-10-23. These findings evaluate alternatives; they do not change the
[current admission policy](../architecture/read-planning.md#software-read-ordering).

**Sourced findings.** LKH limits proposed edges with small candidate sets and performs compound
exchanges. Its study also shows that deeper search and apparently better candidate coverage can
waste work on clustered inputs; larger neighbourhoods are not automatically better.
[Helsgaun, General k-opt submoves (2009)](https://www2.seas.gwu.edu/~simhaweb/champalg/tsp/papers/Helsgaun2.pdf).
LKH-AMZ uses directed 3/4-opt exchanges, preserves segment orientation and applies iterated local
search. Its point-to-point costs are static. The authors distribute that particular implementation
under MIT; this does not establish the licence of every LKH version.
[Author paper](https://www.math.uwaterloo.ca/~bico/papers/jptreport.pdf),
[implementation and licence](https://www.math.uwaterloo.ca/tsp/amz/code.html).

Robust optimization makes the uncertainty set and performance criterion explicit. Bertsimas/Sim
study controlling conservatism, including shared uncertainty factors affecting many coefficients.
They do not prescribe requiring every new route to dominate the previously accepted route.
[The Price of Robustness, especially section 5](https://www.mit.edu/~dbertsim/papers/melvyn/The-Price-Of-Robustness-OR52.pdf).
Minimax regret instead minimizes the worst difference from the optimal route for each scenario.
It adds computational difficulty; the cited TSP guarantees assume undirected nonnegative graphs,
so they do not transfer to YATM's directed stateful costs.
[Robust Algorithms for TSP and Steiner Tree (2020)](https://arxiv.org/abs/2005.08137).

Benchmark guidance distinguishes fixed-budget quality, time to a target quality, and the whole
quality-versus-time curve. It also distinguishes evaluation counts from elapsed time and requires
the actual parameter configuration to be verified.
[Benchmarking in Optimization, v2, sections 4–5](https://arxiv.org/html/2007.03488v2).
CERN's 2021 comparison similarly uses the same selected files for each ordering and measures
both positioning and reading, rather than only predicted seek scores.
[CTA investigation, slide 7](https://indico.cern.ch/event/876801/contributions/4196223/subcontributions/325951/attachments/2186748/3694990/CERNCTARAO-preGDB-9Feb.pdf).

**Application inferences and unresolved evidence.** Historical-route dominance is a conservative
engineering policy, not an established optimum criterion: an early accepted route can prevent a
later nominal improvement with mixed scenario costs. Explaining a rejection by that rule proves
implementation consistency, not that the tradeoff is desirable. A fixed risk objective or fixed
reference envelope would remove this dependence from admission, but its benefit must be compared
with the approved policy before replacement. Six one-factor stress checks also do not establish
protection against simultaneous coefficient errors or a statistical confidence level.

The comparison's quoted scope matters when choosing that objective. Common unquoted LTFS work
cancels from scenario-wise differences, but not from ratios to LOGICAL: adding the same time to
two routes changes their ratios and can change the worst-scenario ranking. A ratio therefore
describes the quoted comparison costs, not the complete Restore workflow. The fixed-reference
alternative `max_s(C_s(route) - C_s(LOGICAL))` compares the least saving in seconds and is invariant
to genuinely route-independent additive terms within each scenario. It is not minimax regret
against an unknown optimum. Route-dependent completion branches still require the complete
provider evaluation; they cannot be dropped as common work. Either objective needs explicit
review of its per-scenario tradeoffs before replacing production admission.

Restore planning is measured by the Go scenario benchmarks and quality tests in
[Performance Acceptance](../development/performance.md#performance-acceptance).
A static nominal optimum would not prove robust or physical optimality.

For further search changes, separately measure candidate generation, evaluator calls, allocation,
full replay and total planning. Compare time to reach the current route quality as well as quality
at the same budget, retaining the fixed development/acceptance inputs and adverse cases. Candidate
lists and bounded directed exchanges are supported techniques to investigate; geometry-only
neighbours, free reversal, and assumptions that a faster solver fixes an unsuitable objective are not.

### Uncompressed Full-Cartridge Measurement

A 2026-10-21 measurement of the normalized YATM development implementation used an HP Ultrium
LTO-5 drive with Z6ED firmware, compression and encryption off, and 256 KiB variable records.
Three Archive Jobs published 45,846 complete members, about 1.525 TB of payload. EW stopped the last
append and its incomplete tail remained unpublished. The observed map contained 80 wraps, including
a short complete interval and a partial final interval; neither was removed from the model.
This study measures the frozen experimental build's full-cartridge write and selected Restores,
not a full-cartridge readback or later optimizer revisions.

The seven-term fit used 32 directed positioning trials; 16 separate held-out trials were not
used for fitting. Training mean absolute/RMS errors were 1.53/1.79 seconds; held-out mean
absolute/RMS errors were 2.86/3.55 seconds, with maximum absolute error 9.12 seconds.
Four held-out errors exceeded the 3.31-second training residual allowance, and only one training
trial exercised step-back. Thus the model is an approximation and its supplied allowance is not
a guaranteed error bound. Post-hoc checks of the unchanged model on the first sparse routes
overpredicted LOGICAL's total LOCATE time by 5.68% and underpredicted AUTO's by 4.03%; these routes
do not independently identify all seven coefficients.

The worst held-out case moved backward four logical objects: native LOCATE took 0.072 seconds,
while the model predicted 9.194 seconds. Drive cache and prefetch were not controlled, so this
does not identify the physical cause. The fitted base term is 3.224 seconds, but setup indicators
and a linear distance term do not model acceleration, deceleration, current velocity or a separate
short-seek regime. They can absorb effects empirically without identifying mechanical constants.
The declared direction feature compares wrap orientations, not an observed transport velocity.
Native LOCATE timing excludes the subsequent READ; its startup and transfer costs are measured
separately. Aggregate route agreement therefore does not establish accurate short-gap decisions
or a confidence bound for choosing between nearly equal alternatives.

A chronological replay reserved the last eight Restore Jobs for comparison. Their 70 LOCATEs
have 3.507-second mean and 9.179-second maximum absolute error; the original allowance covers only
38 of them. First-READ mean/max errors are 2.359/7.768 milliseconds, separating this discrepancy
from READ startup. A diagnostic same-wrap segment within four records fits one earlier forward
two-record observation. It reduces later overall mean error to 3.147 seconds, but leaves the maximum
unchanged. Earlier same-wrap training distances have no observations between two and 16,491 records;
the breakpoint and short backward regime are therefore unidentified. The candidate is not adopted.
Normal passive observations at varied short distances and unused later Jobs are needed before
qualifying that additional model; a better narrow mean alone is insufficient.

Each matched selection ran LOGICAL, AUTO, AUTO, LOGICAL with frozen parameters and fresh
destinations. All 16 Restore Jobs and 2,028 repeated file content/metadata checks passed.
Elapsed times below run from Media admission through Job settlement, including preparation;
Job indexing and independent destination verification are outside that interval.

| Selection | Files | LOGICAL observations | Requested AUTO observations | Observed route |
| --- | ---: | ---: | ---: | --- |
| Sparse across wraps | 80 | 3,208 / 3,201 s | 1,042 / 975 s | Reordered; mean elapsed time fell 68.5%, native LOCATE time 70.1% |
| Adjacent members | 381 | 262 / 194 s | 193 / 193 s | Both AUTO requests retained LOGICAL; one LOCATE per arm |
| Wrap/band boundaries | 30 | 566 / 658 s | 314 / 329 s | Reordered; mean elapsed time fell 47.4%, native LOCATE time 59.7% |
| APPEND boundaries | 16 | 92 / 9 s | 9 / 9 s | Both AUTO requests retained LOGICAL; two LOCATE commands per arm |

The two fallback cases had an active model but no estimated improvement separated by its
uncertainty allowance. Their first LOGICAL runs had much longer label preparation; their elapsed
differences are not sorting gains. Boundary AUTO increased LOCATE count from 13 to 18 while
reducing total LOCATE time, demonstrating why command count alone is an unsuitable objective.
The adjacent case read one contiguous record span with retained-record reuse. A boundary LOGICAL
route also read through an approximately 12.5 MiB gap between selected members, while AUTO avoided
that segment. APPEND boundaries still used LOCATE; these trials do not validate sequential
filemark crossing. Runtime cache/discard counters were not captured, so gap and reuse evidence
comes from selected anchors, serialized native actions and the corresponding reader behavior.

Restore process RSS sampled every five seconds peaked at 56.2 MiB. Separate disk-backed planning
observations used about 37 MiB RSS for both 100,000 and 1,000,000 selections, but took 13.6 and
236 seconds with 105 MB and 1.059 GB temporary indexes. Cumulative allocation was 4.31 and
54.83 GB respectively. These single observations used different wrap occupancy; they do not
establish a general scaling law, and RSS excludes filesystem cache.

Final endpoint/selection identities depended on the post-write map and were frozen before their
timing trials, rather than before writing. No held-out refitting or observation removal was used.
Two repetitions on one cartridge do not control internal drive cache or prove physical LPOS,
layout assumptions, portable coefficients, compressed-media behavior or LTFS benefit. The observed
uncompressed benefit supports this implementation's tested use; broader cost-model, prospective
cartridge and LTFS qualification remain open. Passive read learning does not require user measurement.

## Fixed Half-Cartridge Selected Restore

A later HP LTO-5/Z6ED experiment used 512 KiB variable records with hardware compression and
hardware encryption off. A fixed prefix of the mixed-size 1.5 TB benchmark, plus its small boundary
members, wrote 1,444 files / 699,841,147,558 payload bytes through FORMAT and two APPENDs. The current
single-partition CM directory covered 37 wraps. One frozen selection contained 72 files /
23,946,906,636 bytes: 4.986% of written files and 3.422% of written bytes, touching 32 recorded wraps.

Each order ran once with fresh destinations, the same frozen model and logical locators, and a
fresh CM directory. REOWP was suppressed only by the test transport; CM acquisition and all Tape
reads were real. The generated execution indexes and normalized geometry were retained, and both
Restores passed inline and independent local content/metadata checks with normal unload.

| Measure | LOGICAL | AUTO |
| --- | ---: | ---: |
| Complete model estimate, including finish | 2,281.95 s | 890.37 s |
| Actual execution after preparation/planning | 2,521.35 s | 930.81 s |
| Media admission through settlement | 2,527.61 s | 936.10 s |
| Planning | 0 s | 1.17 s |
| Native LOCATE count / combined service time | 56 / 2,217.67 s | 58 / 673.03 s |
| Host-returned READ bytes, including framing and read-through | 31,300,523,008 | 29,343,880,192 |
| Independent local verification | 57.40 s | 61.50 s |

AUTO reduced this pair's Media elapsed time by 62.96%, despite making two more LOCATE calls. Its
shorter positioning work accounts for most of the difference. Complete execution exceeded the
model by 10.49% for LOGICAL and 4.54% for AUTO; observed READ command counts matched both forecasts.
Actual cache-hit and discarded-byte counters were not exposed. READ reservations and returned
bytes are not selected payload or a measurement of physical tape consumption. The two orders each
read the selection, so this is not a claim that total experiment I/O was 5% of the cartridge.
This one pair does not establish statistical confidence, global optimality or behavior on other
drives, compressed media or unwritten wraps. No model refit or repeated search selected a faster
reported result.

## Reading Position Through SG

SG is a command transport; the returned field determines what can be inferred. For the HP LTO-5
[READ POSITION contract, pages 148-150](https://docs.oracle.com/cd/E21419_04/en/LTO5_Vol3_E5b/LTO5_Vol3_E5b.pdf#page=148),
the position counts logical blocks and filemarks, not physical tape length or a percentage of the
partition consumed. The EOP bit indicates an existing EW-EOM condition; it is not a configurable
warning before that boundary. The old manual and the observed idle short/extended responses mark
buffer block/byte counts as unknown (`LOCU=1`, `BYCU=1`); zero-filled counters with these flags cannot
establish that the write buffer is empty. Idle observations do not establish every firmware's
behavior during writes.
Logical position alone cannot account for physical length spent rewriting the same content.

Reading page `0x31` through SG obtains the same underlying capacity source used by the pinned LTFS
LTO-5 backend. Bypassing LTFS does not improve that source's contract and omits its partition and
Index adjustments.

For the inspected Linux installation, `ltfs --version` reported 2.5.0.0 (Prelim) and the configured
Tape backend was `sg`. Inspection of its installed executable and libraries confirmed the relevant
call path and branches:

`statfs -> ltfs_capacity_data -> tape_get_capacity -> sg_remaining_capacity -> LOG SENSE 0x31`

The LTO-5 branch reads parameters 1/2 for remaining partition 0/1 capacity and 3/4 for their maxima.
After backend adjustments, partition mapping and unit conversion, ordinary data availability is
the remaining data-partition capacity minus half the total Index-partition capacity, clamped at
zero. Known `NO_SPACE` or `LESS_SPACE` states also force zero. The FUSE callback assigns that result
to both `f_bfree` and `f_bavail`. These binary checks establish the inspected functions' behavior,
not the complete build's source revision.

This availability calculation does not use page `0x0C`'s EOD/EW/current-position fields, READ
POSITION coordinates or CM EOD records, and does not separately debit the live contents of LTFS
or drive buffers. A direct page `0x31` observation therefore duplicates its drive-level input,
while page `0x0C` adds boundary and position information rather than another independently verified
remaining-capacity measurement.

The later [SSC-3 draft revision 03e, Section 8.2.2, Table 58](https://citeseerx.ist.psu.edu/document?doi=baaf4ada4b6d66dd63c05f08761157577597d52d&repid=rep1&type=pdf)
defines parameters 4 and 5 as current-partition recorded-EOD/EW distances in decimal megabytes,
with all-ones invalid values and no guarantee about writable bytes before EW. Revision 01c predates
these fields. The matching four-byte HP/IBM layout can supply an estimate without a generation
allowlist; parameter presence does not establish error/lag bounds or streaming-query suitability.

**Observed HP LTO-5 capability:** firmware Z6ED returned complete page `0x0C` responses containing
parameters `0x0004` through `0x0008`, each four bytes with control byte `0x60`. The
[2011 HP LTO-5 manual, page 60](https://docs.oracle.com/cd/E21419_04/en/LTO5_Vol3_E5b/LTO5_Vol3_E5b.pdf#page=60)
omits these fields; that omission does not establish their absence in later firmware. The
[2012 HP LTO-6 manual, pages 64-65](https://docs.oracle.com/cd/E38452_01/en/LTO6_Vol3_E1_D20/LTO6_Vol3_E1_D20.pdf#page=64)
defines the matching layout in megabytes. The observed relationships to page 31 support a
decimal-MB interpretation on Z6ED; the table alone is not a firmware-specific unit/update guarantee:

| Parameter | Meaning in the newer HP definition |
| --- | --- |
| `0x0004` | Approximate native capacity from partition beginning to recorded end of data |
| `0x0005` | Approximate native capacity from partition beginning to EW |
| `0x0006` | Minimum native capacity between EW and partition end |
| `0x0007` | Approximate native capacity from partition beginning to current medium position |
| `0x0008` | Maximum native capacity currently allowed in the device buffer; not its occupied bytes |

Read-only positioning between the two partitions changed these capacity fields consistently with
page `0x17`'s partition totals and recorded use. In both partitions, `0x0004` matched recorded use
even at logical block zero. This supports an EOD interpretation rather than a count of blocks
crossed by the current reader. In the data partition, page `0x31` remaining capacity exceeded
`0x0005 - 0x0004` by approximately the reported EW-to-end region. This supports distinguishing
the two boundaries. The pinned LTFS Index deduction was larger than that difference in the same
sample, so replacing its availability with the raw EW distance would not necessarily be safer.

A read-only comparison at confirmed data-partition logical blocks 0, 100,000 and recorded EOD
(197,013) returned `0x0007` values of 47, 26,263 and 68,738 MB respectively. Recorded EOD
(`0x0004`) remained 68,739 MB. Thus the firmware exposes a position estimate that responds to
positioning and distinguishes the current position from recorded EOD. This is native-capacity
distance, not a measured length in metres or a verified wrap/LPOS interface. An earlier observation
at logical block zero returned 22 MB. The nonzero, differing beginning-position observations leave
the relationship between logical position, physical head position and field refresh unresolved.

These positioning observations are read-only measurements of one firmware; the newer HP definition
is an interpretation supported by them, not an explicit HP5/Z6ED update guarantee. The
[continuous Archive measurements](tape-capacity.md#continuous-archive-measurements) add evidence of delayed capacity
updates during writes. The fields remain useful observations through the LTFS owner without forced
synchronization. Neither the position test nor the size of the EW-to-end region identifies the cause
of a previous capacity shortfall.

A physical-position field could help observe already-consumed tape length. A usable admission rule
would additionally need its partition/warning coordinates, freshness, outstanding-write accounting
and a bound on the next file's physical consumption. The observed `0x0C` fields provide a concrete
candidate for the warning-distance part of that work, while a complete admission guarantee remains
unestablished. The [HP-authored SSC proposal](https://www.t10.org/ftp/t10/document.06/06-235r0.pdf#page=2)
also qualifies its minimum EW-to-end capacity by media condition, block size and compression;
the reported buffer allowance alone is not a worst-case bound for an arbitrary next file.

READ POSITION itself causes no tape movement. Nevertheless, the
[HP integration guidance, page 24](https://docs.oracle.com/cd/E21419_04/en/LTO5_Vol2_E3a/LTO5_Vol2_E3a.pdf#page=24)
advises against interleaving READ POSITION or LOG SENSE with long writes. Any observation used during
Archive must remain coordinated through the LTFS device owner and demonstrate acceptable command
latency and streaming behavior. More frequent polling alone does not prove complete-file admission.

## Direct Physical Coordinates

A physical location needs both the wrap and the longitudinal position (LPOS); the same longitudinal
position occurs on successive passes across the Tape. The
[Ultrium-1 format, ECMA-319](https://ecma-international.org/wp-content/uploads/ECMA-319_1st_edition_june_2001.pdf)
describes servo-encoded longitudinal position and stored EOD coordinates. It establishes the
distinction between physical and logical position, not the later HP5 command or CM-page layout.

Some drives expose these coordinates to the host. The
[IBM SCSI reference, printed page 166](https://www.ibm.com/support/pages/system/files/inline-files/LTO%20SCSI%20Reference_GA32-0928-05%20%28EXTERNAL%29_0.pdf#page=196)
defines physical wrap and relative LPOS in vendor-specific REQUEST SENSE data. That is evidence of
an available IBM interface, not a portable SCSI or HP5 interface. The reviewed HP5 host guide does
not document the same pair. Its attribute `0x1000` contains LPOS at the fixed LP1 reference point,
which does not track current position.

An idle HP5/Z6ED hardware comparison tested these interfaces immediately after completed positioning
at the three data-partition locations above:

- Fixed-format REQUEST SENSE requested 255 bytes and returned 24 at each location. Its additional
  length was 16 bytes; it did not contain the later offsets used by IBM's wrap/LPOS fields.
- Descriptor-format REQUEST SENSE returned 16 bytes at each location: the stream-command and
  drive-error descriptors, without physical coordinates. Both sense replies were unchanged across
  the positions, including after SPACE to EOD.
- READ POSITION service action `0x01`, the legacy device-specific block form, returned ILLEGAL
  REQUEST / INVALID FIELD IN CDB at all three positions. The normal short, long and extended forms
  succeeded and reported logical positions.

These observations establish the tested interfaces' behavior on this firmware. They provide no
live wrap/LPOS pair to replace the native-capacity estimate.

HP5 also documents reading cartridge memory through
[READ BUFFER, buffer ID `0x10`, pages 139-141](https://docs.oracle.com/cd/E21419_04/en/LTO5_Vol3_E5b/LTO5_Vol3_E5b.pdf#page=139).
The observed drive returned an 8,160-byte buffer. Pages `0x104` and `0x10E` were version 2 and
96 bytes each, with the same bytes at all three positions. The stored EOD pages therefore did not
track the read-only head movement in this experiment. Their layout differs from the Ultrium-1
EOD page. An inspected
[community CM decoder](https://github.com/habibkaratas/hp-tape-tools/blob/3bb25b49de1864ed6392fab61f94d17cfa9d8388/lto-cm-decoder/lto_cm_decode.py)
applies older EOD offsets and labels a wrap-section field as a wrap number. Those interpretations
are not validated for HP5. Establish the versioned layout, partition mapping, validity and update
rules before treating CM bytes as coordinates. Stored EOD can locate the recorded tail; it is not
automatically live head telemetry or evidence that all submitted writes have reached that tail.

The production fallback is restricted to the studied LTO-5 directory version below; it does not
implement every generation's raw CM format. ECMA-319 Annex D.2.7.5 defines the
**Ultrium-1** directory: page `0x0103`, 96 half-wrap entries, record/filemark counts and explicit
invalidation/update rules. Its four-byte integrity check is the Reed-Solomon code defined in
section 13.2, not ordinary CRC32. The reviewed HP5 guide documents retrieval without specifying
its later directory layout. IBM's
[ITDT change log](https://www3.software.ibm.com/storage/ITDT2/ListOfChanges.html) records fixes for
missing LTO2-5 directory high bytes and partitioned cartridges; this warns against reusing the
LTO1 offsets without version-specific evidence.

A 2026-10-24 read-only HP LTO-5/Z6ED investigation obtained matching raw CM and REOWP from one
previously written, two-partition LTFS cartridge. The cartridge was physically present but
unloaded: TEST UNIT READY and READ MEDIA SERIAL NUMBER returned `3A/00`, while the CM descriptor
returned GOOD with zero capacity. A separately requested synchronous LOAD completed in 35.57 s;
thereafter the descriptor reported 8,160 bytes. Thus these unloaded responses alone cannot prove
physical cartridge absence or unsupported CM retrieval. No FORMAT, data WRITE, Tape payload READ,
LOCATE or unload was issued in this investigation.

Two CM reads took approximately 1.28 s each and returned identical 8,160-byte images. READ
POSITION before and after showed the same partition-zero, logical-object-zero position. Current
mode data reported density `0x58` and hardware compression disabled; MAM reported two partitions.
These are current observations, not a claim about every historical write to the cartridge.

The actual directory had version 4 and length 2,576: a 16-byte header followed by 80 32-byte units.
Its header and all 80 units passed the RS check (12+4 and 28+4 bytes respectively). Both version-2
EOD pages, `0x0104` and `0x010e`, were 96 bytes and passed their trailing RS checks. The protected
page table passed; the unprotected table passed when its leading `0x0ffc` padding descriptor was
excluded. Including that descriptor, as in the older table-coverage assumption, failed. This
observed coverage rule is not a blanket rule to skip arbitrary padding in every CM version.

For this sample, the sum of the four big-endian 32-bit words at unit offsets `+8`, `+12`, `+16`
and `+20`, accumulated within each partition, reproduced **all 23 REOWP endpoints exactly**.
Grouping the first two as record counts and the last two as filemark counts also reproduced the
respective 48-bit counts at EOD-page offsets `+12` and `+18`:

| Observed partition | First / last recorded wrap | Record-count candidate | Filemark-count candidate | Host logical EOD |
| --- | --- | ---: | ---: | ---: |
| 0 | 0 / 0 | 10 | 4 | 14 |
| 1 | 4 / 25 | 786,456 | 12 | 786,468 |

The bounded decoder takes CM plus a confirmed actual partition count. It finds maximal contiguous runs whose dataset
fields are neither `FFFFFFFF` nor `FFFFFFFE`. Each whole run must match one EOD page's terminal
wrap, write pass and dataset, with EOD validity `0001`. Pages `0x104` and `0x10e` correspond to the
first and second partitions in this studied form. A later single-partition tar FORMAT retained
the second page with validity zero: its presence is not evidence of an active partition. The
actual partition list selects active pages; retained inactive pages still require valid framing
and checksums, but supply no coordinates. It then checks record and filemark totals independently. This
derives the observed starts without using the start flag or hardcoded wrap numbers. It cannot
select a suffix merely because its counters happen to fit. Every valid run must be accounted for.

Under the accepted low-word interpretation, each extended count contributes a nonnegative multiple
of 2^32. Exact equality between each independently bounded run's low-word sum and its EOD total
therefore establishes zero extension contribution. It does not decode or name the packed `+24`
bits, and an unexplained wider total is rejected. Sums may exceed 2^32; no file-count threshold
substitutes for this check. Cumulative counts define exclusive object boundaries from object zero,
and the partial last wrap stops at recorded EOD. The paired HP5/Z6ED density-0x58 REOWP response
uses the same convention: the first full data-partition wrap contains 36,765 records plus filemarks,
which occupy zero-based identifiers 0 through 36,764. Its next boundary is 36,765, matching REOWP;
all 21 full-wrap endpoints and both EOD totals agree. Adding one would count an extra object.
This conclusion combines the accepted independent CM counts with HP5 logical-object numbering,
not raw equality alone. Other REOWP families retain their prior convention: HPE's
[LTO6 J38W release notes](https://support.hpe.com/connect/s/softwaredetails?language=en_US&softwareId=MTX_7b228de1989a4b66ac419acd74)
explicitly describe the last block of the first wrap. That statement is not a definition of HP5/Z6ED.

These rules combine the earlier public field definitions, community decoder and observed version-4/
version-2 correspondence. They were developed after inspecting the initial LTFS sample, so its 23
matching endpoints are consistency evidence. A subsequent single-partition tar experiment on the
same HP5/Z6ED configuration wrote approximately 674 GB, appended 256 MiB within its partial wrap,
and then appended approximately 25.7 GB across two wrap boundaries. The 35 initial endpoints and
37 final endpoints matched independently captured REOWP; the first 34 full boundaries remained
unchanged, and record/filemark totals matched each actual EOD. The production decoder consumed all
three captures using the independently reported partition count. This qualifies the observed
normal FORMAT/APPEND transitions on that configuration, not other drives or unseen update states.
An internally coherent stale directory and every other generation's flags/update rules remain
outside this qualification. Unsupported versions, interrupted-write states and inconsistent
coverage retain ordinary reads.
The later 4 KiB split-read check returned the same image in about 1.29 s with unchanged identity and
READ POSITION; it used the production two-second per-command timeout and added no Tape motion.

The [LTO-CM-Analyzer limitation](https://github.com/Kevin-Nakamoto/LTO-CM-Analyzer/blob/a976175dfbfeed62d913983aa5945f4c043a573b/README.md#limitations)
explicitly restricts its interpretation to LTO1. A contributor's
[LTO6-labelled report and original dumps](https://github.com/Kevin-Nakamoto/LTO-CM-Analyzer/issues/2)
have a version-5, 4,400-byte directory instead: 48 bytes of header and 136 32-byte units all pass
the same RS calculation. The generation label is not independently verified, and both supplied
8,160-byte images end before the table's declared final address. These samples support framing
observations, not another qualified device map or a reason to apply the HP5 layout to later media.

The restricted fallback uses this format-specific interpretation and rejects unexplained states.
READ BUFFER is still mediated by the drive; it does not bypass a drive lacking that command or
guarantee that every firmware exposes its raw CM. Other validity, partition and extended-counter
forms need their own evidence. Hardware-compressed interpolation and live head telemetry remain
separate questions.

For capacity control, relate the data partition's written tail to its writable EW boundary. Head
position by itself can change during reads, Index work and repositioning. Verified coordinates
would improve accounting for already-consumed medium, including length lost to previous rewrites;
conversion of the remaining region into a complete-file allowance still needs the bounds described
above. This separates a promising physical observation from an unproved remaining-byte guarantee.
