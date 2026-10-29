package media

import (
	"math"
	"math/big"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/samuelncui/yatm/entity"
	"github.com/stretchr/testify/require"
)

func readModelEndpoint(object uint64) *uint64 { return &object }
func readModelPosition(object uint64) *entity.TapeBytePosition {
	return &entity.TapeBytePosition{LogicalObject: object}
}

func readModelFixture() (ReadModelProfile, *atomic.Bool) {
	valid := new(atomic.Bool)
	valid.Store(true)
	return ReadModelProfile{ID: "software-only geometry and timing", SnapshotValid: valid.Load,
		Boundary: ReadEndpointExclusive, BOT: map[uint32]uint64{0: 0}, MinLPOS: 0, MaxLPOS: 100,
		Wraps: []ReadModelWrap{
			{Wrap: 3, Band: 0, Direction: ReadWrapForward, Full: true, Endpoint: readModelEndpoint(100)},
			{Wrap: 8, Band: 0, Direction: ReadWrapReverse, Full: true, Endpoint: readModelEndpoint(200)},
			{Wrap: 11, Band: 1, Direction: ReadWrapForward, Full: true, Endpoint: readModelEndpoint(300)},
		}, Coefficients: ReadModelCoefficients{Base: 10e-9, Longitudinal: 1e-9, Wrap: 20e-9, Band: 30e-9, Direction: 40e-9}}, valid
}

func TestReadCostModelSevenSignedTerms(t *testing.T) {
	// Integer geometry isolates every published feature without asserting physical qualification.
	profile, _ := readModelFixture()
	profile.MaxLPOS = 100000
	profile.Coefficients = ReadModelCoefficients{Base: 4.29, Wrap: 6.69, Band: 3.2, LandingZone: -6.04,
		Direction: 5.22, StepBack: 11.32, Longitudinal: .0006192}
	model, err := NewReadCostModel(profile)
	require.NoError(t, err)
	for _, test := range []struct {
		from, to uint64
		want     float64
	}{
		{10, 20, 4.29 + 10000*.0006192},
		{20, 10, 4.29 + 11.32 + 10000*.0006192},
		{49, 50, 4.29 - 6.04 + 1000*.0006192},
		{20, 180, 4.29 + 6.69 + 5.22},
		{20, 220, 4.29 + 6.69 + 3.2},
		{180, 190, 4.29 + 10000*.0006192},
		{190, 180, 4.29 + 11.32 + 10000*.0006192},
	} {
		score, known := model.AccessScore(readModelPosition(test.from), readModelPosition(test.to))
		require.True(t, known)
		require.InDelta(t, test.want, score, 1e-12)
		cost, priced := model.AccessCost(readModelPosition(test.from), readModelPosition(test.to))
		require.Equal(t, test.want >= 0, priced)
		if priced {
			require.InDelta(t, test.want, cost.Seconds(), 1e-9)
		}
	}
}

func TestReadCostModelLandingZoneIncludesCoordinateOrigin(t *testing.T) {
	// A nonzero LPOS origin must translate the midpoint as well as both interpolated positions.
	profile, _ := readModelFixture()
	profile.MinLPOS, profile.MaxLPOS = 2696, 171097
	model, err := NewReadCostModel(profile)
	require.NoError(t, err)
	require.Equal(t, byte(0), readLPOSZone(model.Longitudinal(), 84199))
	require.Equal(t, byte(0), readLPOSZone(model.Longitudinal(), 84200))
	require.Equal(t, byte(0), readLPOSZone(model.Longitudinal(), 86895))
	require.Equal(t, byte(1), readLPOSZone(model.Longitudinal(), 86896))

	// Addition stays inside the coordinate range even when min+max would overflow.
	profile.MinLPOS, profile.MaxLPOS = math.MaxUint64-100, math.MaxUint64
	model, err = NewReadCostModel(profile)
	require.NoError(t, err)
	require.Equal(t, byte(0), readLPOSZone(model.Longitudinal(), math.MaxUint64-51))
	require.Equal(t, byte(1), readLPOSZone(model.Longitudinal(), math.MaxUint64-50))
}

