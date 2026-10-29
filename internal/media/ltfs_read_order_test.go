package media

import (
	"context"
	"math"
	"testing"
	"time"

	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/internal/media/readplan"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

func ltfsOrderFixture(t testing.TB) *ltfsReadOrderProvider {
	t.Helper()
	// Two full wraps expose opposite longitudinal directions in an explicitly mapped physical partition.
	model, err := NewReadCostModel(ReadModelProfile{ID: "ltfs-test", SnapshotValid: func() bool { return true },
		Boundary: ReadEndpointExclusive, BOT: map[uint32]uint64{1: 0}, FullWrapSeconds: proto.Float64(1000),
		Wraps: []ReadModelWrap{
			{Partition: 1, Wrap: 4, Full: true, Endpoint: proto.Uint64(100), Direction: ReadWrapForward},
			{Partition: 1, Wrap: 5, Full: true, Endpoint: proto.Uint64(200), Direction: ReadWrapReverse},
		}})
	require.NoError(t, err)
	provider, reason := NewLTFSReadOrderProvider(LTFSReadOptions{BlockBytes: 4, Compression: proto.Bool(false),
		Partitions: map[string]uint32{"z": 1}, Model: model, PlanningBudget: time.Minute,
		Completion: &ReadCompletion{FullSpanSeconds: 1000, FixedSeconds: 20},
		DataEnd:    &entity.TapeBytePosition{Partition: 1, LogicalObject: 200},
		Timing:     &entity.TapeReadTiming{BytesPerSecond: proto.Float64(4), PostLocateReadOverheadSeconds: proto.Float64(2)}})
	require.NotNil(t, provider, reason)
	return provider.(*ltfsReadOrderProvider)
}

func ltfsRequest(extents ...*entity.LtfsExtent) ReadRequest {
	return ReadRequest{Path: "file", Storage: &entity.StoragePosition{Metadata: (&entity.LtfsMetadata{Extents: extents}).Pack()}}
}

func TestLTFSReadOrderRangesFollowFileOffsetsAndActualBlockSize(t *testing.T) {
	// Stored extent order is retained in metadata; the planner visits the native file's byte order.
	provider := ltfsOrderFixture(t)
	request := ltfsRequest(
		&entity.LtfsExtent{Partition: "z", StartBlock: 40, ByteOffset: 0, ByteCount: 4, FileOffsetBytes: 20},
		&entity.LtfsExtent{Partition: "z", StartBlock: 10, ByteOffset: 3, ByteCount: 10, FileOffsetBytes: 0},
	)
	ranges, known := provider.Ranges(request)
	require.True(t, known)
	require.Len(t, ranges, 2)
	require.Equal(t, uint64(10), ranges[0].Start.LogicalObject)
	require.Equal(t, uint32(1), ranges[0].Start.Partition)
	require.Equal(t, uint32(3), ranges[0].Start.OffsetBytes)
	require.Equal(t, uint64(13), ranges[0].End.LogicalObject)
	require.Equal(t, uint32(1), ranges[0].End.OffsetBytes)
	require.Equal(t, uint64(40), ranges[1].Start.LogicalObject)
	require.Equal(t, uint64(40), request.Storage.Metadata.GetLtfs().Extents[0].StartBlock)

	// Sparse gaps cause no Tape range; the final containing block defines the forecast next/cache state.
	state, known := provider.FixedFirstReadState(readplan.Span{ID: 1, Ranges: ranges})
	require.True(t, known)
	require.Equal(t, uint64(41), state.Position.LogicalObject)
	require.Equal(t, uint64(40), state.Cache.LogicalObject)
	current, available, err := provider.Current(context.Background())
	require.NoError(t, err)
	require.False(t, available)
	require.Nil(t, current.Position)
}

func TestLTFSReadOrderPricesOnlyExistingReaderActions(t *testing.T) {
	for _, test := range []struct {
		name       string
		position   uint64
		cache      *entity.TapeBytePosition
		start, end uint64
		method     readplan.Method
		seconds    float64
	}{
		{"last block", 11, &entity.TapeBytePosition{Partition: 1, LogicalObject: 10}, 10, 11, readplan.MethodCache, 0},
		{"cached start and following block", 11, &entity.TapeBytePosition{Partition: 1, LogicalObject: 10}, 10, 12, readplan.MethodCache, 1},
		{"immediately next block", 10, nil, 10, 11, readplan.MethodContinue, 1},
		{"forward gap seeks", 8, nil, 10, 11, readplan.MethodSeek, 23},
		{"backward seeks", 15, nil, 10, 11, readplan.MethodSeek, 53},
		{"cache without matching host cursor cannot skip", 15, &entity.TapeBytePosition{Partition: 1, LogicalObject: 10}, 10, 11, readplan.MethodSeek, 53},
	} {
		t.Run(test.name, func(t *testing.T) {
			// LTFS's observed cache predicate requires both the last block and its host-next coordinate.
			provider := ltfsOrderFixture(t)
			interval := readplan.Range{Start: &entity.TapeBytePosition{Partition: 1, LogicalObject: test.start},
				End: &entity.TapeBytePosition{Partition: 1, LogicalObject: test.end}, Stream: "ltfs"}
			transition, known := provider.Evaluate(readplan.State{Position: &entity.TapeBytePosition{Partition: 1,
				LogicalObject: test.position}, Cache: test.cache, Stream: "ltfs"}, interval)
			require.True(t, known)
			require.Equal(t, test.method, transition.Method)
			require.Equal(t, time.Duration(test.seconds*float64(time.Second)), transition.Cost)
			require.Equal(t, test.end, transition.Next.Position.LogicalObject)
			require.Equal(t, test.end-1, transition.Next.Cache.LogicalObject)
		})
	}
}

func TestLTFSReadOrderRejectsIncompleteOrInvalidExtentFacts(t *testing.T) {
	for _, test := range []struct {
		name string
		edit func(*entity.LtfsMetadata)
	}{
		{"unknown symbol", func(m *entity.LtfsMetadata) { m.Extents[0].Partition = "a" }},
		{"nil extent", func(m *entity.LtfsMetadata) { m.Extents[0] = nil }},
		{"zero bytes", func(m *entity.LtfsMetadata) { m.Extents[0].ByteCount = 0 }},
		{"offset outside block", func(m *entity.LtfsMetadata) { m.Extents[0].ByteOffset = 4 }},
		{"offset overflow", func(m *entity.LtfsMetadata) { m.Extents[0].ByteCount = math.MaxUint64 }},
		{"block overflow", func(m *entity.LtfsMetadata) { m.Extents[0].StartBlock = math.MaxUint64 }},
		{"file offset overflow", func(m *entity.LtfsMetadata) { m.Extents[0].FileOffsetBytes = math.MaxUint64 }},
		{"outside geometry", func(m *entity.LtfsMetadata) { m.Extents[0].StartBlock = 199 }},
		{"overlapping extents", func(m *entity.LtfsMetadata) {
			m.Extents = append(m.Extents, &entity.LtfsExtent{Partition: "z", StartBlock: 40, ByteCount: 1, FileOffsetBytes: 1})
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			provider := ltfsOrderFixture(t)
			request := ltfsRequest(&entity.LtfsExtent{Partition: "z", StartBlock: 10, ByteOffset: 3, ByteCount: 10})
			test.edit(request.Storage.Metadata.GetLtfs())
			_, known := provider.Ranges(request)
			require.False(t, known)
		})
	}
}

func TestLTFSReadOrderRequiresKnownUncompressedLabel(t *testing.T) {
	for _, compression := range []*bool{nil, proto.Bool(true)} {
		options := ltfsOrderFixture(t).options
		options.Compression = compression
		provider, reason := NewLTFSReadOrderProvider(options)
		require.Nil(t, provider)
		require.Contains(t, reason, "compression")
	}
}

func TestLTFSReadOrderOptimizesCompleteSelectionWithFixedFirstFile(t *testing.T) {
	// A fragmented first file ends near later members, so start-block sorting makes an avoidable round trip.
	provider := ltfsOrderFixture(t)
	provider.options.Timing.BytesPerSecond = proto.Float64(1e9)
	requests := []ReadRequest{
		ltfsRequest(&entity.LtfsExtent{Partition: "z", StartBlock: 10, ByteCount: 4},
			&entity.LtfsExtent{Partition: "z", StartBlock: 79, ByteCount: 4, FileOffsetBytes: 4}),
		ltfsRequest(&entity.LtfsExtent{Partition: "z", StartBlock: 20, ByteCount: 4}),
		ltfsRequest(&entity.LtfsExtent{Partition: "z", StartBlock: 80, ByteCount: 4}),
		ltfsRequest(&entity.LtfsExtent{Partition: "z", StartBlock: 120, ByteCount: 4}),
	}
	spans := make([]readplan.Span, len(requests))
	for i, request := range requests {
		ranges, known := provider.Ranges(request)
		require.True(t, known)
		spans[i] = readplan.Span{ID: int64(i + 1), Ranges: ranges}
	}
	sequence, err := readplan.Plan(t.Context(), t.TempDir(), provider, sequenceTestInput(spans))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, sequence.Close()) })

	// The same first native file remains fixed, and the frozen route is consumed independently of pages.
	ids := sequenceTestIDs(t, sequence, 1)
	require.Equal(t, int64(1), ids[0])
	require.ElementsMatch(t, []int64{1, 2, 3, 4}, ids)
	require.NotEqual(t, []int64{1, 2, 3, 4}, ids, sequence.Reason())

	// Independent complete reads plus the clean terminal action reproduce the committed route's cost.
	baseline := ltfsCompleteRouteCost(t, provider, spans, []int64{1, 2, 3, 4})
	candidate := ltfsCompleteRouteCost(t, provider, spans, ids)
	require.Less(t, candidate, baseline)
}

