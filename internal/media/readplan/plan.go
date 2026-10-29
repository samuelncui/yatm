// Package readplan orders one complete Tape read selection by estimated positioning and transfer time.
// It owns no Tape I/O: providers quote costs, and execution pages the frozen route from a disposable index.
package readplan

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"iter"
	"time"

	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/internal/resource"
)

// MaxRanges bounds one member's extents and one SQLite input batch.
const MaxRanges = 4096

// DefaultBudget covers indexing and search when the provider supplies no positive budget.
const DefaultBudget = 300 * time.Second

const insertBatch = 1024

// ErrBudget asks the caller to retain its original manifest when indexing is incomplete.
var ErrBudget = errors.New("read planning budget expired before a complete index was available")

// Range retains exact member anchors and its provider-owned stream identity.
type Range struct {
	Start, End *entity.TapeBytePosition
	Stream     string
}

// State describes settled host state, not the drive's instantaneous physical head position.
type State struct {
	Position *entity.TapeBytePosition
	Stream   string
	Cache    *entity.TapeBytePosition
}

type Method uint8

const (
	MethodUnknown Method = iota
	MethodCache
	MethodContinue
	MethodSeek
)

// Transition quotes the nonnegative estimated time for a range and its resulting host state.
type Transition struct {
	Cost   time.Duration
	Next   State
	Method Method
}

// Span retains the caller's item identity and planned access sequence without codec-specific metadata.
type Span struct {
	ID     int64
	Ranges []Range
	// Path enables optional current-Index enrichment when Ranges is absent; Size is then known, including zero.
	Path string
	Size int64
}

// RangeEntry is one current on-Media path's complete range observation.
type RangeEntry struct {
	Path   string
	Size   int64
	Ranges []Range
}

// RangeSource streams a prepared, current Index once to fill missing selected ranges.
// It performs no native Tape I/O, honors context and callback errors, and may reuse values after yield returns.
// Its own unavailable/malformed Index errors withdraw enrichment; callback failures remain planner errors.
type RangeSource interface {
	WalkReadRanges(context.Context, func(RangeEntry) error) error
}

// Index assigns one coordinate to the model's sorted spatial search groups.
type Index struct {
	Groups   [][]byte
	Position []byte
}

// Provider quotes one frozen Media snapshot. Every method is pure except Current, which may observe the cursor.
type Provider interface {
	// Current returns the prepared host state; unknown asks for a fixed first member.
	Current(context.Context) (State, bool, error)
	Evaluate(State, Range) (Transition, bool)
	// CanonicalReadState certifies a complete member's incoming-independent exit state.
	CanonicalReadState(Span) (State, bool)
	// FixedFirstReadState forecasts a complete first member's exit despite an unknown origin.
	FixedFirstReadState(Span) (State, bool)
	// ReadIndex supplies candidate neighbourhood coordinates; complete costs still decide every move.
	ReadIndex(*entity.TapeBytePosition) (Index, bool)
	// Finish quotes the completion action from the final host state without I/O.
	Finish(State) (time.Duration, bool)
	ReadPlanningBudget() time.Duration
}

// Planning reasons recorded by callers; LOGICAL ones mean the original manifest order is kept.
const (
	reasonBudget     = "LOGICAL: read planning budget expired"
	reasonIncomplete = "LOGICAL: incomplete read ranges or spatial index"
	reasonCanonical  = "LOGICAL: member exit state cannot be certified"
	reasonInitial    = "LOGICAL: initial read position unavailable"
	reasonCosts      = "LOGICAL: incomplete read costs"
	reasonNoGain     = "LOGICAL: no lower complete-route estimate"
	reasonPlanned    = "AUTO: complete-selection directed planning improves the estimated route"
)

