package executor

import (
	"testing"
	"time"

	"github.com/samuelncui/yatm/entity"
	"github.com/stretchr/testify/require"
)

// observePreview records one decoder sample and reads it back, keeping the sampling rule of the
// Preview stage in one place.
func observePreview(p *Progress, key string, completed, total, work, workTotal int64) *entity.StageProgress {
	in := StageInput{
		Key: key, Phase: entity.JobPhase_JOB_PHASE_GENERATING_PREVIEWS,
		Completed: Counts{Files: completed, Bytes: completed * 10},
		Total:     &Counts{Files: total, Bytes: total * 10},
		Work:      Counts{Files: work}, WorkKnown: true, WorkTotal: &Counts{Files: workTotal}, Minimum: 3,
	}
	return p.ObserveStage(in)
}

// reportPreview reads the stage without recording a sample, exactly as a progress request does.
func reportPreview(p *Progress, in StageInput) *entity.StageProgress {
	return p.StageReport(in).Stage
}

func TestStageMeterWindowsStaleRecoveryAndScopeIsolation(t *testing.T) {
	// Fixed clocks make the minimum warmup and slow-file boundary independent of test speed.
	now := time.Unix(100, 0)
	p := NewProgress()
	p.now = func() time.Time { return now }
	first := observePreview(p, "scope-a", 10, 110, 0, 100)
	require.Equal(t, entity.EstimateState_ESTIMATE_STATE_ESTIMATING, first.EstimateState)
	for index := int64(1); index <= 3; index++ {
		now = now.Add(5 * time.Second)
		observePreview(p, "scope-a", 10+index, 110, index, 100)
	}
	require.Equal(t, first.WindowId, observePreview(p, "scope-a", 13, 110, 3, 100).WindowId, "one attempt keeps one window")
	settled := observePreview(p, "scope-a", 13, 110, 3, 100)
	require.Equal(t, entity.EstimateState_ESTIMATE_STATE_ESTIMATED, settled.EstimateState)
	require.EqualValues(t, 485, *settled.RemainingSeconds)

	// A moderately slow item preserves the estimate rather than flickering at twice its mean.
	now = now.Add(13 * time.Second)
	require.Equal(t, entity.EstimateState_ESTIMATE_STATE_ESTIMATED, observePreview(p, "scope-a", 13, 110, 3, 100).EstimateState)
	now = now.Add(18 * time.Second)
	require.Equal(t, entity.EstimateState_ESTIMATE_STATE_ESTIMATING, observePreview(p, "scope-a", 13, 110, 3, 100).EstimateState)
	for index := int64(4); index <= 6; index++ {
		now = now.Add(5 * time.Second)
		value := observePreview(p, "scope-a", 10+index, 110, index, 100)
		if index < 6 {
			require.Equal(t, entity.EstimateState_ESTIMATE_STATE_ESTIMATING, value.EstimateState)
		}
	}
	require.Equal(t, entity.EstimateState_ESTIMATE_STATE_ESTIMATED, observePreview(p, "scope-a", 16, 110, 6, 100).EstimateState)

	// A new scope cannot borrow either the previous rate or the previous window identity.
	newScope := observePreview(p, "scope-b", 10, 110, 0, 100)
	require.NotEqual(t, first.WindowId, newScope.WindowId)
	require.Nil(t, newScope.RatePerSecond)
	require.Nil(t, newScope.RemainingSeconds)
}

func TestStageMeterDetectsStallWithoutAnIntermediatePoll(t *testing.T) {
	// Worker completion sampling must notice a long pause even with no visible UI consumer.
	now := time.Unix(100, 0)
	p := NewProgress()
	p.now = func() time.Time { return now }
	var value *entity.StageProgress
	for index := int64(0); index <= 3; index++ {
		value = observePreview(p, "work", index, 10, index, 10)
		now = now.Add(5 * time.Second)
	}
	require.Equal(t, entity.EstimateState_ESTIMATE_STATE_ESTIMATED, value.EstimateState)
	now = now.Add(time.Minute)
	value = observePreview(p, "work", 4, 10, 4, 10)
	require.Equal(t, entity.EstimateState_ESTIMATE_STATE_ESTIMATING, value.EstimateState)
	require.Nil(t, value.RemainingSeconds)
}

func TestStageMeterSkippedItemsUnknownWorkAndIndeterminatePhases(t *testing.T) {
	// Settled cache hits update the displayed item counter, never actual generation throughput.
	now := time.Unix(100, 0)
	p := NewProgress()
	p.now = func() time.Time { return now }
	for index := int64(0); index < 5; index++ {
		value := observePreview(p, "preview", index*100, 1000, 0, 10)
		require.Nil(t, value.RatePerSecond)
		require.Nil(t, value.RemainingSeconds)
		now = now.Add(5 * time.Second)
	}
	for index := int64(1); index < 5; index++ {
		value := observePreview(p, "preview", 500+index, 1000, index, 0)
		require.Equal(t, entity.EstimateState_ESTIMATE_STATE_ESTIMATING, value.EstimateState)
		now = now.Add(5 * time.Second)
	}

	// Cancellation, waiting and publication never advertise a running estimate.
	for _, phase := range []entity.JobPhase{
		entity.JobPhase_JOB_PHASE_UNSPECIFIED, entity.JobPhase_JOB_PHASE_QUEUED,
		entity.JobPhase_JOB_PHASE_PUBLISHING_SOURCE,
	} {
		in := StageInput{Key: phase.String(), Phase: phase, Completed: Counts{Files: 505}}
		p.ObserveStage(in)
		value := p.StageReport(in).Stage
		require.Equal(t, entity.EstimateState_ESTIMATE_STATE_NOT_APPLICABLE, value.EstimateState)
		require.Nil(t, value.Total)
		require.Nil(t, value.RemainingSeconds)
		require.EqualValues(t, 505, value.Completed)
	}
}

