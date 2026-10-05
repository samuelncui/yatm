# Jobs and Execution

## Shared Lifecycle

[Job registration](../../internal/executor/job_type.go) installs each kind's runner and typed gRPC service together. The Executor coordinates Job-ID locks, cancellation, runner lifetime, and attempt-scoped Media leases; each runner owns its own phase/state machine. Create builds a complete bundle, then indexes potentially large input asynchronously.

Cached runners retain counters and phase state without retaining idle database or log descriptors.
An admitted attempt keeps one idle SQLite connection and an append log writer available; settlement
closes idle connections and the log file. Read-only queries made between attempts use short-lived
connections. Borrowed connections close when returned, so ending an attempt never swaps a handle
out from under a concurrent reader or reruns runner initialization.

The [public Job contract](../../entity/job.proto) exposes kind, priority, durable status, current phase, catalog revision, timestamps and optional primary target identity/name. [Job properties](../../internal/executor/job_properties.go) index captured Location and Media IDs for search. New execution sources append search values without rewriting earlier values. Kind and property filters run before snapshot paging; changefeed responses include departures so clients remove rows that no longer match. [Persistence](persistence.md) defines projection publication and recovery.

**A state is the one durable lifecycle word a Job reports**, and it is the whole answer for a Job nobody is running:

| State | Meaning | Allowed operations and retained facts |
| --- | --- | --- |
| PREPARING | The manifest is still being built | Background manifest construction or analysis; progress, logs, cancel |
| READY | The manifest is complete and the remaining work waits for the operator to choose Media and start it | Choose Media, run the Media step, inspect results, cancel an active attempt |
| COMPLETED | Every workflow item settled | Read results/logs or delete retained Job metadata |
| FAILED | An admitted Scan or manifest-preparation attempt ended with an error or operator cancellation; the reason is the Job's error field, and an operator cancellation reads as `Cancelled by the operator` rather than as the internal context error | Read the reason and logs or delete retained Job metadata; create a new Job to perform the work again |

**A phase is a live observation, not a second state.** Only an active attempt reports one; an idle Job reports `JOB_PHASE_UNSPECIFIED` even when its runner remains cached, and a restart never resumes a former phase. A running Job therefore reports its durable state plus a working phase, which is how a surface distinguishes "READY and executing its Media step" from "READY and waiting for the operator".

**There is no Job-level retry or restart for any kind.** Failed or cancelled Scan execution and manifest preparation are terminal. A new Scan repeats the selected work; retained manifests, counters and published results do not authorize another run of the failed or cancelled Job. The state and its reason are written in one update.

**Recreating work must return to the normal creation flow.** Restore the original selections and
options in the creation form for the operator to review and edit. Opening or prefilling the form
does not submit work. The operator explicitly submits it through the existing inspection,
estimation, validation and confirmation steps, which read current inputs rather than reusing an
old estimate or approval. Only that submission creates a new Job ID, manifest, progress, error
and log. Keep the old Job and its results intact; never reset its state machine or copy its
completed-item checkpoints into the new Job. Archive and Restore Media failure handling remains
local to the Media operation; it does not recreate or restart the Job.

Failures before attempt admission leave the existing Job state unchanged. Admitted Scan or Index errors and operator cancellation settle as `FAILED`. Only an admitted Archive or Restore Media operation ending in an error or operator cancellation, including while `QUEUED`, returns the Job to its pre-Media `READY` state. It retains the prepared manifest, failure reason and latest attempt timing. Successful per-file and Media checkpoints remain processed; the operator may load Media again for the remaining work. A retained manifest alone never enables a `FAILED` Job's Media action.

Process restart never retries or restarts a Job, revives terminal `FAILED` work, resumes a former live phase, or recreates a queued attempt. An Archive/Restore Job returned to `READY` after a Media error or cancellation still requires an explicit Media operation.

Completion is not a universal assertion that every recovered byte or checked copy is healthy. Job deletion removes its retained execution bundle, not physical copies, versions or original associations.

The durable completion checkpoint can precede the end of an active runner's cleanup. An attached runner reports `JOB_PHASE_COMPLETED` when its work and cleanup finish; a working phase still requires waiting. A completed Job without an attached runner reports `JOB_PHASE_UNSPECIFIED` and has no runner cleanup left to await. `FAILED` is an actionable result, and a `READY` Job whose runner is not working needs its operator. The [CLI waiting command](cli.md) succeeds for a completed Job even after its runner detaches.

