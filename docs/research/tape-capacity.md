# Tape Capacity and Safe Completion

Status: Research; evidence reviewed on 2026-10-24. Findings describe the studied counters,
streaming behavior and normal capacity boundaries, not guarantees for every drive/LTFS combination.
The [Tape contract](../architecture/tape.md#tape-capacity) owns current YATM behavior.

## Objective

Study what capacity counters and write observations can establish for whole-file admission and
controlled EW completion. Account for writes once, retain unknown capacity as unknown, and distinguish
complete published members from pending content. Ordinary write, cleanup and finalization failures
remain errors; partial files must not become successful Positions.

Normal completion and controlled EW require their own physical evidence. Restoring interrupted
Archive prefixes, repairing failed tails or guaranteeing remount after an arbitrary failure are
not product requirements of this study. A successful write or progress counter alone does not
establish physical completion.

The compatibility constraints are ordinary LTFS files, existing user-owned Tape adapters and the
[supported operating model](../architecture/overview.md#supported-operating-model). Replacing the
drive or LTFS distribution, introducing a segmented archive format, or choosing an arbitrary fixed
reserve does not meet this scope. Device-specific capabilities may improve an optional path only
when the existing adapters and other supported configurations retain a defined behavior.

Capacity management must preserve continuous data supply during normal Archive, following the
[Media constraint](../architecture/tape.md#tape-capacity). Periodically pausing submission,
draining buffers, synchronizing an Index and resuming is outside this scope, including per-batch
or increasingly frequent synchronization near the capacity boundary. Normal finalization when
ending use of the current Tape remains necessary.

## Scope and Evidence

The source analysis uses
[LTFS revision 4c0e6faae661e0bfd50bc9707f59ecd88c90b7eb](https://github.com/LinearTapeFileSystem/ltfs/tree/4c0e6faae661e0bfd50bc9707f59ecd88c90b7eb),
including its Linux SG backend and unified scheduler. A version string alone does not establish
that a deployed binary contains these paths. HP LTO-5 and IBM LTO manuals below describe their
own products and revisions; support and units must be checked before applying one vendor's fields
to another. Community observations retain their reported hardware and software scope.

For how YATM currently checks LTFS space, finalizes and reconciles the Index, see the [Tape contract](../architecture/tape.md#tape-capacity),
[Tape finalization](../../internal/executor/media_tape.go) and
[Archive result handling](../../internal/executor/archive/stream.go).

The file-start check only rejects a file when the observation already reports insufficient space.
It cannot detect an overestimate before the drive reports a failure during that file. A single large
output has no further file-start boundary; smaller outputs allow later observations, but still do
not prove that the next file fits. Final Index reconciliation determines which results can be recorded;
it does not prevent the capacity shortfall or make failed-tail cleanup succeed.

## Capacity, Buffering and Warning Boundaries

### Capacity Is Not One Interchangeable Number

The path under examination is:

`drive counters -> LTFS partition/reserve handling -> statfs -> ACP admission`

Advertised native capacity is nominal, not an exact count of available host bytes. IBM labels its
[drive capacity table](https://www.ibm.com/support/pages/tape-drive-model-characteristics-lto-and-3592-drives)
as nominal without compression. The HP manual below explicitly allows more data than its calculated
capacity, while tape defects and block packing can reduce usable capacity. A reported capacity,
advertised cartridge capacity, accepted WRITE bytes and published file payload have distinct meanings;
one must not be treated as an exact upper bound on another.

The [HP LTO-5 integration manual, page 14](https://docs.oracle.com/cd/E21419_04/en/LTO5_Vol2_E3a/LTO5_Vol2_E3a.pdf#page=14)
describes native user-data estimates in units of 1,048,576 bytes, without a predicted compression
multiplier. System areas and a retry allowance are already excluded. It describes conservative
systematic adjustments and random geometric error, but supplies no general maximum estimation error.
The [host-interface manual, page 79](https://docs.oracle.com/cd/E21419_04/en/LTO5_Vol3_E5b/LTO5_Vol3_E5b.pdf#page=79)
ties remaining-capacity updates to physically written data sets.

The [IBM LTO SCSI reference](https://www.ibm.com/support/pages/system/files/inline-files/LTO%20SCSI%20Reference_GA32-0928-05%20%28EXTERNAL%29_0.pdf)
(GA32-0928-05, printed pages 265, 306 and 321) makes a useful distinction:

| Field | Documented meaning |
| --- | --- |
| `0x31`, remaining partition capacity | Native capacity from end of data (EOD) to end of partition (EOP), in MiB; a usage gauge without a promise that those bytes can be written |
| `0x17:0x0204` | Approximate remaining native capacity to early warning (EW), in decimal MB; at most EOD-to-EW capacity, zero at or beyond EW, and explicitly unknown when all bits are set |
| `0x0C:0x0005 - 0x0004` | Approximate EOD-to-EW capacity, in decimal MB; the manual explicitly allows conditions that reduce the amount writable before EW |

These definitions motivate checking the actual warning boundary and supported fields. They do not
prove an upper bound on the next file's physical consumption. The units and meanings above are the
IBM contract, not an assumed replacement contract for HP firmware.

The pinned [SG capacity implementation](https://github.com/LinearTapeFileSystem/ltfs/blob/4c0e6faae661e0bfd50bc9707f59ecd88c90b7eb/src/tape_drivers/linux/sg/sg_tape.c#L3023)
uses page `0x31` for LTO-5 and HP LTO-6; other paths use page `0x17`, including its remaining-to-EW
parameter. The [LTFS adjustment](https://github.com/LinearTapeFileSystem/ltfs/blob/4c0e6faae661e0bfd50bc9707f59ecd88c90b7eb/src/libltfs/ltfs.c#L686)
maps partition roles, subtracts half the total Index-partition capacity from data-partition
availability, and clamps availability after warning states. Reading the raw drive value instead
would omit these adjustments. The compatibility case below explains why changing all drives to
page `0x17` is insufficient.

### Capacity Query Errors and Retries

At the pinned revision, [LTFS capacity collection](https://github.com/LinearTapeFileSystem/ltfs/blob/4c0e6faae661e0bfd50bc9707f59ecd88c90b7eb/src/libltfs/ltfs.c#L686-L751)
returns a failed query before updating its cached capacity. That path does not itself set the
data-write error flag. The cache is used when no device is attached, rather than as a successful
response to an attached device's timeout. Capacity and data commands also share serialization;
this does not establish the behavior of concurrent native queries and writes.

The [SG command helper](https://github.com/LinearTapeFileSystem/ltfs/blob/4c0e6faae661e0bfd50bc9707f59ecd88c90b7eb/src/tape_drivers/linux/sg/sg_scsi_tape.c#L193-L385)
classifies host/driver timeouts separately from busy responses and resets. A returned timeout on
this read-only command is not itself evidence of failed data WRITE. Selected driver suggestions
can trigger one native retry; there is no general two-second sample deadline or three-failure
capacity cutoff. The LTO timeout tables for
[HP](https://github.com/LinearTapeFileSystem/ltfs/blob/4c0e6faae661e0bfd50bc9707f59ecd88c90b7eb/src/tape_drivers/hp_tape.c#L86-L103),
[IBM](https://github.com/LinearTapeFileSystem/ltfs/blob/4c0e6faae661e0bfd50bc9707f59ecd88c90b7eb/src/tape_drivers/ibm_tape.c#L446-L463) and
[Quantum](https://github.com/LinearTapeFileSystem/ltfs/blob/4c0e6faae661e0bfd50bc9707f59ecd88c90b7eb/src/tape_drivers/quantum_tape.c#L106-L123)
specify sixty seconds for LOG SENSE; supported-command recommendations can supersede those tables.
These are implementation defaults, not universal device timing guarantees.

These findings support separating optional capacity samples from data-write failure and
native settlement. Keeping a debited estimate and retrying on a periodic cadence is YATM's
advisory accounting policy, not an LTFS promise about concurrent sampling or counter accuracy.
A later successful query cannot establish that a reset preserved earlier buffered writes.

### Capacity Query Concurrency

Linux SG supports multiple requests and separate opens have separate request state, but those
requests still use the same SCSI device queue. Removing a userspace lock does not establish
independent device execution or a bound on WRITE delay.
SG request allocation can also sleep when shared host request slots are exhausted, even on its
asynchronous path.
[Linux v6.12 SG request state](https://github.com/torvalds/linux/blob/v6.12/drivers/scsi/sg.c#L128-L164),
[shared queue and request allocation](https://github.com/torvalds/linux/blob/v6.12/drivers/scsi/sg.c#L1634-L1664).

The HP LTO-5 host reference describes ordered untagged execution and a separate FC tagged queue;
its ABORTED COMMAND examples include overlapping commands from one host. LOG SENSE checks deferred
errors, and a non-write command can report and clear an earlier write-behind error. A second Linux
descriptor therefore needs shared attempt error handling; it is not an independent SCSI initiator.
[HP EH890-90900 Volume 3, April 2011 edition 5, pages 14, 56, 189–190](https://docs.oracle.com/cd/E21419_04/en/LTO5_Vol3_E5b/LTO5_Vol3_E5b.pdf#page=14).
The integration guide also advises against interspersing position/log queries with long writes.
[HP software integration, page 24](https://docs.oracle.com/cd/E21419_04/en/LTO5_Vol2_E3a/LTO5_Vol2_E3a.pdf#page=24).

The same HP manual reports standard INQUIRY `BQue=0, CmdQue=1` for all drives; the untagged
ordering description alone does not establish that a SAS drive cannot queue commands.
[HP INQUIRY fields, pages 38–39](https://docs.oracle.com/cd/E21419_04/en/LTO5_Vol3_E5b/LTO5_Vol3_E5b.pdf#page=38).

A read-only 2026-10-19 sysfs observation on HP Ultrium 5/Z6ED with Debian Linux
`6.12.111+deb13-amd64` and `mpt3sas` reported device `queue_depth=128`, host `can_queue=6548`
and `cmd_per_lun=128`. Retained standard INQUIRY responses from an earlier serial-polling
comparison on this drive reported `BQue=0, CmdQue=1`. Upstream mpt3sas caps an untagged device's depth at one;
these observations support the configured tagged-capability path, without proving parallel
firmware execution or exact equivalence to Debian's patched driver. Host limits describe
controller resources, not the drive's query latency.
[mpt3sas v6.12.111 queue adjustment](https://github.com/gregkh/linux/blob/v6.12.111/drivers/scsi/mpt3sas/mpt3sas_scsih.c#L1676-L1723),
[Linux v6.12 INQUIRY tag discovery](https://github.com/torvalds/linux/blob/v6.12/drivers/scsi/scsi_scan.c#L916-L920).

These findings distinguish background execution, absence of a shared userspace I/O lock and physical
noninterference. The latter requires applicable driver/device evidence. Under concurrent querying,
a before-WRITE submission counter also cannot prove that the WRITE settled before the sample;
capacity debit ordering must account for overlapping writes. Neither descriptor isolation nor a
small whole-run throughput difference resolves counter lag, native costs or whole-member fit.

### Estimate Accuracy and Shared Inputs

The HP integration manual above describes remaining capacity as calculated from EOD, expressed as
uncompressed user-data capacity with system regions and a read-after-write retry allowance already
excluded. It identifies tape-length and hub-diameter tolerances as error sources. It does not publish
the Z6ED firmware's calculation or a general numeric error bound. The newer HP `0x0C` definitions
describe approximate native-capacity distances; they do not establish a separate, more accurate
measurement than page `0x31`.

In the observed data partition, `0x0C:4` equaled `0x17:0x0203` recorded use. Subtracting recorded
use from `0x17:0x0202` total capacity also matched the MiB-to-MB conversion of page `0x31` remaining
capacity within its reporting granularity. These agreements support consistent boundary meanings
and units. They are not independent accuracy checks: the drive may expose shared estimates through
several pages. Likewise, a changing position value after LOCATE/SPACE demonstrates responsiveness,
not its absolute accuracy or freshness during buffered writes.

The same idle sample provides a useful numeric comparison, in decimal GB: page `0x31` reported
1,395.627 remaining; `0x0C:5 - 0x0C:4` gave 1,383.911 to EW; applying the inspected LTFS algorithm
to that page `0x31` sample gave 1,376.360 after a 19.268 Index allowance. The last value is a
calculation for normal operation without an extra backend offset or warning state, not a concurrent
mounted `statfs` measurement. Replacing it with the raw EOD-to-EW estimate would increase admission
by about 7.551 GB in this sample. Boundary alignment alone therefore does not establish a safer
replacement or a ranking of estimation accuracy.

For append admission, `0x0C:5 - 0x0C:4` is the relevant candidate observation of the recorded tail's
distance to EW. Subtracting the current-head field `0x0C:7` instead can include earlier recorded
regions when the head has moved for reading or Index access. Neither calculation has a demonstrated
complete-file guarantee. A comparison intended to justify changing admission must show its behavior
as Archive approaches EW, with timestamped pending-write and error evidence obtained through the
LTFS owner while preserving continuous data supply. Far-from-full idle samples establish field
support and relationships, not that a positive estimate will remain writable near exhaustion.

The investigated `df` path obtains a drive estimate through LTFS and then applies filesystem
adjustments. Reading a different SG page exposes the EW boundary and useful cross-checks but does
not by itself remove underlying estimation error, pending-write lag or the uncertain cost of later
rewrites. The [single-file prefill measurement](#single-file-prefill-measurement) records a failed
prediction from one starting estimate; the physical causes of its difference remain unattributed.

Keep two questions separate: how accurately the drive locates already-consumed medium, and how much
additional physical medium the next complete file will need. A direct physical coordinate could
improve the first. Complete-file admission also needs partition/EW coordinates, pending-write
accounting and an applicable bound for the second, including metadata needed to finish normally.

### Single-File Prefill Measurement

An earlier full-capacity prefill read filesystem availability once, obtaining 1,445,099,208,704
bytes. The script subtracted 8 GiB and streamed one file with a planned length of
1,436,509,274,112 bytes. Its next capacity check was after successful stream completion;
there was no capacity-driven stop during that single output. This fixture was written directly,
before the ordinary boundary Archive Job was created.

The stream failed. The retained snapshot reported a file length of 1,421,561,823,232 bytes and
filesystem availability of zero. The often-described approximately 15 GB shortfall is exactly
14,947,450,880 bytes between the planned and observed file lengths. It is not a measurement of
positive filesystem availability at the failure. The difference from the initial availability
was 23,537,385,472 bytes, including the planned headroom. Observed file length is not proof of
physically durable content.

This establishes a failed assumption in the experiment: one starting estimate was treated as a
budget sufficient for a very large complete file. The retained data does not provide an aligned
drive-capacity/position timeline to attribute the physical consumption difference. The later
4-GiB-file experiments establish their own observation and admission boundaries; they do not
validate that earlier single-file budget or a general whole-file fit guarantee.

### Continuous Archive Measurements

A 2026-10-09 experiment used HP LTO-5 firmware Z6ED, Linux SG LTFS 2.5.0.0 (Prelim),
512-KiB LTFS blocks and enabled drive compression. SET CAPACITY followed by FORMAT with
`--keep-capacity` shortened the scratch cartridge. Ordinary YATM Archive supplied twenty 4-GiB
incompressible files, three 512-MiB files and a final 4-GiB file. The
[maintained procedure](../release/physical-measurements.md#continuous-boundary-capture) records
application progress, raw device pages, filesystem availability, file metadata and errors on the
same host clock, without recurring synchronization or a separate SG controller.

The run collected 585 usable mounted observations. Device sampling was scheduled every second;
actual requests took a median 0.127 seconds and up to 12.269 seconds. Raw query bounds and gaps
remain in the curves. The following values are decimal GB from the starting and pre-unmount
snapshots, not three independently guaranteed capacities:

| Observation | Before copying | Before normal unmount |
| --- | ---: | ---: |
| LTFS available space | 87.744 | 1.361 |
| Page 31 data-partition remaining space | 88.914 | 2.530 |
| Page 0C recorded-EOD distance to EW | 88.201 | 1.818 |

Twenty-three files, totaling 87,509,958,656 bytes, were finalized and published as a continuous
prefix. The final 4-GiB file was refused before output creation: ACP's error recorded
`want=4294967296 have=2029518848`. It remained pending and absent from the final Index and
Library Positions. Normal unmount and eject succeeded. A subsequent ordinary Restore read all
23 published files; their restored sizes and SHA-256 hashes matched the source manifest, including
independent checks of the output files. No hardware EW or actual write-space failure was reported
in this attempt; it exercised pre-write refusal rather than failed-tail cleanup.

The recorded timing establishes a specific admission gap. Following the refusal, LTFS availability
fell from the reported 2.030 GB to 1.361 GB while the preceding writes settled. In the final
observed portion, both page 0C byte counters were unchanged while its EOD advanced and LTFS
availability fell by another 190,840,832 bytes over about two seconds. Page 02 processed-data-set
and corrected-rewrite counters also increased during that interval. Thus stable host-side byte
counters do not establish that remaining capacity has caught up. This observation does not isolate
each byte of the decline between buffering, counter refresh and rewrites.

Page 31 and the page 0C EW distance moved together; LTFS remained lower after its Index allowance.
Replacing LTFS availability with either raw reading would have admitted more data in this run.
Page 17's data-partition used-capacity descriptor returned all ones after writing began, so it
provided no usable live alternative. Preserve that response as unknown instead of subtracting it
from the partition total. The run ended with 3,050 CCQ rewritten data sets, zero uncorrected data
sets and no retained TapeAlert flags; these counters do not attribute a capacity cost or prove wear.

Job byte progress reached the complete selected work size, including the rejected file. It must not
be used as actual Tape output or subtracted from drive bytes to claim buffer occupancy. Item results,
the final Index and content readback determine completed content. The measured admission gap makes
outstanding-write accounting a concrete research target. A shortened cartridge's successful refusal
does not establish a full-length error bound or demonstrate that every admitted file will finish.

### Full-Capacity EW Measurements

A second 2026-10-09 experiment used the same HP LTO-5/Z6ED and LTFS configuration at full
cartridge capacity. The [EW procedure](../release/physical-measurements.md#continue-to-the-drives-ew-boundary)
streamed incompressible data into ordinary 4-GiB files until LTFS refused further output.
It deliberately bypassed application admission to reach the hardware warning. This was a
drive/LTFS experiment, not an Archive/Restore acceptance or a production change. There were no
recurring forced synchronizations; normal unmount finalized the run once.

The capture contains 2,838,033 SG trace records, 8,255 mounted snapshots and 1,385,800 application
events. Every evidence file was checksum-verified after retrieval. The
[maintained analyzer](../../e2e/physical_ew_analysis.py) produced the sanitized
[full aligned curves](tape-capacity-ew-curves.png) and
[EW boundary enlargement](tape-capacity-ew-boundary.png). The orange line marks the first actual
WRITE EW return. Dots and horizontal query intervals preserve sampling gaps; no interpolation
fills them. There were no malformed/truncated records or injected-query errors. The last capacity
query before EW finished about 6.9 seconds earlier; a one-second schedule did not guarantee a
one-second response while the LTFS owner was busy.

SG boundary ordering uses the owner's monotonic clock. Cross-file observations and plots use
recorded wall-clock intervals for comparison; they do not establish synchronized clocks or an
atomic snapshot. No clock discontinuity was detected in this capture. The analyzer labels this
distinction and suppresses wall-clock alignment when it detects a discontinuity.

**Actual warning and its consequences.** A data-partition WRITE returned current sense
`NO SENSE`, `EOM=1`, `ASC/ASCQ=00/02`, valid information residue zero and SG transfer residue zero.
This is the [documented successful-command EW response](https://docs.oracle.com/cd/E21419_04/en/LTO5_Vol3_E5b/LTO5_Vol3_E5b.pdf#page=225),
not hard EOM. LTFS then failed the application's Close with `ENOSPC`. A subsequent attempt to
delete the temporary file also returned `ENOSPC`. No hard-EOM sense or unrelated command error
was observed during payload writing. Three further EW responses occurred during final Index
writing; mount/unmount status responses are retained separately from payload failures.

The following values use decimal GB. Queries have separate intervals, so each column describes
nearby observations rather than an atomic snapshot:

| Observation | Last before WRITE EW, about -6.9 s | First after EW, +14 to +55 ms | Settled before unmount, about +2.1 s |
| --- | ---: | ---: | ---: |
| LTFS available (`statfs` / `df`) | 0 | 0 | 0 |
| Page 31, data-partition remaining | 13.561 | 12.559 | 12.367 |
| Page 0C, EOD-to-EW estimate | 1.845 | 0.843 | 0.651 |
| Short READ POSITION EOP | 0 | 1 | 1 |

LTFS availability first reached zero **46.9 seconds before WRITE EW**, after the preceding
sample reported only 24,117,248 bytes. Its Index allowance was 19,267,584,000 bytes; the inspected
implementation clamps the subtraction at zero even before a hardware warning. The failed final
file began about 29 seconds before EW, when availability was already zero. Thus this experiment
did not reproduce a positive `df` reading at EW: ordinary fresh file-start admission would have
refused that output. The deliberate bypass was necessary to observe the later warning and cleanup.

Page 31 remained positive at EW because its endpoint differs. After settling, its 12.367 GB
approximately equals page 0C's **11.715 GB minimum EW-to-end region plus 0.651 GB estimated
distance to EW**. This accounts for the relationship between the counters in this run, without
assigning a cause to a different run's shortfall. Of the raw capacity readings, page 0C was
numerically closest to the observed warning boundary, but it still reported positive space after
the drive had warned. Between the first post-EW and settled samples its estimate fell another
192 MB while both byte counters stayed unchanged. That remainder is not demonstrated writable
space, an exact error bound or a new fixed reserve.

READ POSITION supplied the clearest corroborating **already-at-boundary** flag: its first
post-WRITE-EW query started 18 ms later and returned EOP=1. The logical first-block value moved
from 2,766,782 before EW to 2,768,683 after it. Ordinary long-format queries during finalization
also returned EOP=1. Short first/last locations differed, while LOCU/BYCU kept buffer counts
unknown throughout; their difference is not a valid pending-byte measurement. Position therefore
confirmed the warning, but supplied neither an advance warning nor physical remaining length.

**Final files and Index.** All 337 successful Close/rename results matched complete 4-GiB entries
and contiguous, nonoverlapping extents in the final Index, totaling 1,447,403,978,752 bytes.
All writes of the 338th file had returned successfully, but its failed Close left a temporary
entry of only 4,179,099,648 bytes. The accepted-to-indexed difference was 115,867,648 bytes
(110.5 MiB). This is a concrete counterexample to treating successful Write returns as a complete
file, and confirms that cleanup can fail at EW. Normal finalization wrote generation 2 to both
partitions, and unmount/eject completed. Metadata reconciliation is not content readback; these
337 files were not reread in this experiment.

For this configuration, `df` provided the earliest conservative stop, page 0C best described the
approximate EW distance, and READ POSITION confirmed the boundary after it was reached. Replacing
`df` with raw page 31 or 0C would have allowed more writing, not made admission safer. Further
admission work should preserve LTFS's allowance and address pending work and complete-file cost;
these measurements do not establish portable bounds for either. They do establish the actual
failed-Close/failed-cleanup path that finalization and result publication must handle.

### Full-Capacity YATM Admission and Restore

A 2026-10-10–11 retest used ordinary YATM Archive and Restore with ACP v0.2.5. The HP LTO-5/Z6ED,
LTFS 2.5.0.0 (Prelim), 512-KiB blocks and enabled drive compression were unchanged. The scratch
cartridge used its full capacity. Mount options retained `sync_type=unmount` and the existing
buffer pool; no recurring forced synchronization was added. The inputs were 100 ordinary
incompressible files: twenty 64-GiB files followed by eighty 1-GiB files. The
[maintained capacity acceptance](../release/physical-measurements.md#continuous-boundary-capture)
recorded application results, file syscalls, LTFS capacity and same-owner SG observations.

**The earlier failed-tail outcome did not recur in this configuration.** Archive completed
82 files, totaling 1,440,961,527,808 bytes. The next 1-GiB file was refused with
`want=1073741824 have=1002438656`: 1,024 MiB required versus 956 MiB available. Its source had
been opened for prefetch, but its Tape output was never created. Syscalls show exactly 82
temporary outputs, successful closes and successful renames, with no unmatched temporary
output. The final Index contains those 82 files and the setup file; the other 18 selected files
remain pending without published Positions. Normal Index finalization, unmount and eject
completed. A subsequent ordinary Restore recovered all 82 published files; independent size,
SHA-256 and ACP signature-attribute checks passed for every restored file.

ACP's actual `statfs` call completed at 20:20:08.540 (+08:00), returning 1,912 available
512-KiB blocks, exactly the refusal's `have` value. The Job reported refusal at 20:20:09.
The preceding output had already started; its final rename completed at 20:20:20.825.
The next `statfs` call returned zero at 20:20:20.820. These timestamps distinguish stopping
new outputs from allowing already-admitted work to finish.

The following snapshots use decimal GB. The before-unmount hook ran after the final rename;
its observations have separate request intervals rather than an atomic device snapshot.

| Observation | Before copying | Before normal unmount |
| --- | ---: | ---: |
| LTFS available space | 1,445.099 | 0 |
| Page 31 data-partition remaining space | 1,464.367 | 19.172 |
| Page 0C recorded-EOD distance to EW | 1,452.650 | 7.457 |

No WRITE or WRITE FILEMARKS returned hardware EW or hard EOM. Observed short and long
READ POSITION EOP flags stayed clear. Zero filesystem availability was therefore an LTFS
admission boundary, not proof that hardware EW had occurred. Page 31 and page 0C still
reported positive values after LTFS stopped admitting data; replacing LTFS availability
with either raw reading would not have produced an earlier stop here.

The checksum-verified capture includes 2,833,272 Archive and 2,835,165 Restore SG records,
with no malformed/truncated records or injected-query errors. Archive yielded 9,776 usable
capacity observations. Sampling was scheduled every second, but requests took a median
0.149 seconds and up to 14.325 seconds. The first zero-availability observation itself spanned
10.288 seconds. The maintained [capacity analyzer](../../e2e/physical_capacity_analysis.py)
and [SG analyzer](../../e2e/physical_ew_analysis.py) produced the
[aligned Archive curves](tape-capacity-yatm-curves.png) and
[file-refusal enlargement](tape-capacity-yatm-boundary.png), preserving query intervals and gaps.

This result validates pre-output refusal, normal finalization and complete content readback for
this file mix. It does not test recovery after a failed write: the drive warning was never reached.
ACP refreshes capacity asynchronously every five seconds, permits an unknown initial observation,
and cannot infer all outstanding physical consumption from `statfs`. Thus the successful refusal
does not establish a guarantee for an arbitrary next file or every drive. It supports retaining
LTFS's capacity adjustment and the tested ACP integration; it supplies no reason to add periodic
copy pauses or replace the capacity source with a more optimistic raw counter.

### Full-Cartridge Tar Archive Observation

A 2026-10-21 YATM tar Archive run used HP LTO-5 firmware Z6ED, disabled drive compression,
256-KiB variable records and the production five-second capacity observer. This raw-Tape path
does not use LTFS filesystem availability or its Index allowance. Its native write journal
distinguishes full WRITE acknowledgements from application progress and complete published members.

The final APPEND reached current `NO SENSE` / `EOM` / `00/02` EW with confirmed full-record
acceptance. No further data WRITE followed. A non-immediate count-one filemark also reported
confirmed completion at EW; physical finalization and cleanup succeeded. The attempt published
45,592 complete members totaling 1,524,705,599,488 payload bytes. The admitted final 4,048-MiB
member remained incomplete by 389,658,112 payload bytes and was not published. This exercises
the [tar writer's completion boundary](../../internal/media/tar_write.go), rather than assuming
that every acknowledged record belongs to a complete member.

The last successful page `0x0C:5 - 0x0C:4` query reported 1,754,000,000 bytes remaining;
645,922,816 bytes were acknowledged from its issue through the EW record about 4.5 seconds
later. The resulting 1,108,077,184-byte discrepancy is evidence against treating that estimate
as a whole-member fit guarantee. The [cached accounting](../../internal/media/tar_capacity.go)
charged the member's complete encoded cost before its first header and debited reservations beyond
the full-return prefix captured at query issue. No missing member debit was demonstrated; even
retaining only the preparation estimate and cumulative encoded debits would have admitted it.
Full WRITE returns do not prove that the drive's remaining-space counter already reflects them.
The capture cannot apportion the discrepancy among unreflected buffered bytes, estimation error
and physical overhead.

The 49 near-end queries spanning 240 seconds completed in 1.974–12.520 ms, without a
failed query. These native request timings do not bound shared-device interference or physical
stop/reposition activity. This one drive/firmware/workload observation establishes neither a
portable capacity error bound nor complete-member readability after reload. Publication and
physical finalization remain separate from content readback and whole-file admission guarantees.

### Close and Explicit Synchronization

There are three boundaries: data accepted by LTFS, data accepted by the drive, and data physically
written. The configured LTFS memory pool and the LTFS format block size describe different things;
neither is a universal reserve for the next file. A `statfs` query does not itself drain either buffer.

In the pinned implementation, file flush/Close drains the unified scheduler's pending writes, but
does not by itself establish a device-wide physical synchronization. The explicit
[`user.ltfs.sync` path](https://github.com/LinearTapeFileSystem/ltfs/blob/4c0e6faae661e0bfd50bc9707f59ecd88c90b7eb/src/libltfs/ltfs_fsops.c#L1071)
drains the scheduler and requests Index synchronization. A dirty Index reaches a
[non-immediate trailing filemark](https://github.com/LinearTapeFileSystem/ltfs/blob/4c0e6faae661e0bfd50bc9707f59ecd88c90b7eb/src/libltfs/xml_writer_libltfs.c#L1336),
providing a stronger completion boundary. A clean Index can make
[`ltfs_sync_index`](https://github.com/LinearTapeFileSystem/ltfs/blob/4c0e6faae661e0bfd50bc9707f59ecd88c90b7eb/src/libltfs/ltfs.c#L3647)
return without that write; it also rejects an established `NO_SPACE` state.

Inference: a checked synchronization may remove some observation lag, but does not turn an
estimated capacity into a reservation. Its usefulness as an observation boundary does not make
recurring stop/synchronize/resume an acceptable capacity policy under the streaming constraint.

### Streaming and Mechanical Wear

[HPE's Data Rate Matching description](https://www.hpe.com/us/en/collaterals/collateral.c04154359.html)
links fewer rewinds and repositions to reduced mechanical wear and improved reliability. Repeated
interruptions that cause these movements therefore raise a wear and premature-failure concern;
the source does not quantify a lifetime reduction for a particular workload. Buffering and speed
matching mean an application pause does not always produce a physical stop.

The design constraint is to sustain data supply without introducing recurring forced drains for
capacity decisions or recovery checkpoints. Average throughput alone cannot establish that a change
avoids additional stop/reposition cycles, and a short run on one drive cannot establish lifetime risk.

### Compression and Rewrites

The next file can compress differently from everything previously written. Admission must use
an applicable worst-case physical-consumption bound, including formatting and compression behavior,
rather than extrapolating the last file's compression ratio. The
[HP compression specification](https://docs.oracle.com/cd/E21419_04/en/LTO5_Vol4_E4/LTO5_Vol4_E4.pdf#page=39)
describes adaptive pass-through behavior; this alone does not quantify every physical overhead.

Read-while-write verification can cause another copy of a data set or section to be written. HP's
[integration manual, page 20](https://docs.oracle.com/cd/E21419_04/en/LTO5_Vol2_E3a/LTO5_Vol2_E3a.pdf#page=20)
identifies degraded media, dirty heads, drive faults and operating conditions as causes of increased
retries. Quantum's [LTO technology brief](https://www.quantum.com/globalassets/documents/lto-tech-brief.pdf)
describes capacity consumed by rewritten sections and changes to that overhead in later LTO formats.
Those format comparisons do not bound loss on an arbitrary worn drive or cartridge.

This establishes a mechanism by which tape length is consumed faster for the same logical content,
even when writes eventually succeed. It does not identify wear as the cause of every capacity loss.
Establish this general behavior from specifications and the write/rewrite mechanism; an experiment
on one drive and cartridge of unknown condition cannot prove a wear curve or a universal bound.

### EW Does Not Reserve an Arbitrary Whole File

The [HP integration manual, page 40](https://docs.oracle.com/cd/E21419_04/en/LTO5_Vol2_E3a/LTO5_Vol2_E3a.pdf#page=40)
describes drive-managed space between EW and physical EOT. That does not establish capacity for an arbitrarily
large remainder of an application file.

The pinned [LTFS tape layer](https://github.com/LinearTapeFileSystem/ltfs/blob/4c0e6faae661e0bfd50bc9707f59ecd88c90b7eb/src/libltfs/tape.c#L1206)
blocks ordinary data writes after its EW/programmable-EW warning states. Some Index writes have
different allowances. Consequently, successful Index writing after EW does not establish that a
partial data file can finish, be renamed or be removed. Cleanup requires a writable namespace;
deleting an LTFS file does not in general reclaim the physical length already consumed.

Programmable early warning (PEW) is useful only if the device supports the required configuration
and LTFS exposes a usable interval for stopping and finishing work. Merely moving LTFS's refusal
point earlier does not solve the incomplete-file problem.

## Community Evidence

These are original reports and follow-ups, not controlled cross-device experiments. Their diagnostic
value is in observed failure modes and actual resolutions, with the limits below.

| Source and scope | Observation and resolution | Consequence for this research |
| --- | --- | --- |
| [HPE LTO-5 diagnostics, February 2022](https://community.hpe.com/t5/hpe-storeever-tape-storage/lto5-read-warning-insufficient-margin/td-p/7160723) | Published diagnostics show different losses on alternating wraps, including 15.47% versus 0.78% on one pair. HPE participant Curtis Ballard suggested cleaning the affected heads. No follow-up proves recovery. | Supports studying write/rewrite behavior and drive condition. A wrap percentage is not a whole-cartridge reserve. |
| [Bareos capacity loss, August-November 2018](https://groups.google.com/g/bareos-users/c/6nRtBu6rRXc) | HP Ultrium 920 LTO-3 application writes ended early despite successful synthetic fills. The reporter later wrote about 407 GB to four cartridges using an HP 1760 LTO-4 drive; catalog verification passed, Restore was not yet reported. | Drive/path/workload changes matter. This does not isolate worn heads, and a synthetic fill is insufficient application acceptance. |
| [HP LTO-5 capacity report, May 2024](https://www.reddit.com/r/DataHoarder/comments/1czsqvv/lto_tape_capacity_do_tapes_lose_capacity_with_use/) | A `tar`/`mbuffer` user reported roughly 1.33-1.42 TB per nominal 1.5 TB cartridge and supplied drive counters. The thread has no established root cause or demonstrated fix. | The counters expose separate EOD/EW quantities worth checking. This is a raw-Tape workload, not evidence of an LTFS defect or an aging formula. |
| [LTFS issue 582, April 2026](https://github.com/LinearTapeFileSystem/ltfs/issues/582#issuecomment-4283090092) | LTO-9 / LTFS 2.4.5.1 reporter recovered access by returning to an older Index. A contributor identified an unexpected generation with zero files and was still investigating. | Repeated mount/unmount and completed-file readability need acceptance. An Index-write success alone is insufficient; the report does not prove that EW itself caused the loss. |
| [LTFS issue 450](https://github.com/LinearTapeFileSystem/ltfs/issues/450#issuecomment-2097480390) and [fix 248](https://github.com/LinearTapeFileSystem/ltfs/pull/248) | An HP LTO-6 capacity-query failure was referred to an existing fix selecting page `0x31` instead of `0x17`. The issue was closed for inactivity. The pinned source contains that selection. | Counter availability depends on drive and LTFS branch. This fixes obtaining a value, not its suitability as a write guarantee. |

An [L&TT explanation in the HPE community](https://community.hpe.com/t5/hpe-storeever-tape-storage/interpreting-library-tools-reports/td-p/3840111)
also connects capacity loss to rewritten data and distinguishes normalized diagnostic health margins
from raw error counts. A health score is not available space. Community advice to clean or replace
hardware can diagnose a case, but does not supply a portable YATM capacity algorithm.

## Evaluating Possible Approaches

Whole-file admission requires both a usable-space lower bound and a bound on everything consumed
before safe finalization: the new file, pending writes, required metadata/Index updates and applicable
rewrite overhead. All quantities must refer to the same partition, warning boundary and completion
state. An allowance already included by the drive or LTFS must be accounted for once.

The accounting below remains advisory wherever its physical bounds are unknown. Public LTFS
finalization and Index findings are separate from YATM's ordinary operation contract.

### Initial Capacity and Whole-File Accounting

This approach would obtain a valid, LTFS-adjusted capacity observation before the first Archive output,
with no earlier unaccounted writes. Charge each file's entire bounded physical cost before creating
its output. Keep that charge after Write and Close, and provide for finalization of every admitted
prefix. When the next file exceeds the remaining budget, end this Tape operation and finalize once.

The condition is `admitted-file costs + next-file cost + finalization cost <= initial usable-space lower bound`,
applied to each affected partition. This is application accounting, not a drive-side reservation.
Every admitted byte remains accounted for while moving through FUSE, LTFS and drive buffers; an exact
pending-byte counter or recurring drain is unnecessary. Tape owns the capacity interpretation and
ACP performs the admission decision once at output creation.

This also avoids assuming that a background capacity query is free: the pinned
[capacity path holds the device lock](https://github.com/LinearTapeFileSystem/ltfs/blob/4c0e6faae661e0bfd50bc9707f59ecd88c90b7eb/src/libltfs/ltfs.c#L696),
which is also needed for writes. The [HP integration manual, page 24](https://docs.oracle.com/cd/E21419_04/en/LTO5_Vol2_E3a/LTO5_Vol2_E3a.pdf#page=24)
advises against interleaving non-data commands with long transfers.

Compression savings are not credited during that operation. The budget uses documented worst-case
costs, including relevant formatting, rewrites and Index work, rather than a compression average or
fixed reserve. Existing allowances must not be counted twice. The HP bounds identified above remain
unproved, so using estimates in this workflow improves accounting but still gives advisory admission.
Its capacity-utilization cost must be explicit; rereading space and resuming after a forced drain
would reintroduce the excluded workflow.

### LTFS Finalization and Index Evidence

The pinned [unmount path](https://github.com/LinearTapeFileSystem/ltfs/blob/4c0e6faae661e0bfd50bc9707f59ecd88c90b7eb/src/libltfs/ltfs.c#L2079)
uses Index-writing logic that tolerates `NO_SPACE` and can attempt the Index partition after a
data-partition failure. Its [FUSE callback](https://github.com/LinearTapeFileSystem/ltfs/blob/4c0e6faae661e0bfd50bc9707f59ecd88c90b7eb/src/ltfs_fuse.c#L1165)
discards finalization return values, so command exit status alone is insufficient. YATM reconciles
staged results with its captured final Index. Neither that metadata nor these source paths prove
that failed writes can be recovered, and no automatic remount/readback phase is part of Archive.

The implementation also supports an
[external Index with the actual Tape for read-only access](https://github.com/LinearTapeFileSystem/ltfs/blob/4c0e6faae661e0bfd50bc9707f59ecd88c90b7eb/src/libltfs/ltfs.c#L1931).
An old Index describes only its own files; a readable XML document or mount does not prove the
referenced bytes remain intact. Stock `ltfsindextool` validates XML rather than installing a
filtered Index. The pinned [ltfsck path](https://github.com/LinearTapeFileSystem/ltfs/blob/4c0e6faae661e0bfd50bc9707f59ecd88c90b7eb/src/utils/ltfsck.c#L1280)
can return immediately when the requested generation is already current. These are public-tool
findings, not a YATM repair, automatic rollback or interrupted-write recovery workflow.

### Remaining Research Boundary

The evidence does not establish a portable bound proving every admitted whole file and its
finalization will fit. Capacity estimates remain advisory where such a bound is absent. Fixed
reserves and compression/rewrite averages cannot supply an unmeasured guarantee. PEW remains an
optional device-specific capability, not the compatibility baseline.

Further qualification should establish the counter's boundary and units, measured write/formatting
costs, and the cost of querying while streaming. Use native tests for admission, error propagation
and publication; use the [physical measurement procedure](../release/physical-measurements.md#capacity-measurement)
only for separately authorized hardware questions. Reuse completed evidence for unchanged paths.
Normal Archive/EW, unload and Restore must be assessed together for the tested configuration;
a giant filler, `df` comparison or unmount exit alone cannot establish that outcome. Raw failures
remain in the evidence. No arbitrary-failure recovery matrix is required to close this study.
