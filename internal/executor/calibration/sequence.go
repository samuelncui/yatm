// Package calibration plans the fixed LOCATE sequence of a read calibration and fits the drive's
// positioning terms from the measured moves. It issues no commands; the caller moves the tape.
package calibration

import (
	"errors"
	"fmt"
	"math"
)

// Point is a physical tape position: a wrap and the longitudinal fraction of one span measured from
// the beginning-of-tape end. Even wraps read toward increasing U.
type Point struct {
	Wrap uint32
	U    float64
}

// Move is one LOCATE that moved the tape, from its physical start to its target.
type Move struct {
	From, To Point
	Seconds  float64
}

// Result holds the fitted terms. Spans are fractions of one longitudinal tape length.
type Result struct {
	// BaseSeconds is the fixed time of every LOCATE that the drive does not stream through.
	BaseSeconds float64
	// SeekSecondsPerWrap is the seek-speed time for one complete span.
	SeekSecondsPerWrap float64
	// ApproachReversalSeconds is added when the tape arrives against the target wrap's direction.
	ApproachReversalSeconds float64
	// ReadSecondsPerWrap is the reading-speed time for one complete span.
	ReadSecondsPerWrap float64
	// StreamSpans is the forward distance up to which the drive streams instead of positioning.
	StreamSpans float64
	// MaxResidualSeconds is the largest error of the fit over its own moves.
	MaxResidualSeconds float64
}

const (
	// MaxResidualSeconds rejects a calibration whose own moves the fitted terms miss by more.
	MaxResidualSeconds = 3.0
	// readSpeedTolerance bounds the disagreement between the reading speed seen while streaming and
	// the speed of a same-half forward LOCATE; beyond it the drive positions differently.
	readSpeedTolerance = 0.10

	origin       = 0.040
	probeLow     = 0.010
	probeHigh    = 0.120
	probes       = 5
	probeLimit   = 0.47
	crossOriginU = 0.300
)

var (
	// ErrIrregular means the measured moves do not follow the positioning behaviour the read cost
	// model describes, so no parameters are produced.
	ErrIrregular = errors.New("read calibration moves do not fit the positioning model")

	forward = []float64{0.120, 0.270}
	back    = []float64{0.250, 0.200, origin}
	cross   = []Point{
		{1, 0.280}, {0, 0.310}, {1, 0.330}, {0, 0.800}, {1, 0.820},
		{0, 0.350}, {1, 0.850}, {0, 0.870}, {1, 0.350}, {0, 0.030},
	}
)

// Sequence yields the targets of one calibration in order. The threshold probes depend on the
// measured times, so each target is requested only after the previous move was recorded.
type Sequence struct {
	moves    []Move
	at       Point
	started  bool
	step     int
	low, top float64
	probed   int
	returns  int
}

// NewSequence starts at the given physical position, normally the load point.
func NewSequence(start Point) *Sequence {
	return &Sequence{at: start, low: probeLow, top: probeHigh}
}

// Next returns the next target, or false when the sequence is complete.
func (s *Sequence) Next() (Point, bool) {
	fixed := 1 + len(forward) + len(back)
	switch {
	case s.step == 0:
		return Point{0, origin}, true
	case s.step <= len(forward):
		return Point{0, forward[s.step-1]}, true
	case s.step < fixed:
		return Point{0, back[s.step-1-len(forward)]}, true
	case s.probed < probes:
		distance := (s.low + s.top) / 2
		if s.at.U+distance > probeLimit {
			return Point{0, origin}, true
		}
		return Point{0, s.at.U + distance}, true
	case s.step == fixed+s.extra()+probes:
		return Point{0, crossOriginU}, true
	}
	index := s.step - fixed - s.extra() - probes - 1
	if index >= len(cross) {
		return Point{}, false
	}
	return cross[index], true
}

// extra counts the returns to the origin inserted between probes.
func (s *Sequence) extra() int { return s.returns }

// Record stores the measured time of the move to the last target.
func (s *Sequence) Record(to Point, seconds float64) {
	move := Move{From: s.at, To: to, Seconds: seconds}
	fixed := 1 + len(forward) + len(back)
	if s.step >= fixed && s.probed < probes {
		if to.U < s.at.U {
			s.returns++
		} else {
			distance := to.U - s.at.U
			if s.locates(distance, seconds) {
				s.top = distance
			} else {
				s.low = distance
			}
			s.probed++
		}
	}
	if s.step > 0 {
		s.moves = append(s.moves, move)
	}
	s.at = to
	s.step++
}

// locates reports whether a forward move paid the fixed LOCATE time on top of its reading-speed
// travel. The two fixed forward LOCATEs give both terms.
func (s *Sequence) locates(distance, seconds float64) bool {
	first, second := s.moves[0], s.moves[1]
	near, far := first.To.U-first.From.U, second.To.U-second.From.U
	rate := (second.Seconds - first.Seconds) / (far - near)
	base := first.Seconds - rate*near
	return seconds-rate*distance > base/2
}

