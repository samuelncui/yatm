package scan

import (
	"context"
	"fmt"

	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/internal/executor"
)

// contentTotals is one content attempt's durable denominator and completed base. The manifest
// supplies it once, before the stage becomes observable, because the work path advances every
// later number and a progress request does not aggregate the manifest.
type contentTotals struct {
	// manifest counters stay cumulative over the Job's indexed manifest.
	manifest executor.Counts
	// scope counters are the active range's totals and completed base, which the content stage
	// uses as its own denominator.
	scope     executor.Counts
	scopeDone executor.Counts
}

func (r *runner) progressSnapshot(ctx context.Context) (*entity.Progress, error) {
	// Runtime speed is attempt-local; durable rows remain the authority for completed stage work.
	r.lock.Lock()
	defer r.lock.Unlock()
	live := r.progress.ToEntity()
	phase := r.phase
	in, err := r.stageInput(ctx, phase)
	if err != nil {
		return nil, err
	}

	// A progress request only reads the window the work path recorded; it never samples. The stage
	// denominator is not the business total: a phase may know its cumulative total and still have
	// no percentage, and the manifest total stays reportable either way.
	result := r.progress.StageReport(in)
	result.StartedAtNs, result.TotalKnown = live.StartedAtNs, in.TotalKnown
	result.CopiedFileCount, result.CopiedBytes = in.Completed.Files, in.Completed.Bytes
	// The cumulative business total is one fact, independent of the stage's own denominator: a
	// phase without one still reports what the manifest holds once it has rows.
	manifest := in.Manifest
	if manifest == nil {
		manifest = in.Total
	}
	if manifest != nil {
		result.TotalFileCount, result.TotalBytes = manifest.Files, manifest.Bytes
	}
	// Throughput is meaningful only where ACP is transferring, so every other phase reports none of
	// it even though the same Progress object keeps the attempt's history.
	if phase == entity.JobPhase_JOB_PHASE_PROCESSING_CONTENT || phase == entity.JobPhase_JOB_PHASE_VERIFYING_MEDIA {
		result.SpeedBytesPerSecond, result.AverageSpeedBytesPerSecond = live.SpeedBytesPerSecond, live.AverageSpeedBytesPerSecond
		result.HistoricalAverageSpeedBytesPerSecond = live.HistoricalAverageSpeedBytesPerSecond
	} else {
		result.SpeedBytesPerSecond, result.AverageSpeedBytesPerSecond, result.HistoricalAverageSpeedBytesPerSecond = 0, 0, 0
	}
	return result, nil
}

// manifestCounts are the manifest's cumulative counters over its non-removed entries. The work
// path counts them once when a phase that reports them starts and advances them as it writes rows,
// so a progress request never aggregates the manifest.
type manifestCounts struct {
	found     executor.Counts
	processed executor.Counts // entries whose content facts are settled
	compared  executor.Counts
}

// countedPhase reports whether a phase reports manifest counters outside the content stage window.
func countedPhase(phase entity.JobPhase) bool {
	switch phase {
	case entity.JobPhase_JOB_PHASE_INDEXING, entity.JobPhase_JOB_PHASE_UNSPECIFIED, entity.JobPhase_JOB_PHASE_COMPARING_CONTENT,
		entity.JobPhase_JOB_PHASE_GENERATING_PREVIEWS, entity.JobPhase_JOB_PHASE_VALIDATING_SOURCE,
		entity.JobPhase_JOB_PHASE_FINALIZING_MEDIA, entity.JobPhase_JOB_PHASE_PUBLISHING_SOURCE,
		entity.JobPhase_JOB_PHASE_COMPLETED, entity.JobPhase_JOB_PHASE_QUEUED:
		return true
	}
	return false
}

// countManifest reads every manifest counter in one statement.
func (r *runner) countManifest(ctx context.Context) (manifestCounts, error) {
	var row struct {
		FoundFiles, FoundBytes, ProcessedFiles, ProcessedBytes, ComparedFiles, ComparedBytes int64
	}
	err := r.db.WithContext(ctx).Model(&Entry{}).Where("change != ?", entity.ScanChange_SCAN_CHANGE_REMOVED).Select(
		"COUNT(*) AS found_files, COALESCE(SUM(size), 0) AS found_bytes, " +
			"COALESCE(SUM(CASE WHEN needs_hash THEN 0 ELSE 1 END), 0) AS processed_files, " +
			"COALESCE(SUM(CASE WHEN needs_hash THEN 0 ELSE size END), 0) AS processed_bytes, " +
			"COALESCE(SUM(CASE WHEN compared THEN 1 ELSE 0 END), 0) AS compared_files, " +
			"COALESCE(SUM(CASE WHEN compared THEN size ELSE 0 END), 0) AS compared_bytes").Scan(&row).Error
	return manifestCounts{
		found:     executor.Counts{Files: row.FoundFiles, Bytes: row.FoundBytes},
		processed: executor.Counts{Files: row.ProcessedFiles, Bytes: row.ProcessedBytes},
		compared:  executor.Counts{Files: row.ComparedFiles, Bytes: row.ComparedBytes},
	}, err
}