func TestReadCostFeaturesRetainPhysicalFactorsAndUnits(t *testing.T) {
	// Movement and binary setup factors remain independently observable for passive fitting.
	profile, valid := readModelFixture()
	model, err := NewReadCostModel(profile)
	require.NoError(t, err)
	for _, test := range []struct {
		from, to uint64
		want     [7]float64
	}{
		{10, 20, [7]float64{1, 0, 0, 0, 0, 0, 10}},
		{20, 10, [7]float64{1, 0, 0, 0, 0, 1, 10}},
		{20, 180, [7]float64{1, 1, 0, 0, 1, 0, 0}},
		{20, 270, [7]float64{1, 1, 1, 1, 0, 0, 50}},
	} {
		features, known := model.AccessFeatures(readModelPosition(test.from), readModelPosition(test.to))
		require.True(t, known)
		require.Equal(t, test.want, features)
	}

	// Normalized distance changes units without changing setup factors or authorizing stale maps.
	seconds := 96.0
	profile.FullWrapSeconds = &seconds
	profile.Coefficients.Longitudinal = 0
	model, err = NewReadCostModel(profile)
	require.NoError(t, err)
	features, known := model.AccessFeatures(readModelPosition(20), readModelPosition(270))
	require.True(t, known)
	require.Equal(t, [7]float64{1, 1, 1, 1, 0, 0, .5}, features)
	valid.Store(false)
	_, known = model.AccessFeatures(readModelPosition(20), readModelPosition(270))
	require.False(t, known)
}

func TestReadCostModelEndpointConventionsAndCoverage(t *testing.T) {
	for _, convention := range []ReadEndpointConvention{ReadEndpointExclusive, ReadEndpointInclusive} {
		// Equivalent conventions retain BOT, interior boundaries and the final full endpoint.
		profile, _ := readModelFixture()
		profile.Boundary = convention
		if convention == ReadEndpointInclusive {
			for _, wrap := range profile.Wraps {
				*wrap.Endpoint--
			}
		}
		model, err := NewReadCostModel(profile)
		require.NoError(t, err)
		for _, object := range []uint64{0, 25, 100, 200, 300} {
			_, known := model.AccessScore(readModelPosition(25), readModelPosition(object))
			require.True(t, known, "object %d", object)
		}
		for _, position := range []*entity.TapeBytePosition{nil, {LogicalObject: 301},
			{LogicalObject: 25, OffsetBytes: 1}, {Partition: 7, LogicalObject: 25}} {
			_, known := model.AccessScore(readModelPosition(25), position)
			require.False(t, known)
		}
	}
}

func TestReadCostModelDoesNotBridgeMissingOrPartialPredecessors(t *testing.T) {
	for _, kind := range []string{"descriptor", "partial", "BOT"} {
		t.Run(kind, func(t *testing.T) {
			// Missing/fullness facts prevent bridging; a known full endpoint anchors its successor without BOT.
			profile, _ := readModelFixture()
			switch kind {
			case "descriptor":
				profile.Wraps[0].Endpoint = nil
			case "partial":
				profile.Wraps[0].Full = false
			case "BOT":
				delete(profile.BOT, 0)
			}
			model, err := NewReadCostModel(profile)
			require.NoError(t, err)
			_, known := model.AccessScore(readModelPosition(225), readModelPosition(25))
			require.False(t, known)
			_, known = model.AccessScore(readModelPosition(225), readModelPosition(125))
			require.Equal(t, kind == "BOT", known)
			_, known = model.AccessScore(readModelPosition(225), readModelPosition(275))
			require.True(t, known)
		})
	}

	// A final partial endpoint remains observed and unavailable, including the N=2 CTA shrink example.
	profile, _ := readModelFixture()
	profile.Wraps = profile.Wraps[:2]
	profile.Wraps[0].Endpoint = readModelEndpoint(1000)
	profile.Wraps[1].Endpoint = readModelEndpoint(1500)
	profile.Wraps[1].Full = false
	model, err := NewReadCostModel(profile)
	require.NoError(t, err)
	_, known := model.AccessScore(readModelPosition(25), readModelPosition(1400))
	require.False(t, known)
	require.Equal(t, uint64(1500), *profile.Wraps[1].Endpoint)
}

func TestReadCostModelCopiesFactsAndSnapshotLifetime(t *testing.T) {
	// Constructor copies all supplied geometry/coefficient containers; only lifetime stays live.
	profile, valid := readModelFixture()
	model, err := NewReadCostModel(profile)
	require.NoError(t, err)
	before, known := model.AccessScore(readModelPosition(25), readModelPosition(275))
	require.True(t, known)
	*profile.Wraps[0].Endpoint = 10
	profile.Wraps[0].Band = 99
	profile.Coefficients.Base = 900
	profile.BOT[0] = 90
	after, known := model.AccessScore(readModelPosition(25), readModelPosition(275))
	require.True(t, known)
	require.Equal(t, before, after)
	valid.Store(false)
	require.False(t, model.Valid())
	_, known = model.AccessScore(readModelPosition(25), readModelPosition(275))
	require.False(t, known)
	var absent *ReadCostModel
	require.False(t, absent.Valid())
}

