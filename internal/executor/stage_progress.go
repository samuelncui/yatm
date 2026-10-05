package executor

import (
	"math"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/samuelncui/yatm/entity"
	"google.golang.org/protobuf/proto"
)

// Counts is one coherent size/item counter pair.
type Counts struct {
	Files int64
	Bytes int64
}

// Denominated reports whether the pair can serve as a meaningful denominator. A nil pointer and an
// all-zero pair both mean the stage has nothing to measure against.
func (c *Counts) Denominated() bool {
	return c != nil && (c.Files > 0 || c.Bytes > 0)
}

// StageInput is everything one stage report needs. Completed and Total are in the unit the stage
// rule selects; its work and classified workload are what the estimator samples.
type StageInput struct {
	Key       string // attempt/phase/source-range identity; a new key discards earlier samples
	Phase     entity.JobPhase
	Completed Counts
	Total     *Counts // nil, or an empty pair: the stage has no denominator
	Work      Counts  // the work the last event reported, in both units
	WorkTotal *Counts // classified remaining workload
	// Manifest carries the Job's cumulative business counters, which are reportable in every phase
	// whether or not the stage owns a denominator. Nil means Total is the only total there is.
	Manifest   *Counts
	TotalKnown bool
	// WorkKnown reports that the stage classified its remaining workload. WorkTotal then holds it,
	// and its absence means the classified workload is zero work; without this fact the stage is
	// measured against its own denominator.
	WorkKnown bool
	Minimum   int64 // minimum sampled work before an estimate is offered
}

// StageRule is the single decision about what one phase may display.
type StageRule struct {
	Unit     entity.ProgressUnit // counter unit of the stage
	Ratio    bool                // the denominator may be shown as a percentage
	Estimate bool                // a rate and a remaining time may be offered
}

// RuleFor returns the display rule of one phase. A nil or empty denominator always yields counters
// only, whatever the phase, so no waiting phase can display a false percentage.
func RuleFor(phase entity.JobPhase, denominator *Counts) StageRule {
	switch phase {
	case entity.JobPhase_JOB_PHASE_PROCESSING_CONTENT, entity.JobPhase_JOB_PHASE_VERIFYING_MEDIA:
		// Content usually measures bytes, and a scope whose files are all empty measures its items
		// instead of a false byte percentage.
		return StageRule{Unit: stageUnit(denominator), Ratio: true, Estimate: true}
	case entity.JobPhase_JOB_PHASE_COMPARING_CONTENT:
		// Comparison settles from durable rows, so it has a ratio but no throughput to estimate.
		return StageRule{Unit: entity.ProgressUnit_PROGRESS_UNIT_ITEMS, Ratio: true}
	case entity.JobPhase_JOB_PHASE_GENERATING_PREVIEWS:
		// The decoder report, not the settled item count, is real generation throughput; its work
		// is a count of decoder observations, never the bytes of the content it produced.
		return StageRule{Unit: entity.ProgressUnit_PROGRESS_UNIT_ITEMS, Ratio: true, Estimate: true}
	case entity.JobPhase_JOB_PHASE_COPYING_TO_MEDIA, entity.JobPhase_JOB_PHASE_COPYING_FROM_MEDIA:
		return StageRule{Unit: stageUnit(denominator), Ratio: true, Estimate: true}
	case entity.JobPhase_JOB_PHASE_COMPLETED:
		return StageRule{Unit: stageUnit(denominator), Ratio: true}
	default:
		return StageRule{Unit: entity.ProgressUnit_PROGRESS_UNIT_ITEMS}
	}
}

// stageUnit selects the counter unit of a stage whose denominator may hold either amount. A
// zero-byte workload keeps its real work in item counts instead of a false byte percentage.
func stageUnit(denominator *Counts) entity.ProgressUnit {
	if denominator != nil && denominator.Bytes > 0 {
		return entity.ProgressUnit_PROGRESS_UNIT_BYTES
	}
	return entity.ProgressUnit_PROGRESS_UNIT_ITEMS
}

// amount reports one counter pair's amount in the stage's own unit.
func amount(unit entity.ProgressUnit, pair Counts) int64 {
	if unit == entity.ProgressUnit_PROGRESS_UNIT_BYTES {
		return pair.Bytes
	}
	return pair.Files
}