// lockedCounts returns the maintained counters. A runner that no attempt has counted yet, such as
// a reopened idle Job, counts its unchanging manifest once. The caller holds the runner lock.
func (r *runner) lockedCounts(ctx context.Context) (manifestCounts, error) {
	if r.countsKnown {
		return r.counts, nil
	}
	counts, err := r.countManifest(ctx)
	if err != nil {
		return manifestCounts{}, err
	}
	r.counts, r.countsKnown = counts, true
	return counts, nil
}

// resetCounts records that the work path emptied the manifest.
func (r *runner) resetCounts() {
	r.lock.Lock()
	r.counts, r.countsKnown = manifestCounts{}, true
	r.results, r.resultsKnown = resultCounts{}, true
	r.lock.Unlock()
}

// resultCounts are the manifest's result counters a progress reply reports: entries per change
// and finding, the bytes of non-removed entries and the entries whose Preview failed. An attempt
// counts them before its first write and its work path advances them from its own writes, so a
// progress request never aggregates the manifest.
type resultCounts struct {
	changes         [entity.ScanChange_SCAN_CHANGE_UNCHANGED + 1]int64
	findings        [entity.ScanFinding_SCAN_FINDING_UNVERIFIABLE + 1]int64
	bytes           int64
	previewFailures int64
}

// add adds one row's contribution n times; a negative n removes it.
func (c *resultCounts) add(row *Entry, n int64) {
	if change := int(row.Change); change >= 0 && change < len(c.changes) {
		c.changes[change] += n
	}
	if finding := int(row.Finding); finding >= 0 && finding < len(c.findings) {
		c.findings[finding] += n
	}
	if row.Change != entity.ScanChange_SCAN_CHANGE_REMOVED {
		c.bytes += n * row.Size
	}
	if row.PreviewError != "" {
		c.previewFailures += n
	}
}

// countResults reads every result counter in one statement.
func (r *runner) countResults(ctx context.Context) (resultCounts, error) {
	var groups []struct {
		Change                 entity.ScanChange
		Finding                entity.ScanFinding
		Count, Bytes, Failures int64
	}
	var counts resultCounts
	if err := r.db.WithContext(ctx).Model(&Entry{}).
		Select("change, finding, COUNT(*) AS count, COALESCE(SUM(size), 0) AS bytes, " +
			"COALESCE(SUM(CASE WHEN preview_error <> '' THEN 1 ELSE 0 END), 0) AS failures").
		Group("change, finding").Find(&groups).Error; err != nil {
		return resultCounts{}, err
	}
	for _, g := range groups {
		if change := int(g.Change); change >= 0 && change < len(counts.changes) {
			counts.changes[change] += g.Count
		}
		if finding := int(g.Finding); finding >= 0 && finding < len(counts.findings) {
			counts.findings[finding] += g.Count
		}
		if g.Change != entity.ScanChange_SCAN_CHANGE_REMOVED {
			counts.bytes += g.Bytes
		}
		counts.previewFailures += g.Failures
	}
	return counts, nil
}

// recountResults counts the result counters on the work path, before its writes depend on them.
func (r *runner) recountResults(ctx context.Context) error {
	counts, err := r.countResults(ctx)
	if err != nil {
		return err
	}
	r.lock.Lock()
	r.results, r.resultsKnown = counts, true
	r.lock.Unlock()
	return nil
}

// lockedResults returns the maintained result counters. A runner that no attempt has counted yet,
// such as a reopened idle Job, counts its unchanging manifest once. The caller holds the runner lock.
func (r *runner) lockedResults(ctx context.Context) (resultCounts, error) {
	if r.resultsKnown {
		return r.results, nil
	}
	counts, err := r.countResults(ctx)
	if err != nil {
		return resultCounts{}, err
	}
	r.results, r.resultsKnown = counts, true
	return counts, nil
}

// advanceResults applies the difference the work path's own writes made. Unknown counters stay
// unknown, so the next count reads them instead of adding to an unknown base.
func (r *runner) advanceResults(d resultCounts) {
	r.lock.Lock()
	defer r.lock.Unlock()
	if r.resultsKnown {
		r.results.merge(d)
	}
}

func (c *resultCounts) merge(d resultCounts) {
	for i, n := range d.changes {
		c.changes[i] += n
	}
	for i, n := range d.findings {
		c.findings[i] += n
	}
	c.bytes += d.bytes
	c.previewFailures += d.previewFailures
}