func TestReadCostModelRejectsMalformedProfiles(t *testing.T) {
	for _, change := range []func(*ReadModelProfile){
		func(p *ReadModelProfile) { p.ID = " " }, func(p *ReadModelProfile) { p.SnapshotValid = nil },
		func(p *ReadModelProfile) { p.Boundary = 0 }, func(p *ReadModelProfile) { p.Wraps = nil },
		func(p *ReadModelProfile) { p.Coefficients.Base = math.NaN() },
		func(p *ReadModelProfile) { p.Coefficients.StepBack = math.Inf(1) },
		func(p *ReadModelProfile) { p.Coefficients.Longitudinal = -1 },
		func(p *ReadModelProfile) { p.Coefficients.Longitudinal = math.MaxFloat64 },
		func(p *ReadModelProfile) { p.Wraps[1].Wrap = p.Wraps[0].Wrap },
		func(p *ReadModelProfile) { p.Wraps[0].Direction = 0 },
		func(p *ReadModelProfile) { p.Wraps[1].Endpoint = readModelEndpoint(50) },
		func(p *ReadModelProfile) { p.BOT[0] = 101 }, func(p *ReadModelProfile) { p.BOT[7] = 0 },
	} {
		profile, _ := readModelFixture()
		change(&profile)
		_, err := NewReadCostModel(profile)
		require.Error(t, err)
	}
}

func TestReadCostModelExactLargeLPOSAndDurationLimits(t *testing.T) {
	// Exact integer interpolation survives products larger than uint64 and nonzero coordinate origins.
	profile, _ := readModelFixture()
	profile.MinLPOS, profile.MaxLPOS = 17, math.MaxUint64
	profile.Wraps = profile.Wraps[:1]
	profile.Wraps[0].Endpoint = readModelEndpoint(math.MaxUint64)
	model, err := NewReadCostModel(profile)
	require.NoError(t, err)
	for _, object := range []uint64{0, 1, math.MaxUint64 / 2, math.MaxUint64 - 1, math.MaxUint64} {
		interval, n, known := model.position(readModelPosition(object))
		require.True(t, known)
		want := new(big.Int).Mul(new(big.Int).SetUint64(object), new(big.Int).SetUint64(math.MaxUint64-17))
		want.Quo(want, new(big.Int).SetUint64(math.MaxUint64)).Add(want, big.NewInt(17))
		require.Equal(t, want.Uint64(), readLPOS(model.Longitudinal(), n, interval.span))
	}

	// Finite signed ranking survives a duration overflow; physical quotes never fabricate a time.
	profile, _ = readModelFixture()
	profile.Coefficients = ReadModelCoefficients{Base: 1e20}
	model, err = NewReadCostModel(profile)
	require.NoError(t, err)
	_, known := model.AccessScore(readModelPosition(25), readModelPosition(75))
	require.True(t, known)
	_, known = model.AccessCost(readModelPosition(25), readModelPosition(75))
	require.False(t, known)
}

func TestReadCostModelConcurrentReaders(t *testing.T) {
	profile, _ := readModelFixture()
	model, err := NewReadCostModel(profile)
	require.NoError(t, err)
	want, known := model.AccessScore(readModelPosition(25), readModelPosition(275))
	require.True(t, known)
	var failed atomic.Bool
	var workers sync.WaitGroup
	for range 8 {
		workers.Go(func() {
			for range 1000 {
				got, ok := model.AccessScore(readModelPosition(25), readModelPosition(275))
				if !ok || got != want {
					failed.Store(true)
				}
			}
		})
	}
	workers.Wait()
	require.False(t, failed.Load())
}

func TestReadCostModelUnavailableGeometryDoesNotObserveSnapshot(t *testing.T) {
	// No full interval means no interpolation; even MaxUint64 inclusive endpoints cannot wrap to zero.
	for _, inclusive := range []bool{false, true} {
		profile, _ := readModelFixture()
		profile.Wraps = profile.Wraps[:1]
		profile.SnapshotValid = func() bool { t.Fatal("unavailable geometry observed snapshot"); return true }
		if inclusive {
			profile.Boundary = ReadEndpointInclusive
			profile.Wraps[0].Endpoint = readModelEndpoint(math.MaxUint64)
		} else {
			profile.Wraps[0].Full = false
		}
		model, err := NewReadCostModel(profile)
		require.NoError(t, err)
		require.False(t, model.Valid())
		_, known := model.AccessScore(readModelPosition(0), readModelPosition(25))
		require.False(t, known)
	}
}

