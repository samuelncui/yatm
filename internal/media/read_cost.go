package media

import (
	"errors"
	"fmt"
	"math"
	"math/big"
	"math/bits"
	"sort"
	"strings"
	"time"

	"github.com/samuelncui/yatm/entity"
)

// ReadWrapDirection is an explicitly supplied wrap orientation, never inferred by the model.
type ReadWrapDirection uint8

const (
	ReadWrapForward ReadWrapDirection = iota + 1
	ReadWrapReverse
)

// ReadEndpointConvention specifies whether a reported full-wrap endpoint includes its final object.
type ReadEndpointConvention uint8

const (
	ReadEndpointInclusive ReadEndpointConvention = iota + 1
	ReadEndpointExclusive
)

// ReadModelWrap occurs in the profile's complete, declared traversal order within its partition.
// A missing descriptor remains an entry with Endpoint nil; Full declares a full-span model interval.
type ReadModelWrap struct {
	Partition, Wrap, Band uint32
	Direction             ReadWrapDirection
	Full                  bool
	Endpoint              *uint64
	// EstimatedSpanObjects supplies only the final partial wrap's interpolation denominator.
	// Endpoint remains its actual exclusive EOD, never an extrapolated address boundary.
	EstimatedSpanObjects *uint64
	// EndpointUncertaintyObjects is a symmetric observed/estimated boundary allowance in logical objects.
	EndpointUncertaintyObjects *uint64
}

// ReadModelCoefficients use signed seconds for setup and nonnegative seconds per LPOS for motion.
// They retain all seven terms of CERN CTA's positioning regression.
type ReadModelCoefficients struct {
	Base, Wrap, Band, LandingZone, Direction, StepBack, Longitudinal float64
}

// ReadModelProfile supplies explicit geometry and timing facts for one owned snapshot.
// BOT anchors the first declared wrap. A final partial estimate changes only the coordinate scale.
type ReadModelProfile struct {
	ID               string
	SnapshotValid    func() bool
	Boundary         ReadEndpointConvention
	BOT              map[uint32]uint64
	Wraps            []ReadModelWrap
	MinLPOS, MaxLPOS uint64
	Coefficients     ReadModelCoefficients
	// FullWrapSeconds selects continuous normalized coordinates instead of absolute LPOS units.
	FullWrapSeconds *float64
}

type readModelInterval struct {
	low, high                      uint64
	span                           uint64
	index, wrap                    uint32
	band                           uint32
	direction                      ReadWrapDirection
	lowError, highError, spanError uint64
}

// ReadCostModel owns immutable interpolation and empirical cost facts for one supplied profile.
// It does not establish freshness, fit coefficients, issue commands or publish a provider.
type ReadCostModel struct {
	snapshotValid    func() bool
	coefficients     ReadModelCoefficients
	minLPOS, maxLPOS uint64
	normalized       bool
	fullWrapSeconds  float64
	intervals        map[uint32][]readModelInterval
}

