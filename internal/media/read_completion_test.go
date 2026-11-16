package media

import (
	"math"
	"testing"
	"time"

	"github.com/samuelncui/yatm/entity"
	"github.com/stretchr/testify/require"
)

func TestReadCompletionPhysicalCoordinates(t *testing.T) {
	parameters := &ReadCompletion{FullSpanSeconds: 100, FixedSeconds: 3}
	profile, _ := readModelFixture()
	// Deliberately unrelated LOCATE coefficients must not price unload.
	profile.Coefficients = ReadModelCoefficients{Base: 9, Seek: 7, ApproachReversal: 99}
	profile.ReadObjectSeconds = 11
	model, err := NewReadCostModel(profile)
	require.NoError(t, err)
	for _, test := range []struct {
		object uint64
		want   time.Duration
	}{
		{0, 3 * time.Second}, {10, 13 * time.Second}, {90, 93 * time.Second},
		{100, 103 * time.Second}, {110, 93 * time.Second}, {190, 13 * time.Second},
		{200, 3 * time.Second}, {290, 93 * time.Second}, {300, 103 * time.Second},
	} {
		cost, known := model.UnloadCost(readModelPosition(test.object), parameters)
		require.True(t, known, "object=%d", test.object)
		require.Equal(t, test.want, cost)
	}
}

func TestReadCompletionFinalPartialEODPreservesScaleAndDirection(t *testing.T) {
	for _, reverse := range []bool{false, true} {
		profile, _ := readModelFixture()
		profile.Coefficients.Seek = 1
		last, eod, want := 2, uint64(225), 25*time.Second
		if reverse {
			profile.Wraps = profile.Wraps[:2]
			last, eod, want = 1, 125, 75*time.Second
		}
		profile.Wraps[last].Full = false
		profile.Wraps[last].Endpoint = readModelEndpoint(eod)
		profile.Wraps[last].EstimatedSpanObjects = readModelEndpoint(100)
		model, err := NewReadCostModel(profile)
		require.NoError(t, err)
		cost, known := model.UnloadCost(readModelPosition(eod), &ReadCompletion{FullSpanSeconds: 100})
		require.True(t, known)
		require.Equal(t, want, cost)
		_, known = model.UnloadCost(readModelPosition(eod+1), &ReadCompletion{FullSpanSeconds: 100})
		require.False(t, known, "a completion endpoint must not extend retained coverage")
		require.Equal(t, uint64(100), model.intervals[0][last].span)
		require.Equal(t, eod, model.intervals[0][last].high)
	}
}

func TestReadCompletionRejectsUnknownOrInvalidInputs(t *testing.T) {
	profile, valid := readModelFixture()
	model, err := NewReadCostModel(profile)
	require.NoError(t, err)
	parameters := &ReadCompletion{FullSpanSeconds: 100}
	for _, position := range []*entity.TapeBytePosition{nil, {LogicalObject: 301}, {LogicalObject: 10, OffsetBytes: 1}, {Partition: 7, LogicalObject: 10}} {
		_, known := model.UnloadCost(position, parameters)
		require.False(t, known)
	}
	for _, parameters := range []*ReadCompletion{nil, {}, {FullSpanSeconds: -1}, {FullSpanSeconds: math.NaN()},
		{FullSpanSeconds: math.Inf(1)}, {FullSpanSeconds: 1, FixedSeconds: -1}, {FullSpanSeconds: 1, FixedSeconds: math.NaN()},
		{FullSpanSeconds: 1, FixedSeconds: math.Inf(1)},
		{FullSpanSeconds: math.MaxFloat64}, {FullSpanSeconds: 1, FixedSeconds: float64(math.MaxInt64) / float64(time.Second)},
	} {
		_, known := model.UnloadCost(readModelPosition(90), parameters)
		require.False(t, known, "parameters=%+v", parameters)
	}
	valid.Store(false)
	_, known := model.UnloadCost(readModelPosition(10), parameters)
	require.False(t, known)
	_, known = (*ReadCostModel)(nil).UnloadCost(readModelPosition(10), parameters)
	require.False(t, known)
	_, known = (&ReadCostModel{}).UnloadCost(readModelPosition(10), parameters)
	require.False(t, known)
}

func TestReadCompletionDoesNotMutateOrAllocate(t *testing.T) {
	profile, _ := readModelFixture()
	model, err := NewReadCostModel(profile)
	require.NoError(t, err)
	before := append([]readModelInterval(nil), model.intervals[0]...)
	parameters := ReadCompletion{FullSpanSeconds: 100, FixedSeconds: 3}
	position := readModelPosition(190)
	allocations := testing.AllocsPerRun(1000, func() {
		_, known := model.UnloadCost(position, &parameters)
		if !known {
			panic("valid completion quote became unknown")
		}
	})
	require.Zero(t, allocations)
	require.Equal(t, before, model.intervals[0])
	require.Equal(t, ReadCompletion{FullSpanSeconds: 100, FixedSeconds: 3}, parameters)
	require.Equal(t, readModelPosition(190), position)
}