func normalizedReadModelFixture(seconds float64) ReadModelProfile {
	profile, _ := readModelFixture()
	profile.FullWrapSeconds = &seconds
	profile.MinLPOS, profile.MaxLPOS = 0, 0
	profile.Coefficients.Longitudinal = 0
	return profile
}

func TestReadCostModelNormalizedUnitsAndSevenTerms(t *testing.T) {
	// Continuous full-span seconds need no absolute LPOS and keep the six signed setup features.
	profile := normalizedReadModelFixture(100)
	profile.Coefficients = ReadModelCoefficients{Base: 4.29, Wrap: 6.69, Band: 3.2, LandingZone: -6.04,
		Direction: 5.22, StepBack: 11.32}
	model, err := NewReadCostModel(profile)
	require.NoError(t, err)
	for _, test := range []struct {
		from, to uint64
		want     float64
	}{
		{10, 20, 4.29 + 10}, {20, 10, 4.29 + 11.32 + 10}, {49, 50, 4.29 - 6.04 + 1},
		{20, 180, 4.29 + 6.69 + 5.22}, {20, 220, 4.29 + 6.69 + 3.2},
		{180, 190, 4.29 + 10}, {190, 180, 4.29 + 11.32 + 10},
	} {
		score, known := model.AccessScore(readModelPosition(test.from), readModelPosition(test.to))
		require.True(t, known)
		require.InDelta(t, test.want, score, 1e-12)
	}

	// Zero is a valid explicitly selected full-span coefficient; no guessed bounds are introduced.
	*profile.FullWrapSeconds = 0
	model, err = NewReadCostModel(profile)
	require.NoError(t, err)
	require.True(t, model.Valid())
	require.Equal(t, uint64(0), model.maxLPOS)
}

func TestReadCostModelNormalizedPartialSeparatesCoverageAndScale(t *testing.T) {
	for _, direction := range []ReadWrapDirection{ReadWrapForward, ReadWrapReverse} {
		// Actual EOD150 bounds addresses while an estimated100-object span sets partial progress.
		profile := normalizedReadModelFixture(100)
		profile.Coefficients = ReadModelCoefficients{}
		profile.Wraps = profile.Wraps[:2]
		profile.Wraps[1].Full = false
		profile.Wraps[1].Direction = direction
		profile.Wraps[1].Endpoint = readModelEndpoint(150)
		profile.Wraps[1].EstimatedSpanObjects = readModelEndpoint(100)
		model, err := NewReadCostModel(profile)
		require.NoError(t, err)
		interval, numerator, known := model.position(readModelPosition(125))
		require.True(t, known)
		want := uint64(25)
		if direction == ReadWrapReverse {
			want = 75
		}
		require.Equal(t, want, numerator)
		require.Equal(t, uint64(100), interval.span)
		require.Equal(t, uint64(150), interval.high)
		_, known = model.AccessScore(readModelPosition(25), readModelPosition(150))
		require.True(t, known, "actual EOD cursor remains representable")
		for _, object := range []uint64{151, 175, 200} {
			_, known = model.AccessScore(readModelPosition(25), readModelPosition(object))
			require.False(t, known)
		}

		// Compiled scalar values cannot be altered by later caller mutation, and EOD is never overwritten.
		require.Equal(t, uint64(150), *profile.Wraps[1].Endpoint)
		*profile.Wraps[1].EstimatedSpanObjects = 999
		*profile.FullWrapSeconds = 999
		*profile.Wraps[1].Endpoint = 999
		after, n, known := model.position(readModelPosition(125))
		require.True(t, known)
		require.Equal(t, interval, after)
		require.Equal(t, numerator, n)
		score, known := model.AccessScore(readModelPosition(0), readModelPosition(125))
		require.True(t, known)
		require.Equal(t, float64(want), score)
	}
}