// addedResults is the contribution of rows the work path inserted.
func addedResults(rows []*Entry) resultCounts {
	var d resultCounts
	for _, row := range rows {
		d.add(row, 1)
	}
	return d
}

// advanceCounts applies the work path's own row changes. Unknown counters stay unknown, so the
// next count reads them instead of adding to an unknown base.
func (r *runner) advanceCounts(found, compared executor.Counts) {
	r.lock.Lock()
	defer r.lock.Unlock()
	if !r.countsKnown {
		return
	}
	r.counts.found.Files += found.Files
	r.counts.found.Bytes += found.Bytes
	r.counts.compared.Files += compared.Files
	r.counts.compared.Bytes += compared.Bytes
}

// stageInput assembles the stage report one phase may show. A phase without a denominator reports
// its counters only, and the rule decides whether a percentage or an estimate exists at all.
func (r *runner) stageInput(ctx context.Context, phase entity.JobPhase) (executor.StageInput, error) {
	if phase == entity.JobPhase_JOB_PHASE_PROCESSING_CONTENT || phase == entity.JobPhase_JOB_PHASE_VERIFYING_MEDIA {
		// The running attempt is the only owner of these phases: its snapshot carries the scope
		// denominator and the manifest's cumulative counters.
		in := r.contentStageInput()
		if in == nil {
			// An attempt that has not published its snapshot yet reports the manifest.
			counts, err := r.lockedCounts(ctx)
			if err != nil {
				return executor.StageInput{}, err
			}
			found := counts.found
			in = &executor.StageInput{Phase: phase, Completed: found, Total: &found, Manifest: &found}
		}
		in.TotalKnown = true
		return *in, nil
	}
	if !countedPhase(phase) {
		return executor.StageInput{Phase: phase}, nil
	}
	counts, err := r.lockedCounts(ctx)
	if err != nil {
		return executor.StageInput{}, err
	}
	found := counts.found
	switch phase {
	case entity.JobPhase_JOB_PHASE_UNSPECIFIED:
		// A failed attempt retains its observed manifest and processed counters, but this phase
		// cannot prove that enumeration finished, so the manifest total remains provisional.
		if found.Files == 0 {
			return executor.StageInput{Phase: phase}, nil
		}
		return executor.StageInput{Phase: phase, Completed: counts.processed, Manifest: &found}, nil
	case entity.JobPhase_JOB_PHASE_COMPARING_CONTENT:
		in := denominated(phase, counts.compared, found)
		in.TotalKnown = true
		return in, nil
	case entity.JobPhase_JOB_PHASE_GENERATING_PREVIEWS:
		in := r.previewStageInput(phase, found)
		in.TotalKnown = true
		return in, nil
	case entity.JobPhase_JOB_PHASE_COMPLETED:
		in := denominated(phase, found, found)
		in.TotalKnown = true
		return in, nil
	case entity.JobPhase_JOB_PHASE_QUEUED:
		// Verification freezes its baseline before waiting for a drive, so the waiting card reports
		// what the manifest holds. It never carries a denominator: no work in that phase can advance
		// a percentage.
		if !r.checking {
			return executor.StageInput{Phase: phase}, nil
		}
		return executor.StageInput{Phase: phase, Completed: found}, nil
	default:
		// Enumeration may still add rows, and validation, finalization and publication have no
		// meaningful denominator, but completed discovery must not disappear.
		return executor.StageInput{Phase: phase, Completed: found}, nil
	}
}

// denominated reports processed counters against the manifest total once it has rows.
func denominated(phase entity.JobPhase, processed, total executor.Counts) executor.StageInput {
	if total.Files == 0 {
		return executor.StageInput{Phase: phase, Completed: processed}
	}
	return executor.StageInput{Phase: phase, Completed: processed, Total: &total}
}

// contentStageInput assembles the running content attempt's durable snapshot. It is called under
// the runner lock from both the request path and the work path, so the two never disagree.
func (r *runner) contentStageInput() *executor.StageInput {
	stream := r.content
	if stream == nil {
		return nil
	}
	readBytes, processed := stream.counters()
	totals := stream.totals
	// The window is the scope's own range: the content already read in this scope plus the current stream's
	// own reads, clamped inside the range so a stage never exceeds its own total.
	completed := executor.Counts{
		Files: min(totals.scope.Files, totals.scopeDone.Files+processed),
		Bytes: min(totals.scope.Bytes, totals.scopeDone.Bytes+readBytes+r.inflightBytes.Load()),
	}
	// The sampled work includes the item still in flight, so one large file keeps the estimate
	// moving; the displayed counter clamps that amount inside the range.
	return &executor.StageInput{Key: r.stageKey(stream.scope), Phase: r.phase, Completed: completed,
		Total: &totals.scope, Work: completed, Manifest: &totals.manifest}
}