func TestLTFSReadOrderReturnedStateOwnership(t *testing.T) {
	for _, test := range []struct {
		name      string
		position  uint64
		end       uint64
		offset    uint32
		canonical bool
		forecast  uint64
	}{
		{"cache", 11, 11, 0, false, 11},
		{"continue", 10, 12, 0, false, 12},
		{"seek", 8, 12, 0, false, 12},
		{"partial end", 8, 12, 1, false, 13},
		{"canonical", 8, 12, 1, true, 13},
	} {
		t.Run(test.name, func(t *testing.T) {
			// Keep independent incoming, extent and canonical values to detect borrowed result pointers.
			provider := ltfsOrderFixture(t)
			initial := readplan.State{Position: &entity.TapeBytePosition{Partition: 1, LogicalObject: test.position},
				Cache: &entity.TapeBytePosition{Partition: 1, LogicalObject: test.position - 1}, Stream: "ltfs"}
			interval := readplan.Range{Start: &entity.TapeBytePosition{Partition: 1, LogicalObject: 10},
				End: &entity.TapeBytePosition{Partition: 1, LogicalObject: test.end, OffsetBytes: test.offset}, Stream: "ltfs"}
			beforePosition, beforeCache := proto.Clone(initial.Position), proto.Clone(initial.Cache)
			beforeStart, beforeEnd := proto.Clone(interval.Start), proto.Clone(interval.End)
			span := readplan.Span{ID: 1, Ranges: []readplan.Range{interval}}
			canonical, known := provider.CanonicalReadState(span)
			require.True(t, known)
			quote := func() (readplan.State, bool) {
				if test.canonical {
					return provider.CanonicalReadState(span)
				}
				transition, known := provider.Evaluate(initial, interval)
				return transition.Next, known
			}
			first, known := quote()
			require.True(t, known)
			second, known := quote()
			require.True(t, known)
			require.Equal(t, test.forecast, first.Position.LogicalObject)
			require.Equal(t, test.forecast-1, first.Cache.LogicalObject)
			require.NotSame(t, first.Position, first.Cache)
			require.True(t, sameTarReadState(first, canonical))
			require.True(t, sameTarReadState(second, canonical))

			// Position and Cache remain independent mutable messages, even when stored in one owned allocation.
			first.Position.Partition, first.Position.LogicalObject, first.Position.OffsetBytes = 7, 777, 3
			require.True(t, proto.Equal(first.Cache, canonical.Cache))
			first.Cache.Partition, first.Cache.LogicalObject, first.Cache.OffsetBytes = 8, 888, 2
			require.Equal(t, uint32(7), first.Position.Partition)
			require.Equal(t, uint64(777), first.Position.LogicalObject)
			require.Equal(t, uint32(3), first.Position.OffsetBytes)

			// Mutating one result cannot change another call, the incoming state, the extent or its canonical proof.
			require.True(t, sameTarReadState(second, canonical))
			require.Equal(t, test.forecast, canonical.Position.LogicalObject)
			require.Equal(t, test.forecast-1, canonical.Cache.LogicalObject)
			require.True(t, proto.Equal(beforePosition, initial.Position))
			require.True(t, proto.Equal(beforeCache, initial.Cache))
			require.True(t, proto.Equal(beforeStart, interval.Start))
			require.True(t, proto.Equal(beforeEnd, interval.End))
		})
	}
}