// NewReadCostModel validates and copies a profile without certifying its physical evidence.
func NewReadCostModel(profile ReadModelProfile) (*ReadCostModel, error) {
	// Missing identity/lifetime or an unspecified interpretation cannot define even a structural model.
	if strings.TrimSpace(profile.ID) == "" || profile.SnapshotValid == nil {
		return nil, errors.New("read cost profile requires identity and snapshot lifetime")
	}
	if profile.Boundary != ReadEndpointInclusive && profile.Boundary != ReadEndpointExclusive {
		return nil, errors.New("read cost profile requires an explicit endpoint convention")
	}
	if len(profile.Wraps) == 0 || uint64(len(profile.Wraps)) > math.MaxUint32 {
		return nil, errors.New("read cost profile has no representable traversal")
	}

	// Validate finite signed setup terms and explicitly selected longitudinal units before compiling geometry.
	if profile.FullWrapSeconds == nil && profile.MaxLPOS <= profile.MinLPOS {
		return nil, errors.New("read cost profile requires longitudinal coordinates")
	}
	bound := 0.0
	for _, value := range []float64{profile.Coefficients.Base, profile.Coefficients.Wrap,
		profile.Coefficients.Band, profile.Coefficients.LandingZone, profile.Coefficients.Direction,
		profile.Coefficients.StepBack} {
		if math.IsNaN(value) || math.IsInf(value, 0) {
			return nil, errors.New("read cost profile has a nonfinite coefficient")
		}
		bound += math.Abs(value)
	}
	slope := profile.Coefficients.Longitudinal
	extent := float64(profile.MaxLPOS - profile.MinLPOS)
	if profile.FullWrapSeconds != nil {
		if slope != 0 {
			return nil, errors.New("read cost profile mixes longitudinal units")
		}
		slope, extent = *profile.FullWrapSeconds, 1
	}
	if slope < 0 || math.IsNaN(slope) || math.IsInf(slope, 0) || math.IsInf(bound+extent*slope, 0) {
		return nil, errors.New("read cost profile has unbounded longitudinal cost")
	}
	model := &ReadCostModel{snapshotValid: profile.SnapshotValid, coefficients: profile.Coefficients,
		minLPOS: profile.MinLPOS, maxLPOS: profile.MaxLPOS,
		normalized: profile.FullWrapSeconds != nil, fullWrapSeconds: slope, intervals: make(map[uint32][]readModelInterval)}

	// Only a declared full predecessor supplies a lower boundary; a final estimate cannot bridge a gap.
	seen := make(map[[2]uint32]bool, len(profile.Wraps))
	previous := make(map[uint32]ReadModelWrap)
	indices := make(map[uint32]uint32)
	endpoints := make(map[uint32]uint64)
	last := make(map[uint32]int)
	maxError := uint64(0)
	for i, wrap := range profile.Wraps {
		last[wrap.Partition] = i
		if wrap.EndpointUncertaintyObjects != nil {
			maxError = max(maxError, *wrap.EndpointUncertaintyObjects)
		}
	}
	for i, wrap := range profile.Wraps {
		key := [2]uint32{wrap.Partition, wrap.Wrap}
		if seen[key] || !readDirection(wrap.Direction) {
			return nil, errors.New("read cost profile has a duplicate wrap or unknown direction")
		}
		seen[key] = true
		if wrap.Endpoint != nil {
			if bot, exists := profile.BOT[wrap.Partition]; exists && *wrap.Endpoint < bot {
				return nil, errors.New("read cost endpoint precedes its declared BOT anchor")
			}
			if last, exists := endpoints[wrap.Partition]; exists && *wrap.Endpoint < last {
				return nil, errors.New("read cost endpoints contradict the declared traversal")
			}
			endpoints[wrap.Partition] = *wrap.Endpoint
		}
		before, exists := previous[wrap.Partition]
		low, known := profile.BOT[wrap.Partition]
		if exists {
			low, known = readWrapEnd(before, profile.Boundary)
		}
		high, covered := readWrapEnd(wrap, profile.Boundary)
		span := uint64(0)
		if wrap.EstimatedSpanObjects != nil {
			if !model.normalized || wrap.Full || wrap.Endpoint == nil || last[wrap.Partition] != i ||
				*wrap.EstimatedSpanObjects == 0 {
				return nil, errors.New("read cost estimated span requires a normalized final partial wrap")
			}
			high, covered, span = *wrap.Endpoint, true, *wrap.EstimatedSpanObjects
			if known && (high < low || span < high-low) {
				return nil, errors.New("read cost estimated span is shorter than observed coverage")
			}
		}
		if known && covered {
			if high <= low {
				return nil, fmt.Errorf("read cost wrap %d has reversed or empty boundaries", wrap.Wrap)
			}
			if span == 0 {
				span = high - low
			}
			lowError, highError := uint64(0), uint64(0)
			if exists && before.EndpointUncertaintyObjects != nil {
				lowError = *before.EndpointUncertaintyObjects
			}
			if wrap.EndpointUncertaintyObjects != nil {
				highError = *wrap.EndpointUncertaintyObjects
			}
			spanError, carry := bits.Add64(lowError, highError, 0)
			if wrap.EstimatedSpanObjects != nil {
				// The final full-span denominator inherits two uncertain inferred boundaries.
				spanError, carry = bits.Add64(maxError, maxError, 0)
			}
			if carry != 0 || spanError >= span {
				return nil, errors.New("read cost spatial uncertainty exceeds its interpolation span")
			}
			model.intervals[wrap.Partition] = append(model.intervals[wrap.Partition], readModelInterval{
				low: low, high: high, span: span, index: indices[wrap.Partition], wrap: wrap.Wrap, band: wrap.Band,
				direction: wrap.Direction, lowError: lowError, highError: highError, spanError: spanError,
			})
		}
		previous[wrap.Partition] = wrap
		indices[wrap.Partition]++
	}

	// Sorted, nonoverlapping per-partition intervals bound every scheduler lookup to a binary search.
	for partition := range profile.BOT {
		if _, exists := previous[partition]; !exists {
			return nil, errors.New("read cost BOT anchor has no declared partition traversal")
		}
	}
	for partition, intervals := range model.intervals {
		sort.Slice(intervals, func(i, j int) bool { return intervals[i].low < intervals[j].low })
		for i := 1; i < len(intervals); i++ {
			if intervals[i].low < intervals[i-1].high {
				return nil, errors.New("read cost profile has overlapping wrap intervals")
			}
		}
		model.intervals[partition] = intervals
	}

	return model, nil
}

