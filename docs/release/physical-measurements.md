# Physical Tape Measurements

Repeatable hardware measurement procedures for Restore timing, performance comparison and capacity
boundaries. The [physical Tape E2E suite](physical-tape-e2e.md) owns gate cases and safety
preconditions, which apply here as well; its PT-07 boundary case uses the capacity procedure below.

## Fixed Partial Restore Benchmark

The shared [population](../../e2e/fixtures/restore-1p5tb-v1/manifest.json) and
[selection](../../e2e/fixtures/restore-1p5tb-v1/selection.txt) define one immutable synthetic
workload; the manifest owns its file count, sizes and paths, and units are decimal. It is a
reproducible reference, not a claim about an observed user collection. Never resample it, choose
a more favorable seed or replace failed/fallback observations; a deliberate replacement uses a new
fixture identity and preserves the previous comparison. `go test ./internal/media` checks the
frozen hashes. No maintained tool materializes the physical corpus; a physical run first needs a
reviewed materializer, the full source plus selected Restore output budget, and receipts kept
outside the corpus. Archive placement is established by actual recorded locators or the final LTFS
Index, not by manifest order; an incomplete population does not satisfy this benchmark.

Use the same corpus, frozen selection, recorded layout, source build, parameters and completion
policy for paired LOGICAL/AUTO/AUTO/LOGICAL arms with fresh Restore destinations. Keep hardware
compression off for this smart-ordering comparison. Measure admission through normal publication
and unload, including preparation, first-file access, planning, all reads and the final return.
Report content verification separately, actual executed order, LOGICAL fallbacks, read/discard and
positioning observations where available, first-file latency, elapsed time and resource curves.
One fixed sample supports only its measured result; do not substitute the earlier sparse controls'
percentage or compare AUTO against an artificially random execution order.

The software-only forecast uses the production tar evaluator and optimizer with the frozen paths
and sizes in ordinary Archive target-path order, assumed nominal 80-wrap uncompressed geometry
and fixed HP5-derived timing priors. It includes prepared object 2 through final unload, but no
common label-preparation time.
Its `ns/op` measures local planning, index consumption and cleanup; model seconds are predictions,
not physical Restore timings. Rejected lower central estimates remain distinct from an adopted route:

```shell
go test ./internal/media -run '^$' -bench '^BenchmarkReadSequenceRestore1p5TB$' \
  -benchtime=1x -count=1 -benchmem
```

## Bounded Performance Comparison

Use `performance-prepare`, `performance-format`, and `performance-run` with a new
state and isolated installation for each compared build. Capacity uses a fresh formatted
installation without performance-case writes. Preview is optional. Preparation creates a deterministic
64 KiB setup Archive; FORMAT uses ordinary packaged data-partition placement,
without `-r size=1M`. Measured cases APPEND, normally unmount, validate the captured
final Index and Library publication, then Restore and verify actual output bytes.

Transfer `e2e/physical_performance_*.py` with the controller and `e2e/package*.py` modules.
`make test-package-tools` runs their local regressions with the package-controller tests and
mocked device observations, without Tape access. SVG rendering additionally needs `matplotlib`;
its rendering test reports a skip when that optional dependency is absent. Measurement parsing,
fixture checks and verification tests still run.

Supply the explicit package, host, device, barcode and evidence arguments. Use
`--performance-source-read-mode buffered` or `mapped` consistently across stages;
mapped uses the existing Location `--use-mmap` setting. Select cases with repeated
`--performance-case`. `--performance-size-gib` accepts 4 (default), 32 or 64 GiB per
case, saved in state. The fixed order and file sizes are:

| Case | File size | Content |
| --- | ---: | --- |
| `small-random` | 256 KiB | Deterministic SHAKE-256 bytes |
| `small-compressible` | 256 KiB | Adjacent duplicated 4 KiB random blocks |
| `large-random` | 1 GiB | Deterministic SHAKE-256 bytes |
| `large-compressible` | 1 GiB | Adjacent duplicated 4 KiB random blocks |