func TestStageRuleNeverShowsAPercentageWithoutADenominator(t *testing.T) {
	// A known business total is not a stage denominator: waiting for Media, discovery, validation
	// and publication report counters and a unit, but no total a card could turn into 0%.
	for _, phase := range []entity.JobPhase{
		entity.JobPhase_JOB_PHASE_INDEXING,
		entity.JobPhase_JOB_PHASE_QUEUED, entity.JobPhase_JOB_PHASE_PREPARING_MEDIA,
		entity.JobPhase_JOB_PHASE_VALIDATING_SOURCE, entity.JobPhase_JOB_PHASE_FINALIZING_MEDIA,
		entity.JobPhase_JOB_PHASE_PUBLISHING_SOURCE, entity.JobPhase_JOB_PHASE_UNSPECIFIED,
	} {
		rule := RuleFor(phase, &Counts{Files: 0, Bytes: 0})
		require.False(t, rule.Ratio, "phase %s must not own a ratio", phase)
		require.False(t, rule.Estimate, "phase %s must not own an estimate", phase)
		require.Equal(t, entity.ProgressUnit_PROGRESS_UNIT_ITEMS, rule.Unit)

		// Even a caller that attaches a denominator cannot make such a phase a percentage.
		p := NewProgress()
		now := time.Unix(100, 0)
		p.now = func() time.Time { return now }
		in := StageInput{Key: "attempt", Phase: phase, Completed: Counts{Files: 3, Bytes: 30},
			Total: &Counts{Files: 10, Bytes: 100}, Work: Counts{Files: 3}, WorkTotal: &Counts{Files: 10}}
		p.ObserveStage(in)
		value := p.StageReport(in).Stage
		require.Nil(t, value.Total, "phase %s must report no denominator", phase)
		require.Nil(t, value.RatePerSecond)
		require.Nil(t, value.RemainingSeconds)
		require.Equal(t, entity.EstimateState_ESTIMATE_STATE_NOT_APPLICABLE, value.EstimateState)
		require.EqualValues(t, 3, value.Completed, "counters stay visible in every phase")
	}
}

func TestStageRuleZeroByteDenominatorUsesItems(t *testing.T) {
	// Empty files are real work: a zero-byte workload reports items instead of a false 0%.
	for _, phase := range []entity.JobPhase{
		entity.JobPhase_JOB_PHASE_COPYING_TO_MEDIA, entity.JobPhase_JOB_PHASE_COPYING_FROM_MEDIA,
	} {
		rule := RuleFor(phase, &Counts{Files: 4, Bytes: 0})
		require.True(t, rule.Ratio)
		require.True(t, rule.Estimate)
		require.Equal(t, entity.ProgressUnit_PROGRESS_UNIT_ITEMS, rule.Unit)
	}
	rule := RuleFor(entity.JobPhase_JOB_PHASE_COMPLETED, &Counts{Files: 4})
	require.True(t, rule.Ratio)
	require.False(t, rule.Estimate)
	require.Equal(t, entity.ProgressUnit_PROGRESS_UNIT_ITEMS, rule.Unit)

	// Content measures bytes, and an all-empty scope measures its items like any other stage.
	require.Equal(t, entity.ProgressUnit_PROGRESS_UNIT_BYTES, RuleFor(entity.JobPhase_JOB_PHASE_PROCESSING_CONTENT, &Counts{Files: 2, Bytes: 30}).Unit)
	require.Equal(t, entity.ProgressUnit_PROGRESS_UNIT_BYTES, RuleFor(entity.JobPhase_JOB_PHASE_VERIFYING_MEDIA, &Counts{Files: 2, Bytes: 30}).Unit)
	require.Equal(t, entity.ProgressUnit_PROGRESS_UNIT_ITEMS, RuleFor(entity.JobPhase_JOB_PHASE_PROCESSING_CONTENT, &Counts{Files: 2}).Unit)
	require.Equal(t, entity.ProgressUnit_PROGRESS_UNIT_ITEMS, RuleFor(entity.JobPhase_JOB_PHASE_COMPARING_CONTENT, nil).Unit)
}

