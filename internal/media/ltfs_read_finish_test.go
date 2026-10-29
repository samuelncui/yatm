package media

import (
	"math"
	"testing"
	"time"

	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/internal/media/readplan"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

func BenchmarkLTFSReadFinish(b *testing.B) {
	// Quote-only overhead belongs to planning; this benchmark performs no device I/O.
	provider := ltfsOrderFixture(b)
	state := readplan.State{Position: &entity.TapeBytePosition{Partition: 1, LogicalObject: 110}}
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		if _, known := provider.Finish(state); !known {
			b.Fatal("qualified completion estimate is unavailable")
		}
	}
}

func ltfsCompleteRouteCost(t *testing.T, provider *ltfsReadOrderProvider, spans []readplan.Span, ids []int64) time.Duration {
	t.Helper()
	// Follow complete native extent transitions after the common first file, then direct rewind/unload.
	require.Len(t, ids, len(spans))
	require.Equal(t, spans[0].ID, ids[0])
	byID := make(map[int64]readplan.Span, len(spans))
	for _, span := range spans {
		byID[span.ID] = span
	}
	state, known := provider.FixedFirstReadState(byID[ids[0]])
	require.True(t, known)
	delete(byID, ids[0])
	total := time.Duration(0)
	for _, id := range ids[1:] {
		span, exists := byID[id]
		require.True(t, exists)
		delete(byID, id)
		for _, interval := range span.Ranges {
			transition, known := provider.Evaluate(state, interval)
			require.True(t, known)
			total += transition.Cost
			state = transition.Next
		}
	}
	require.Empty(t, byID)

	// Price the selected terminal policy independently of the provider's Finish implementation.
	finish, known := provider.options.Model.UnloadCost(state.Position, provider.options.Completion)
	require.True(t, known)
	total += finish
	return total
}

func TestLTFSReadFinishUsesNoIndexRewriteAndReflectedEndpoints(t *testing.T) {
	// One full-span time and fixed unload overhead define the explicitly selected clean model.
	provider := ltfsOrderFixture(t)
	provider.options.Completion = &ReadCompletion{FullSpanSeconds: 100, FixedSeconds: 3}
	for _, test := range []struct {
		object  uint64
		seconds int64
	}{{0, 3}, {10, 13}, {90, 93}, {100, 103}, {110, 93}, {190, 13}, {200, 3}} {
		state := readplan.State{Position: &entity.TapeBytePosition{Partition: 1, LogicalObject: test.object}}
		cost, known := provider.Finish(state)
		require.True(t, known)
		require.Equal(t, time.Duration(test.seconds)*time.Second, cost)
	}

	// Reflected wrap positions at the same physical coordinate have the same return cost.
	forward, known := provider.Finish(readplan.State{Position: &entity.TapeBytePosition{Partition: 1, LogicalObject: 10}})
	require.True(t, known)
	reverse, known := provider.Finish(readplan.State{Position: &entity.TapeBytePosition{Partition: 1, LogicalObject: 190}})
	require.True(t, known)
	require.Equal(t, forward, reverse)
}

func TestLTFSReadFinishAtEODRetainsUnloadOverhead(t *testing.T) {
	// EOD at physical BOT needs no longitudinal travel, but still pays fixed unload work.
	provider := ltfsOrderFixture(t)
	provider.options.Model.coefficients.Base = 8
	provider.options.Completion = &ReadCompletion{FullSpanSeconds: 100, FixedSeconds: 3}
	cost, known := provider.Finish(readplan.State{Position: provider.options.DataEnd})
	require.True(t, known)
	require.Equal(t, 3*time.Second, cost)

	// Protocol fields outside the physical coordinate cannot invent motion at the same endpoint.
	position := proto.Clone(provider.options.DataEnd).(*entity.TapeBytePosition)
	position.ProtoReflect().SetUnknown([]byte{0x28, 0x01})
	cost, known = provider.Finish(readplan.State{Position: position})
	require.True(t, known)
	require.Equal(t, 3*time.Second, cost)
}

func TestLTFSReadFinishPartialEODDoesNotStretchCoverage(t *testing.T) {
	// The final reverse wrap is only one quarter written and retains its full physical scale.
	provider := ltfsOrderFixture(t)
	options := provider.options
	model, err := NewReadCostModel(ReadModelProfile{ID: "ltfs-partial-test", SnapshotValid: func() bool { return true },
		Boundary: ReadEndpointExclusive, BOT: map[uint32]uint64{1: 0}, FullWrapSeconds: proto.Float64(1000),
		Wraps: []ReadModelWrap{
			{Partition: 1, Full: true, Endpoint: proto.Uint64(100), Direction: ReadWrapForward},
			{Partition: 1, Wrap: 1, Endpoint: proto.Uint64(125), EstimatedSpanObjects: proto.Uint64(100), Direction: ReadWrapReverse},
		}})
	require.NoError(t, err)
	options.Model, options.DataEnd = model, &entity.TapeBytePosition{Partition: 1, LogicalObject: 125}
	options.Completion = &ReadCompletion{FullSpanSeconds: 100}
	selected, reason := NewLTFSReadOrderProvider(options)
	require.NotNil(t, selected, reason)
	provider = selected.(*ltfsReadOrderProvider)

	// Returning from the true three-quarter coordinate costs 75 seconds, with no invented Index branch.
	cost, known := provider.Finish(readplan.State{Position: options.DataEnd})
	require.True(t, known)
	require.Equal(t, 75*time.Second, cost)
	_, known = provider.Finish(readplan.State{Position: &entity.TapeBytePosition{Partition: 1, LogicalObject: 126}})
	require.False(t, known)
	_, known = provider.Ranges(ltfsRequest(&entity.LtfsExtent{Partition: "z", StartBlock: 125, ByteCount: 1}))
	require.False(t, known)
	_, known = provider.Ranges(ltfsRequest(&entity.LtfsExtent{Partition: "z", StartBlock: 124, ByteCount: 4}))
	require.True(t, known)
}

