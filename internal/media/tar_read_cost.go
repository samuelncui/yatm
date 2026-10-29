package media

import (
	"math"
	"math/bits"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/internal/media/readplan"
	"google.golang.org/protobuf/proto"
)

func tarReadStreamFile(stream string) (uint64, bool) {
	if len(stream) != len("archives/")+16 || !strings.HasPrefix(stream, "archives/") {
		return 0, false
	}
	hex := stream[len("archives/"):]
	for _, digit := range hex {
		if !(digit >= '0' && digit <= '9' || digit >= 'a' && digit <= 'f') {
			return 0, false
		}
	}
	file, err := strconv.ParseUint(hex, 16, 64)
	return file, err == nil
}

func cloneTarReadState(state readplan.State) readplan.State {
	if state.Position != nil {
		state.Position = proto.Clone(state.Position).(*entity.TapeBytePosition)
	}
	if state.Cache != nil {
		state.Cache = proto.Clone(state.Cache).(*entity.TapeBytePosition)
	}
	return state
}

func (s *tarReadSession) readRange(locator *entity.TarMetadata) (readplan.Range, bool) {
	// Exact byte anchors remain authoritative; whole-record rounding belongs to action accounting.
	interval := readplan.Range{Start: locator.Header, End: locator.End, Stream: tarPrefix(locator.TapeFile)}
	if !s.validReadRange(interval) {
		return readplan.Range{}, false
	}
	interval.Start = proto.Clone(interval.Start).(*entity.TapeBytePosition)
	interval.End = proto.Clone(interval.End).(*entity.TapeBytePosition)
	return interval, true
}

func (s *tarReadSession) validReadRange(interval readplan.Range) bool {
	if interval.Start == nil || interval.End == nil || interval.Start.Partition != 0 || interval.End.Partition != 0 ||
		uint64(interval.Start.OffsetBytes) >= uint64(s.options.Mode.MaximumBytes) ||
		uint64(interval.End.OffsetBytes) >= uint64(s.options.Mode.MaximumBytes) || comparePosition(interval.Start, interval.End) >= 0 {
		return false
	}
	if _, ok := tarReadStreamFile(interval.Stream); !ok {
		return false
	}
	width := uint32(64)
	if s.navigator != nil {
		width = s.navigator.AddressBits()
	}
	_, known := tarReadRecordEnd(interval.End, width)
	return known && (width != 32 || interval.Start.LogicalObject <= math.MaxUint32)
}

func (s *tarReadSession) evaluateRead(state readplan.State, interval readplan.Range) (readplan.Transition, bool) {
	// An actual cached containing record is usable without any measured timing or position model.
	if s.readOrderDisabled || !s.validReadRange(interval) {
		return readplan.Transition{}, false
	}
	if s.options.ReadModel.Valid() && !s.modelCoversReadRange(interval) {
		return readplan.Transition{}, false
	}
	end, _ := tarReadRecordEnd(interval.End, 64)
	start := interval.Start.LogicalObject
	var selected readplan.Transition
	var selectedRecords uint64
	known := false
	offer := func(method readplan.Method, records, marks uint64, setup time.Duration) bool {
		cost, ok := s.readTransferCost(records, marks)
		if !ok {
			return false
		}
		transition := readplan.Transition{Cost: cost + setup, Method: method}
		if !known || transition.Cost < selected.Cost || transition.Cost == selected.Cost && method < selected.Method {
			selected, selectedRecords, known = transition, records, true
		}
		return true
	}
	if state.Stream == interval.Stream && state.Cache != nil && state.Cache.Partition == 0 &&
		state.Cache.OffsetBytes == 0 && state.Cache.LogicalObject == start {
		if end-start == 1 {
			offer(readplan.MethodCache, 0, 0, 0)
		} else if state.Position != nil && state.Position.Partition == 0 && state.Position.OffsetBytes == 0 &&
			start != math.MaxUint64 && state.Position.LogicalObject == start+1 {
			offer(readplan.MethodCache, end-state.Position.LogicalObject, 0, 0)
		}
	}

	// Only a real LOCATE pays the effective post-LOCATE READ overhead.
	seekKnown := false
	if s.navigator != nil && s.options.ReadModel.Valid() && state.Position != nil &&
		s.options.ReadTiming != nil && s.options.ReadTiming.PostLocateReadOverheadSeconds != nil {
		target := &entity.TapeBytePosition{LogicalObject: start}
		setup, ok := s.options.ReadModel.AccessCost(state.Position, target)
		extra, extraOK := readSeconds(*s.options.ReadTiming.PostLocateReadOverheadSeconds)
		if ok && extraOK {
			seekKnown = offer(readplan.MethodSeek, end-start, 0, setup+extra)
		}
	}

	// With an eligible LOCATE lane, missing positioning cost cannot justify an avoidable gap READ.
	// Zero-gap and cached-tail reads need no such comparison; a sequential-only lane keeps read-through.
	if state.Position != nil && state.Position.Partition == 0 && state.Position.OffsetBytes == 0 && state.Position.LogicalObject <= start {
		from, fromKnown := tarReadStreamFile(state.Stream)
		to, _ := tarReadStreamFile(interval.Stream)
		if fromKnown && to >= from {
			marks := to - from
			if marks <= start-state.Position.LogicalObject &&
				(s.navigator == nil || seekKnown || marks == 0 && start == state.Position.LogicalObject) {
				offer(readplan.MethodContinue, end-state.Position.LogicalObject-marks, marks, 0)
			}
		}
	}

	// Allocate only the chosen successor; competing actions use immutable incoming values while pricing.
	if known {
		if selectedRecords == 0 {
			selected.Next = cloneTarReadState(state)
		} else {
			selected.Next = readplan.State{Position: &entity.TapeBytePosition{LogicalObject: end}, Stream: interval.Stream,
				Cache: &entity.TapeBytePosition{LogicalObject: end - 1}}
		}
	}
	return selected, known
}