func readDirection(value ReadWrapDirection) bool {
	return value == ReadWrapForward || value == ReadWrapReverse
}

func readWrapEnd(wrap ReadModelWrap, convention ReadEndpointConvention) (uint64, bool) {
	if !wrap.Full || wrap.Endpoint == nil {
		return 0, false
	}
	if convention == ReadEndpointInclusive {
		if *wrap.Endpoint == math.MaxUint64 {
			return 0, false
		}
		return *wrap.Endpoint + 1, true
	}
	return *wrap.Endpoint, true
}

// Valid requires explicit usable geometry and the still-owned snapshot, not an invented freshness flag.
func (m *ReadCostModel) Valid() bool {
	return m != nil && len(m.intervals) != 0 && m.snapshotValid()
}

func (m *ReadCostModel) position(position *entity.TapeBytePosition) (readModelInterval, uint64, bool) {
	// Logical anchors remain authoritative; byte offsets do not identify another physical coordinate.
	if position == nil || position.OffsetBytes != 0 {
		return readModelInterval{}, 0, false
	}
	intervals := m.intervals[position.Partition]
	index := sort.Search(len(intervals), func(i int) bool { return intervals[i].high > position.LogicalObject })
	if index == len(intervals) {
		if index == 0 || position.LogicalObject != intervals[index-1].high {
			return readModelInterval{}, 0, false
		}
		index--
	}
	interval := intervals[index]
	if position.LogicalObject < interval.low {
		return readModelInterval{}, 0, false
	}
	// A boundary allowance cannot assert which side of a wrap this object occupies.
	if interval.lowError != 0 && position.LogicalObject-interval.low <= interval.lowError ||
		interval.highError != 0 && interval.high-position.LogicalObject <= interval.highError {
		return readModelInterval{}, 0, false
	}
	numerator := position.LogicalObject - interval.low
	if interval.direction == ReadWrapReverse {
		numerator = interval.span - numerator
	}
	return interval, numerator, true
}

func readLPOS(unit ReadLongitudinal, offset, span uint64) uint64 {
	high, low := bits.Mul64(offset, unit.MaxLPOS-unit.MinLPOS)
	quotient, _ := bits.Div64(high, low, span)
	return unit.MinLPOS + quotient
}

func readLPOSZone(unit ReadLongitudinal, lpos uint64) byte {
	// Classify against the physical midpoint, including a nonzero LPOS origin without overflow.
	if lpos < unit.MinLPOS+(unit.MaxLPOS-unit.MinLPOS)/2 {
		return 0
	}
	return 1
}

// AccessScore retains the signed seven-term CERN prediction for candidate ranking.
// A negative regression prediction is meaningful only as a ranking result, never elapsed time.
func (m *ReadCostModel) AccessScore(from, to *entity.TapeBytePosition) (float64, bool) {
	// Learning and route costing share one feature definition and the same coordinate units.
	features, known := m.AccessFeatures(from, to)
	if !known {
		return 0, false
	}
	c := m.coefficients
	slope := c.Longitudinal
	if m.normalized {
		slope = m.fullWrapSeconds
	}
	coefficients := [7]float64{c.Base, c.Wrap, c.Band, c.LandingZone, c.Direction, c.StepBack, slope}
	score := 0.0
	for i, feature := range features {
		score += feature * coefficients[i]
	}
	return score, !math.IsNaN(score) && !math.IsInf(score, 0)
}

// ReadPoint is one physical coordinate resolved through a position map. Offset is the longitudinal
// distance from the wrap's BOT-side end, measured in logical objects of the wrap's interpolation Span.
type ReadPoint struct {
	Partition, Wrap, Band uint32
	Direction             ReadWrapDirection
	Offset, Span          uint64
}

// ReadLongitudinal selects normalized whole-span units, or absolute LPOS interpolated between explicit bounds.
type ReadLongitudinal struct {
	Normalized       bool
	MinLPOS, MaxLPOS uint64
}

// Longitudinal returns the unit and LPOS frame this model's access features use.
func (m *ReadCostModel) Longitudinal() ReadLongitudinal {
	return ReadLongitudinal{Normalized: m.normalized, MinLPOS: m.minLPOS, MaxLPOS: m.maxLPOS}
}

// Point resolves one logical anchor to its physical wrap and longitudinal offset in this snapshot.
func (m *ReadCostModel) Point(position *entity.TapeBytePosition) (ReadPoint, bool) {
	if !m.Valid() {
		return ReadPoint{}, false
	}
	interval, numerator, known := m.position(position)
	if !known {
		return ReadPoint{}, false
	}
	return ReadPoint{Partition: position.Partition, Wrap: interval.wrap, Band: interval.band,
		Direction: interval.direction, Offset: numerator, Span: interval.span}, true
}

