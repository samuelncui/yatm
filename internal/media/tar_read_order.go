package media

import (
	"context"
	"errors"
	"math"
	"time"

	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/internal/media/readplan"
)

func (s *tarReadSession) ReadOrder(ctx context.Context) (ReadOrderProvider, string, error) {
	// Missing qualification preserves the existing pipeline without an optional cursor query.
	if s.options.ReadOrder != entity.TapeReadOrder_TAPE_READ_ORDER_AUTO {
		return nil, "LOGICAL read order selected", nil
	}
	if s.readOrderDisabled {
		return nil, "tar record consumption no longer matches the read-order model", nil
	}
	if !s.readModelAvailable() {
		reason := s.options.ReadOrderReason
		if reason == "" {
			reason = "qualified seek and access-cost model are unavailable"
		}
		return nil, reason, nil
	}
	if s.options.Finish != nil && s.options.ReadCompletion == nil {
		return nil, "Normal Tape unload timing is unavailable", nil
	}

	// An eligible model cannot bypass cancellation or an unavailable physical read lane.
	if err := ctx.Err(); err != nil {
		return nil, "", err
	}
	if s.failed != nil || s.active || s.finalized {
		return nil, "", errors.Join(ErrReadUnavailable, s.failed, errors.New("tar read-order lane is unavailable"))
	}
	return &tarReadOrderProvider{session: s}, "", nil
}

type tarReadOrderProvider struct{ session *tarReadSession }

var _ OrderedReadSession = (*tarReadSession)(nil)

// ReadOrderPlanned forwards the planner outcome to the Session owner's optional observer.
func (s *tarReadSession) ReadOrderPlanned(order entity.TapeReadOrder, reason string) {
	if s.options.ReadOrderPlanned != nil {
		s.options.ReadOrderPlanned(order, reason)
	}
}

// FixedFirstReadState is unavailable: an unknown tar cursor retains LOGICAL order.
func (p *tarReadOrderProvider) FixedFirstReadState(readplan.Span) (readplan.State, bool) {
	return readplan.State{}, false
}

func (p *tarReadOrderProvider) Finish(from readplan.State) (time.Duration, bool) {
	return p.finishCost(from, p.session.options.ReadModel, p.session.options.ReadCompletion)
}

func (p *tarReadOrderProvider) finishCost(from readplan.State, model *ReadCostModel,
	completion *ReadCompletion,
) (time.Duration, bool) {
	s := p.session
	if s.failed != nil || s.active || s.finalized || !s.readModelAvailable() || !model.Valid() {
		return 0, false
	}
	// Prepared production Sessions unload; a content-only adapter may have no physical finish action.
	if s.options.Finish == nil {
		return 0, true
	}
	return model.UnloadCost(from.Position, completion)
}

func (p *tarReadOrderProvider) ReadPlanningBudget() time.Duration {
	return p.session.options.ReadPlanningBudget
}

// CanonicalReadState certifies the reachable post-member state without changing cache semantics.
func (p *tarReadOrderProvider) CanonicalReadState(span readplan.Span) (readplan.State, bool) {
	// Existing cached input must satisfy the same record-reader invariant as every reachable successor.
	s := p.session
	if !s.readModelAvailable() || len(span.Ranges) != 1 || !s.validReadRange(span.Ranges[0]) {
		return readplan.State{}, false
	}
	initial := s.readState()
	if initial.Cache != nil && (initial.Position == nil || initial.Stream == "" ||
		initial.Cache.Partition != 0 || initial.Cache.OffsetBytes != 0 || initial.Position.Partition != 0 ||
		initial.Position.OffsetBytes != 0 || initial.Cache.LogicalObject == math.MaxUint64 ||
		initial.Position.LogicalObject != initial.Cache.LogicalObject+1) {
		return readplan.State{}, false
	}

	// A real read ends after its containing record; cache-only duplicates already have that exact state.
	interval := span.Ranges[0]
	end, known := tarReadRecordEnd(interval.End, s.navigator.AddressBits())
	if !known || end == 0 || !s.modelCoversReadRange(interval) {
		return readplan.State{}, false
	}
	return readplan.State{Position: &entity.TapeBytePosition{LogicalObject: end}, Stream: interval.Stream,
		Cache: &entity.TapeBytePosition{LogicalObject: end - 1}}, true
}

