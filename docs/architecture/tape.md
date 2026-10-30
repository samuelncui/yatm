# Tape

Tape device observation, the configured script contract, cartridge lifecycle, the sequential tar
format and capacity admission. [Media backends](media-io.md) own the shared Session, Volume and
integrity boundaries; [read planning](read-planning.md) owns read order.

## Device Observation and Raw Transport

[Device discovery](../../internal/executor/device_inspect.go) accepts only configured Tape entries.
The configured spelling remains the request and script input; passive Linux sysfs resolution unifies
st, nst, SG and configured symlink aliases under one device resource. A captured lease key survives
loss of discovery and uncertain cleanup. A new observed identity may replace a free resource mapping;
it cannot bypass an existing hold, waiter or retained unavailable guard. Failure to resolve raw access
does not prevent the existing LTFS adapters from using their configured entry.

ListDevices reports every configured entry with its busy status and reason, using configuration,
sysfs and Executor lease state without SCSI commands. InspectDevice takes a non-waiting lease and
checks ownership before opening raw access. A busy device returns that reason without probing it.
Raw access requires visible host process descriptor tables and a clean negative holder check;
incomplete visibility is unknown ownership, not evidence that the drive is free. This observation
uses the supported same-host namespace and does not coordinate another operator's device access.
Inspection reports identity, readiness, block limits, current mode facts, non-secret encryption state
and available READ POSITION/LOCATE form observations. It sends no positioning, write, mode-setting, key-loading
or measurement command.
Cartridge Inspect remains the distinct LTFS adapter action and can load the cartridge.

Feature reports separate support, current availability and validation, with typed reasons, limits
and evidence. Missing or unreadable facts remain absent or explicitly unknown. A clean unsupported
optional query does not invalidate other observations. Passive queries do not qualify write, seek,
recovery or cleanup behavior; features whose applicable operation is not established remain
unvalidated. Reports are transient observations, not Library facts or saved configuration.
Archive displays optional-query diagnostics without treating unsupported, malformed or unimplemented
optional observations as a blanket write veto. Known busy, missing-medium, write-protection and
readiness/ownership failures still block its form; leased Executor preparation owns final admission.
The Tape transport owns command status, sense and residual classification, including clean optional
rejection and qualified WRITE warnings. Format backends consume that evidence and retain their
own stop, framing and publication decisions; an incompatible joined error cannot become a safe warning.

