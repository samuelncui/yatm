package media

import (
	"math"
	"testing"
	"time"

	"github.com/samuelncui/yatm/entity"
	"github.com/stretchr/testify/require"
)

func TestReadCompletionPhysicalCoordinatesAndUnits(t *testing.T) {
	parameters := &ReadCompletion{FullSpanSeconds: 100, FixedSeconds: 3}
	for _, normalized := range []bool{false, true} {
		profile, _ := readModelFixture()
		profile.MinLPOS, profile.MaxLPOS = 2696, 171097
		// Deliberately unrelated LOCATE coefficients and uncertainty must not price unload.
		profile.Coefficients = ReadModelCoefficients{Base: -9, Wrap: 23, Band: -3, Direction: 99, Longitudinal: .3}
		if normalized {
			profile.FullWrapSeconds = readModelFloat(7)
			profile.Coefficients.Longitudinal = 0
		}
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
			require.True(t, known, "normalized=%t object=%d", normalized, test.object)
			require.Equal(t, test.want, cost)
		}
	}
}

func TestReadCompletionFinalPartialEODPreservesScaleAndDirection(t *testing.T) {
	for _, reverse := range []bool{false, true} {
		profile, _ := readModelFixture()
		profile.FullWrapSeconds, profile.Coefficients.Longitudinal = readModelFloat(1), 0
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

func TestReadCompletionAmbiguousWrapBoundary(t *testing.T) {
	profile, _ := readModelFixture()
	profile.Wraps[0].EndpointUncertaintyObjects = readModelEndpoint(2)
	profile.Wraps[1].EndpointUncertaintyObjects = readModelEndpoint(2)
	model, err := NewReadCostModel(profile)
	require.NoError(t, err)
	parameters := &ReadCompletion{FullSpanSeconds: 100}
	cost, known := model.UnloadCost(readModelPosition(150), parameters)
	require.True(t, known)
	require.Equal(t, 50*time.Second, cost)
	_, known = model.UnloadCost(readModelPosition(100), parameters)
	require.False(t, known, "an ambiguous wrap boundary cannot supply a physical coordinate")
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