// ObserveStage records one work sample for a stage and reports the resulting window. Only a work
// path calls it, next to the counter update that produced the sample. A stage that may not offer an
// estimate has no window to keep; a stage whose denominator is not classified yet still records its
// work, because its rate must survive a request that has no counters to show.
func (p *Progress) ObserveStage(in StageInput) *entity.StageProgress {
	rule := RuleFor(in.Phase, in.Total)
	if !rule.Estimate {
		return nil
	}
	work, workTotal := sampledWork(rule, in)
	return p.meter().observe(in, rule, work, workTotal)
}

// StageReport assembles the shared reply from the recorded window without sampling; a progress
// request only calls this.
func (p *Progress) StageReport(in StageInput) *entity.Progress {
	rule := RuleFor(in.Phase, in.Total)
	work, workTotal := sampledWork(rule, in)
	result := p.ToEntity()
	result.Stage = p.meter().report(in, rule, work, workTotal)
	return result
}

// sampledWork selects the measured amount and the remaining workload of one stage, both in the
// stage's own unit. A stage the rule may not estimate has no sampled window at all, and a stage
// that never classified its workload is measured against its own denominator.
func sampledWork(rule StageRule, in StageInput) (int64, *int64) {
	if !rule.Estimate {
		return 0, nil
	}
	unit := rule.Unit
	if unit == entity.ProgressUnit_PROGRESS_UNIT_UNSPECIFIED {
		unit = stageUnit(in.Total)
	}
	work := amount(unit, in.Work)
	if !in.WorkKnown {
		if !in.Total.Denominated() {
			return work, nil
		}
		total := amount(unit, *in.Total)
		return work, &total
	}
	// A classified workload may legitimately be zero work; that reports counters without an ETA.
	total := amount(unit, in.Work)
	if in.WorkTotal != nil {
		total = amount(unit, *in.WorkTotal)
	}
	return work, &total
}

// StagePrefix is the attempt identity of one stage window, without its phase. A runner composes it
// with the phase it is about to report, so a new attempt under the same runner cannot inherit the
// previous attempt's samples.
func (p *Progress) StagePrefix() string {
	return strconv.FormatInt(p.startTime.UnixNano(), 10) + "/" +
		strconv.FormatInt(atomic.LoadInt64(&p.sessionStartedAt), 10)
}

// CopyStageInput maps the durable completed base and this attempt's session counters onto the copy
// stage's counters, and reports the manifest built so far as Completed during discovery. Archive
// and Restore share this one mapping; the caller supplies the attempt's own window prefix.
func (p *Progress) CopyStageInput(phase entity.JobPhase, prefix string) StageInput {
	live := p.ToEntity()
	completed := Counts{Files: live.CopiedFileCount, Bytes: live.CopiedBytes}
	// The counters a request displays are also the work this stage samples and the workload it has
	// to finish, so one mapping serves the request path and the work path.
	in := StageInput{Key: prefix + "/" + phase.String(), Phase: phase,
		Completed: completed, Work: completed, WorkKnown: true}
	if phase == entity.JobPhase_JOB_PHASE_COPYING_TO_MEDIA || phase == entity.JobPhase_JOB_PHASE_COPYING_FROM_MEDIA ||
		phase == entity.JobPhase_JOB_PHASE_COMPLETED {
		in.Total = &Counts{Files: live.TotalFileCount, Bytes: live.TotalBytes}
		in.WorkTotal = in.Total
	}
	return in
}

// stageMeter owns the bounded, attempt-local sampling window of one Progress object. Callers
// supply one coherent work window; the meter never reads durable state.
type stageMeter struct {
	sync.Mutex
	now      func() time.Time
	key      string
	windowID string
	sequence int64
	last     time.Time
	lastWork time.Time
	// base and previous hold sampled work in the unit the observing stage reported, so one meter
	// never mixes a byte window with an item window.
	base     int64
	previous int64
	rate     float64
	windows  int
	interval float64
	stale    bool
}

// meter returns the attempt-local sampling window, creating it with the Progress clock.
func (p *Progress) meter() *stageMeter {
	p.stageOnce.Do(func() {
		now := p.now
		if now == nil {
			now = time.Now
		}
		p.stage = &stageMeter{now: now}
	})
	return p.stage
}