func BenchmarkLTFSReadEvaluate(b *testing.B) {
	for _, test := range []struct {
		name     string
		position uint64
		method   readplan.Method
		cost     time.Duration
	}{
		{"cache", 11, readplan.MethodCache, time.Second},
		{"continue", 10, readplan.MethodContinue, 2 * time.Second},
		{"seek", 8, readplan.MethodSeek, 24 * time.Second},
	} {
		b.Run(test.name, func(b *testing.B) {
			// Every path reads nonzero content, including the block after a cached first block.
			var provider ReadOrderProvider = ltfsOrderFixture(b)
			initial := readplan.State{Position: &entity.TapeBytePosition{Partition: 1, LogicalObject: test.position},
				Cache: &entity.TapeBytePosition{Partition: 1, LogicalObject: test.position - 1}, Stream: "ltfs"}
			interval := readplan.Range{Start: &entity.TapeBytePosition{Partition: 1, LogicalObject: 10},
				End: &entity.TapeBytePosition{Partition: 1, LogicalObject: 12}, Stream: "ltfs"}
			transition, known := provider.Evaluate(initial, interval)
			want := test.cost
			if !known || transition.Method != test.method || transition.Cost != want || transition.Cost <= 0 {
				b.Fatalf("qualified nonzero %s quote: known=%v transition=%+v", test.name, known, transition)
			}
			span := readplan.Span{ID: 1, Ranges: []readplan.Range{interval}}

			// Repeat the production interface call, retaining its resulting state and complete read cost.
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				transition, known := provider.Evaluate(initial, span.Ranges[0])
				if !known || transition.Cost != want ||
					transition.Next.Position.LogicalObject != 12 || transition.Next.Cache.LogicalObject != 11 {
					b.Fatal("repeated LTFS evaluation changed its known complete read cost or exit")
				}
			}
		})
	}
}