// row belongs only to the disposable planning database. Baseline ranks and CopyIDs never change;
// Planned is a completely scored execution permutation, never a Tape address.
type row struct {
	Rank      int64 `gorm:"primaryKey;autoIncrement:false"`
	CopyID    int64 `gorm:"not null;uniqueIndex"`
	Planned   int64 `gorm:"not null;index"`
	Ranges    []byte
	Canonical []byte
	Path      string
	Size      int64
	RangeSeen uint8
}

func (row) TableName() string { return "read_order" }

type candidate struct {
	Rank     int64  `gorm:"primaryKey;autoIncrement:false;index:idx_read_position,priority:4;index:idx_read_group_rank,priority:3"`
	Side     uint8  `gorm:"primaryKey;autoIncrement:false;index:idx_read_position,priority:1;index:idx_read_group_rank,priority:1"`
	GroupKey []byte `gorm:"primaryKey;type:blob;index:idx_read_position,priority:2;index:idx_read_group_rank,priority:2"`
	Position []byte `gorm:"type:blob;index:idx_read_position,priority:3"`
}

func (candidate) TableName() string { return "read_candidates" }

// Sequence owns one complete disposable execution index; execution never re-enters its provider.
type Sequence struct {
	stage         *resource.TemporaryDB
	provider      Provider
	after         int64
	planned       bool
	reason        string
	deadline      time.Time
	now           func() time.Time
	fixedFirst    *State
	runEnds       map[int64]int64
	runGeneration uint64
}

// Plan improves one complete LOGICAL route by directed segment moves, reconnection and bounded kicks.
// The budget never becomes the context of a native observation or unfinished transaction.
func Plan(ctx context.Context, root string, provider Provider, input iter.Seq2[Span, error]) (*Sequence, error) {
	return plan(ctx, root, provider, input, time.Now)
}

func plan(ctx context.Context, root string, provider Provider, input iter.Seq2[Span, error],
	now func() time.Time,
) (_ *Sequence, rerr error) {
	// Construction retains cleanup ownership until every selected identity has been indexed.
	if provider == nil || input == nil {
		return nil, errors.New("read sequence requires a provider and an input")
	}
	budget := DefaultBudget
	if value := provider.ReadPlanningBudget(); value > 0 {
		budget = value
	}
	stage, err := resource.OpenTemporaryDB(root, "read-order-")
	if err != nil {
		return nil, err
	}
	s := &Sequence{stage: stage, provider: provider, now: now, deadline: now().Add(budget)}
	defer func() {
		if rerr != nil {
			rerr = errors.Join(rerr, s.Close())
		}
	}()
	for _, statement := range []string{"PRAGMA cache_size = -2048", "PRAGMA temp_store = FILE"} {
		if err := stage.DB.WithContext(ctx).Exec(statement).Error; err != nil {
			return nil, err
		}
	}
	if err := stage.DB.WithContext(ctx).AutoMigrate(&row{}, &candidate{}); err != nil {
		return nil, fmt.Errorf("create read execution index failed, %w", err)
	}

	// Partial materialization cannot stand in for the caller's complete manifest.
	count, missing, err := s.collect(ctx, input)
	if err != nil {
		return nil, err
	}
	if s.reason != "" {
		return s, nil
	}
	if count < 2 {
		s.reason = reasonNoGain
		return s, nil
	}

	// Current Index ranges share the selected rows, budget and cleanup with ordinary planning.
	if missing {
		known, err := s.completeRanges(ctx)
		if errors.Is(err, ErrBudget) {
			s.reason = reasonBudget
			return s, nil
		}
		if err != nil {
			return nil, err
		}
		if !known {
			return s, nil
		}
	}

	// An unknown origin fixes the first LOGICAL member and compares only the identical-prefix remainder.
	if err := s.check(ctx); errors.Is(err, ErrBudget) {
		s.reason = reasonBudget
		return s, nil
	} else if err != nil {
		return nil, err
	}
	current, available, err := provider.Current(ctx)
	if err != nil {
		return nil, err
	}
	if !available || current.Position == nil {
		first, err := s.firstSpan(ctx)
		if err != nil {
			return nil, err
		}
		state, known := provider.FixedFirstReadState(first)
		if !known || state.Position == nil {
			s.reason = reasonInitial
			return s, nil
		}
		s.fixedFirst = &state
		current = state
	}

	// Whole-route scoring and checkpoint commits own final adoption; partial ranks are never exposed.
	if err := s.optimize(ctx, current, count); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *Sequence) check(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if !s.now().Before(s.deadline) {
		return ErrBudget
	}
	return nil
}