// AccessFeatures returns intercept, wrap, band, landing-zone, direction, same-wrap step-back,
// and longitudinal distance. The last feature uses complete-span units in normalized models,
// or LPOS units in absolute models; observations must retain that model's applicability.
func (m *ReadCostModel) AccessFeatures(from, to *entity.TapeBytePosition) ([7]float64, bool) {
	// Resolve both explicit coordinates within the same valid snapshot and partition.
	if from == nil || to == nil || from.Partition != to.Partition {
		return [7]float64{}, false
	}
	a, known := m.Point(from)
	if !known {
		return [7]float64{}, false
	}
	b, known := m.Point(to)
	if !known {
		return [7]float64{}, false
	}
	return ReadAccessFeatures(a, b, m.Longitudinal())
}

// ReadAccessFeatures computes the seven access features from two resolved physical points.
// Learning applies it to retained points, so route costing and refits share one definition.
func ReadAccessFeatures(a, b ReadPoint, unit ReadLongitudinal) ([7]float64, bool) {
	// Points must share a partition and lie within their wraps; absolute units need an LPOS frame.
	if a.Partition != b.Partition || a.Span == 0 || b.Span == 0 || a.Offset > a.Span || b.Offset > b.Span ||
		!readDirection(a.Direction) || !readDirection(b.Direction) || !unit.Normalized && unit.MaxLPOS <= unit.MinLPOS {
		return [7]float64{}, false
	}
	x, y := uint64(0), uint64(0)
	if !unit.Normalized {
		x, y = readLPOS(unit, a.Offset, a.Span), readLPOS(unit, b.Offset, b.Span)
	}
	movement := 0.0
	zoneChange := readLPOSZone(unit, x) != readLPOSZone(unit, y)
	backward := a.Direction == ReadWrapForward && y < x || a.Direction == ReadWrapReverse && y > x
	if unit.Normalized {
		movement = readNormalizedDistance(a.Offset, a.Span, b.Offset, b.Span)
		zoneChange = readNormalizedZone(a.Offset, a.Span) != readNormalizedZone(b.Offset, b.Span)
		backward = a.Direction == ReadWrapForward && b.Offset < a.Offset || a.Direction == ReadWrapReverse && b.Offset > a.Offset
	} else {
		distance := x - y
		if x < y {
			distance = y - x
		}
		movement = float64(distance)
	}

	// Preserve the six CERN setup terms; the selected coordinate path owns its zone classification.
	features := [7]float64{1, 0, 0, 0, 0, 0, movement}
	if a.Wrap != b.Wrap {
		features[1] = 1
	}
	if a.Band != b.Band {
		features[2] = 1
	}
	if zoneChange {
		features[3] = 1
	}
	if a.Direction != b.Direction {
		features[4] = 1
	}
	if a.Wrap == b.Wrap && backward {
		features[5] = 1
	}
	return features, true
}

// AccessCost quotes only nonnegative, representable positioning time.
func (m *ReadCostModel) AccessCost(from, to *entity.TapeBytePosition) (time.Duration, bool) {
	score, known := m.AccessScore(from, to)
	if !known {
		return 0, false
	}
	return readSeconds(score)
}

// readSeconds converts a nonnegative finite estimate, rounding up to whole nanoseconds.
func readSeconds(seconds float64) (time.Duration, bool) {
	nanos := math.Ceil(seconds * float64(time.Second))
	if seconds < 0 || math.IsNaN(nanos) || math.IsInf(nanos, 0) || nanos >= float64(math.MaxInt64) {
		return 0, false
	}
	return time.Duration(nanos), true
}

// readNormalizedZone compares to the exact midpoint without overflowing twice the numerator.
func readNormalizedZone(numerator, denominator uint64) byte {
	if numerator < denominator-numerator {
		return 0
	}
	return 1
}

func readNormalizedDistance(an, ad, bn, bd uint64) float64 {
	// Common tape lengths keep exact products below float's integer precision without allocations.
	var numerator, denominator uint64
	if ad == bd {
		denominator = ad
		numerator = an - bn
		if an < bn {
			numerator = bn - an
		}
	} else if hi, product := bits.Mul64(ad, bd); hi == 0 {
		denominator = product
		left, right := an*bd, bn*ad
		numerator = left - right
		if left < right {
			numerator = right - left
		}
	}
	if denominator != 0 && numerator <= 1<<53 && denominator <= 1<<53 {
		return float64(numerator) / float64(denominator)
	}

	// Wide unequal lengths retain correctly rounded exact rational distance and monotone nearest scores.
	var a, b big.Rat
	a.SetFrac(new(big.Int).SetUint64(an), new(big.Int).SetUint64(ad))
	b.SetFrac(new(big.Int).SetUint64(bn), new(big.Int).SetUint64(bd))
	a.Sub(&a, &b).Abs(&a)
	value, _ := a.Float64()
	return value
}