A zero reported maximum block length leaves the maximum absent; it is not a zero-byte limit.
The SG descriptor's reserved buffer can grow for a selected record, so its current allocation is
not reported as a transport maximum. Preparation requests the selected size and checks the actual
readback; an insufficient allocation fails before record I/O. An immutable transport limit can be
reported, while an unknown adapter maximum remains absent.
Position overflow/unknown flags prevent treating a returned object address as exact. Navigation
selects a reported READ POSITION/LOCATE pair before motion, checks its address width and partition,
and never retries another LOCATE form after a real command failure. These follow
the [SSC-3 Revision 01c](https://www.13thmonkey.org/documentation/SCSI/ssc3r01c.pdf) block-limit contract
and the applicable [IBM 2026 reference](https://www.ibm.com/support/pages/system/files/inline-files/LTO%20SCSI%20Reference%20GA32-0928-08%20%28EXTERNAL%29.pdf)
position forms. Holder visibility follows the
[psmisc 23.7 fuser contract](https://gitlab.com/psmisc/psmisc/-/blob/v23.7/doc/fuser.1); an empty
fuser result alone cannot establish permission to observe all holders.

The [raw Tape provider](../../internal/media/tape) uses Linux SG v3 and a serialized data/control lane.
Recurring capacity observations alone use a private descriptor under the same attempt lease;
ordinary passive inspection remains separate.
Read/write access submits each command header once and receives its matching response separately;
restarting an interrupted receive cannot submit the CDB again. Submission errors and short headers
are never retried. Passive inspection retains read-only SG_IO access and its idempotent query
allowlist. It does not require write permission. The split exchange avoids SG_IO's post-submission
`ERESTARTSYS` behavior under signal handlers with `SA_RESTART`.

Cancellation before admission issues no command. Once issued, the owner retains the lane and waits
for the native response and any failure settlement before returning or closing its descriptor.
The header timeout remains the kernel request timeout, including its error handling; it is not an
independent wall-clock cancellation deadline. Read/write transports own reusable anonymous memory
for command, sense and record bytes. The kernel retains those stable addresses instead of caller
Go buffers; only a correlated response with valid counts copies returned bytes to the caller.
Normal close clears and releases the allocation. Unknown settlement retains both it and the fd.
These private CLOEXEC descriptors are never forwarded or used across credential changes.
Host/driver failures, invalid accepted-count evidence and unknown completion prevent later command
grants. With concurrent capacity observation, the sole WRITE granted before a fatal query result
may still reach native submission afterward. It must settle, and the attempt publishes nothing.
The Executor retains the affected resource as unavailable until it stops; these failures do not
create Position health findings. Closing a raw transport sends no unload, reset or filemark.

Record commands distinguish fixed-record counts from variable-record byte counts. Current/deferred
sense, residuals and warning scope govern acceptance; an unexplained residual cannot prove a complete
write. EW handling uses an applicable provider contract, not an LTO-generation assumption. Filemark
completion uses non-immediate commands: count one synchronizes prior data and records the terminating
mark; count zero synchronizes without adding a mark. A qualified current completion warning must prove
the entire requested count completed and remains observable as EW. Other completion errors withhold
publication. These primitives and their synthetic tests do not establish physical qualification.
LTFS and Volume retain their native I/O paths; tar uses the raw provider under the same attempt lease.

## Tape Script Compatibility

Configured scripts are a long-lived user interface. Preserve previously supported adapters wherever
possible, including their helpers, input variables, output format, working directory and exit-status
meaning. LTFS distribution and hardware differences remain inside those adapters. A necessary
incompatible change requires explicit developer approval, an actionable installer notice before
upgrade and regression checks of the changed boundary. Installation preserves customized adapters
and their execution context instead of replacing them automatically. This contract applies
independently of the pre-stable API/data policy and preserves the [Tape lifecycle](#tape-lifecycle).

Script changes require automated checks of the published interface and representative unchanged
historical adapters. Physical acceptance checks the selected host adapter separately; it does not
replace the historical compatibility checks.

LTFS Archive requires a completed per-Job captured Index; historical adapters without that output
need manual adaptation for Archive. This required incompatibility is described in the
[upgrade notice](../operations/migration.md#tape-script-adaptation) displayed by the installer.

## Tape

The immutable Tape profile selects LTFS or `tar_pax_v1`. FORMAT resolves explicit choices and the
selected device's frozen defaults; APPEND and reads use the stored profile. Unset format defaults to
LTFS. Tar defaults to variable records with a 512 KiB (524288-byte) maximum, overridden by saved
Device settings and then explicit FORMAT values; device/transport limits remain mandatory.
APPEND and reads retain the recorded policy. Tar options cannot be supplied to LTFS, and conflicting
APPEND options fail before access.
Compression omission survives admission so leased preparation can apply a qualified recommendation
under the [shared precedence](contracts.md#tape-options-and-selection); without one its final
profile defaults off. [Encryption](#encryption) has no per-device or per-write switch.

The Device capacity-polling interval is an execution preference for tar FORMAT and APPEND,
independent of the cartridge profile. Its admitted snapshot resolves omission to five seconds,
explicit zero to Off, and other values to an interval of at least five seconds. Saving it performs
no hardware operation and cannot change an active attempt.

Configured Tape scripts are the anti-corruption boundary for environment-specific executable paths, device mapping, vendor options and helper logic. The service supplies operation inputs and validates identity, content and final Index output; it does not absorb those local decisions into configuration switches. The [Tape scripts guide](../operations/tape-scripts.md) owns their inputs, outputs and completion requirements; the [upgrade guide](../operations/migration.md#tape-script-adaptation) owns v0.1.x adaptation. Installer preflight never invokes these scripts.

Operators may edit configured scripts and their helpers to match local software versions,
platforms and hardware. Bundled commands are starting points; local changes retain the
published inputs, outputs, exit-status meaning and physical finalization requirements.

The format script passes the resolved device, barcode and complete Tape name as separate literal
arguments to `mkltfs`; spaces and shell pattern characters in the name do not split or expand it.
Regression checks execute the shipped script with a stub LTFS command, while physical acceptance
uses the packaged script with only the host executable path and test placement rule adapted.

The Linux `get_device` adapter resolves the Tape node through the kernel's sysfs mapping to its
SCSI generic node. This lookup does not open the drive, so it still works while LTFS owns the
device and during unmount; an absent or ambiguous mapping fails explicitly.

The [Tape scripts guide](../operations/tape-scripts.md#bundled-templates) describes the bundled templates and
the file-backend test adapters.

[LTFS Sessions](../../internal/executor/media_tape.go) identify the cartridge, configure encryption, format when requested, mount LTFS, and unmount/finalize. FORMAT accepts a successful probe reporting an empty cartridge memory barcode and uses the explicit user-supplied six-character barcode; the format script passes that identity to `mkltfs` to write it during initialization. A non-empty device barcode must match the requested identity, and probe failures stop the Session before encryption or formatting. An empty barcode does not establish that the cartridge contains no data: FORMAT remains an explicit destructive operation. FORMAT rejects a barcode already in the Library; explicit metadata deletion is required before creating a new Media identity.

LTFS APPEND and Restore require a non-empty valid device barcode before encryption or mounting; APPEND also requires an exact match with the requested identity. Device inspection may use its documented read-only manual identity fallback when automatic MAM lookup is unavailable, but that fallback does not authorize a write or Restore. APPEND requires compatible `ltfs_v1`, preserves Media ID/key/Positions, and chooses a short base36 timestamp prefix only to avoid path collisions.

### Tape Lifecycle

The configured LTFS adapter lifecycle remains:

1. For an uninitialized cartridge, the operator chooses its six-character barcode and explicitly
   submits FORMAT. The cartridge memory barcode may be empty; the formatting script writes the supplied
   identity and name while initializing LTFS. An empty barcode does not establish empty content.
2. Archive writes files, normally unmounts and waits for device release, then validates the final
   captured Index and publishes Media and Positions. The Library records the Tape's identity and key.
3. On later loads, APPEND verifies the existing barcode and compatible Library Media, reuses its key
   and adds files without replacing earlier Positions. Restore verifies the loaded Tape against its
   recorded identity, uses its key and reads the selected saved content.
4. Metadata deletion leaves the cartridge's bytes intact. Reinitialization requires explicit FORMAT
   after deleting the registered Media; formatting erases the old physical contents.

Script and caller changes must keep every step available, including FORMAT before a cartridge memory
barcode exists. Tar FORMAT follows the same first step natively, writing the supplied barcode into
the cartridge memory as described in [Sequential tar Tape](#sequential-tar-tape). Per-operation
identity and finalization rules remain defined above and below.

The final captured LTFS Index is the physical fact source. [LTFS processing](../../internal/media) retains backend extents in the typed storage-metadata envelope and stores opaque binary order from partition/block/offset. `ltfs_v0` is path-order compatibility; `ltfs_v1` has validated LTFS order/extents and supports append. Versioning belongs to the storage format, not a second independent version field.

After physical Finalize, Archive matches staged files against the returned Index by actual Media path,
size and valid extents. On normal completion every staged result must match. On target-full it submits
only the continuous verified prefix and resets the later suffix to PENDING. A successful normal unmount
includes waiting up to ten minutes for the LTFS process to release the SG device and for `mt status` to
report `DR_OPEN`; the Job attempt, device lease and Job bundle remain owned throughout this wait. An
unmount timeout or invalid/missing final Index publishes no unverified result. An uncertain device
remains unavailable for that Executor lifetime. Force and lazy unmount are manual stopped-service
recovery tools whose write result must be discarded. Ordinary LTFS files stay readable without a
YATM-specific segmented format.

LTFS Read Sessions validate the expected Tape before mounting and keep the attempt's device lease
through normal unmount. Finalize does not run `readinfo` again: that script loads the cartridge,
which must not occur while LTFS owns it. Successful reads become publishable only after normal
unmount succeeds; a failed unmount still withholds publication and keeps the device unavailable.

### Sequential tar Tape

[Tar Sessions](../../internal/executor/media_tar.go) use a known single data partition and fixed or
variable records. Physical Tape file zero is an ordinary PAX tar containing `YATM/identity.json`
and `YATM/README.txt`; it records the barcode, name, random initialization ID and immutable record
profile. Each later physical Tape file is an independent PAX archive for one Archive attempt.
Members use `archives/<16-digit hexadecimal tape-file-number>/<frozen target path>`, preserving
separate saved versions during independent extraction. The label is identity/recovery data, not a
content index. The [recovery guide](../operations/tar-tape-recovery.md) owns the public-tool procedure.

Every tar FORMAT, APPEND and read runs the configured encryption adapter; there is no plaintext tar
path. Writes establish the selected hardware compression policy; the immutable profile records that choice.
Writes also require known rewritable, unprotected media and applicable buffering/EOD generation;
the baseline does not offer WORM writes.
Known-absent features need no unsupported control command. Reads require the record/decoding policy
without writable-media or write-compression prerequisites. Compressed reads independently establish
hardware decompression. New tar FORMAT generates a key and invokes the configured Encrypt script;
APPEND and reads restore the stored key, and a tar Media without one is refused before device access.
Software read ordering uses the qualified
provider boundary below and otherwise retains logical order.
Optional cached capacity admission
does not replace EW/error handling; unknown capacity does not prevent writing. Record choices must fit observed block limits
and the selected transport allocation. Generic applicable SSC forms can establish eligibility without
an exact-model allowlist, but unknown required semantics or failed safety evidence refuses access.
Passive observations remain UNVALIDATED and supply no physically validated write recommendations.

FORMAT remains explicit and destructive and rejects a registered barcode. Its record profile and
key use the existing Media/Job publication path; failure does not create a separate recovery copy. As in LTFS, the
cartridge memory barcode is the Tape identity. FORMAT accepts an absent barcode, which does not imply a blank cartridge, and a
reported conflicting barcode fails. After initialization, FORMAT writes an absent barcode as the
requested identity with WRITE ATTRIBUTE, as `mkltfs -s` does: attribute 0806h in ASCII format, a
32-byte value left-aligned and space-padded. It reads the attribute back and requires equality
before the Encrypt script and the label; a present matching barcode is not rewritten. A write or
readback failure stops FORMAT before any data write, without retry or fallback. APPEND and reads
require a matching cartridge memory barcode. The label verifies the selected Media and never substitutes for that identity.
Actual partition-list evidence confirms committed layout; mode-page counts may remain unchanged until
FORMAT. A required transition from multipartition media or a nonzero active partition needs an
applicable supported form before mutation. Initialization establishes partition zero/BOT and a known single partition;
the new label's synchronized terminating filemark establishes the fresh recorded boundary. This
replaces the recorded archive layout rather than claiming to erase every physical byte. A proven EW
on the label's filemark closes content admission while permitting zero-member Media settlement,
without another data WRITE or filemark.

APPEND checks the registered label/initialization/profile before proving the physical tail. It
requires the long READ POSITION/LOCATE pair, issues SPACE to EOD (mandatory in SSC) without a
support query, obtains the actual object/filemark counters at EOD, locates the preceding object,
and verifies a zero-byte filemark followed by zero-byte EOD. It restores and checks the proven
post-filemark position before enabling write controls. This needs two boundary READs regardless
of the written prefix. A missing long position form, a rejected command, an EOD position without
object or file counters, failed motion or a contradictory tail fails APPEND without retry or a
record/filemark walk from BOT. The proof never uses CM or catalog counts as write authority or
rewrites the previous tar terminator. A capacity/EW-terminated archive ending at a proven filemark can precede another archive. Unterminated trailing records, unreadable required
boundary data and unresolved final position refuse APPEND. Existing Media and Positions are preserved.

The [codec](../../internal/media/tar_write.go) packs members across records without per-member device
flush/sync. It uses standard Go PAX framing, regular files, permissions including special mode bits,
subsecond mtime and local `SCHILY.xattr` records with raw empty/binary values. ACP excludes its managed
cache keys before supplying ordinary metadata; the codec owns PAX representation and encoded-header
limits. Metadata/header allocation is bounded; oversized or unsupported metadata fails before
member emission. Reference construction may run ahead of writing; dynamic admission belongs to
the ordered target Open lane, without reading mutable writer state from the feeder. A prepared
label warning can reject references immediately. Global PAX state, sparse extensions and links are unsupported. Sequential locators
contain the actual Tape file ordinal and header-chain byte offset. Optional paired header/exclusive-end
anchors use partition zero, a logical object and an offset within its actual record. Their containing
record's tar offset cannot underflow. The Tape provider owns eligibility for the positively reported
standard READ POSITION/LOCATE pairing, partition-zero scope and address width. Its logical-object
contract counts records and filemarks independently of compression or record byte lengths. Missing
pairing or applicable failed evidence withdraws optional navigation. Physical validation remains
UNVALIDATED until its own acceptance; it is not another protocol-eligibility switch.

With an eligible navigator, the writer captures one fresh start position before content emission,
counts accepted complete records and checks the expected next object after its synchronized
terminating filemark. It also checks the file ordinal when the device reports it exactly. Member
boundaries are retained on existing target references and included in Archive's ordinary staged item
results; there is no second member inventory, whole-Tape record map or per-member position query. A clean unsupported observation or valid unknown address omits both
anchors while retaining otherwise proven sequential content. Contradictory or uncertain position
evidence makes finalization unusable. Logical counters and command-width bounds are checked.
Exhausting an optional address width omits the attempt's anchors while preserving sequential
completion; it does not wrap a logical object or reject otherwise valid content. An observed
early-warning position closes content admission under the same physical completion rules.

Successful member Finish proves complete header/payload/zero padding, separately from physical record acceptance.
Archive's existing item results retain provisional positioning after ACP and member framing succeed.
Two optional scalar header offsets track the last successful member still buffered and the last successful
member covered by fully accepted records. Byte zero remains valid; an active unfinished member cannot
advance confirmation. Finalize requires synchronized count-one filemark completion and verified cleanup
before its frozen validator admits staged positions on normal completion or an exclusively
capacity/EW stop. Archive applies it in the existing path-update pass,
including optional-anchor withdrawal and final short-record normalization. Staged rows cannot authorize
restart publication; a new attempt resets them.
Successful normal completion or an exclusively capacity/EW stop also requires synchronous unload
before publication. An independent failure or cancellation uses control cleanup without unload;
the runner still retains its original content/stop result.
Archive joins those path/size/storage facts with successful STAGED content results. Complete members
can survive source-open gaps; they need not form a continuous manifest prefix. Successful physical
finalization makes a newly initialized Media publishable even when no content member survives.

An exclusively capacity/EW stop closes admission and discards the unsubmitted suffix without
adding payload or a tar trailer. Its completed filemark and unload can admit only fully accepted,
complete members. Cancellation, a source failure after a member starts, or another independent
failure stops the attempt, runs ordinary cleanup and returns no usable result from that Archive.
It does not add a filemark to salvage a prefix, repair a tail or resume the interrupted stream.
Completed command observations remain diagnostic facts; they do not authorize publication. Unknown
or partial acceptance, deferred/hardware errors and failed completion or cleanup also withhold publication. Qualified non-immediate count-zero synchronization or
count-one filemark EW can establish completion from current NO SENSE/EOM command semantics;
absent sense information does not contradict that success, while a valid remainder must be zero.
Data WRITE acceptance still requires qualified valid transferred-unit evidence. Contradictory native
status/residuals or mixed errors remain failures. The runner retains the copy/stop cause independently
when physical finalization succeeds. An EW first reported by the completing filemark also reaches
the runner as the stop reason without discarding completed members. Normal completion writes two zero tar blocks, then a short final
variable record or legal fixed-record padding.

#### Tar Capacity Admission

Capacity admission uses a transient cached native-space allowance. Preparation can observe the
current partition's recorded-EOD-to-EW estimate from applicable page 0C parameters 4 and 5; current
head position, nominal capacity and logical object counts cannot substitute. Missing/invalid counters
retain the sequential baseline. Clean optional query failures remain feature-local; observed medium
loss/not-ready state or uncertain ownership/command settlement invalidates the affected attempt.
The existing current-mode active partition can associate a passive sample without logical-position
support; conflicting partition observations leave capacity unknown.

Ordered target Open reserves the actual source metadata size, encoded PAX/header chain and 512-byte
payload padding before the first header. Reference creation performs no reservation or query.
Cumulative native cost counts shared records once; failed members do not reclaim Tape space.
A separate reserve covers tar end blocks, fixed-record final padding and the terminating filemark.
A cached refusal admits no bytes of the next member and closes a normal tar from that reserve;
EW during WRITE overrides the budget and stops further data/trailer writes.

The optional observer owns at most one query. On Linux, its first recurring observation lazily
opens one private SG descriptor with independent native buffers and response matching. Only the
exact current-value page 0C query is allowed there, with kernel queue-tail placement. Explicit Off
never opens it. The writer does not wait for this query's I/O or an I/O-spanning software lock.
The kernel, HBA and drive remain shared, so this does not promise physical zero delay.
Its issue callback captures the cumulative cost of WRITEs already known to have returned full
acceptance. Atomic publication deducts all reservations beyond that prefix, completion reserve and
known error/unreflected allowances from an absolute sample, saturating at zero. An overlapping
WRITE remains charged even if its before-submission callback has run; a conservative double debit
is possible. No admission lock spans I/O; finalization joins the observer before physical
completion, control/key cleanup or descriptor release.
Live accounting requires an explicit transport issue-callback contract for both queries and WRITE;
an Execute-only transport retains ordinary records but cannot promise that accounting boundary.
Both descriptors share terminal failure admission. A clean unsupported query, or a clean optional
attachment rejection before any command, disables recurrence. Preparation, passive Inspect and
recurring observations use the LTFS LTO LOG SENSE default native timeout of sixty seconds, without
an additional sample deadline or failure-count cutoff. The timeout is not a wall-clock cleanup
bound through kernel error handling; cancellation never releases an issued request early.
Inspection admits its finite query sequence under the caller's context and each native timeout;
it adds no shorter whole-inspection deadline that could promote an optional miss to device failure.
A matched, settled capacity-only timeout or busy response, undecodable counters within a GOOD
native response, or an unknown counter value leaves cached debits and EW handling intact.
Clean local descriptor/memory exhaustion while opening the private query attachment also leaves
WRITE available and retries attachment on the next poll; no query was submitted.
Capacity polling continues at the configured cadence and can recover after any number of local misses.
One worker issues its first observation immediately and then follows a ticker. An outstanding query
prevents another from starting; missed ticks are dropped, with at most one due tick consumed immediately
after a slow query. There is no retry backlog, separate retry loop or exponential backoff.
The observer logs failed queries and missing values at warning level with the consecutive failure
count, and logs recovery at info level. Busy admission and ordinary shutdown cancellation are quiet.
Logging uses the existing Job logger outside capacity-accounting and native WRITE locks.
Unknown native outcome, reset, deferred write errors, medium loss/not-ready and other unclassified
query/attachment failures still invalidate the attempt. A query-local error cannot hide a joined
failure. Finalization joins the issued request before completion and cleanup.

Only fresh lower-bound observations with known allowances and bounded native costs establish bounded
admission. Estimates cannot promise that a member fits or increase an applicable bounded budget.
The current provider supplies estimates with unknown error, buffer lag and physical overhead.
Production retains the preparation observation and cached debits. Unless the optional query is
confirmed unsupported, the admitted polling interval enables background capacity polling even when
preparation could not obtain a value. Explicit Off retains only the preparation/debit path.
Unsupported queries retain EW/error handling without a polling loop.
This protocol-based policy does not certify device performance or physical costs. Unknown freshness
cannot establish a lower bound; the [capacity study](../research/tape-capacity.md) explains the
separate measurement limits.
The worker and data path remain independent; per-file admission consumes cached facts only.
Polling remains advisory and does not establish a bound on native counter lag or physical costs.

[Reads](../../internal/media/tar_read.go) without eligible anchors rewind/space to the containing Tape file and reuse
the forward cursor for selected members. They validate the stored header boundary/path, stream actual
payload and require observed legal padding at Close; incomplete/failed Close invalidates the cursor.
Eligible selected reads locate the containing header record, check its actual length, parse the
PAX/header chain and compare the observed exclusive member end at Close. The session retains one
record for adjacent or earlier headers in those same bytes; record lengths need not equal the
configured maximum. Failed motion or record reads stop the source lane, and later unstarted reads
issue no retry or fallback commands. Session-wide unavailability does not create per-file corruption
findings; it does not by itself imply an uncertain hardware outcome. Read cleanup remains a separate
publication gate for previously successful reads.
Fresh inventory traverses every content Tape file and excludes only a readable incomplete tail at a
clean filemark. Actual I/O faults or an unterminated recorded tail fail enumeration; complete inventory
and successful read Finalize are both required before replacement publication. Reads emit no filemark.
Automatic tar selection reads the cartridge memory barcode under the lease and resolves it against frozen
candidates; a Tape without one fails identification. Known LTFS selections retain
their configured adapter flow without requiring SG permissions.

Preparation captures touched non-secret session controls before the first mutation, uses MODE SELECT
with SP clear and verifies readback. Both session and partition selection preserve buffering and speed.
Cleanup restores captured record/compression controls and uncommitted pending partition controls after
commands settle; it never saves defaults or resets. Normal tar Archive, Restore and Scan completion
then issues one synchronous UNLOAD, which owns rewind/unthread/eject; no extra REWIND is added.
Failed or canceled work and transport Close do not issue that motion. Restoration failure prevents
unload. Required release failure or preserved command uncertainty retains device unavailability.
Synthetic record/SG tests and GNU tar disk interoperability establish software behavior. Physical
acceptance separately covers initialization, EW/count/completion, reload-stable seek, cleanup and
Linux public-reader recovery for each tested record policy and device path. Discovery, a local
commit or disk extraction does not establish those gates.

### Hardware Compression

Hardware compression uses the SSC data-compression page's DCC capability and DCE write setting,
independently of its DDE read setting. The legacy device-configuration compression selector is
resolved consistently with DCE for writes; APPEND first validates the label and recorded tail under
read controls, then applies the cartridge's write policy. READ preserves write DCE and does not
require writable media or a changeable write-compression setting. A compressed profile requires
enabled hardware decoding with supported exception handling before reading its label. Plain reads
remain usable on devices without that optional capability.

The inspection report exposes the observed numeric algorithm identifier and optional enable-bit
changeability through `TapeCompressionLimits`, reusing the existing current-enabled observations.
An observed algorithm is neither a supported-algorithm list nor a compatibility guarantee for another
cartridge. Algorithms are preserved rather than selected by name or LTO generation. Required
controls need known changeability when changed and exact readback. Cleanup restores only touched
controls and preserves actual partition state and any last-read decompression algorithm. It never
saves power-on defaults. Unknown active write settings fail preparation; a known absent compressor
with explicit Off needs no unsupported compression control. No software compression is introduced,
and the capacity estimate grants no expected compression savings. Physical compression, cleanup
and compatible Linux-reader recovery qualification remains pending.
Job diagnostics distinguish the immutable write choice from effective write compression and read
decompression in FORMAT, READ and both APPEND preparation phases. Algorithm observations are
refreshed from existing current-mode queries; missing values remain unknown. Unexpected write-algorithm
drift cannot count as verified cleanup, while normal last-read decoder algorithm changes are retained.

### Encryption

The configured `Scripts.Encrypt` adapter owns encryption setup for both LTFS and tar, using the
existing `DEVICE`, `KEY_FILE`, `TAPE_BARCODE` and `TAPE_NAME` inputs and the service working directory.
FORMAT generates the existing `v1:` plus 64-hex-digit key; successful publication retains it in
Media and the normal Job profile. APPEND and reads restore that saved key for the adapter. The
Executor supplies an owner-only (0600) temporary key file and removes it through ordinary cleanup.
There is no additional persistent recovery descriptor, key copy or failed-FORMAT recovery store.

The native tar provider owns records, partition/buffering and compression controls, not secret
keys or encryption commands. Read-only encryption diagnostics report available modes, algorithm
and counters; a counter is not key presence. A present encryption protocol with an unnamed
algorithm remains partially observed, rather than unsupported. Malformed optional algorithm
reports retain their diagnostic reason without preventing script-prepared record access. A script-prepared session is not subjected to a second
native key setup/verification/clearing state machine. Optional `TarMediaProfile.encryption_algorithm`
is an observed public fact, not a user control; a saved key does not require this observation, and
an unknown algorithm leaves read-parameter applicability unknown.

The adapter runs under the attempt lease with raw descriptors closed. FORMAT first finishes
its partition initialization and barcode assignment on the raw descriptor, closes it, invokes Encrypt,
then opens a fresh descriptor, requires the matching barcode and reapplies record controls before
writing the label. APPEND/read first opens the raw device and cross-checks its
barcode (after any explicit position-map refresh), closes it, runs Encrypt with the saved key, then
reopens a fresh raw session and cross-checks the barcode again. Script failure stops preparation. The cartridge memory
barcode selects one saved Media and therefore one key; selection never tries candidate keys. The label contains no key.
Normal independent recovery uses the existing protected Library export and a compatible reader,
as described in the [public-tool guide](../operations/tar-tape-recovery.md). Media deletion removes
its saved profile; Job deletion removes its attachments, including old key files if present. Those
are existing deletion operations, not a new key-retention or recovery workflow.

### Tape Capacity

Capacity management must preserve continuous data supply during normal Archive. It must not
introduce recurring pause/drain/synchronize/resume cycles per file or batch; normal finalization
when ending use of the current Tape remains necessary. The
[capacity study](../research/tape-capacity.md#streaming-and-mechanical-wear) explains the mechanical
wear concern and the limits of the available evidence.

For LTFS, ACP checks a cached available-space estimate immediately before starting each positive-size
linear target, debiting that target's logical size. One independent worker per filesystem starts
an observation immediately and refreshes every five seconds, with at most one query in flight.
Copying does not wait for a query: unknown initial capacity permits a file, while a completed
query error is returned to subsequent targets until a successful refresh. A refresh deducts
admissions that overlapped its query; it does not reserve the outstanding bytes of files already
in progress. Tape target-path resolution can run ahead of copying and does not perform a second
capacity decision. The estimate can refuse a file before output creation, but cannot guarantee
that an admitted file will finish or interrupt it when a later observation falls. ACP does not
credit failed-output cleanup as reclaimed Tape space. Stream shutdown joins outstanding queries.

Capacity observations have three distinct boundaries: bytes accepted by LTFS, bytes accepted by
the drive, and bytes physically written. A successful file Close does not establish that the
drive's remaining-capacity counter includes every accepted byte. Reading `statfs` does not itself
flush either buffer. LTFS can also adjust the reported capacity for its Index and warning state;
raw drive capacity is not interchangeable with filesystem available space.

Actual write, Close and finalization errors remain authoritative. On exhaustion, retain the
complete error chain and publish only the verified prefix described above. Removing an LTFS
temporary file is a namespace cleanup, not proof that its written Tape capacity was reclaimed.
YATM does not automatically roll back LTFS history, shorten cartridges, configure hardware early
warning thresholds, or force recurring Index synchronization for capacity management. The public
[Tape capacity study](../research/tape-capacity.md) owns the supporting hardware/source evidence,
community reports and unresolved prevention/recovery questions. Authorized experiments follow the
[physical acceptance procedure](../release/physical-measurements.md#capacity-measurement).