// collect stores every selected identity; the first unusable member records a LOGICAL reason and
// stops further provider calls while the remaining identities are still indexed for execution.
func (s *Sequence) collect(ctx context.Context, input iter.Seq2[Span, error]) (int64, bool, error) {
	rows := make([]row, 0, insertBatch)
	var candidates []candidate
	count, ranges, missing := int64(0), 0, false
	_, enrich := s.provider.(RangeSource)
	for span, err := range input {
		if err != nil {
			return 0, false, err
		}
		if err := s.check(ctx); err != nil {
			return 0, false, err
		}
		if span.ID <= 0 {
			return 0, false, errors.New("read sequence has an invalid identity")
		}
		count++
		value := row{Rank: count, CopyID: span.ID}
		var indices [2]Index
		switch {
		case s.reason != "":
		case len(span.Ranges) == 0 && enrich && span.Path != "" && span.Size >= 0:
			value.Path, value.Size, missing = span.Path, span.Size, true
		case len(span.Ranges) == 0:
			s.reason = reasonIncomplete
		default:
			var err error
			if value, indices, err = s.rangeRow(count, span); err != nil {
				return 0, false, err
			}
		}

		// Keep row and range batches bounded even when a single selected member has many extents.
		if len(rows) > 0 && (len(rows) == insertBatch || len(span.Ranges) > MaxRanges-ranges) {
			if err := s.insert(ctx, rows, candidates); err != nil {
				return 0, false, err
			}
			rows, candidates, ranges = rows[:0], candidates[:0], 0
		}
		rows = append(rows, value)
		if value.Ranges != nil {
			ranges += len(span.Ranges)
			candidates = appendCandidates(candidates, count, indices)
		}
	}
	if err := s.insert(ctx, rows, candidates); err != nil {
		return 0, false, err
	}
	return count, missing, ctx.Err()
}

// rangeRow encodes one complete member with its certified exit and both spatial endpoints.
// An unusable member records the LOGICAL reason and returns a row without ranges.
func (s *Sequence) rangeRow(rank int64, span Span) (row, [2]Index, error) {
	value := row{Rank: rank, CopyID: span.ID}
	var indices [2]Index
	if !validRanges(span.Ranges) {
		if len(span.Ranges) > MaxRanges {
			s.reason = reasonIncomplete
			return value, indices, nil
		}
		return value, indices, errors.New("read selection has an invalid interval")
	}
	exit, known := s.provider.CanonicalReadState(span)
	if !known || exit.Position == nil {
		s.reason = reasonCanonical
		return value, indices, nil
	}
	for side, position := range [2]*entity.TapeBytePosition{span.Ranges[0].Start, exit.Position} {
		if indices[side], known = s.spatialIndex(position); !known {
			s.reason = reasonIncomplete
			return value, indices, nil
		}
	}

	// Encoding succeeds before any database row or candidate becomes visible.
	encoded, err := json.Marshal(span.Ranges)
	if err != nil {
		return value, indices, err
	}
	canonical, err := json.Marshal(exit)
	if err != nil {
		return value, indices, err
	}
	value.Ranges, value.Canonical = encoded, canonical
	return value, indices, nil
}

func appendCandidates(candidates []candidate, rank int64, indices [2]Index) []candidate {
	for side, index := range indices {
		for _, group := range index.Groups {
			candidates = append(candidates, candidate{Rank: rank, Side: uint8(side), GroupKey: group, Position: index.Position})
		}
	}
	return candidates
}

