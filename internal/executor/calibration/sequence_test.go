package calibration

import (
	"errors"
	"math"
	"testing"
)

// hpLTO5 holds the times one HP LTO-5 drive took for the sequence, in order from the load point.
var hpLTO5 = []float64{6.877, 16.747, 26.109, 12.835, 15.486, 25.282, 14.789, 4.973, 12.884, 5.896, 12.472, 1.853,
	7.942, 9.033, 13.339, 48.714, 13.199, 56.055, 56.719, 7.861, 56.104, 40.109}

func run(t *testing.T, times []float64) *Sequence {
	t.Helper()
	sequence := NewSequence(Point{})
	for _, seconds := range times {
		target, ok := sequence.Next()
		if !ok {
			t.Fatalf("sequence ended after %d moves", len(sequence.Moves())+1)
		}
		sequence.Record(target, seconds)
	}
	if _, ok := sequence.Next(); ok {
		t.Fatalf("sequence wants more than %d moves", len(times))
	}
	return sequence
}

func TestSequenceFitsMeasuredDrive(t *testing.T) {
	sequence := run(t, hpLTO5)
	low, high := sequence.Threshold()
	if math.Abs(low-0.044375) > 1e-9 || math.Abs(high-0.0478125) > 1e-9 {
		t.Fatalf("threshold bracket = [%v, %v]", low, high)
	}
	result, err := Fit(sequence.Moves(), low, high)
	if err != nil {
		t.Fatal(err)
	}
	for name, pair := range map[string][2]float64{
		"base": {result.BaseSeconds, 6.1}, "seek": {result.SeekSecondsPerWrap, 93.4},
		"reversal": {result.ApproachReversalSeconds, 4.8}, "read": {result.ReadSecondsPerWrap, 132.6},
	} {
		if math.Abs(pair[0]-pair[1]) > 0.5 {
			t.Errorf("%s = %.2f, want about %.1f", name, pair[0], pair[1])
		}
	}
	if result.MaxResidualSeconds > 2 {
		t.Errorf("max residual = %.2f", result.MaxResidualSeconds)
	}
	t.Logf("%+v", *result)
}

func TestFitRejectsIrregularMoves(t *testing.T) {
	for name, change := range map[string]func([]float64){
		"one slow move":            func(times []float64) { times[17] += 8 },
		"seek-speed short locates": func(times []float64) { times[1], times[2] = 13.5, 20 },
	} {
		t.Run(name, func(t *testing.T) {
			times := append([]float64(nil), hpLTO5...)
			change(times)
			sequence := run(t, times)
			low, high := sequence.Threshold()
			if _, err := Fit(sequence.Moves(), low, high); !errors.Is(err, ErrIrregular) {
				t.Fatalf("err = %v", err)
			}
		})
	}
}

func TestProbesReturnBeforeTheMiddle(t *testing.T) {
	// A drive that always positions keeps halving toward short probes; one that always streams
	// keeps lengthening them and must return to the origin instead of crossing the middle.
	sequence := NewSequence(Point{})
	for {
		target, ok := sequence.Next()
		if !ok {
			break
		}
		if target.Wrap == 0 && len(sequence.Moves()) > 5 && len(sequence.Moves()) < 14 && target.U > probeLimit+1e-9 {
			t.Fatalf("probe target %v crosses the limit", target)
		}
		seconds := 6 + 130*math.Abs(target.U-sequence.at.U)
		if len(sequence.Moves()) >= 5 && target.U > sequence.at.U && target.Wrap == 0 && sequence.probed < probes {
			seconds = 130 * (target.U - sequence.at.U)
		}
		sequence.Record(target, seconds)
	}
	if low, _ := sequence.Threshold(); low < 0.1 {
		t.Fatalf("low = %v", low)
	}
}