func TestReadCostModelNormalizedPartialRejectsInventedCoverage(t *testing.T) {
	for _, change := range []func(*ReadModelProfile){
		func(p *ReadModelProfile) { p.Coefficients.Longitudinal = 1 },
		func(p *ReadModelProfile) { *p.FullWrapSeconds = -1 },
		func(p *ReadModelProfile) { *p.FullWrapSeconds = math.NaN() },
		func(p *ReadModelProfile) { *p.FullWrapSeconds = math.Inf(1) },
		func(p *ReadModelProfile) { p.Wraps[1].Full = true },
		func(p *ReadModelProfile) { p.Wraps[1].Endpoint = nil },
		func(p *ReadModelProfile) { *p.Wraps[1].EstimatedSpanObjects = 0 },
		func(p *ReadModelProfile) { *p.Wraps[1].EstimatedSpanObjects = 49 },
		func(p *ReadModelProfile) {
			p.Wraps = append(p.Wraps, ReadModelWrap{Wrap: 99, Direction: ReadWrapForward})
		},
	} {
		profile := normalizedReadModelFixture(1)
		profile.Wraps = profile.Wraps[:2]
		profile.Wraps[1].Full = false
		profile.Wraps[1].Endpoint = readModelEndpoint(150)
		profile.Wraps[1].EstimatedSpanObjects = readModelEndpoint(100)
		change(&profile)
		_, err := NewReadCostModel(profile)
		require.Error(t, err)
	}

	// An estimate cannot manufacture the missing lower anchor of a partial successor.
	profile := normalizedReadModelFixture(1)
	profile.Wraps = profile.Wraps[:2]
	profile.Wraps[0].Endpoint = nil
	profile.Wraps[1].Full = false
	profile.Wraps[1].EstimatedSpanObjects = readModelEndpoint(100)
	model, err := NewReadCostModel(profile)
	require.NoError(t, err)
	require.False(t, model.Valid())
}

func TestReadCostModelNormalizedDistanceRetainsTinyDifferences(t *testing.T) {
	// Subtract before float conversion, including exact rational fallback for huge unequal spans.
	profile := normalizedReadModelFixture(1e18)
	profile.Coefficients = ReadModelCoefficients{}
	profile.Wraps = profile.Wraps[:1]
	profile.Wraps[0].Endpoint = readModelEndpoint(math.MaxUint64)
	model, err := NewReadCostModel(profile)
	require.NoError(t, err)
	score, known := model.AccessScore(readModelPosition(math.MaxUint64-2), readModelPosition(math.MaxUint64-1))
	require.True(t, known)
	require.InDelta(t, 1e18/float64(uint64(math.MaxUint64)), score, 1e-17)
	for _, values := range [][4]uint64{{1, 3, 2, 5}, {math.MaxUint64 - 1, math.MaxUint64, math.MaxUint64 - 2, math.MaxUint64 - 1},
		{math.MaxUint64 / 2, math.MaxUint64, math.MaxUint64/2 + 1, math.MaxUint64}} {
		a := new(big.Rat).SetFrac(new(big.Int).SetUint64(values[0]), new(big.Int).SetUint64(values[1]))
		b := new(big.Rat).SetFrac(new(big.Int).SetUint64(values[2]), new(big.Int).SetUint64(values[3]))
		a.Sub(a, b).Abs(a)
		want, _ := a.Float64()
		require.Equal(t, want, readNormalizedDistance(values[0], values[1], values[2], values[3]))
	}
}

func TestReadCostModelNormalizedInclusiveFullEndpointAndExclusiveEOD(t *testing.T) {
	// Complete wrap endpoint100 owns object100; the final EOD150 denotes the next unwritten object.
	profile := normalizedReadModelFixture(100)
	profile.Boundary = ReadEndpointInclusive
	profile.Wraps = profile.Wraps[:2]
	profile.Wraps[1].Full = false
	profile.Wraps[1].Endpoint = readModelEndpoint(150)
	profile.Wraps[1].EstimatedSpanObjects = readModelEndpoint(101)
	model, err := NewReadCostModel(profile)
	require.NoError(t, err)
	first, n, known := model.position(readModelPosition(100))
	require.True(t, known)
	require.Equal(t, uint32(0), first.index)
	require.Equal(t, uint64(100), n)
	tail, n, known := model.position(readModelPosition(101))
	require.True(t, known)
	require.Equal(t, uint32(1), tail.index)
	require.Equal(t, uint64(101), n)
	require.Equal(t, uint64(150), tail.high)
	_, _, known = model.position(readModelPosition(150))
	require.True(t, known)
	_, _, known = model.position(readModelPosition(151))
	require.False(t, known)
}