func (s *tarReadSession) readModelAvailable() bool {
	return !s.readOrderDisabled && s.navigator != nil && s.options.ReadModel.Valid()
}

func tarReadRecordEnd(position *entity.TapeBytePosition, bits uint32) (uint64, bool) {
	// A member ending within a record consumes that whole record from the device, including retained bytes.
	if position == nil || position.Partition != 0 {
		return 0, false
	}
	limit := uint64(math.MaxUint64)
	if bits == 32 {
		limit = math.MaxUint32
	}
	end := position.LogicalObject
	if end > limit || position.OffsetBytes != 0 && end == limit {
		return 0, false
	}
	if position.OffsetBytes != 0 {
		end++
	}
	return end, true
}

func (s *tarReadSession) finishReadOrder(end *entity.TapeBytePosition, prediction *readplan.State) {
	// Use existing reader/locator state; no estimated cursor replaces an observed position.
	if s.options.ReadModel == nil && s.options.ReadTiming == nil || s.readOrderDisabled {
		return
	}
	if s.records == nil || !s.records.nextKnown {
		s.readOrderDisabled = true
		return
	}
	expected, known := tarReadRecordEnd(end, 64)
	if !known || expected != s.records.nextObject || prediction != nil && !sameTarReadState(s.readState(), *prediction) {
		s.readOrderDisabled = true
	}
}

func (p *tarReadOrderProvider) Ranges(request ReadRequest) ([]readplan.Range, bool) {
	// Keep member byte anchors and the physical file domain in every planning range.
	s := p.session
	if !s.readModelAvailable() {
		return nil, false
	}
	locator, err := tarLocator(request, uint32(s.options.Mode.MaximumBytes))
	if err != nil {
		return nil, false
	}
	interval, known := s.readRange(locator)
	if !known || !s.modelCoversReadRange(interval) {
		return nil, false
	}
	return []readplan.Range{interval}, true
}

func (p *tarReadOrderProvider) Current(ctx context.Context) (readplan.State, bool, error) {
	// The settled reader is the sole owner of host-next, current stream and retained record state.
	s := p.session
	if s.failed != nil || s.active || s.finalized {
		return readplan.State{}, false, errors.Join(ErrReadUnavailable, s.failed, errors.New("tar read-order cursor is unavailable"))
	}
	if err := ctx.Err(); err != nil {
		return readplan.State{}, false, err
	}
	if !s.readModelAvailable() {
		return readplan.State{}, false, nil
	}
	state := s.readState()
	if state.Position != nil {
		return state, true, nil
	}

	// An optional observation supplies a coordinate only, never an invented Tape file or buffer.
	position, err := s.observe(ctx)
	if err != nil {
		s.failed = err
		return readplan.State{}, false, err
	}
	if !position.ObjectKnown {
		return readplan.State{}, false, nil
	}
	if s.records == nil {
		s.records = s.recordReader(ctx, 0, position.Object, true)
	} else {
		s.records.nextObject, s.records.nextKnown = position.Object, true
	}
	return s.readState(), true, nil
}

func (p *tarReadOrderProvider) Evaluate(from readplan.State, to readplan.Range) (readplan.Transition, bool) {
	s := p.session
	if s.failed != nil || s.finalized || s.active || !s.readModelAvailable() {
		return readplan.Transition{}, false
	}
	return s.evaluateRead(from, to)
}

func (p *tarReadOrderProvider) ReadIndex(position *entity.TapeBytePosition) (readplan.Index, bool) {
	s := p.session
	if s.failed != nil || s.finalized || s.active || !s.readModelAvailable() {
		return readplan.Index{}, false
	}
	return s.options.ReadModel.ReadIndex(tarReadContainingRecord(position))
}

func tarReadContainingRecord(position *entity.TapeBytePosition) *entity.TapeBytePosition {
	if position == nil {
		return nil
	}
	return &entity.TapeBytePosition{Partition: position.Partition, LogicalObject: position.LogicalObject}
}
