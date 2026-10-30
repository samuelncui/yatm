# Tar on Tape

Status: Research reviewed on 2026-10-29 for [Issue #14](https://github.com/samuelncui/yatm/issues/14).
Sources examined include ACP `v0.2.6`'s native API, Go `1.26.8`, GNU tar `1.35` documentation
and Linux `v6.12` driver documentation. Current software behavior belongs to the
[Tape contract](../architecture/tape.md); this study supplies evidence and remaining physical
questions, not a hardware-support guarantee. Physical measurements apply only to their stated
device, firmware, format and workload.

## Objective

Evaluate ordinary tar archives on raw Tape for drives that cannot use LTFS, including the
LTO-4 use case. Files should remain recoverable with standard tools, while YATM retains
per-file Archive, explicit FileVersion Restore, inventory and Verify behavior. Archiving a
`.tar` file as one ordinary File does not provide that integration. Supporting an older
drive does not establish support for an older CPU, every cartridge generation or every OS.

## Integration Constraints

The [Tape contract](../architecture/tape.md), [Job flows](../architecture/jobs.md) and
[Library](../architecture/library.md) retain ownership of physical operations, manifests and
catalog publication respectively. These consumer boundaries explain the integration requirements:

| Area | Confirmed constraint | Consequence for tar |
| --- | --- | --- |
| Tape identity/profile | The [glossary](../../CONTEXT.md) and [Library](../architecture/library.md) own cartridge identity and immutable profiles | Record decoding parameters must belong to the cartridge rather than current device defaults |
| Content access | [Sessions](../../internal/media/backend.go) distinguish native paths from ordered content endpoints | A raw device is not a pathname for an individual tar member |
| Copy engine | [ACP v0.2.6's filesystem adapter](https://github.com/samuelncui/acp/blob/v0.2.6/filesystem.go) includes commit/discard/metadata/cache operations | A narrow stream endpoint keeps those native assumptions out of tar; source metadata and closure still precede native output completion |
| Archive publication | [Reconciliation](../../internal/executor/archive/media_finalize.go) joins staged content with physical completed-file evidence | tar needs confirmed records and filemark completion, not a synthetic LTFS Index or submitted-file list |
| Restore | Its [frozen copies](../../internal/executor/restore/init.go) retain the selected content and physical placement | Re-querying a current Library locator later would abandon that frozen-candidate boundary |
| Scan | The [pipeline](../../internal/executor/scan/pipeline.go) enumerates fresh inventory before content reads for inventory/report policies; Verify resolves frozen expected copies | Inventory plus content acquisition may need another traversal or a defined positioning strategy; Verify needs direct access to its frozen copies |

Existing device leases, Job lifecycle, bounded manifests, content identity and Library publication
remain useful. Their ownership does not require a new Job kind or another catalog. Existing LTFS
scripts and their [compatibility contract](../architecture/tape.md#tape-script-compatibility)
remain independently supported. Replacing a mount command with `tar` cannot satisfy these consumers.

## Format and Device Findings

GNU tar documents several independent archives on one non-rewinding Tape, separated by tape
marks. Appending another archive at the recorded end differs from `tar --append`, which
rewrites an existing archive's ending and has device-specific limitations. Correct positioning
is essential because a new write can destroy later recorded content.
[GNU tar manual, multiple archives and append](https://www.gnu.org/software/tar/manual/tar.html).

PAX is the stronger interoperability candidate for YATM's long UTF-8 paths, large files and
subsecond timestamps. USTAR has an approximately 8-GiB member limit and more restrictive names;
an unspecified tar implementation's default is not a format contract.
[GNU archive formats](https://www.gnu.org/software/tar/manual/html_chapter/Formats.html),
[Go tar formats](https://pkg.go.dev/archive/tar#Format).

Tar's 512-byte blocks are not physical Tape records. Linux variable-block writes establish
records whose readers need sufficiently large buffers; fixed-block mode has alignment rules.
Record buffering therefore belongs below the tar codec. Tar byte offsets also cannot be used
unchanged as driver seek addresses. Non-immediate filemarks synchronize device buffers; tar
footer output alone does not.
[Linux v6.12 st documentation](https://github.com/torvalds/linux/blob/v6.12/Documentation/scsi/st.rst).

Tar's checksum covers its header, not payload integrity. YATM still needs the actual transferred
SHA-256 and size for Restore and Verify. Standard listing can reconstruct physical member
inventory after catalog loss; it does not reconstruct Library organization, expected hashes
or saved FileVersion relationships. Repeated member names also require distinguishing their
archive/occurrence, rather than treating a name as a unique physical address.
[GNU tar format](https://www.gnu.org/software/tar/manual/tar.html).

Standard logical-object addressing is distinct from vendor-specific position cookies. SSC-3 draft
r01c §4.2.5.2 assigns each recorded block/filemark a sequential per-partition identifier and permits
later LOCATE using a saved READ POSITION identifier. Sections 7.3 and 7.5 distinguish standard
identifiers from vendor-specific forms. This supports protocol-level eligibility for an exact
reported pairing, not a claim of empirical firmware conformance or physical seek performance.
Fresh exact observations, unchanged recorded content and command error handling remain necessary;
the physical acceptance matrix separately tests reloads, record modes and device combinations.
[SSC-3 r01c, sections 4.2.5.2, 7.3 and 7.5](https://www.13thmonkey.org/documentation/SCSI/ssc3r01c.pdf).

### Native Command Submission

Linux `v6.12.111` SG_IO submits a request before its interruptible completion wait. A signal can
leave that request orphaned and return `ERESTARTSYS`; a handler with `SA_RESTART` can then restart
the ioctl and submit another CDB. Go's Linux runtime uses `SA_RESTART`, including its SIGURG
preemption handler. Therefore one successful Go syscall return does not establish one submitted
READ or WRITE, and handling only a visible EINTR misses hidden replay.
[Linux stable SG_IO](https://github.com/gregkh/linux/blob/v6.12.111/drivers/scsi/sg.c#L931-L946),
[Linux syscall restart](https://github.com/torvalds/linux/blob/v6.12/arch/x86/kernel/signal.c#L244-L261),
[Go Linux signal handlers](https://go.dev/src/runtime/os_linux.go),
[Go preemption signals](https://go.dev/src/runtime/signal_unix.go).

SG v3's separate header write returns after submission; its response read only receives an existing
request. Repeating a receive cannot submit another CDB. This separation supports a single-command
exchange under YATM's existing serialized ownership, with matching responses and stable native
buffer lifetime. It does not itself establish a drive's residual semantics or physical qualification.
[Linux stable submission and receive paths](https://github.com/gregkh/linux/blob/v6.12.111/drivers/scsi/sg.c#L436-L830).

### Encrypted Full-Cartridge Observations

A 2026-10-28/29 run through the installed package used the same HP
LTO-5/Z6ED drive, 256 KiB variable records and AES-256-GCM configured by the `encrypt` adapter.
Every operation went through the installed service and CLI.

One Archive APPEND of 64 GiB members published 22 members, 1,512 GB, in 10,995 s. Capacity
admission then refused the next member: the drive's EOD-to-EW estimate was 17.7 GB. The Job
returned to its pre-Media state with a pending suffix, and earlier Positions were unchanged.
Restores of the first, middle and last members matched. Verify matched every member on the
cartridge at about eight minutes per 64 GiB member. After `stenc` loaded the saved key, GNU `dd`
and `tar` read this archive, which has no tar end blocks, to its filemark with tar exit 0. They
hashed all 22 published members, and the read ended at the drive-reported EOD after four
filemarks.

A further APPEND of 256 MiB members ran with capacity polling Off, so admission kept only its
17.5 GB preparation sample. The drive reported EW (sense 0/0002) after 54 members, about
14.5 GB, roughly 3 GB before that estimate. The Job settled with 54 published and 46 pending
members, and every published member restored with matching bytes. On this encrypted cartridge,
small members therefore reach the drive's EW before the estimate is exhausted. The single sample
does not say whether encryption overhead or the estimate's own granularity causes the 3 GB gap.

One LOGICAL and one AUTO Restore then read 36 members from both ends of the filled cartridge: the
fixture members at BOT and every other EW-tail member. They took 276.1 s and 272.0 s. The learned
model was available, and AUTO planned in 0.16 s but kept the logical route because it found no
lower complete-route estimate. This selection leaves no room for reordering and measures no gain.

### Public-Tool Recovery Checks

GNU tar 1.34 disk-only checks cover long paths, permissions, subsecond modification times, empty and
binary xattrs, and extraction of completed members preceding a truncated tail. Original stenc
2.0.1 parser and decrypt-only/empty-key payload checks cover the exported key representation.
These checks establish software framing only.

Normal physical recovery has been checked with Linux st, mt-st 1.8, GNU dd and GNU tar 1.35 for
variable and fixed records, compression On and Off, and script-configured encryption. Each check
copied the identity archive and both content archives separately from BOT, extracted all nine
fixture files with their permissions, exact modification times and ordinary xattrs, observed three
terminating filemarks and recorded EOD, and restored the original driver block setting. Encrypted
recovery used the writing drive; it does not establish compatibility with a different reader or
qualify every configured encryption adapter.

A separate reduced-capacity EW check recovered all 43 published complete members. GNU tar
returned nonzero for the following unpublished partial member; that result is retained and does
not invalidate the independently checked complete files. Empty physical Tape files, hard EOM,
encrypted EW tails and arbitrary faults are outside those results.

## Archive Layout Evidence

### Hardware Compression Controls

Hardware compression preserves the host record sequence and logical positioning. Its variable
compression ratio instead weakens interpolation from logical objects to physical distance, even
with known wrap endpoints. A possible extension would use additional position samples or applicable
physical-consumption observations with explicit uncertainty; a whole-cartridge compression ratio
cannot recover intra-wrap placement. It also needs local read-rate observations for READ/discard
costs. These are research inputs, not an implemented compressed-media cost provider.

Without physical samples or qualified physical-consumption observations, hardware-compressed
logical offsets do not uniquely determine within-wrap distance. REOWP can constrain the wrap but
does not solve its interior density. Recurring throughput events can suggest boundaries; host-side
byte periodicity is less stable when hardware compression changes. The current HP5 reports REOWP
but no verified live LPOS, while its native-capacity position estimate remains unqualified as a
physical coordinate. Such observations must not be promoted to exact locations.

HPE describes LTO-DC as an ALDC-derived hardware compressor that switches to pass-through for
incompressible input. Modern software codecs offer different ratio/CPU tradeoffs: the Zstandard
v1.5.7 README reports a version-1.5.6 level-1 Silesia result of 2.887:1 and 510 MB/s compression on
an i7-9700K. That in-memory software comparison contains no LTO measurement and cannot establish
superiority over drive compression. At 3:1, feeding 300 MB/s of compressed bytes requires about
900 MB/s of original-data compression throughput. Compare the same files, CPU, drive and end-to-end
Archive/Restore operations before recommending either approach.
[HPE QuickSpecs, compression](https://www.hpe.com/psnow/doc/c04123270),
[Zstandard v1.5.7 benchmark documentation](https://github.com/facebook/zstd/blob/v1.5.7/README.md#benchmarks).

The published 2:1 LTO-5 and 2.5:1 LTO-6 figures are capacity/throughput assumptions, not fixed
compression ratios for arbitrary content. For a simple streaming software-compression pipeline,
original-byte throughput is bounded by `min(source-read rate, compressor input rate, compression
ratio * stored-byte tape rate)` before staging and other overhead. A slow source disk can dominate
both hardware and software paths. Restore destination writes may likewise dominate selected-file
output, while gap discard need not decompress or write skipped files; a whole-Restore throughput
is therefore not the right rate for every discard estimate.
[IBM LTO throughput figures](https://www.ibm.com/support/pages/lto-and-3592-throughput-information-performance-troubleshooting).

The software option considered here compresses each file independently into a public codec stream.
An ordinary `.zst` member preserves tar-member addressing; independent recovery uses tar and the
public decoder. Path and metadata representation still require a concrete format design. With
hardware compression off and uniform stored records, geometry estimates use actual stored archive
bytes, not the original file sizes; per-file compression does not itself invalidate that mapping.
Tar needs the stored member size before its header, so compression must establish the output size
before Tape submission, using bounded staging or another explicitly evaluated pass. Account for
temporary-disk traffic, capacity and pipeline backpressure rather than buffering an unbounded file
in memory. This option is not implemented; user-supplied compressed source files remain ordinary
archive inputs.

SSC-3 draft r01c §8.3.2 distinguishes write DCE, capability DCC and read DDE. Its current algorithm
fields report the selected writer algorithm and last encountered reader algorithm; they do not
enumerate every compatible algorithm. DDE leaves uncompressed data unaffected, and RED controls
exceptions at compression boundaries.
[SSC-3 r01c, pages 106–109](https://www.13thmonkey.org/documentation/SCSI/ssc3r01c.pdf#page=122).

IBM GA32-0928-08 §6.6.10–11 reports default algorithm 1 for LTO-5 and unregistered ID FF for later
drives. The legacy page-10 compression selector can override page-0F DCE, so changing only DCE is
insufficient evidence that a write policy became effective.
[IBM LTO SCSI Reference, pages 378–381](https://www.ibm.com/support/pages/system/files/inline-files/LTO%20SCSI%20Reference%20GA32-0928-08%20(EXTERNAL).pdf#page=407).

HP LTO-6 Volume 3 describes algorithm 1, independently enabled read decoding, and the legacy
selector's pass-through/default choices.
[HP LTO-6 reference, pages 104–106](https://docs.oracle.com/cd/E38452_01/en/LTO6_Vol3_E1_D20/LTO6_Vol3_E1_D20.pdf#page=104).

These sources establish control semantics for the studied versions, not physical qualification of
YATM's transport, cleanup or public-reader recovery. Unknown firmware behavior remains unqualified.

### Hardware Encryption Controls

SSC-3 r03e §4.2.21 specifies separate encryption/decryption parameters and I_T-nexus ownership.
Disabling encryption together with disabled or raw decryption releases the stored parameters and
changes memory containing the key. A status-page key-instance counter is an opaque observation,
not a key-presence bit or proof of the key's correctness.
[SSC-3 r03e, section 4.2.21](https://citeseerx.ist.psu.edu/document?doi=baaf4ada4b6d66dd63c05f08761157577597d52d&repid=rep1&type=pdf).

IBM GA32-0928-08 §6.8.2–3 and HP LTO-6 Volume 3 describe Tape Data Encryption protocol pages,
algorithm descriptors, parameter management, status and plaintext-key Set Data Encryption. Their
algorithm indexes are reader-local; the public code `00010014h` identifies AES-256-GCM with a
128-bit authentication tag and a 32-byte key. Read decryption and write encryption are separate
modes. A compatible algorithm descriptor alone does not establish safe ownership or clearing.
[IBM LTO SCSI Reference, pages 451–461 and 468–473](https://www.ibm.com/support/pages/system/files/inline-files/LTO%20SCSI%20Reference%20GA32-0928-08%20(EXTERNAL).pdf),
[HP LTO-6 reference, pages 218–226](https://docs.oracle.com/cd/E38452_01/en/LTO6_Vol3_E1_D20/LTO6_Vol3_E1_D20.pdf#page=218).

The studied public reader is stenc 2.0.1, pinned to
[`704b5dd7dd0be93390e35b286bbf4af0b24c9ff4`](https://github.com/scsitape/stenc/tree/704b5dd7dd0be93390e35b286bbf4af0b24c9ff4).
Its main.cpp reads a hexadecimal key from the first line of a file and accepts 64 digits for this
algorithm; the YATM profile prefix must be excluded. Explicit `-e off -d on` selects decrypt-only
mode, whereas `-d on` alone also enables encryption. Off/off without a key builds a zero-key Set
Data Encryption page. Its All-I_T-nexus scope differs from a Local writer's ownership but does not
change the recorded public algorithm. These are source-level interoperability findings; public
unlock after reload, actual missing/wrong-key reads, clearing and cross-reader portability still
require physical qualification of the tested writer/reader path.

### Ordinary Archive Boundaries

Independent uncompressed PAX archives separate tar framing from physical Tape files.
Go's streaming [archive/tar](https://pkg.go.dev/archive/tar) exposes member boundaries and per-file
metadata; GNU tar supplies an independent interoperability reader. The selected layout belongs
to the Media contract. Hardware compression and encryption need separate evidence for the reader
and writer combination.

| Boundary | Benefit | Cost or unresolved limitation |
| --- | --- | --- |
| One archive per Media attempt | Natural existing lifecycle boundary; continuous payload streaming | A failed late member leaves the current archive incomplete; late-member Restore may read a large prefix |
| Several complete archives per attempt | More independently completed and locatable groups | More padding, filemarks and synchronization; grouping needs a demonstrated recovery/throughput requirement |
| One archive per file | Fine filemark-level selection | Filemarks and synchronization may dominate small-file workloads |

No source establishes a universally safe group size or optimal record size. The first row is a
useful comparison baseline, not a complete capacity policy. Finishing groups periodically must
be evaluated against the [continuous-supply constraint](../architecture/tape.md#tape-capacity),
not assumed to be free. Whole-stream software compression would add another requirement to
indexed member access; the baseline above avoids that extra dependency.

Physical addressing needs enough evidence to find a particular member, preserve required PAX
headers and order reads. Filemark-level positioning plus scanning within an archive is a baseline;
faster record-level positioning requires drive/HBA/driver validation. Neither an archive number
nor a tar byte offset alone proves portable fast access.

## Completion Is the Hard Boundary

Linux's early-warning behavior can return a successful write, then `ENOSPC`, then allow a trailer
write before physical exhaustion. This does not guarantee enough room to finish the member already
in progress. Buffered failures can surface later, including at physical finalization.
[Linux v6.12 st documentation](https://github.com/torvalds/linux/blob/v6.12/Documentation/scsi/st.rst).

Non-immediate filemark completion has its own semantics. [SSC-3 revision 01c, §6.9](https://www.13thmonkey.org/documentation/SCSI/ssc3r01c.pdf#page=89)
defines an EW NO SENSE result as successful transfer of all buffered objects; SEW=0 requires EOM
without requiring VALID. A missing information count therefore differs from a valid nonzero
remainder. [Linux v6.12 filemark handling](https://github.com/torvalds/linux/blob/v6.12/drivers/scsi/st.c#L1306)
also accepts a current NO SENSE/RECOVERED ERROR EOM completion with absent or zero remainder.
These sources support interpreting the command's completed warning; they do not make SG residual
zero alone a durability proof or qualify an application's full EW cleanup/publication path.

Go `tar.Writer` retains I/O errors; `Close` refuses an incomplete member and does not close the
underlying device. Calling it after a failed payload write is not a repair operation.
[Go 1.26.8 writer](https://github.com/golang/go/blob/go1.26.8/src/archive/tar/writer.go).

Consequently, complete earlier members being extractable from a damaged tail is different from
proving a safely finalized Archive result. For normal EW completion, publication must establish which complete members reached the
filemark-terminated Archive and which rows remain pending. An independent failure does not
authorize prefix publication or tail repair. Earlier independently finalized archives must remain usable.
An external inventory updated while issuing writes is a candidate list, not independent proof
that buffered bytes reached Tape. Reusing the current LTFS reconciliation flag would not supply
that proof. No full-tape safe-prefix guarantee is established by this study.

## Content-I/O Boundary

Ordered member endpoints allow ACP to retain bounded buffering, hashing, progress and per-item
completion without exporting its private filesystem adapter. They require actual observed
metadata before native target creation and successful source closure before
output commit. A standalone `io.Copy` would also need those completion and safe-output rules;
its short body is not evidence that a second transfer engine is simpler. The
[Media I/O contract](../architecture/media-io.md#acp-and-integrity) defines the implemented boundary.

Issue #14 also mentions STFS. Its [studied revision](https://github.com/pojntfx/stfs/tree/3aff00b57e42600cc930f99c4ef1242431c37432)
provides its own metadata inventory and Go filesystem API; that API requires a write cache sized
for the largest file, while its operation API avoids that requirement. It is a separate integration
choice, not a drop-in path adapter for ACP or evidence that YATM's lifecycle is already satisfied.

## Unresolved Questions and Required Evidence

- **Initialization and identity:** validate raw initialization and later reads/appends, including
  devices whose electronic identity is empty. Drive encryption support cannot be assumed from age.
- **Failed tail:** demonstrate error, cancellation, short-write and finalization outcomes. The
  [encrypted full-cartridge observations](#encrypted-full-cartridge-observations) covers one
  admission stop and one drive EW, including standard-tool extraction of the admission-stopped
  archive.
- **Addressing and inventory:** demonstrate append/reload, duplicate names, long paths, empty and
  large files, catalog-free enumeration and selected Restore with the necessary locator context.
- **Scan and Verify:** measure inventory/content traversal costs. Inventory policies require fresh
  physical observations; Verify reads uncached content against its deliberately frozen old Positions.
- **Throughput:** compare representative small/mixed/large files, record sizes and archive boundaries
  on the selected drive. Filesystem or in-memory tar tests cannot validate Tape positioning or EOM.

These are physical evaluation questions, not an implementation sequence or a claim of completed
qualification. Evaluation retains the
[supported operating model](../architecture/overview.md#supported-operating-model) and existing LTFS behavior.