// observe records one work sample for the stage range and reports the resulting window. It measures
// actual work separately from the displayed settled amount, and reports only the counter pair,
// which the caller supplied in the stage's own unit.
func (m *stageMeter) observe(in StageInput, rule StageRule, work int64, workTotal *int64) *entity.StageProgress {
	m.Lock()
	defer m.Unlock()
	now := m.now()
	if in.Key != m.key || m.windowID == "" {
		// A new range or phase discards every sample from its predecessor.
		m.sequence++
		m.key, m.windowID = in.Key, strconv.FormatInt(now.UnixNano(), 36)+"-"+strconv.FormatInt(m.sequence, 36)
		m.last, m.lastWork = now, now
		m.base, m.previous = work, work
		m.rate, m.windows, m.interval, m.stale = 0, 0, 0, false
	}

	// Completion intervals detect a genuinely stale estimate without reacting to every slow file.
	if now.Sub(m.lastWork).Seconds() > math.Max(30, 3*m.interval) && !m.stale {
		m.windows, m.rate, m.stale = 0, 0, true
		m.last, m.base, m.previous = now, work, work
	}
	if work > m.previous {
		interval := now.Sub(m.lastWork).Seconds() / float64(work-m.previous)
		if m.interval == 0 {
			m.interval = interval
		} else {
			m.interval = .25*interval + .75*m.interval
		}
		m.lastWork, m.previous = now, work
	}
	if now.Sub(m.lastWork).Seconds() > math.Max(30, 3*m.interval) {
		if !m.stale {
			m.windows, m.rate, m.stale = 0, 0, true
		}
		m.last, m.base = now, work
		return m.window(in, rule, work, workTotal)
	}

	// Fixed minimum windows smooth wall-clock throughput, including concurrent work and queue time.
	if elapsed := now.Sub(m.last).Seconds(); elapsed >= 5 {
		if delta := work - m.base; delta > 0 {
			rate := float64(delta) / elapsed
			if m.windows == 0 {
				m.rate = rate
			} else {
				m.rate = .25*rate + .75*m.rate
			}
			m.windows++
			m.stale = false
		}
		m.last, m.base = now, work
	}
	return m.window(in, rule, work, workTotal)
}

// report assembles the window the work path last sampled without recording a sample, so a progress
// request reads the estimate without changing it. A range this meter never sampled has no window
// identity and no estimate.
func (m *stageMeter) report(in StageInput, rule StageRule, work int64, workTotal *int64) *entity.StageProgress {
	m.Lock()
	defer m.Unlock()
	return m.window(in, rule, work, workTotal)
}

// window assembles the stage report from the recorded window and the caller's current range.
func (m *stageMeter) window(in StageInput, rule StageRule, work int64, workTotal *int64) *entity.StageProgress {
	denominator, settled := in.Total, in.Completed
	if !rule.Ratio {
		denominator = nil
	}
	if denominator.Denominated() {
		// A stage never exceeds its own total or reports a negative amount when a source changed.
		settled = Counts{Files: min(settled.Files, denominator.Files), Bytes: min(settled.Bytes, denominator.Bytes)}
	}
	unit := rule.Unit
	if unit == entity.ProgressUnit_PROGRESS_UNIT_UNSPECIFIED {
		unit = stageUnit(denominator)
	}
	result := &entity.StageProgress{Phase: in.Phase, Unit: unit, Completed: amount(unit, settled), EstimateState: entity.EstimateState_ESTIMATE_STATE_NOT_APPLICABLE}
	if denominator.Denominated() {
		result.Total = proto.Int64(amount(unit, *denominator))
	}
	if in.Key != m.key || m.windowID == "" {
		return result
	}
	result.WindowId = m.windowID
	if !rule.Estimate {
		return result
	}
	result.EstimateState = entity.EstimateState_ESTIMATE_STATE_ESTIMATING
	if m.rate > 0 {
		result.RatePerSecond = proto.Float64(m.rate)
	}
	if workTotal == nil || work < in.Minimum || work >= *workTotal || m.windows < 3 || m.rate <= 0 {
		return result
	}
	result.EstimateState = entity.EstimateState_ESTIMATE_STATE_ESTIMATED
	result.RemainingSeconds = proto.Int64(int64(math.Ceil(float64(*workTotal-work) / m.rate)))
	return result
}
