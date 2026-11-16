package media

import (
	"errors"
	"fmt"
	"math"
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

// ReadModelWrap occurs in the profile's complete, declared traversal order within its partition.
// A missing descriptor remains an entry with Endpoint nil; Full declares a full-span model interval.
// Endpoint is exclusive: the first logical object after the wrap, or actual EOD for a partial wrap.
type ReadModelWrap struct {
	Partition, Wrap, Band uint32
	Direction             ReadWrapDirection
	Full                  bool
	Endpoint              *uint64
	// EstimatedSpanObjects supplies only the final partial wrap's interpolation denominator.
	// Endpoint remains its actual exclusive EOD, never an extrapolated address boundary.
	EstimatedSpanObjects *uint64
}

// ReadModelCoefficients are nonnegative positioning terms: the positioning intercept, the seek-speed
// time for one complete longitudinal span and the approach reversal, all in seconds.
type ReadModelCoefficients struct {
	Base, Seek, ApproachReversal float64
}

// ReadModelProfile supplies explicit geometry and timing facts for one owned snapshot.
// BOT anchors the first declared wrap. A final partial estimate changes only the coordinate scale.
type ReadModelProfile struct {
	ID            string
	SnapshotValid func() bool
	BOT           map[uint32]uint64
	Wraps         []ReadModelWrap
	Coefficients  ReadModelCoefficients
	// ReadObjectSeconds is the time to pass one logical object at reading speed: record bytes divided
	// by the read rate. It is derived from the read timing, never fitted as a positioning term.
	ReadObjectSeconds float64
	// StreamingObjects is the streaming distance in logical objects: how far ahead the drive reaches
	// by continuing to read instead of positioning; zero never applies.
	StreamingObjects uint64
	// MinimumGainSeconds is a floor under the minimum gain, which is otherwise Coefficients.Base, so
	// that a zero configured intercept cannot remove it.
	MinimumGainSeconds float64
}

type readModelInterval struct {
	low, high   uint64
	span        uint64
	index, wrap uint32
	band        uint32
	direction   ReadWrapDirection
}

// ReadCostModel owns immutable interpolation and empirical cost facts for one supplied profile.
// It does not establish freshness, fit coefficients, issue commands or publish a provider.
type ReadCostModel struct {
	snapshotValid     func() bool
	coefficients      ReadModelCoefficients
	readObjectSeconds float64
	streamingObjects  uint64
	minimumGain       float64
	intervals         map[uint32][]readModelInterval
}

// NewReadCostModel validates and copies a profile without certifying its physical evidence.
func NewReadCostModel(profile ReadModelProfile) (*ReadCostModel, error) {
	// Missing identity or lifetime cannot define even a structural model.
	if strings.TrimSpace(profile.ID) == "" || profile.SnapshotValid == nil {
		return nil, errors.New("read cost profile requires identity and snapshot lifetime")
	}
	if len(profile.Wraps) == 0 || uint64(len(profile.Wraps)) > math.MaxUint32 {
		return nil, errors.New("read cost profile has no representable traversal")
	}

	// Validate finite nonnegative terms before compiling geometry; a prediction that still overflows
	// is refused where it is quoted.
	for _, value := range []float64{profile.Coefficients.Base, profile.Coefficients.Seek,
		profile.Coefficients.ApproachReversal, profile.ReadObjectSeconds, profile.MinimumGainSeconds} {
		if value < 0 || math.IsNaN(value) || math.IsInf(value, 0) {
			return nil, errors.New("read cost profile has a negative or nonfinite term")
		}
	}
	model := &ReadCostModel{snapshotValid: profile.SnapshotValid, coefficients: profile.Coefficients,
		readObjectSeconds: profile.ReadObjectSeconds, streamingObjects: profile.StreamingObjects,
		minimumGain: max(profile.Coefficients.Base, profile.MinimumGainSeconds),
		intervals:   make(map[uint32][]readModelInterval)}

	// Only a declared full predecessor supplies a lower boundary; a final estimate cannot bridge a gap.
	seen := make(map[[2]uint32]bool, len(profile.Wraps))
	previous := make(map[uint32]ReadModelWrap)
	indices := make(map[uint32]uint32)
	endpoints := make(map[uint32]uint64)
	last := make(map[uint32]int)
	for i, wrap := range profile.Wraps {
		last[wrap.Partition] = i
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
			low, known = readWrapEnd(before)
		}
		high, covered := readWrapEnd(wrap)
		span := uint64(0)
		if wrap.EstimatedSpanObjects != nil {
			if wrap.Full || wrap.Endpoint == nil || last[wrap.Partition] != i || *wrap.EstimatedSpanObjects == 0 {
				return nil, errors.New("read cost estimated span requires a final partial wrap")
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
			model.intervals[wrap.Partition] = append(model.intervals[wrap.Partition], readModelInterval{
				low: low, high: high, span: span, index: indices[wrap.Partition], wrap: wrap.Wrap, band: wrap.Band,
				direction: wrap.Direction,
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

func readWrapEnd(wrap ReadModelWrap) (uint64, bool) {
	if !wrap.Full || wrap.Endpoint == nil {
		return 0, false
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
	numerator := position.LogicalObject - interval.low
	if interval.direction == ReadWrapReverse {
		numerator = interval.span - numerator
	}
	return interval, numerator, true
}

// covers reports whether contiguous wrap intervals of partition hold every object from start up to
// the exclusive end. Both endpoints alone cannot establish coverage across a missing interior interval.
func (m *ReadCostModel) covers(partition uint32, start, end uint64) bool {
	wraps := m.intervals[partition]
	at := sort.Search(len(wraps), func(i int) bool { return wraps[i].high > start })
	if at == len(wraps) || start < wraps[at].low {
		return false
	}
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

// AccessScore is the nonnegative positioning prediction in seconds for candidate ranking.
func (m *ReadCostModel) AccessScore(from, to *entity.TapeBytePosition) (float64, bool) {
	return m.accessScore(from, to, ObjectsAhead(from, to))
}

func (m *ReadCostModel) accessScore(from, to *entity.TapeBytePosition, ahead int64) (float64, bool) {
	access, known := m.accessFeatures(from, to, ahead)
	if !known {
		return 0, false
	}
	score := access.Seconds(m.coefficients, m.readObjectSeconds)
	return score, !math.IsNaN(score) && !math.IsInf(score, 0)
}

// ReadPoint is one physical coordinate resolved through a position map. Offset is the longitudinal
// distance from the wrap's BOT-side end, measured in logical objects of the wrap's interpolation Span.
type ReadPoint struct {
	Partition, Wrap uint32
	Direction       ReadWrapDirection
	Offset, Span    uint64
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
	return ReadPoint{Partition: position.Partition, Wrap: interval.wrap,
		Direction: interval.direction, Offset: numerator, Span: interval.span}, true
}

func (m *ReadCostModel) accessFeatures(from, to *entity.TapeBytePosition, ahead int64) (ReadAccess, bool) {
	// Resolve both explicit coordinates within the same valid snapshot and partition.
	if from == nil || to == nil || from.Partition != to.Partition {
		return ReadAccess{}, false
	}
	a, known := m.Point(from)
	if !known {
		return ReadAccess{}, false
	}
	b, known := m.Point(to)
	if !known {
		return ReadAccess{}, false
	}
	return ReadAccessFeatures(a, b, ahead, m.streamingObjects)
}

// ObjectsAhead returns how many logical objects b lies ahead of a in one partition, or -1 when b is
// behind a, in another partition or unknown.
func ObjectsAhead(a, b *entity.TapeBytePosition) int64 {
	if a == nil || b == nil || a.Partition != b.Partition || b.LogicalObject < a.LogicalObject ||
		b.LogicalObject-a.LogicalObject > math.MaxInt64 {
		return -1
	}
	return int64(b.LogicalObject - a.LogicalObject)
}

// ReadAccess holds one LOCATE's positioning features: the positioning intercept, the seek-speed
// longitudinal distance in complete spans and the approach reversals, either one for arriving against
// the target wrap's read direction or the wrap turns a streamed move crosses. ReadObjects counts the
// logical objects passed at reading speed. A streamed move has only ReadObjects and its wrap turns; a
// read-speed locate has the intercept and ReadObjects.
type ReadAccess struct {
	Features    [3]float64
	ReadObjects uint64
}

// Streamed reports a target reached by continuing to read, without the positioning intercept; its
// only positioning feature is the wrap turns it crosses.
func (a ReadAccess) Streamed() bool {
	return a.Features[0] == 0
}

// Seconds prices the features with nonnegative terms and the time to pass one object at reading speed.
func (a ReadAccess) Seconds(c ReadModelCoefficients, readObjectSeconds float64) float64 {
	f := a.Features
	return c.Base*f[0] + c.Seek*f[1] + c.ApproachReversal*f[2] + float64(a.ReadObjects)*readObjectSeconds
}

// ReadAccessFeatures computes the positioning features of a move from a to b, two resolved physical
// points that lie ahead logical objects apart (-1 when b is not logically ahead).
//
// A target at most streamingObjects ahead, in any wrap, is reached by continuing to read: it costs the
// skipped objects at reading speed plus one approach reversal for each wrap turn crossed, and no
// other positioning term; a zero streaming distance never applies. A move in the same wrap interval that stays
// within one half and does not go behind (b is at or after a in the wrap's read direction) is a
// read-speed locate: the positioning intercept plus |Δobjects| at reading speed. Halves split the span
// at its physical midpoint; the midpoint itself belongs to the second half. Every other move travels
// the physical longitudinal distance |Δx| at seek speed and adds the positioning intercept, plus a
// full approach reversal when the signed distance along b's wrap read direction is negative (forward
// wraps read toward increasing x, reverse wraps toward decreasing x).
func ReadAccessFeatures(a, b ReadPoint, ahead int64, streamingObjects uint64) (ReadAccess, bool) {
	// Points must share a partition and lie within their wraps.
	if a.Partition != b.Partition || a.Span == 0 || b.Span == 0 || a.Offset > a.Span || b.Offset > b.Span ||
		!readDirection(a.Direction) || !readDirection(b.Direction) {
		return ReadAccess{}, false
	}

	// The drive keeps streaming toward a target close enough ahead, turning at each wrap end on the way.
	if streamingObjects != 0 && ahead >= 0 && uint64(ahead) <= streamingObjects {
		return ReadAccess{Features: [3]float64{2: readTurns(a, b)}, ReadObjects: uint64(ahead)}, true
	}

	// Resolve the physical distance in complete spans, the motion direction and halves.
	delta := readFraction(b.Offset, b.Span) - readFraction(a.Offset, a.Span)
	sameHalf := readNormalizedZone(a.Offset, a.Span) == readNormalizedZone(b.Offset, b.Span)

	// Reading speed covers forward moves within one half of the same wrap interval.
	access := ReadAccess{Features: [3]float64{1}}
	if a.Wrap == b.Wrap && a.Span == b.Span && sameHalf {
		if a.Direction == ReadWrapForward && b.Offset >= a.Offset {
			access.ReadObjects = b.Offset - a.Offset
			return access, true
		}
		if a.Direction == ReadWrapReverse && b.Offset <= a.Offset {
			access.ReadObjects = a.Offset - b.Offset
			return access, true
		}
	}

	// Every other move seeks; arriving against b's read direction reverses once more.
	access.Features[1] = math.Abs(delta)
	along := delta
	if b.Direction == ReadWrapReverse {
		along = -delta
	}
	if along < 0 {
		access.Features[2] = 1
	}
	return access, true
}

// readTurns counts the wrap turns continuous reading crosses from a to b: logical order runs through
// a partition's wraps in increasing number.
func readTurns(a, b ReadPoint) float64 {
	if b.Wrap <= a.Wrap {
		return 0
	}
	return float64(b.Wrap - a.Wrap)
}

// ReadingSeconds prices reading objects logical objects continuously from the object at first up to
// the host-next position next in one partition: each at reading speed, plus one approach reversal
// for each wrap turn between first and next. A read that ends with a wrap's last object leaves next
// in the following wrap and so pays that turn, which a move starting at next never pays again. An
// endpoint the position map does not resolve leaves the time unknown.
func (m *ReadCostModel) ReadingSeconds(first, next *entity.TapeBytePosition, objects uint64) (float64, bool) {
	if first == nil || next == nil || first.Partition != next.Partition || next.LogicalObject <= first.LogicalObject {
		return 0, false
	}
	a, known := m.Point(first)
	if !known {
		return 0, false
	}
	b, known := m.Point(next)
	if !known {
		return 0, false
	}
	seconds := float64(objects)*m.readObjectSeconds + readTurns(a, b)*m.coefficients.ApproachReversal
	return seconds, !math.IsNaN(seconds) && !math.IsInf(seconds, 0)
}

// MinimumGain is one positioning base time, the fixed cost every LOCATE pays, and never less than the
// profile's floor. A planned route must save at least that much over LOGICAL: a smaller saving
// removes less than one LOCATE and lies within the estimate's accumulated per-move error.
func (m *ReadCostModel) MinimumGain() time.Duration {
	if m == nil {
		return 0
	}
	gain, _ := readSeconds(m.minimumGain)
	return gain
}

// AccessCost quotes only nonnegative, representable positioning time.
func (m *ReadCostModel) AccessCost(from, to *entity.TapeBytePosition) (time.Duration, bool) {
	score, known := m.AccessScore(from, to)
	if !known {
		return 0, false
	}
	return readSeconds(score)
}

// UnstreamedAccessCost quotes a move from a position the drive has not read up to, so it cannot reach
// any target by continuing to read.
func (m *ReadCostModel) UnstreamedAccessCost(from, to *entity.TapeBytePosition) (time.Duration, bool) {
	score, known := m.accessScore(from, to, -1)
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

// readFraction is a point's longitudinal coordinate as a fraction of its span.
func readFraction(numerator, denominator uint64) float64 {
	return float64(numerator) / float64(denominator)
}
