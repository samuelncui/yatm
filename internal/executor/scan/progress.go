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
	manifest     executor.Counts
	manifestDone executor.Counts
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

// stageInput assembles the stage report one phase may show. A phase without a denominator reports
// its counters only, and the rule decides whether a percentage or an estimate exists at all.
func (r *runner) stageInput(ctx context.Context, phase entity.JobPhase) (executor.StageInput, error) {
	switch phase {
	case entity.JobPhase_JOB_PHASE_PROCESSING_CONTENT, entity.JobPhase_JOB_PHASE_VERIFYING_MEDIA:
		// The running attempt is the only owner of these phases: its snapshot carries the scope
		// denominator and the manifest's cumulative counters.
		in := r.contentStageInput()
		if in == nil {
			// An attempt that has not published its snapshot yet reports the durable manifest.
			found, err := r.entryProgress(ctx, "change != ?", entity.ScanChange_SCAN_CHANGE_REMOVED)
			if err != nil {
				return executor.StageInput{}, err
			}
			in = &executor.StageInput{Phase: phase, Completed: found, Total: &found, Manifest: &found}
		}
		in.TotalKnown = true
		return *in, nil
	case entity.JobPhase_JOB_PHASE_INDEXING:
		// Enumeration may still add rows, so only the discovered count is reportable.
		discovered, err := r.entryProgress(ctx, "change != ?", entity.ScanChange_SCAN_CHANGE_REMOVED)
		if err != nil {
			return executor.StageInput{}, err
		}
		return executor.StageInput{Phase: phase, Completed: discovered}, nil
	case entity.JobPhase_JOB_PHASE_UNSPECIFIED:
		// A failed attempt retains its observed manifest and processed counters, but this phase
		// cannot prove that enumeration finished, so the manifest total remains provisional.
		discovered, err := r.entryProgress(ctx, "change != ?", entity.ScanChange_SCAN_CHANGE_REMOVED)
		if err != nil {
			return executor.StageInput{}, err
		}
		if discovered.Files == 0 {
			return executor.StageInput{Phase: phase}, nil
		}
		processed, err := r.entryProgress(ctx, "change != ? AND needs_hash = ?", entity.ScanChange_SCAN_CHANGE_REMOVED, false)
		if err != nil {
			return executor.StageInput{}, err
		}
		return executor.StageInput{Phase: phase, Completed: processed, Manifest: &discovered}, nil
	case entity.JobPhase_JOB_PHASE_COMPARING_CONTENT:
		in, err := r.entryStageInput(ctx, phase, "change != ? AND compared = ?", entity.ScanChange_SCAN_CHANGE_REMOVED, true)
		in.TotalKnown = true
		return in, err
	case entity.JobPhase_JOB_PHASE_GENERATING_PREVIEWS:
		in, err := r.previewStageInput(ctx, phase)
		if err != nil {
			return executor.StageInput{}, err
		}
		in.TotalKnown = true
		return in, nil
	case entity.JobPhase_JOB_PHASE_VALIDATING_SOURCE, entity.JobPhase_JOB_PHASE_FINALIZING_MEDIA,
		entity.JobPhase_JOB_PHASE_PUBLISHING_SOURCE:
		// These stages have no meaningful denominator, but completed discovery must not disappear.
		observed, err := r.entryProgress(ctx, "change != ?", entity.ScanChange_SCAN_CHANGE_REMOVED)
		if err != nil {
			return executor.StageInput{}, err
		}
		return executor.StageInput{Phase: phase, Completed: observed}, nil
	case entity.JobPhase_JOB_PHASE_COMPLETED:
		in, err := r.entryStageInput(ctx, phase, "change != ?", entity.ScanChange_SCAN_CHANGE_REMOVED)
		in.TotalKnown = true
		return in, err
	case entity.JobPhase_JOB_PHASE_QUEUED:
		// Verification freezes its baseline before waiting for a drive, so the waiting card reports
		// what the manifest holds. It never carries a denominator: no work in that phase can advance
		// a percentage.
		var config Config
		if err := r.db.WithContext(ctx).First(&config, 1).Error; err != nil {
			return executor.StageInput{}, err
		}
		if config.Spec.GetResultPolicy() != entity.ScanResultPolicy_SCAN_RESULT_POLICY_VERIFY_COPIES {
			return executor.StageInput{Phase: phase}, nil
		}
		found, err := r.entryProgress(ctx, "change != ?", entity.ScanChange_SCAN_CHANGE_REMOVED)
		if err != nil {
			return executor.StageInput{}, err
		}
		return executor.StageInput{Phase: phase, Completed: found}, nil
	default:
		return executor.StageInput{Phase: phase}, nil
	}
}

// entryStageInput reports the manifest's processed and total counters of one predicate.
func (r *runner) entryStageInput(
	ctx context.Context, phase entity.JobPhase, predicate string, args ...any,
) (executor.StageInput, error) {
	total, err := r.entryProgress(ctx, "change != ?", entity.ScanChange_SCAN_CHANGE_REMOVED)
	if err != nil {
		return executor.StageInput{}, err
	}
	processed, err := r.entryProgress(ctx, predicate, args...)
	if err != nil {
		return executor.StageInput{}, err
	}
	if total.Files == 0 {
		return executor.StageInput{Phase: phase, Completed: processed}, nil
	}
	return executor.StageInput{Phase: phase, Completed: processed, Total: &total}, nil
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
	if totals.manifestDone, err = r.entryProgress(ctx, "change != ? AND needs_hash = ?", entity.ScanChange_SCAN_CHANGE_REMOVED, false); err != nil {
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
func (r *runner) previewStageInput(ctx context.Context, phase entity.JobPhase) (executor.StageInput, error) {
	total, err := r.entryProgress(ctx, "change != ?", entity.ScanChange_SCAN_CHANGE_REMOVED)
	if err != nil {
		return executor.StageInput{}, err
	}
	timing := r.previewTiming
	processed := r.previewOutcomes.ready + r.previewOutcomes.skipped + r.previewOutcomes.failed + timing.completed
	settled := executor.StageInput{Phase: phase, Key: r.stageKey(r.contentScope), Completed: executor.Counts{Files: processed}}
	if total.Files != 0 {
		settled.Total = &total
	}
	settled.Work, settled.Minimum = executor.Counts{Files: timing.samples}, previewMinimumSamples
	// A classified workload may be zero, which reports counters without a fabricated estimate.
	settled.WorkKnown, settled.WorkTotal = timing.workKnown, &executor.Counts{Files: timing.workTotal}
	return settled, nil
}

// stageKey identifies one attempt's phase window; a new attempt, phase or source range discards
// every earlier sample.
func (r *runner) stageKey(scope *Scope) string {
	key := fmt.Sprintf("%d/%d", r.phaseStarted.UnixNano(), r.phase)
	if scope != nil {
		key += fmt.Sprintf("/%d/%d/%s", scope.LocationID, scope.ID, scope.Path)
	}
	return key
}

func (r *runner) samplePreview() {
	// Called under the phase lock by worker completions; estimation does not depend on UI polling.
	// Only the sampled work matters here, so the settled counters are left to the request path.
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
