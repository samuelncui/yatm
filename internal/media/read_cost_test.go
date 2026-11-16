package media

import (
	"math"
	"sync"
	"sync/atomic"
	"testing"
	"time"

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
		BOT: map[uint32]uint64{0: 0},
		Wraps: []ReadModelWrap{
			{Wrap: 3, Band: 0, Direction: ReadWrapForward, Full: true, Endpoint: readModelEndpoint(100)},
			{Wrap: 8, Band: 0, Direction: ReadWrapReverse, Full: true, Endpoint: readModelEndpoint(200)},
			{Wrap: 11, Band: 1, Direction: ReadWrapForward, Full: true, Endpoint: readModelEndpoint(300)},
		}, Coefficients: ReadModelCoefficients{Base: 10e-9, Seek: 100e-9, ApproachReversal: 40e-9}}, valid
}

// readCostMoves covers the reading/seek speed boundary and approach reversal in the fixture, whose
// wraps 3 (forward), 8 (reverse) and 11 (forward, band 1) each span 100 objects; a seek's
// features are the intercept, seek distance in objects (a hundredth of a span each) and approach
// reversal, and a read-speed locate has only the intercept and its objects.
var readCostMoves = []struct {
	name     string
	from, to uint64
	seek     float64
	reversal float64
	reads    bool
	read     uint64
}{
	{name: "same half ahead reads", from: 10, to: 20, reads: true, read: 10},
	{name: "same position reads nothing", from: 20, to: 20, reads: true},
	{name: "behind reverses", from: 20, to: 10, seek: 10, reversal: 1},
	{name: "crossing the midpoint ahead seeks", from: 40, to: 60, seek: 20},
	{name: "one object across the midpoint seeks", from: 49, to: 50, seek: 1},
	{name: "other wrap without motion", from: 20, to: 180},
	{name: "other wrap against its direction", from: 20, to: 170, seek: 10, reversal: 1},
	{name: "other wrap along its direction", from: 20, to: 190, seek: 10},
	{name: "other band without motion", from: 20, to: 220},
	{name: "reverse wrap ahead reads", from: 180, to: 190, reads: true, read: 10},
	{name: "reverse wrap behind reverses", from: 190, to: 180, seek: 10, reversal: 1},
	{name: "reverse wrap crossing the midpoint seeks", from: 130, to: 180, seek: 50},
}

func readCostAccess(reads bool, seek, reversal float64, read uint64) ReadAccess {
	if reads {
		return ReadAccess{Features: [3]float64{1}, ReadObjects: read}
	}
	return ReadAccess{Features: [3]float64{1, seek / 100, reversal}}
}

func TestReadCostModelPositioningTerms(t *testing.T) {
	// A 100-second full span costs one second per object of distance and prices every term independently.
	profile, _ := readModelFixture()
	profile.Coefficients = ReadModelCoefficients{Base: 4, Seek: 100, ApproachReversal: 5}
	profile.ReadObjectSeconds = .2
	model, err := NewReadCostModel(profile)
	require.NoError(t, err)
	for _, test := range readCostMoves {
		want := 4 + test.seek + 5*test.reversal
		if test.reads {
			want = 4 + float64(test.read)*.2
		}
		score, known := model.AccessScore(readModelPosition(test.from), readModelPosition(test.to))
		require.True(t, known, test.name)
		require.InDelta(t, want, score, 1e-9, test.name)
		cost, priced := model.AccessCost(readModelPosition(test.from), readModelPosition(test.to))
		require.True(t, priced, test.name)
		require.InDelta(t, want, cost.Seconds(), 2e-9, test.name, "quotes round up to whole nanoseconds")
	}

	// Every term is nonnegative, including the derived reading-speed time.
	for _, change := range []func(*ReadModelProfile){
		func(p *ReadModelProfile) { p.Coefficients.Base = -1 },
		func(p *ReadModelProfile) { p.Coefficients.Seek = math.Inf(1) },
		func(p *ReadModelProfile) { p.Coefficients.ApproachReversal = -1 },
		func(p *ReadModelProfile) { p.ReadObjectSeconds = -1 },
		func(p *ReadModelProfile) { p.ReadObjectSeconds = math.NaN() },
	} {
		profile, _ := readModelFixture()
		change(&profile)
		_, err := NewReadCostModel(profile)
		require.Error(t, err)
	}
}