func TestCopyStageInputUsesItemsForZeroByteFilesAndResetsAttemptWindow(t *testing.T) {
	// Empty files remain real work despite their byte denominator being zero.
	p := NewProgress()
	now := time.Unix(100, 0)
	p.now = func() time.Time { return now }
	p.SetGlobalTotal(0, 3)
	p.SetGlobalCopied(0, 1)
	p.StartSession()
	copied := p.CopyStageInput(entity.JobPhase_JOB_PHASE_COPYING_TO_MEDIA, p.StagePrefix())
	unobserved := p.StageReport(copied).Stage
	require.Empty(t, unobserved.WindowId, "a progress request never opens a window")
	require.Equal(t, entity.ProgressUnit_PROGRESS_UNIT_ITEMS, unobserved.Unit)
	require.EqualValues(t, 1, unobserved.Completed)
	require.EqualValues(t, 3, *unobserved.Total)
	require.Equal(t, entity.EstimateState_ESTIMATE_STATE_NOT_APPLICABLE, unobserved.EstimateState)

	// A subsequent Media attempt changes identity even if nobody polled the waiting state.
	first := p.ObserveStage(copied)
	p.CommitSession()
	now = now.Add(time.Second)
	p.StartSession()
	second := p.ObserveStage(p.CopyStageInput(entity.JobPhase_JOB_PHASE_COPYING_TO_MEDIA, p.StagePrefix()))
	require.NotEmpty(t, first.WindowId)
	require.NotEqual(t, first.WindowId, second.WindowId)
	waiting := p.StageReport(p.CopyStageInput(entity.JobPhase_JOB_PHASE_QUEUED, p.StagePrefix())).Stage
	require.Nil(t, waiting.Total)
	require.Nil(t, waiting.RemainingSeconds)
}

func TestStageReportReadsWithoutSamplingAndWorkDrivesTheEstimate(t *testing.T) {
	// Only the work path records samples: any number of requests reports the same window, and the
	// estimate exists because the work advanced rather than because a client polled.
	now := time.Unix(100, 0)
	p := NewProgress()
	p.now = func() time.Time { return now }
	in := StageInput{Key: "attempt", Phase: entity.JobPhase_JOB_PHASE_COPYING_TO_MEDIA,
		Total: &Counts{Files: 10, Bytes: 1000}, WorkTotal: &Counts{Files: 10, Bytes: 1000}}
	sample := func(bytes int64) {
		in.Completed, in.Work = Counts{Files: bytes / 100, Bytes: bytes}, Counts{Files: bytes / 100, Bytes: bytes}
		p.ObserveStage(in)
	}
	sample(0)
	for step := int64(1); step <= 3; step++ {
		now = now.Add(5 * time.Second)
		sample(step * 100)
	}
	for index := 0; index < 5; index++ {
		value := p.StageReport(in).Stage
		require.Equal(t, entity.EstimateState_ESTIMATE_STATE_ESTIMATED, value.EstimateState)
		require.Positive(t, *value.RemainingSeconds)
	}
	// A long gap the work path observed invalidates the rate; a request during the gap keeps it.
	now = now.Add(10 * time.Minute)
	require.Equal(t, entity.EstimateState_ESTIMATE_STATE_ESTIMATED, p.StageReport(in).Stage.EstimateState)
	sample(400)
	require.Equal(t, entity.EstimateState_ESTIMATE_STATE_ESTIMATING, p.StageReport(in).Stage.EstimateState)
}

// TestPreviewSamplingWithoutADenominatorKeepsItsEstimate pins the Preview work path: it records
// decoder observations without a classified denominator, because the request path owns the settled
// counters. The sample must not be dropped — otherwise the stage can never show a rate or a
// remaining time, which is what a live Preview phase did before this test existed.
func TestPreviewSamplingWithoutADenominatorKeepsItsEstimate(t *testing.T) {
	now := time.Unix(300, 0)
	p := NewProgress()
	p.now = func() time.Time { return now }

	// Exactly what scan.samplePreview records: sampled decoder work and its classified workload,
	// with no denominator.
	observeWork := func(work int64) {
		p.ObserveStage(StageInput{
			Key: "attempt/preview", Phase: entity.JobPhase_JOB_PHASE_GENERATING_PREVIEWS,
			Work: Counts{Files: work}, Minimum: 3, WorkKnown: true, WorkTotal: &Counts{Files: 100},
		})
	}
	observeWork(0)
	for index := int64(1); index <= 3; index++ {
		now = now.Add(5 * time.Second)
		observeWork(index * 10)
	}

	// A progress request supplies the counters and reads the window the work path recorded.
	stage := reportPreview(p, StageInput{
		Key: "attempt/preview", Phase: entity.JobPhase_JOB_PHASE_GENERATING_PREVIEWS,
		Completed: Counts{Files: 30}, Total: &Counts{Files: 100},
		Work: Counts{Files: 30}, Minimum: 3, WorkKnown: true, WorkTotal: &Counts{Files: 100},
	})
	require.Equal(t, entity.EstimateState_ESTIMATE_STATE_ESTIMATED, stage.EstimateState)
	require.NotNil(t, stage.RatePerSecond)
	require.NotNil(t, stage.RemainingSeconds)
	require.Greater(t, *stage.RemainingSeconds, int64(0))
	require.EqualValues(t, 30, stage.Completed)
	require.EqualValues(t, 100, *stage.Total)
}