func (s *tarReadSession) modelCoversReadRange(interval readplan.Range) bool {
	// EOD is a usable host-next cursor, but never a containing record of a member.
	model := s.options.ReadModel
	if !model.Valid() || !s.validReadRange(interval) {
		return false
	}
	end, _ := tarReadRecordEnd(interval.End, 64)
	start := interval.Start.LogicalObject
	wraps := model.intervals[interval.Start.Partition]
	at := sort.Search(len(wraps), func(i int) bool { return wraps[i].high > start })
	if at == len(wraps) || start < wraps[at].low {
		return false
	}

	// Both endpoints alone cannot establish coverage across a missing interior interval.
	covered := wraps[at].high
	for covered < end && at+1 < len(wraps) {
		at++
		if wraps[at].low != covered {
			return false
		}
		covered = wraps[at].high
	}
	return end <= covered
}

func (s *tarReadSession) readTransferCost(records, marks uint64) (time.Duration, bool) {
	// Pure host-buffer work has no native latency or empirical timing prerequisite.
	if records == 0 && marks == 0 {
		return 0, true
	}
	// A nonempty transfer needs the measured rate.
	timing := s.options.ReadTiming
	if timing == nil || timing.BytesPerSecond == nil {
		return 0, false
	}
	rate := *timing.BytesPerSecond
	if rate <= 0 || math.IsNaN(rate) || math.IsInf(rate, 0) {
		return 0, false
	}
	high, bytes := bits.Mul64(records, uint64(s.options.Mode.MaximumBytes))
	if high != 0 {
		return 0, false
	}
	estimate := float64(bytes) / rate
	if marks != 0 {
		if timing.FilemarkOverheadSeconds == nil || *timing.FilemarkOverheadSeconds < 0 ||
			math.IsNaN(*timing.FilemarkOverheadSeconds) || math.IsInf(*timing.FilemarkOverheadSeconds, 0) {
			return 0, false
		}
		estimate += float64(marks) * *timing.FilemarkOverheadSeconds
	}
	return readSeconds(estimate)
}

func (s *tarReadSession) readState() readplan.State {
	// Only preparation or controlled traversal can establish the physical file identity.
	state := readplan.State{}
	if s.fileKnown {
		state.Stream = tarPrefix(s.file)
	}
	// Host-next and retained bytes remain distinct facts owned by the settled record reader.
	r := s.records
	if r == nil {
		return state
	}
	if r.nextKnown {
		state.Position = &entity.TapeBytePosition{LogicalObject: r.nextObject}
	}
	if s.fileKnown && r.known && r.length > 0 && !r.boundary.Filemark && !r.boundary.EOD {
		state.Cache = &entity.TapeBytePosition{LogicalObject: r.object}
	}
	return state
}

func (s *tarReadSession) readMemberEnd(locator *entity.TarMetadata) (uint64, bool) {
	if locator == nil || !s.validReadRange(readplan.Range{Start: locator.Header, End: locator.End, Stream: tarPrefix(locator.TapeFile)}) ||
		locator.HeaderOffsetBytes < uint64(locator.Header.OffsetBytes) {
		return 0, false
	}
	start := locator.HeaderOffsetBytes - uint64(locator.Header.OffsetBytes)
	high, span := bits.Mul64(locator.End.LogicalObject-locator.Header.LogicalObject, uint64(s.options.Mode.MaximumBytes))
	if high != 0 || start > math.MaxUint64-span || start+span > math.MaxUint64-uint64(locator.End.OffsetBytes) {
		return 0, false
	}
	return start + span + uint64(locator.End.OffsetBytes), true
}

func sameTarReadState(left, right readplan.State) bool {
	return left.Stream == right.Stream && proto.Equal(left.Position, right.Position) && proto.Equal(left.Cache, right.Cache)
}