The Executor records the latest attempt's start and finish in the common Job bundle record. Typed progress replies expose optional `elapsed_ms`: live server-measured duration while executing, then a frozen duration after success, error or cancellation. Waiting between attempts is excluded; a new attempt starts a new measurement. Historical Jobs without timing and attempts interrupted by process loss have unknown duration rather than a value inferred from catalog timestamps. Progress polling does not write timing or catalog revisions.

An admitted attempt waiting for its Media lease reports `QUEUED`. Each Tape drive and Volume UUID
has its own FIFO queue in resource-arrival order, without preemption or Job-priority scheduling.
Different resources run independently. A queued attempt remains cancellable and excludes another
start or Job deletion; the CLI continues waiting. Cancelling removes its queued request and does
not publish inventory. An Archive/Restore cancellation returns to pre-Media `READY` with its
reason and prepared manifest; a Scan cancellation is terminal `FAILED`. An idle Job awaiting
an operator's Media choice reports no live phase, and process restart does not recreate
queued attempts. Attempt settlement publishes the disappearance of its live phase to list readers.

Public typed progress also reports no live phase, denominator, rate or ETA while idle. Archive and
Restore retain their business counters; `total_known` reflects whether the manifest is complete.
Media actions require an idle `READY` Job; counters or manifest completeness do not enable a
`FAILED` Job.

## Execution Settings

The typed `jobs` Settings group supplies one complete set of pipeline limits. Defaults are
`read_batch=256`, `read_buffer_max=4096`, `write_buffer_max=4096`,
`write_batch_size=256` and `flush_interval_ms=1000`. Read batch and write batch must be at
least 1; each buffer is limited to 1–1,000,000 items; the write batch cannot exceed the
write buffer; and the flush interval must be at least 100 milliseconds. A malformed saved
group is an error rather than permission to substitute defaults. Each new attempt reads
the current group and keeps its own effective limits.

## Archive

[Archive indexing](../../internal/executor/archive/init.go) expands registered Library/Location `selections` and records each selected File and target in a durable [item manifest](../../internal/executor/archive/item.go). `target_path` is immutable input identity and unique for deduplication. `media_path` is empty while pending; only ACP's actual successful relative target populates it. Indexes on `(status, target_path)` and `(status, media_path)` serve copying and finalization respectively. Staging is on each item row, with no separate staging table. The v0.1.x migrator writes historical items directly into their frozen manifest.