func TestReadCostModelStreaming(t *testing.T) {
	// Wrap 3 ends at object 100 where reverse wrap 8 starts, so 90 and 110 share one longitudinal place.
	{
		profile, _ := readModelFixture()
		profile.Coefficients.Seek = 96
		profile.StreamingObjects, profile.ReadObjectSeconds = 20, 1e-9
		model, err := NewReadCostModel(profile)
		require.NoError(t, err)
		unit := .01
		for _, test := range []struct {
			name     string
			from, to uint64
			want     ReadAccess
		}{
			{name: "streaming at the streaming distance", from: 10, to: 30, want: ReadAccess{ReadObjects: 20}},
			{name: "same position streams nothing", from: 20, to: 20, want: ReadAccess{}},
			{name: "beyond the streaming distance is a read-speed locate", from: 10, to: 31, want: ReadAccess{Features: [3]float64{1}, ReadObjects: 21}},
			{name: "behind never streams", from: 20, to: 10, want: ReadAccess{Features: [3]float64{1, 10 * unit, 1}}},
			{name: "short move along the target wrap", from: 20, to: 190, want: ReadAccess{Features: [3]float64{1, 10 * unit}}},
			{name: "motionless wrap change", from: 89, to: 111, want: ReadAccess{Features: [3]float64{1}}},
			{name: "short move against the target wrap", from: 20, to: 170,
				want: ReadAccess{Features: [3]float64{1, 10 * unit, 1}}},
			{name: "long move along the target wrap", from: 60, to: 190,
				want: ReadAccess{Features: [3]float64{1, 50 * unit}}},
		} {
			access, known := model.accessFeatures(readModelPosition(test.from), readModelPosition(test.to), ObjectsAhead(readModelPosition(test.from), readModelPosition(test.to)))
			require.True(t, known, test.name)
			require.Equal(t, test.want.ReadObjects, access.ReadObjects, test.name)
			require.InDeltaSlice(t, test.want.Features[:], access.Features[:], 1e-12, test.name)
		}

		// Streaming costs the skipped objects at reading speed; the unstreamed quote seeks instead.
		c := profile.Coefficients
		score, known := model.AccessScore(readModelPosition(10), readModelPosition(30))
		require.True(t, known)
		require.InDelta(t, 20e-9, score, 1e-18)
		cost, known := model.UnstreamedAccessCost(readModelPosition(90), readModelPosition(110))
		require.True(t, known)
		require.InDelta(t, c.Base, cost.Seconds(), 1e-9)
		score, known = model.AccessScore(readModelPosition(10), readModelPosition(31))
		require.True(t, known)
		require.InDelta(t, c.Base+21e-9, score, 1e-18)
		score, known = model.AccessScore(readModelPosition(20), readModelPosition(170))
		require.True(t, known)
		require.InDelta(t, c.Base+c.Seek*10*unit+c.ApproachReversal, score, 1e-12)
	}

	// An absent streaming distance never streams.
	profile, _ := readModelFixture()
	model, err := NewReadCostModel(profile)
	require.NoError(t, err)
	access, known := model.accessFeatures(readModelPosition(90), readModelPosition(110), ObjectsAhead(readModelPosition(90), readModelPosition(110)))
	require.True(t, known)
	require.False(t, access.Streamed())
	require.Equal(t, int64(-1), ObjectsAhead(readModelPosition(2), readModelPosition(1)))
	require.Equal(t, int64(-1), ObjectsAhead(readModelPosition(1), &entity.TapeBytePosition{Partition: 1, LogicalObject: 2}))
}

// readTurnFixture declares three consecutive 100-object wraps with a 150-object streaming distance, 0.01 seconds
// per object and a 4-second approach reversal.
func readTurnFixture(t *testing.T) *ReadCostModel {
	t.Helper()
	model, err := NewReadCostModel(ReadModelProfile{ID: "software-only wrap turns", SnapshotValid: func() bool { return true },
		BOT: map[uint32]uint64{0: 0}, ReadObjectSeconds: .01, StreamingObjects: 150,
		Coefficients: ReadModelCoefficients{Base: 5, Seek: 100, ApproachReversal: 4},
		Wraps: []ReadModelWrap{
			{Wrap: 0, Direction: ReadWrapForward, Full: true, Endpoint: readModelEndpoint(100)},
			{Wrap: 1, Direction: ReadWrapReverse, Full: true, Endpoint: readModelEndpoint(200)},
			{Wrap: 2, Direction: ReadWrapForward, Full: true, Endpoint: readModelEndpoint(300)},
		}})
	require.NoError(t, err)
	return model
}

func TestReadCostModelStreamingCrossesWrapTurns(t *testing.T) {
	// Streaming keeps no positioning intercept; each wrap turn crossed costs one approach reversal.
	model := readTurnFixture(t)
	for _, test := range []struct {
		name     string
		from, to uint64
		turns    float64
	}{{"within one wrap", 40, 90, 0}, {"across one turn", 90, 110, 1}, {"across two turns", 90, 210, 2}} {
		access, known := model.accessFeatures(readModelPosition(test.from), readModelPosition(test.to), ObjectsAhead(readModelPosition(test.from), readModelPosition(test.to)))
		require.True(t, known, test.name)
		require.True(t, access.Streamed(), test.name)
		require.Equal(t, ReadAccess{Features: [3]float64{2: test.turns}, ReadObjects: test.to - test.from}, access, test.name)
		score, known := model.AccessScore(readModelPosition(test.from), readModelPosition(test.to))
		require.True(t, known, test.name)
		require.InDelta(t, float64(test.to-test.from)*.01+test.turns*4, score, 1e-12, test.name)
	}
}