// readContentTotals reads the durable denominator and completed base of one content attempt.
// Completed work includes entries whose content is already known before this stream reads them.
func (r *runner) readContentTotals(ctx context.Context, scope *Scope) (contentTotals, error) {
	var totals contentTotals
	var err error
	if totals.manifest, err = r.entryProgress(ctx, "change != ?", entity.ScanChange_SCAN_CHANGE_REMOVED); err != nil {
		return contentTotals{}, err
	}
	if totals.scope, err = r.scopeProgress(ctx, scope, "change != ?", entity.ScanChange_SCAN_CHANGE_REMOVED); err != nil {
		return contentTotals{}, err
	}
	if totals.scopeDone, err = r.scopeProgress(ctx, scope, "change != ? AND needs_hash = ?", entity.ScanChange_SCAN_CHANGE_REMOVED, false); err != nil {
		return contentTotals{}, err
	}
	return totals, nil
}

// publishContentStage exposes the running attempt's durable snapshot. It is called before the
// content phase becomes observable and stays published until the next source scope replaces it, so a
// request that sees a content phase always reads one attempt's snapshot instead of querying.
func (r *runner) publishContentStage(stream *contentStream) {
	r.lock.Lock()
	r.content = stream
	r.lock.Unlock()
}

// sampleContentStage advances the content stage window for the work the last ACP event reported. It
// runs on the path that already advances the work counters, so a hidden Job card cannot freeze the
// estimate and clients polling at different cadences cannot change its quality. The sampled amount
// is the settled plus in-flight amount a request displays, so the two never disagree.
func (r *runner) sampleContentStage() {
	r.lock.Lock()
	defer r.lock.Unlock()
	if in := r.contentStageInput(); in != nil {
		r.progress.ObserveStage(*in)
	}
}

// previewStageInput assembles the Preview stage's settled generation workload. Nothing about a
// successful Preview is durable, so the attempt's own counters are its settled work: outcomes
// accumulated by finished scopes plus the scope in flight. The manifest still supplies the stage
// denominator, and only decoder completions advance the samples.
func (r *runner) previewStageInput(phase entity.JobPhase, total executor.Counts) executor.StageInput {
	timing := r.previewTiming
	processed := r.previewOutcomes.ready + r.previewOutcomes.skipped + r.previewOutcomes.failed + timing.completed
	settled := executor.StageInput{Phase: phase, Key: r.stageKey(r.contentScope), Completed: executor.Counts{Files: processed}}
	if total.Files != 0 {
		settled.Total = &total
	}
	settled.Work, settled.Minimum = executor.Counts{Files: timing.samples}, previewMinimumSamples
	// A classified workload may be zero, which reports counters without a fabricated estimate.
	settled.WorkKnown, settled.WorkTotal = timing.workKnown, &executor.Counts{Files: timing.workTotal}
	return settled
}

// stageKey identifies one attempt's phase window; a new attempt, phase or source range discards
// every earlier sample.
func (r *runner) stageKey(scope *Scope) string {
	key := fmt.Sprintf("%d/%d", r.phaseStarted.UnixNano(), r.phase)
	if scope != nil {
		key += fmt.Sprintf("/%d/%s", scope.LocationID, scope.Path)
	}
	return key
}

func (r *runner) samplePreview() {
	// Called under the phase lock by worker completions; estimation does not depend on UI polling.
	// Only the sampled work matters here, so the settled counters are left to the request path.
	// Preview that overlaps an earlier phase never samples into that phase's window.
	if r.phase != entity.JobPhase_JOB_PHASE_GENERATING_PREVIEWS {
		return
	}
	r.progress.ObserveStage(executor.StageInput{Key: r.stageKey(r.contentScope), Phase: r.phase,
		Work: executor.Counts{Files: r.previewTiming.samples}, Minimum: previewMinimumSamples,
		WorkKnown: r.previewTiming.workKnown, WorkTotal: &executor.Counts{Files: r.previewTiming.workTotal}})
}

func (r *runner) entryProgress(ctx context.Context, predicate string, args ...any) (executor.Counts, error) {
	var result executor.Counts
	err := r.db.WithContext(ctx).Model(&Entry{}).Where(predicate, args...).
		Select("COUNT(*) AS files, COALESCE(SUM(size), 0) AS bytes").Scan(&result).Error
	return result, err
}

// scopeProgress reports the same counters for one selected range instead of the whole manifest.
func (r *runner) scopeProgress(ctx context.Context, scope *Scope, predicate string, args ...any) (executor.Counts, error) {
	var result executor.Counts
	err := r.scopeQuery(ctx, scope).Model(&Entry{}).Where(predicate, args...).
		Select("COUNT(*) AS files, COALESCE(SUM(size), 0) AS bytes").Scan(&result).Error
	return result, err
}