func TestLTFSReadFinishRequiresAndCopiesCompletionFacts(t *testing.T) {
	for _, test := range []struct {
		name string
		edit func(*LTFSReadOptions)
	}{
		{"missing completion", func(o *LTFSReadOptions) { o.Completion = nil }},
		{"zero full span", func(o *LTFSReadOptions) { o.Completion.FullSpanSeconds = 0 }},
		{"nonfinite full span", func(o *LTFSReadOptions) { o.Completion.FullSpanSeconds = math.NaN() }},
		{"unrepresentable full span", func(o *LTFSReadOptions) { o.Completion.FullSpanSeconds = math.MaxFloat64 }},
		{"negative fixed time", func(o *LTFSReadOptions) { o.Completion.FixedSeconds = -1 }},
		{"missing EOD", func(o *LTFSReadOptions) { o.DataEnd = nil }},
		{"offset EOD", func(o *LTFSReadOptions) { o.DataEnd.OffsetBytes = 1 }},
		{"unknown partition", func(o *LTFSReadOptions) { o.DataEnd.Partition = 2 }},
		{"unmapped partition", func(o *LTFSReadOptions) { o.Partitions = map[string]uint32{"q": 2} }},
		{"short EOD", func(o *LTFSReadOptions) { o.DataEnd.LogicalObject = 199 }},
		{"beyond EOD", func(o *LTFSReadOptions) { o.DataEnd.LogicalObject = 201 }},
	} {
		t.Run(test.name, func(t *testing.T) {
			options := ltfsOrderFixture(t).options
			test.edit(&options)
			provider, reason := NewLTFSReadOrderProvider(options)
			require.Nil(t, provider)
			require.NotEmpty(t, reason)
		})
	}

	// Admission owns immutable copies; later caller changes must not retarget ranges or finish costing.
	options := ltfsOrderFixture(t).options
	selected, reason := NewLTFSReadOrderProvider(options)
	require.NotNil(t, selected, reason)
	provider := selected.(*ltfsReadOrderProvider)
	state := readplan.State{Position: &entity.TapeBytePosition{Partition: 1, LogicalObject: 10}}
	before, known := provider.Finish(state)
	require.True(t, known)
	options.Completion.FullSpanSeconds = 1
	options.DataEnd.LogicalObject = 1
	options.Partitions["z"] = 2
	after, known := provider.Finish(state)
	require.True(t, known)
	require.Equal(t, before, after)
	require.Equal(t, uint64(200), provider.options.DataEnd.LogicalObject)
}

func TestLTFSReadFinishWithdrawsInvalidStatesAndModels(t *testing.T) {
	// Only known coordinates inside the admitted Data partition can quote completion.
	provider := ltfsOrderFixture(t)
	provider.options.Partitions["q"] = 2
	_, known := provider.Ranges(ltfsRequest(&entity.LtfsExtent{Partition: "q", StartBlock: 10, ByteCount: 1}))
	require.False(t, known)
	for _, state := range []readplan.State{{}, {Position: &entity.TapeBytePosition{Partition: 2, LogicalObject: 10}},
		{Position: &entity.TapeBytePosition{Partition: 1, LogicalObject: 10, OffsetBytes: 1}},
		{Position: &entity.TapeBytePosition{Partition: 1, LogicalObject: 201}}} {
		_, known := provider.Finish(state)
		require.False(t, known)
	}

	// Completion has its own timing parameters; unrelated LOCATE coefficients do not change it.
	state := readplan.State{Position: &entity.TapeBytePosition{Partition: 1, LogicalObject: 10}}
	before, known := provider.Finish(state)
	require.True(t, known)
	provider.options.Model.coefficients.Base = math.MaxFloat64
	after, known := provider.Finish(state)
	require.True(t, known)
	require.Equal(t, before, after)

	// Releasing the position-map owner still withdraws every later quote.
	provider.options.Model.snapshotValid = func() bool { return false }
	_, known = provider.Finish(readplan.State{Position: provider.options.DataEnd})
	require.False(t, known)
}