// Moves returns the recorded moves, without the initial positioning from the load point.
func (s *Sequence) Moves() []Move { return s.moves }

// Threshold returns the bracket the probes left around the streaming distance.
func (s *Sequence) Threshold() (low, high float64) { return s.low, s.top }

// Fit derives the positioning terms from the moves and the streaming bracket.
func Fit(moves []Move, low, high float64) (*Result, error) {
	threshold := (low + high) / 2

	// Streamed moves show the reading speed directly; same-half LOCATEs must agree with it.
	var streamSeconds, streamSpans float64
	var locates []Move
	for _, move := range moves {
		if kind(move) != sameHalf {
			continue
		}
		if distance := span(move); distance <= threshold {
			streamSeconds, streamSpans = streamSeconds+move.Seconds, streamSpans+distance
		} else {
			locates = append(locates, move)
		}
	}
	if streamSpans == 0 || len(locates) < 2 {
		return nil, fmt.Errorf("%w: no streamed move or no reading-speed LOCATE was observed", ErrIrregular)
	}
	read := streamSeconds / streamSpans
	if slope := locateSlope(locates); math.Abs(slope-read) > readSpeedTolerance*read {
		return nil, fmt.Errorf("%w: a same-half LOCATE takes %.1f s per span, streaming takes %.1f s", ErrIrregular, slope, read)
	}

	// The remaining time of every positioned move is base + seek travel + reversal.
	var rows [][3]float64
	var times []float64
	for _, move := range moves {
		row, seconds := [3]float64{1, 0, 0}, move.Seconds
		switch kind(move) {
		case sameHalf:
			if span(move) <= threshold {
				continue
			}
			seconds -= read * span(move)
		case against:
			row[1], row[2] = span(move), 1
		default:
			row[1] = span(move)
		}
		rows, times = append(rows, row), append(times, seconds)
	}
	terms, ok := solve(rows, times)
	if !ok || terms[0] < 0 || terms[1] <= 0 || terms[2] < 0 {
		return nil, fmt.Errorf("%w: positioning terms are not identified", ErrIrregular)
	}

	result := &Result{BaseSeconds: terms[0], SeekSecondsPerWrap: terms[1], ApproachReversalSeconds: terms[2],
		ReadSecondsPerWrap: read, StreamSpans: threshold}
	for i, row := range rows {
		residual := math.Abs(terms[0]*row[0] + terms[1]*row[1] + terms[2]*row[2] - times[i])
		result.MaxResidualSeconds = math.Max(result.MaxResidualSeconds, residual)
	}
	if result.MaxResidualSeconds > MaxResidualSeconds {
		return nil, fmt.Errorf("%w: a move misses the fit by %.1f s", ErrIrregular, result.MaxResidualSeconds)
	}
	return result, nil
}

const (
	sameHalf = iota // same wrap, forward, without crossing the middle of the tape
	along           // seek-speed move arriving in the target wrap's direction
	against         // seek-speed move arriving against it
)

func kind(move Move) int {
	direction := 1.0
	if move.To.Wrap%2 == 1 {
		direction = -1
	}
	delta := (move.To.U - move.From.U) * direction
	switch {
	case delta < 0:
		return against
	case move.From.Wrap == move.To.Wrap && (move.From.U-0.5)*(move.To.U-0.5) >= 0:
		return sameHalf
	}
	return along
}

func span(move Move) float64 { return math.Abs(move.To.U - move.From.U) }

// locateSlope is the least-squares seconds per span of the same-half LOCATEs.
func locateSlope(moves []Move) float64 {
	var n, x, y, xx, xy float64
	for _, move := range moves {
		d := span(move)
		n, x, y, xx, xy = n+1, x+d, y+move.Seconds, xx+d*d, xy+d*move.Seconds
	}
	return (n*xy - x*y) / (n*xx - x*x)
}

// solve returns the least-squares solution of rows·terms = values by the normal equations.
func solve(rows [][3]float64, values []float64) ([3]float64, bool) {
	var a [3][4]float64
	for i, row := range rows {
		for j := 0; j < 3; j++ {
			for k := 0; k < 3; k++ {
				a[j][k] += row[j] * row[k]
			}
			a[j][3] += row[j] * values[i]
		}
	}
	for i := 0; i < 3; i++ {
		pivot := i
		for j := i + 1; j < 3; j++ {
			if math.Abs(a[j][i]) > math.Abs(a[pivot][i]) {
				pivot = j
			}
		}
		a[i], a[pivot] = a[pivot], a[i]
		if math.Abs(a[i][i]) < 1e-9 {
			return [3]float64{}, false
		}
		for j := 0; j < 3; j++ {
			if j == i {
				continue
			}
			factor := a[j][i] / a[i][i]
			for k := i; k < 4; k++ {
				a[j][k] -= factor * a[i][k]
			}
		}
	}
	return [3]float64{a[0][3] / a[0][0], a[1][3] / a[1][1], a[2][3] / a[2][2]}, true
}