func TestReadCostModelReadingSecondsPricesWrapTurns(t *testing.T) {
	// Objects read at reading speed, plus one approach reversal per wrap turn between the endpoints.
	model := readTurnFixture(t)
	for _, test := range []struct {
		name        string
		first, next uint64
		objects     uint64
		seconds     float64
	}{
		{"within one wrap", 10, 60, 50, .5}, {"across one turn", 90, 110, 20, 4.2},
		{"across two turns", 50, 250, 200, 10}, {"actual EOD", 299, 300, 1, .01},
		{"ending with a wrap's last object", 90, 100, 10, 4.1},
	} {
		seconds, known := model.ReadingSeconds(readModelPosition(test.first), readModelPosition(test.next), test.objects)
		require.True(t, known, test.name)
		require.InDelta(t, test.seconds, seconds, 1e-12, test.name)
	}

	// The turn paid by a read ending at a wrap's last object is not paid again by streaming from next.
	split, _ := model.ReadingSeconds(readModelPosition(90), readModelPosition(100), 10)
	streamed, _ := model.AccessScore(readModelPosition(100), readModelPosition(110))
	whole, _ := model.ReadingSeconds(readModelPosition(90), readModelPosition(110), 20)
	require.InDelta(t, whole, split+streamed, 1e-12)

	// An unresolved, reversed, empty or cross-partition endpoint leaves the time unknown.
	for _, next := range []*entity.TapeBytePosition{readModelPosition(301), readModelPosition(5), readModelPosition(10),
		{Partition: 1, LogicalObject: 20}, nil} {
		_, known := model.ReadingSeconds(readModelPosition(10), next, 1)
		require.False(t, known)
	}
}

func TestReadAccessReversalOnlyAgainstTargetDirection(t *testing.T) {
	// A full reversal is added only when the signed distance along the target wrap's direction is negative.
	from := ReadPoint{Wrap: 0, Direction: ReadWrapForward, Offset: 5000, Span: 10000}
	for _, test := range []struct {
		name   string
		offset uint64
		want   float64
	}{{"against the direction", 4000, 1}, {"motionless", 5000, 0}, {"slightly along", 5050, 0}, {"along", 6000, 0}} {
		to := ReadPoint{Wrap: 2, Direction: ReadWrapForward, Offset: test.offset, Span: 10000}
		access, known := ReadAccessFeatures(from, to, -1, 0)
		require.True(t, known, test.name)
		require.InDelta(t, test.want, access.Features[2], 1e-12, test.name)
	}
}

func TestReadCostFeaturesRetainPhysicalFactors(t *testing.T) {
	// Movement and reversal factors remain independently observable.
	profile, valid := readModelFixture()
	model, err := NewReadCostModel(profile)
	require.NoError(t, err)
	for _, test := range readCostMoves {
		access, known := model.accessFeatures(readModelPosition(test.from), readModelPosition(test.to), ObjectsAhead(readModelPosition(test.from), readModelPosition(test.to)))
		require.True(t, known, test.name)
		want := readCostAccess(test.reads, test.seek, test.reversal, test.read)
		require.Equal(t, want.ReadObjects, access.ReadObjects, test.name)
		require.InDeltaSlice(t, want.Features[:], access.Features[:], 1e-12, test.name)
	}

	// A stale map authorizes no features.
	valid.Store(false)
	_, known := model.accessFeatures(readModelPosition(20), readModelPosition(270), ObjectsAhead(readModelPosition(20), readModelPosition(270)))
	require.False(t, known)
}