At 32 GiB, a small case has 131,072 files and a large case has 32. Generation emits
an external JSON Lines SHA-256/size manifest. CLI manifest pages contain at most
1,000 rows; a private validation database checks selection, duplicates, content,
previous Positions, final Index extents and Restore order without whole-manifest
maps. Index parsing and artifact transfer stream; actual restored-byte checks use
batches of at most 64. Source/Restore fixtures are removed only after complete
verification; Job bundles and evidence remain. Interrupted cases are preserved
for explicit diagnosis. Each scene requires twice its logical size plus 8 GiB
of free evidence headroom.

Media timing includes admission through normal finalization and publication.
Fixture generation, preparation and external content checks are outside it. Keep
input bytes/order, source mode, compression/encryption, LTFS settings and physical
layout equal. Source generation and Archive preparation affect page cache; record
cache preparation and host load rather than claiming a cold-cache measurement.

Independent bounded pollers record application progress about every two seconds,
drive metrics about every five seconds and execution-host resources about every
five seconds. One writer streams JSONL, with request wall/monotonic start and end,
errors and timeouts. Drive reads cannot block the application polling channel.
Use `--no-performance-driver-sampling` consistently across stages to measure collector
overhead. The sampler reads xattrs only through the LTFS process whose exact
`work_directory` identifies the admitted Job; it never opens a competing SG reader.
Timeout cleanup kills only the sampler subprocess group.

The controller retries only read-only Job waits when Get reports
a SQLite lock, within the original deadline and with every failed transcript kept.
Delays grow from 2 to 4, 8 and 16 seconds, then remain capped at 30 seconds;
the remaining observation budget bounds the final delay. Attempts, lock errors
and chosen delays remain in the report. There is no separate attempt-count limit.
It never repeats a Create or Media action; durable failure and other errors stop
the stage for inspection.

Application `copied_bytes` during COPYING describes ACP source/handoff progress,
not physical commitment or final Library publication. ACP `copied_file_count`
increments when work enters content processing, so its curve is labeled file-work
entries rather than completed files. Final Index/Library and restored-byte checks
establish completed content separately. Stage changes, resets and missing samples
break interval arithmetic. Root xattrs `user.ltfs.vendor.IBM.logPage.34.00`
and `.0C.00` retain their raw bytes and request times. Binary page framing, required
widths and duplicate parameters are checked before decoding. Page 0C provides
Sequential Access byte proxies; page 34 parameter 0 reports repositions per 100 MB,
not cumulative events. Equal values do not prove freshness or its absence.

Analysis emits observations, intervals, resources, fixed 10/30-second bins,
all candidate windows, auxiliary drive samples, controller milestones, file-work
entry rates, COPY phase-edge brackets and summary CSV/JSON plus a standalone SVG.
Phase durations retain request-boundary uncertainty; unknown edges stay absent.
The observed byte-endpoint subset rate is distinct from a full successful COPY rate. Matplotlib is needed on the controller only for
plot generation. Counters produce rates over actual sample intervals; UI smoothed
speed is not averaged again. Invalid gaps remain gaps, and zero increments mean
only that no progress was observed in that interval.

The first observed positive ACP increment anchors candidate windows every ten
seconds. Each 180-second window has six 30-second bins. A qualifying window needs
at least 90% valid time coverage per bin, no gap longer than twice the nominal
sample period, rate coefficient of variation at most 15%, and first/last 60-second
mean drift at most 10% of the window mean. Only valid adjacent increments are
allocated by time overlap. Rates with incomplete coverage describe covered time.
These fixed engineering thresholds classify application averages; they are not
vendor guarantees, physical-streaming proof or optimization acceptance thresholds.
The first qualifying window and every later candidate remain visible, including
periodic no-progress intervals that stable averages can hide.

Use the complete curve to determine whether a longer fixture is necessary. Fix
sampling gaps before extending duration; persistent low speed and repeated pauses
are results to diagnose. Compare one matched baseline/candidate pair first. When
noise prevents a decision, use at most one reversed affected-case pair. Keep a
change only when throughput or reposition behavior demonstrably improves without
a confirmed regression in the other measure, correctness or bounded resources.
A short successful run or failed candidate alone does not establish a hardware
limit. This flow does not claim capacity-exhaustion or release acceptance.