func validRanges(ranges []Range) bool {
	if len(ranges) == 0 || len(ranges) > MaxRanges {
		return false
	}
	for _, interval := range ranges {
		if interval.Start == nil || interval.End == nil || interval.Start.Partition != interval.End.Partition ||
			comparePosition(interval.Start, interval.End) >= 0 {
			return false
		}
	}
	return true
}

func comparePosition(a, b *entity.TapeBytePosition) int {
	if a.LogicalObject < b.LogicalObject || a.LogicalObject == b.LogicalObject && a.OffsetBytes < b.OffsetBytes {
		return -1
	}
	if a.LogicalObject == b.LogicalObject && a.OffsetBytes == b.OffsetBytes {
		return 0
	}
	return 1
}

func (s *Sequence) insert(ctx context.Context, rows []row, candidates []candidate) error {
	if len(rows) == 0 {
		return nil
	}
	// Batches bound SQLite parameters independently of its build-specific variable limit.
	if err := s.stage.DB.WithContext(ctx).CreateInBatches(rows, 100).Error; err != nil {
		return fmt.Errorf("store read execution index failed, %w", err)
	}
	if len(candidates) > 0 {
		if err := s.stage.DB.WithContext(ctx).CreateInBatches(candidates, 100).Error; err != nil {
			return fmt.Errorf("store read search index failed, %w", err)
		}
	}
	return nil
}

// spatialIndex keeps the model's first two local groups and adds one per-partition group,
// so cross-wrap neighbours stay reachable independently of the cartridge's populated wrap count.
func (s *Sequence) spatialIndex(position *entity.TapeBytePosition) (Index, bool) {
	if position == nil {
		return Index{}, false
	}
	index, known := s.provider.ReadIndex(position)
	if !known || len(index.Groups) == 0 || len(index.Position) == 0 {
		return Index{}, false
	}
	index.Groups = append([][]byte(nil), index.Groups[:min(2, len(index.Groups))]...)
	global := make([]byte, 5)
	global[0] = 255
	binary.BigEndian.PutUint32(global[1:], position.Partition)
	index.Groups = append(index.Groups, global)
	return index, true
}

// Next pages the frozen sequence, independent of source settlement and result publication.
func (s *Sequence) Next(ctx context.Context, limit int) ([]int64, error) {
	if s.stage == nil {
		return nil, errors.New("read execution index is closed")
	}
	if limit <= 0 {
		return nil, errors.New("read execution page limit must be positive")
	}

	// Select only ranks and identities; execution no longer loads geometry or invokes its provider.
	column := "rank"
	if s.planned {
		column = "planned"
	}
	var rows []row
	if err := s.stage.DB.WithContext(ctx).Select("rank", "planned", "copy_id").
		Where(column+" > ?", s.after).Order(column).Limit(limit).Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("load read execution page failed, rank=%d, %w", s.after, err)
	}
	ids := make([]int64, len(rows))
	for i, value := range rows {
		ids[i], s.after = value.CopyID, value.Rank
		if s.planned {
			s.after = value.Planned
		}
	}
	return ids, nil
}

// Reason describes the strategy selected for this complete attempt.
func (s *Sequence) Reason() string { return s.reason }

// Order reports AUTO only when a lower complete route replaced the LOGICAL manifest.
func (s *Sequence) Order() entity.TapeReadOrder {
	if s.planned {
		return entity.TapeReadOrder_TAPE_READ_ORDER_AUTO
	}
	return entity.TapeReadOrder_TAPE_READ_ORDER_LOGICAL
}

// Close releases the disposable index after the caller has finished feeding it.
func (s *Sequence) Close() error {
	// Only successful cleanup relinquishes the retryable directory owner.
	if s.stage == nil {
		return nil
	}
	if err := s.stage.Close(); err != nil {
		return err
	}
	s.stage, s.runEnds = nil, nil
	return nil
}