[Selected input](../../internal/executor/location_selections.go) combines Library visibility with live Location roots and observed file facts. Location files need neither a File ID nor prior Analyze; the shared admission path creates or updates their associations. Bounded expansion applies Ignore, including an explicitly named path, and deduplicates overlapping selections during preparation; administrator authorization is never bypassed. Library selections use recorded associations and are independent of Location Ignore. Indexing records the File identity, its current content facts, and the Library-root-relative target path, including when selected through Location browsing. A selected directory may include Files without an original association; these are counted as missing originals during inspection and omitted from the prepared manifest. Preparation fails if no eligible regular Files remain. Later logical edits do not change a prepared target. [Content capture](../../internal/executor/content.go) reads original associations and validates Locations in bounded batches, observes each eligible original once and hashes unknown content through ACP. The existing Archive manifest rejects conflicting targets and removes repeated selections before capture, including overlaps across batches; completed batches are recorded before proceeding. Capture preserves selection order and shares the admission path used by individual original reads. Copying resolves the selected File's current original once; ACP's actual transfer result owns the archived bytes and publication facts. Failure to observe an associated original is an error, not an automatic Restore request. [Selection inspection](library.md#visibility-and-selection) reads bounded metadata without admission or hashing; it is a review, not the executable manifest.

[Live preparation](../../internal/executor/selection_stage.go) uses a disposable selection database for bounded observations, directory references and matching evidence. The matcher completes path, signature and native-identity rounds in order. Its observed File IDs and content facts enter the Archive `items` manifest; the temporary database is closed and removed after preparation. Preparation does not repeat filesystem observations to prove that the selected source stayed unchanged.

An optional companion Scan with Preview enabled starts only after Archive indexing succeeds, from the item paths and recorded content facts. It never independently re-admits the same live roots. Progress exposes its Job ID or separate creation error; Preview failure does not undo a prepared or completed Archive. That companion is a Job of its own: its preparation is not retried in place, and a new Scan repeats the complete selection.

One [Media operation](../../internal/executor/archive/media.go) proceeds in this order:

1. Acquire the target's exclusive attempt lease and create its typed Write Session.
2. Submit PENDING items to ACP in target order, one bounded manifest page per submission.
   Each item uses the current original Location's read mode; Archive does not regroup files by Location because that would change Media order and the prefix that fits on a full target.
3. Persist each successful ACP result as STAGED immediately, including the actual Media path, hash, size, mode, and times. The Archive records the bytes ACP transferred instead of running a second source-stability policy.
4. Call Write Finalize exactly once after ACP returns, including error/cancellation, using a cleanup context without the original deadline.
5. Publish only backend-verified STAGED items with `CommitMedia`. Create or reuse the selected File's version from the actual transferred facts in that same transaction; when those facts match the earlier observation, preserve its opaque signature. Raw inputs already have per-Job File associations; no later signature-based File merge or binding occurs.
6. Mark published items SUBMITTED with their Media ID; complete the Job when nothing remains pending. Return copy/finalization/publication errors after retaining eligible successes.

The [result stream](../../internal/executor/archive/stream.go) validates completed copy facts and hands them to the shared [result writer](../../internal/executor/result_writer.go): the results callback only starts a write of the outcomes a durable row can express, which ACP hands over in batches of up to `write_batch_size` items, and the writer stages them outside the feed. A staging failure is the writer's terminal error and fails the phase; a result no durable row can express, such as a target that refused its file, is a per-item outcome the attempt reports while it keeps the item PENDING. STAGED is a candidate result, not durable archive success. Initialization resets leftover unsubmitted STAGED items and attempt report data to PENDING; resetting also clears Media path and result. Only submitted files count toward durable progress, historical speed, ETA, and reports; verified zero-byte files count too.

An Archive Media operation ending in an error or operator cancellation settles the runner and returns the Job to its pre-Media `READY` state with its reason and timing. The prepared manifest and successfully submitted items remain; another explicit load-Media operation processes the remaining items.

[Media finalization](media-io.md) defines which candidates survive normal completion, cancellation, target-full conditions, or validation failure. Prefix directories have no batch identity or cleanup ownership.

## Restore

[Restore indexing](../../internal/executor/restore/init.go) accepts file/directory selections following a latest or inclusive recorded-time cutoff policy, plus explicit FileVersion IDs. [Shared selection resolution](../../internal/library/restore_selection.go) serves both inspection and indexing. Explicit versions replace automatic selection for their File, including a File inside a selected directory; several explicit versions remain separate outputs. Roots and overlaps are deduplicated with bounded traversal. It freezes version content, own mode/mtime, File identity and Library-root-relative target path in [selected File rows](../../internal/executor/restore/file.go), then pages signature-matched Positions into bounded physical [Copy rows](../../internal/executor/restore/copy.go). Missing copies are errors, not permission to substitute older versions.

No saved version, only newer archive dates, and unknown archive dates are unmatched selection results, separate from a chosen version with no usable copy. Unmatched selections block preparation by default. `skip_unmatched_versions` requires a cutoff and explicit consent; it skips only automatic unmatched selections, never missing-copy errors. An all-skipped selection cannot become an executable Job. Inspection reports unmatched/skipped counts and resolved version/date details only for bounded explicit regular-file roots or explicit versions, not an entire directory manifest. Directory membership and logical output paths use the Library at preparation, not historical directory snapshots.

The `files` table freezes one row per selected version, including content facts, logical name/parent, the reconnect decision and output state. `copies` contains only persisted Media-candidate facts. Execution explicitly assembles a private runtime candidate from one Copy row and the matching File row; it never hydrates execution fields into the persisted Copy type. The latest selected version per logical File is the only reconnect candidate. Once the manifest is frozen, the remaining Media work uses its persisted output paths without selecting newer versions from the Library.

A Restore Media operation ending in an error or operator cancellation returns the Job to its pre-Media `READY` state with its reason and timing. It preserves the prepared manifest and successful per-file/Media checkpoints; another explicit load-Media operation processes the remaining work without restarting the Job.

[Destination admission](../../internal/executor/restore_destination.go) freezes a registered Location's root, Executor and relative directory; preference is not permission and no full Scan is required. Each real attempt rechecks authorization and rejects a destination whose registration no longer matches; it does not exclude other Location operations. [Output reservations](../../internal/executor/restore/output.go) persist each item's actual path. Unowned equal content is adopted only after a real ACP read and metadata restoration. Conflicts use `stem.restored.<base36-version-id>.ext`, then numeric suffixes. Frozen-path conflicts fail rather than silently choosing new names. Migrated legacy Jobs retain their explicitly configured legacy root without guessed original association.

[Native name reservation](../../internal/executor/restore/output_names.go) uses temporary directory-only namespaces under each actual existing output parent, including parent aliases and nested mounts. Filesystem lookup handles case and Unicode equivalence; no SQL collation guesses those rules. A target whose new directories do not inherit its observed name rules is rejected, as are selected file/directory conflicts. Reservations are rebuilt from bounded Job pages when an attempt starts. Probe directories contain no file content, are excluded dynamically from original readers and are removed after indexing; interrupted leftovers contain only directories and cannot become indexed ordinary files.

The [Restore runner](../../internal/executor/restore/media.go) opens a typed Read Session and owns the ACP items of its candidates. The [results callback](../../internal/executor/restore/buffer.go) only starts a write of each batch ACP hands over, and the shared [result writer](../../internal/executor/result_writer.go) completes and stages those batches of up to `write_batch_size` items outside the feed; a failed persistence is the writer's terminal error and stops the run exactly like a caller cancellation, while the drain still persists every accepted item. One target that wrote nothing is an item outcome, not a pipeline failure: its copy keeps its PENDING status and the attempt reports that error while the other candidates of the Media are still persisted and published. Sequential-read Media follows backend storage order; random-read Media uses ordinary path order. [Copy completion](../../internal/executor/restore/stream.go) compares ACP's real hash and size with the expected content and restores the version's own mode/mtime. The output retains read-source and ready facts, but Library association and item/alternative completion wait for successful Session Finalize. Failed physical finalization retains complete bytes without claiming success; a later attempt cannot adopt its own unfinalized output as an unrelated preexisting file. Completion is per item/version, not every version of its File. Metadata restoration may invalidate disposable ACP caches; completed targets are not reread to rebuild caches. Restore does not share Archive's STAGED/submit state machine.

Candidate listing reports missing catalog Media explicitly, including its ID; it never silently removes a frozen candidate from the response. Catalog metadata must be restored before that candidate can be used.

### Restore Outcomes

Indexing designates the latest selected version per File by last archive time, then ID, with unknown times last. This candidate is frozen independently of Media order; failure or Ignore does not promote an older version. [Library publication](../../internal/library/restore_result.go) applies these rules only after complete content, version metadata and Media identity verification:

| Situation | Result | Retention and next Media operation |
| --- | --- | --- |
| Candidate; original File has no FileLocation | Reconnect that File | Preserve its organization, tags and Note |
| Any existing binding, or another selected version | New independent File | Frozen logical parent and version-suffixed name; only this saved version and evidenced archive dates, no copied tags/Note |
| Output matches Location Ignore | Restored, unlinked | Preserve bytes; disclose before copying; do not bypass Ignore |
| Library association or Job checkpoint fails | Content retained; error reported | Inspect the current output and association before another attempt; no receipt-based repair |
| Complete read mismatches expected bytes with salvage enabled | Recovered with damage | Retain actual bytes/facts, no expected-version or original association |
| Interrupted read | Failed attempt | ACP cleans partial target; no bad-block skip/fill or prefix salvage |

Library publication changes File, FileLocation and tracking in one metadata transaction, then the Job records its state. No permanent Restore receipt is written. If the Job checkpoint fails after Library publication, the error is reported; a subsequent attempt must not infer that an occupied path is its own successful output. A recovered FileLocation records the observation it made without claiming a Location-wide observation. Missing logical parents and occupied paths are association conflicts; an existing recorded original is never stolen.

Restore records individual read/target failures, including failure to read an existing output during
a Media attempt, and continues independent files. Destination authorization, unsafe paths and
cancellation still stop the attempt. Persistence failure stops the feed because outcomes can no
longer be recorded reliably. Draining retains every
accepted result; an attempt with an item failure still reports failure after draining. Reusing an
already verified staged output preserves its damaged flag. A Media session installs finalization
immediately after acquisition, including failures while constructing the pipeline.

Default Restore candidates are healthy or unchecked and are still validated during transfer. `allow_damaged_copies`, frozen in the specification and off by default, additionally permits damaged/previously unreadable copies after normal candidates; known missing copies are not candidates. It never relaxes identity/path checks. Results distinguish verified, damaged, unlinked and pending items; merely producing a file is not verified success.

## Scan

[ScanJobService](../../entity/job_scan.proto) owns one SCAN kind, [runner](../../internal/executor/scan/runner.go), manifest and [pipeline](../../internal/executor/scan/pipeline.go) for Location/Library `selections` or a whole Volume/Tape Media. One `LocationSelection` with an empty path selects an entire Location; selected paths and overlapping roots use the same bounded expansion. Only selections all naming one Location give the Job a single Location target, while properties retain all involved Location IDs. Content acquisition, comparison, Preview, validation and publication are stages, not delegated Jobs. Source adapters supply access, enumeration, order, leases and identity checks. Ordinary browsing and Refresh remain request-bound reads.

1. Freeze source bindings and old content baselines into the Job bundle. Media keeps identity, profile, expected Positions and storage order; Location selections keep the registered root and bounded selected scopes.
2. Enumerate real entries in ordered, bounded pages into the existing `entries` manifest. Logical selections expand once across all Locations; physical and logical overlaps share the `(location_id, path)` key. All selected sources finish preparation before content processing or Library publication begins. Location selections prune Ignore, including an explicitly named path; administrator authorization is never bypassed. Media inventory requires fresh physical enumeration, including the current trusted LTFS index for Tape; old Positions cannot stand in for it. Enumeration reports discovered files and bytes but keeps the total explicitly unknown until its manifest is frozen.
3. Acquire content facts under the signature policy below, optionally look up matching Library content, and optionally generate Preview assets in this same pipeline.
4. Finalize the physical Media session where applicable. Location publication uses the observations already produced by enumeration and content reads; it does not run a second filesystem pass to prove that the source stayed unchanged.
5. Publish the selected result policy through metadata-only Library transactions, then checkpoint the Job independently. Keep detailed results in the paged `entries` table; range descriptions come from the specification and current attempt.

A failed or cancelled Scan is terminal at every stage, including an admitted Media operation.
It keeps its observed manifest, processed counters and already published results for inspection;
they never enable another run of that Job. The operator creates a new Scan. A failed Scan does
not assert a frozen total without durable evidence that enumeration finished. Preparation failure
publishes no Library observations. Once content processing begins, failure retains earlier completed
Location publications.

Original enrichment and publication query only manifest paths in bounded batches. Explicit physical
ranges establish absence; logical selections and an empty manifest never imply whole-Location
coverage. Matching claims existing exact paths before relocation rules. It retains only selected
old File IDs and eligible detached IDs, then reads their current facts in global File ID order for
the shared path, signature and native-identity rounds. These scalar IDs scale with the matching
scope; paths, content and tracking rows stay in their existing paged stores.

| Signature policy | Content reads |
| --- | --- |
| KNOWN_ONLY | Reuse applicable observations or ACP's cache-only read; cache misses remain unsigned, with no hash fallback |
| FILL_MISSING | Reuse first, then hash unknown or changed ordinary files through ACP |
| FORCE_READ | Read every selected ordinary file; cached facts cannot substitute |

Valid cached SHA-256/size facts do not replace opaque YATM signatures. Matching prior content retains its existing signature. Reuse requires applicable metadata and binding/native evidence; metadata-preserving changes require FORCE_READ. Cache read failures never silently turn KNOWN_ONLY into hashing. A Scan records what its reads return under the supported single-operator content-stability model; it does not implement snapshot or drift-detection semantics.

Indexing resolves and stores each selected source path once; the content phase reads that stored path without re-resolving it or comparing before/after metadata. The phase submits caller-owned items to ACP one `read_batch`-sized manifest page at a time, and its results callback only starts a write: it performs no I/O and never waits for a database write, so the one place it waits is a busy writer. ACP owns the result queue and the batch, and the shared [result writer](../../internal/executor/result_writer.go) persists each batch the engine hands over through at most eight concurrent write goroutines and records the first write error once; that error stops the feed and fails the phase, and the drain before the phase returns persists everything the callback accepted. Verification persists a failed item as a per-file finding and continues, while every other result policy reports the cause and stops its scope. `read_batch` is the manifest page, and `read_buffer_max`, `write_buffer_max`, `write_batch_size` and `flush_interval_ms` are ACP's read buffer, result queue, result batch and result flush interval, so the engine owns the queue and the delivery cadence while the runner owns persistence.

| Result policy | Publication boundary |
| --- | --- |
| REPORT_ONLY | Retain results without admitting Files or replacing inventory; requested derivative assets may be generated |
| PUBLISH_ORIGINALS | Publish the observations produced for each selected Location range; an unrecoverable read failure stops later Locations |
| PUBLISH_INVENTORY | Automatically replace inventory only after complete trustworthy Media observation and identity validation; no Apply step |
| VERIFY_COPIES | Require real uncached reads against frozen old Position baselines; publish guarded historical check findings, not replacement content |

Inventory publication permits unsigned content, does not manufacture health checks, and preserves applicable prior bad observations. A genuinely empty observed range can remove original references or inventory Positions, never File organization or saved history. Location publication covers the manifest observations directly; it does not omit entries because a later `stat` differs. An unrecoverable read failure stops later Locations, while earlier published Locations remain; the failed Scan is terminal, and a new Scan covers the full selection again, including the Locations that already published. Inventory failures preserve the previous inventory. Library and Job commits remain separate, with errors reported directly.

Progress is stage-local rather than a false whole-Job percentage. One decision in the shared [stage helper](../../internal/executor/stage_progress.go) answers what a phase may display: phases `PROCESSING_CONTENT`, `VERIFYING_MEDIA`, `GENERATING_PREVIEWS`, `COPYING_TO_MEDIA` and `COPYING_FROM_MEDIA` may show a percentage and an estimate, `COMPARING_CONTENT` and `COMPLETED` may show a percentage, and every other phase reports counters alone. Content acquisition opens its window once per attempt from the durable manifest — the active scope's denominator and its already completed work — and ACP work events advance it from there; comparison uses manifest outcomes for its processed/total window, Preview uses its attempt's own completions over the manifest's denominator because a successful Preview is never stored per entry, and validation, waiting and publication name their phase without inventing a denominator. A new Location Scan Job builds fresh observations and progress; it does not resume a failed or cancelled Scan. Completion reports the non-removed manifest total.

All typed Job progress replies include the shared `StageProgress` contract: a window identity, phase, work unit, processed amount, optional total/rate, and explicit ETA state. `total` is present only where the phase's rule allows a ratio, so a phase with no denominator cannot become a false percentage even when the Job's business total is known; those phases report the manifest's cumulative counters in `Progress.total_*` and never re-query them per request. The runner locks phase and range observation while assembling the snapshot; clients use that snapshot's phase rather than combining it with independently refreshed catalog state. Scan read and Preview windows refer to the active source scope, while existing business counters retain their own cumulative meaning. Attempt, phase and source changes discard old samples. Zero-byte copy workloads use item counts instead of a false byte percentage, and a zero-byte content scope does the same.

The bounded [stage helper](../../internal/executor/stage_progress.go) uses wall-clock throughput, minimum five-second sampling windows and EWMA smoothing (0.25). Three productive windows are required; Preview also requires three actual decoder-start/completion observations. Samples come from work rather than from polling: Scan read, Archive write and Restore read sample the one-second ACP progress event that already advances their counters, and Preview samples decoder completions, while a progress request only reads the recorded window without recording a sample. Cache hits and joined content-addressed work never become generation samples. Preview measures decoder items: its work unit is never the bytes of the content whose previews settled. Preview first streams ordered content groups and reads existing manifests to classify its unique generation workload without hashing or invoking a decoder; only one group is retained, and SQL does not grow per row. Runtime disabling makes its workload unknown. Estimates exclude later stages and remain `ESTIMATING` until their own denominator and samples are meaningful. A gap longer than the greater of 30 seconds or three observed average completion intervals invalidates the rate; three productive windows restore it, and because the work path observes that gap, a stalled stage is still detected while no card is visible. A merely slower-than-average file no longer repeatedly removes the ETA. Comparison, waiting, validation, publication and finalization expose `NOT_APPLICABLE`, never a fabricated remaining time.

Job cards use one stable metric layout and the server-provided stage percentage/estimate for every kind. One shared module owns the frame, the counter wording and the waiting/active/terminal classification behind the status label and the action buttons; only active work animates a bar of unknown length. Polling keeps the last coherent snapshot through refresh/error/offscreen transitions and rejects obsolete requests, and it never changes an estimate, because every sample is recorded on a work path. Current and session-average byte speeds remain distinct from persisted business results; elapsed time stays the common latest-attempt measurement.

Scan uses the same append-only `jobs/<id>/job.log` and public log reader as Archive and Restore. Logs are retained until Job deletion without disk rotation or a size limit; bounded log reads do not bound retained disk usage. Logs record phase and source-range boundaries, read outcomes, Preview starts/results and summary counts with duration; high-frequency progress events remain counters rather than log lines. Per-file completion does not imply that the source range has been published.

The Location walker reads at most 256 directory entries per call and rejects traversal beyond 256 directory levels; its recursive implementation can therefore retain at most one open directory handle per allowed level. Manifest traversal, matching and publication use 256-row ordered pages. Public Scan result queries return at most 1,000 rows per request. These are structural bounds, not a fixed RSS guarantee: the per-Job SQLite manifest and its journal still scale with the number of observed entries, while callers must replace result windows rather than retain the whole manifest.

Every Job manifest listing shares one page request: a bounded `limit`, an order key cursor, an `order` direction, an `offset`, and an optional total. The listing defines one order key and applies the same clauses in every case; a cursor restricts the sequence to rows beyond that key in the requested direction, an offset addresses a position in the filtered sequence, and the two combine without a separate jump mode. A page reads one sentinel row to report `has_more`, and continuous paging uses the cursor so a page never re-walks the rows it skips; `offset` is only how a client anchors a jump to a position it has not loaded. `include_total` additionally reports the filtered result-set size, which is the height a client needs to present the manifest as one list; a request without it issues no count. Scan orders by entry ID, Archive by the frozen logical target path, and Restore Media by Media ID; Restore Files orders by the composite `(media_id, id)` and carries an index for it, so one sequence spans every required Media.

Verification findings distinguish match, mismatch, missing, unreadable and unavailable baseline. A per-file issue permits continued checking; device errors/cancellation retain completed observations and leave the rest unexamined. Session finalization failure prevents health publication. Expected hashes are never replaced by damaged bytes. Mounted Volume scans execute automatically; Tape scans wait for an explicit matching device through ReadMedia. Waiting holds no Media lease. Sequential sources do not support Preview or implicit Restore/staging.

Preview is optional and off by default. Its policy selects missing-only generation or forced regeneration of all identified content. KNOWN_ONLY without a usable identity skips generation and counts it. Generator failures are per-item findings, not a rollback of otherwise valid scanning. Asset addressing and validity belong to [Preview storage](preview.md).

## Analyze

Location analysis is the original-publication configuration of [Scan](#scan), not a separate runner or Job kind.

## Verify

Integrity checking is Scan's VERIFY_COPIES result policy; [copy health](media-io.md#copy-health) remains independent of inventory and accessibility.

[Matching](../../internal/executor/observation/matching.go), shared with live Archive preparation, runs ordered rounds over the current complete Location selection: path → available signature → native identity. One shared observation-store implementation applies matching to the common identity/evidence columns of disposable Archive observations and durable Scan entries. Each caller supplies its own scoped table; consumer tests verify that assignments cannot cross the admitted Location. Matching evidence lives in the owning row. Old File IDs and new paths are ordered; later rounds never override earlier matches. Matching does not hash. Unmatched entries become independent Files; occupied paths preserve their File without retaining invalid content facts.

Cross-Location reassociation requires a successfully removed old binding and valid same-Executor tracking evidence. Unavailable Locations and unobserved ranges cannot lose bindings to another analysis. Native tracking keys are derived from existing stat observations and published with originals; matching never reads or writes tracking xattrs. Matching is organization continuity, not proof of move versus copy-and-delete.

## Location File Operations

Ordinary mutations use the shared [organization engine](library.md#shared-organization-interface), not Jobs. Library edits, browsing, annotation and manual original relocation also remain request-bound. Initial whole-Location collection uses Scan with KNOWN_ONLY and PUBLISH_ORIGINALS.