## Capacity Measurement

### Continuous Boundary Capture

Use one planned Archive run to compare the capacity sources on the same timeline. Reuse
`performance-prepare` and `performance-format` for a fresh isolated installation, then select
`capacity-write` and `capacity-verify`. Transfer `e2e/physical_capacity_*.py` and
`e2e/physical_performance_*.py` with the controller and `e2e/package*.py` modules.
This is the PT-07 ordinary Archive and full published Restore path.

Choose the cartridge capacity and input size before starting. A separately authorized hardware
capacity reduction can shorten an experiment: HP LTO-5 documents SET CAPACITY `0x0FB3`, roughly
92 GB, in its [integration guide, page 15](https://docs.oracle.com/cd/E21419_04/en/LTO5_Vol2_E3a/LTO5_Vol2_E3a.pdf#page=15).
It erases the cartridge. Verify actual drive support, beginning-of-medium position and measured
capacity. Keep that reduction during FORMAT with the implementation's `mkltfs --keep-capacity`
option; ordinary formatting can restore full capacity. Record partition sizes after formatting.
Restore full capacity only after the experiment's evidence and readback are preserved. Reduced
media establish that configuration's boundary behavior, not a full-length capacity guarantee.

Pass `--capacity-observe` during `performance-prepare` and repeat it for each later stage.
The controller installs `physical_capacity_boundary.py` in the isolated adapters after successful
mount and before returning to YATM. Pass `--work-directory "$TAPE_DIR" --mount "$MOUNT_POINT"`,
`--phase before-copy` and `--output "$TAPE_DIR/capacity-before-copy.json"`, using the reviewed
absolute helper path. It verifies required probes before copying. At the start of normal unmount,
capture `--phase before-unmount` into `capacity-before-unmount.json`; retain a failed observation
and proceed with normal finalization. These passive hooks do not synchronize or pause an active
copy. Unload clears some HP error counters, so the final mounted snapshot is part of the evidence.

Pass the FORMAT Job's saved `capacity-before-copy.json` as `--capacity-preflight` to
`capacity-write`. It checks the owning installation/Job and usable starting page 31, page 0C
and `statvfs` before generating payload. The live before-copy hook additionally requires raw page 17
before permitting payload writes. Each LOG page uses one Linux xattr read with a 64 KiB buffer;
this avoids LTFS's oversized-reply error for a small caller buffer and double reads of read-cleared
counters. The default is 24 ordinary 4 GiB files, with disjoint
deterministic AES streams and an immutable SHA-256/size manifest; `--capacity-files` selects
1–100. Repeated `--capacity-file-mib` instead provides up to 100 ordered sizes, allowing large
files for the main transfer and smaller files approaching the measured boundary. Freeze this
selection before the run. Reserve twice the payload size plus 8 GiB on the source/Restore filesystem.
For a filesystem supporting Linux FICLONE, `--capacity-source-reflink` instead generates one
pseudorandom base, clones ordinary files and gives each a distinct pseudorandom prefix. Each
file receives a full size/hash manifest before Archive. This saves source-disk space; every byte
still passes through Archive and the drive. It requires the payload size plus the largest file
plus 8 GiB for source and Restore space. Check this generator on the selected filesystem with
a small fixture first; unsupported cloning fails explicitly. Repeat the flag on later stages.

Use the [stage launcher](physical-tape-runbook.md#launch-one-durable-host-local-stage) with a separate `STATE` and new outputs for each capacity stage.
Its baseline-only stage selector must be replaced by the following selector. Keep the `common`
package/identity arguments and construct capacity options from the frozen workload before launch:

```bash
case "$STAGE" in
  performance-prepare|performance-format|capacity-write|capacity-verify|cleanup) ;;
  *) echo 'Select one documented capacity stage' >&2; exit 1 ;;
esac
capacity_options=(--capacity-observe)
# Optional: add once at preparation and repeat for every subsequent invocation.
# capacity_options+=(--capacity-source-reflink)
# Set FILE_MIB to the reviewed ordered list (1–100 files, 1–65536 MiB each).
for size in "${FILE_MIB[@]}"; do
  capacity_options+=(--capacity-file-mib "$size")
done
if [[ "$STAGE" == capacity-write ]]; then
  capacity_options+=(--capacity-preflight "$FORMAT_PREFLIGHT")
fi
# For cleanup, set cleanup_options as described in the physical Tape E2E guide's Cleanup Evidence Prerequisites;
# for other stages, use cleanup_options=().
printf -v stage_command '%q ' python3 e2e/physical_package_acceptance.py \
  "${common[@]}" "${capacity_options[@]}" "${cleanup_options[@]}" \
  --physical-stage "$STAGE" --out "$STAGE_OUT"
```

`FORMAT_PREFLIGHT` is the saved FORMAT Job's `capacity-before-copy.json` from this installation;
it is not a fresh device query. `FILE_MIB` is a Bash array populated from the approved workload.
Alternatively use `--capacity-files N` for N ordinary 4 GiB files. Repeat the source mode, fixture
sizes, observation and reflink options on subsequent stages. Preview is optional for this sequence,
including `cleanup`. Preserve the written cartridge after cleanup.

The capacity sampler records application progress and owned LTFS readings every second. Its raw
pages are 31, 0C, 17, 02 and 34, alongside `statvfs`, bounded file metadata including temporary
outputs, and resource observations. All channels use the host's wall/monotonic request times;
each sequential device query retains its own start/end time. Preserve gaps, unsupported fields,
counter resets and observation delays. Job byte progress can include rejected work; it is not
actual Tape throughput or a buffer-occupancy measurement. File-work entries and file size changes
do not establish successful Close, final publication or readable content.

Read TapeAlert through `user.ltfs.mediaStorageAlert`, which lets LTFS retain flags in its own
cache. Reading raw page 2E independently can consume read-cleared flags before LTFS observes them.
Keep the returned bitmask and query times; this observation must not hide a warning from its owner.

To distinguish the copyer's actual capacity reads from the independent sampler, an isolated
service can run under `strace -D -f --seccomp-bpf`, selecting `statfs` and the required file
lifecycle calls. Verify that the service's main PID remains the HTTP listener and that syscall
filtering works before admitting Media. Keep raw syscall arguments, results, durations and
process identity; a capacity reading alone does not identify its caller. Go's direct `statfs`
syscall is not intercepted by a libc preload wrapper. Keep this optional observer with the
isolated service until normal settlement, and record its potential measurement cost. Before
cleanup, finalize and explicitly supply every raw output under
[Cleanup Evidence Prerequisites](physical-tape-e2e.md#cleanup-evidence-prerequisites); files outside Job bundles
are not captured by the Job collector.

A capacity error returns nonzero after retaining observations, item rows, Job/LTFS logs, final
Index and Library results. Inspect those results before running `capacity-verify`; the latter
requires observed settlement and device release, then restores every published file and verifies
its bytes. Restore retains continuous samples and allows eight hours for settlement; an empty
published selection cannot pass readback acceptance. No stage automatically repeats a Media operation. A software refusal before creating
the next file, a partial-write failure, and successful readback are separate findings.

Analyze the saved stream without accessing hardware:

```sh
python3 -B e2e/physical_capacity_analysis.py /private/evidence/archive-samples.jsonl \
  --job-log /private/evidence/job.log --output /private/evidence/capacity
```

The analyzer writes observation/interval/page/event CSVs, a JSON summary and, with matplotlib,
aligned SVG/PNG curves. The optional same-host Job log adds its first error as a one-second
interval and a separate plot of the 15 seconds on either side. Request bounds remain visible;
resets, gaps and partition-coordinate changes break
curves. Compare remaining-to-EW, remaining-to-partition-end and filesystem availability
with byte counters and error events. Their overlap does not alone establish causation or an
exact physical byte position. Page 17 remains available as raw parameter data for vendor-specific
interpretation; an unsupported field is not synthesized from another source.

### Continue to the Drive's EW Boundary

Use [physical_ew_run.py](../../e2e/physical_ew_run.py) for an explicitly authorized
scratch-cartridge experiment that must continue beyond application admission. This is an
LTFS/drive measurement; ordinary Archive acceptance remains the workflow above. The runner
verifies the cartridge, formats once, mounts the installed LTFS and generates an incompressible
AES stream directly into ordinary files. Source data need not occupy another filesystem.
It stops new payload at the first write/Close failure, observation failure or frozen workload
bound, records partial-file cleanup, then attempts normal Index finalization and eject once.
The written cartridge and evidence remain available for review.

Run it in the host-local tmux owner using the quoted command/exit-file construction under
[Launch One Durable Host-Local Stage](physical-tape-runbook.md#launch-one-durable-host-local-stage). Preserve the numeric controller exit
status with a single `printf '%s\n' "$stage_status"` command; verify that the exit file contains
one integer before interpreting it. An empty file is missing evidence, not exit zero. Keep the
controller's structured lifecycle, LTFS log and captured Index as independent completion evidence.
Supply a new output directory, the verified device aliases,
installed LTFS tools, the `yatm-lto-info` executable and the explicitly authorized barcode.
Choose `--max-bytes` above the measured native capacity for this incompressible workload;
reaching that bound is a stopped experiment, not an observed EW. The optional
`--expected-native-min-bytes` catches an unintentionally shortened cartridge before format.
The CLI requires `--authorize-erase` to repeat the scratch barcode. Existing run directories
cannot replay the destructive sequence.

Build the [test-only SG observer](../../e2e/physical_sg_trace.c) on the Linux host and run
its fake-device tests before using it:

```sh
python3 -B -m unittest discover -s e2e -p 'test_physical_sg_trace.py' -v
cc -std=c11 -O2 -Wall -Wextra -Werror -shared -fPIC \
  e2e/physical_sg_trace.c -o /private/evidence/physical_sg_trace.so -ldl -pthread
```

The runner loads this library only into its foreground LTFS process. It records SG_IO command
times, raw CDBs, status, residual and sense, with bounded LOG SENSE and READ POSITION responses.
File payload is excluded. Optional short READ POSITION queries execute after clean page 31
queries on the same device descriptor, before the owning call releases LTFS's device lock,
at most once per second. Verify this locking path in the installed LTFS build. The preflight
requires usable filesystem/page readings, initialized tracing and a successful position reply.

A position query can receive a deferred write error. Its raw response is retained separately;
an injected error disables further injection, creates a trace error marker and stops new payload.
Such a result establishes observer interaction, not the unmodified LTFS error path. Every command
keeps its own timing, so query cost and sample gaps remain measurable. Passive snapshots use the
retained TapeAlert cache and continue briefly after payload failure; command tracing remains
active through the normal unmount. Evidence contains per-write application returns, file hashes,
Close/rename/delete results, mounted attributes, LTFS logs and captured Indexes.

Analyze the complete or still-growing capture without accessing the drive:

```sh
python3 -B e2e/physical_ew_analysis.py \
  --sg /private/evidence/ew-run/sg.jsonl \
  --samples /private/evidence/ew-run/capture/samples.jsonl \
  --events /private/evidence/ew-run/capture/events.jsonl \
  --metadata /private/evidence/ew-run/capture/metadata.json \
  --output /private/evidence/ew-analysis
```

The analyzer preserves partition attribution, unknown position/buffer fields, query intervals
and current/deferred sense. With optional Matplotlib installed it writes full-run curves and,
when EW is observed, a boundary enlargement from 120 seconds before to 30 seconds after EW.
Successful EW responses have a separate command-warning count from command errors. Compare
observations immediately before and after actual EW with
their different endpoints: page 31 remaining partition capacity, page 0C EOD-to-EW distance,
LTFS availability and logical READ POSITION. A filesystem error or EOP flag alone does not prove
a successful WRITE's EW response, and logical blocks are not physical tape length. Report what
the tested workload establishes separately from a general prediction or complete-file guarantee.

### Interpretation and Additional Observations

Use the [Tape capacity study](../research/tape-capacity.md#remaining-research-boundary) to
choose the question and success criteria before a capacity experiment. Assess pre-write refusal,
retained file readability, cleanup, Index finalization and accurate Library publication separately.
Screen candidate policies against the [continuous data-supply constraint](../architecture/tape.md#tape-capacity)
before hardware work. Record observation-induced stalls and reposition activity as well as throughput;
a diagnostic synchronization experiment does not qualify recurring forced drains for production.
PT-07 establishes the observed publication and readback of the tested run; it does not guarantee
that every admitted file will fit on other cartridges or workloads.

Use one host-local hardware executor for both capacity and performance experiments. Record the
actual LTFS executable and build, backend, mount options, drive firmware and cartridge identity
with each run. Read drive information through the mounted LTFS implementation's supported
interface; its device lock coordinates the observation with active I/O. An unavailable counter
is an explicit gap, not zero and not permission for another process to access the SG device.

For a capability investigation on an idle, unmounted Linux drive, use the maintained collector
after confirming completed Jobs, device ownership and the assigned cartridge:

```sh
python3 -B e2e/physical_tape_observe.py --device /dev/sgN --output /private/evidence/new-observation
```

Substitute the resolved SG device and a new host-local evidence directory. Run under the existing
host-local executor; load and final eject remain explicit operator steps. The collector checks all
Tape aliases before each query, requests complete raw LOG SENSE and READ POSITION responses, and
records timings and errors. Ownership checks do not reserve the device: keep other users and Jobs
out for the entire experiment. Queries with unsupported fields remain recorded failures, not zeros.
The collector never positions or writes the Tape. Separately approved read-only positioning can
compare the same fields at different locations; record its exact commands and completion status.
`--cartridge-memory` additionally reads HP's documented CM buffer up to its advertised size, capped
at 8 KiB. Keep all raw output private because device pages and cartridge memory can contain
identifying information. Publish only the relevant findings in the capacity study. Fast queries
on an idle drive do not establish acceptable polling cost during Archive.

For a physical-position comparison, add `--position-probe --cartridge-memory` immediately after
each completed authorized position change. This captures full fixed/descriptor REQUEST SENSE
replies and tests the vendor READ POSITION form before the normal observations. REQUEST SENSE can
consume pending sense information, so this mode belongs only to the isolated idle-drive experiment.
Compare beginning, an existing intermediate logical block and recorded EOD in the same partition;
confirm actual logical positions before interpreting any changing field. Preserve response lengths
and unsupported-command errors. Compare CM pages separately from live replies: unchanged stored
EOD information does not describe current head movement.

Capture timestamped raw capacity replies and the `statvfs` block counts and units alongside
logical bytes submitted, file completion, explicit synchronization and final Index results.
Keep request start/end times: separate device queries are not one atomic observation. Interpret
partition numbers using the FORMAT log and label, and record LTFS capacity adjustments before
comparing raw drive units with filesystem available bytes. A passive sample does not synchronize
the filesystem. Synchronization experiments record the requested operation, duration and error
separately from the resulting capacity sample.

When diagnosing an authorized capacity run, preserve its available write-error, corrected-rewrite
and TapeAlert observations before unloading. On HP LTO-5, LOG page `0x02` is specific to the current Tape,
updates on physical data-set writes and clears on unload. Its parameters count different units
(including data sets and CCQ sets), so a retry count is not a byte count. Treat reload, reset,
unsupported fields and counter decreases explicitly instead of subtracting across them. Establish
the effects of head or media degradation from the drive's documented write/rewrite mechanism and
capacity guarantees. A run on one cartridge of unknown condition cannot prove those general
properties; it supplies observations of that run only.

Compare native capacity consumed with both logical content and corrected-write activity for
compressible and incompressible input. The [capacity study](../research/tape-capacity.md#compression-and-rewrites)
explains why a prior compression ratio or retry allowance does not prove that the next complete
file will fit. Record successful admission, rejection before output creation, and failure after
partial output as different results. End-of-media alone is not a passing pre-write refusal test.