func TestReadCostModelCoverage(t *testing.T) {
	// Exclusive endpoints retain BOT, interior boundaries and the final full endpoint.
	profile, _ := readModelFixture()
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
		func(p *ReadModelProfile) { p.Wraps = nil },
		func(p *ReadModelProfile) { p.Coefficients.Base = math.NaN() },
		func(p *ReadModelProfile) { p.Coefficients.ApproachReversal = math.Inf(1) },
		func(p *ReadModelProfile) { p.Coefficients.Seek = -1 },
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

func TestReadCostModelMinimumGainKeepsItsFloor(t *testing.T) {
	// The minimum gain is the positioning base time, but never below the profile's floor.
	profile, _ := readModelFixture()
	profile.Coefficients.Base, profile.MinimumGainSeconds = 7, 5
	model, err := NewReadCostModel(profile)
	require.NoError(t, err)
	require.Equal(t, 7*time.Second, model.MinimumGain())
	profile.Coefficients.Base = 0
	model, err = NewReadCostModel(profile)
	require.NoError(t, err)
	require.Equal(t, 5*time.Second, model.MinimumGain())
	profile.MinimumGainSeconds = -1
	_, err = NewReadCostModel(profile)
	require.Error(t, err)
}

func TestReadCostModelDurationLimits(t *testing.T) {
	// A finite ranking survives a duration overflow; physical quotes never fabricate a time.
	profile, _ := readModelFixture()
	profile.Coefficients = ReadModelCoefficients{Base: 1e20}
	model, err := NewReadCostModel(profile)
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
	// No full interval means no interpolation.
	profile, _ := readModelFixture()
	profile.Wraps = profile.Wraps[:1]
	profile.SnapshotValid = func() bool { t.Fatal("unavailable geometry observed snapshot"); return true }
	profile.Wraps[0].Full = false
	model, err := NewReadCostModel(profile)
	require.NoError(t, err)
	require.False(t, model.Valid())
	_, known := model.AccessScore(readModelPosition(0), readModelPosition(25))
	require.False(t, known)
}

func readModelSeekFixture(seconds float64) ReadModelProfile {
	profile, _ := readModelFixture()
	profile.Coefficients.Seek = seconds
	return profile
}

func TestReadCostModelNormalizedPartialSeparatesCoverageAndScale(t *testing.T) {
	for _, direction := range []ReadWrapDirection{ReadWrapForward, ReadWrapReverse} {
		// Actual EOD150 bounds addresses while an estimated100-object span sets partial progress.
		profile := readModelSeekFixture(100)
		profile.Coefficients = ReadModelCoefficients{Seek: 100}
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
		profile.Coefficients.Seek = 999
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
		func(p *ReadModelProfile) { p.Coefficients.Seek = -1 },
		func(p *ReadModelProfile) { p.Coefficients.Seek = math.NaN() },
		func(p *ReadModelProfile) { p.Coefficients.Seek = math.Inf(1) },
		func(p *ReadModelProfile) { p.Wraps[1].Full = true },
		func(p *ReadModelProfile) { p.Wraps[1].Endpoint = nil },
		func(p *ReadModelProfile) { *p.Wraps[1].EstimatedSpanObjects = 0 },
		func(p *ReadModelProfile) { *p.Wraps[1].EstimatedSpanObjects = 49 },
		func(p *ReadModelProfile) {
			p.Wraps = append(p.Wraps, ReadModelWrap{Wrap: 99, Direction: ReadWrapForward})
		},
	} {
		profile := readModelSeekFixture(1)
		profile.Wraps = profile.Wraps[:2]
		profile.Wraps[1].Full = false
		profile.Wraps[1].Endpoint = readModelEndpoint(150)
		profile.Wraps[1].EstimatedSpanObjects = readModelEndpoint(100)
		change(&profile)
		_, err := NewReadCostModel(profile)
		require.Error(t, err)
	}

	// An estimate cannot manufacture the missing lower anchor of a partial successor.
	profile := readModelSeekFixture(1)
	profile.Wraps = profile.Wraps[:2]
	profile.Wraps[0].Endpoint = nil
	profile.Wraps[1].Full = false
	profile.Wraps[1].EstimatedSpanObjects = readModelEndpoint(100)
	model, err := NewReadCostModel(profile)
	require.NoError(t, err)
	require.False(t, model.Valid())
}

func TestReadCostModelNormalizedDistanceResolvesOneObject(t *testing.T) {
	// One object of a wrap far longer than any recorded one still prices its own seek distance.
	profile := readModelSeekFixture(1e9)
	profile.Coefficients = ReadModelCoefficients{Seek: 1e9}
	profile.Wraps = profile.Wraps[:1]
	profile.Wraps[0].Endpoint = readModelEndpoint(1 << 32)
	model, err := NewReadCostModel(profile)
	require.NoError(t, err)
	// Moving back one object seeks; reading ahead would not exercise the distance.
	score, known := model.AccessScore(readModelPosition(1<<32-1), readModelPosition(1<<32-2))
	require.True(t, known)
	require.InDelta(t, 1e9/float64(1<<32), score, 1e-6)
}

func TestReadCostModelNormalizedFullEndpointAndEOD(t *testing.T) {
	// Complete wrap endpoint101 starts the next wrap; the final EOD150 denotes the next unwritten object.
	profile := readModelSeekFixture(100)
	profile.Wraps = profile.Wraps[:2]
	profile.Wraps[0].Endpoint = readModelEndpoint(101)
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
