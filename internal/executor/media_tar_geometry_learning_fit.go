package executor

import (
	"math"
	"math/cmplx"
	"sort"
)

type tarWriteGeometryFit struct {
	points              []int
	period, correlation float64
	spacingDeviation    float64
}

func fitTarWriteGeometry(bins []tarWriteGeometryBin, width, nominal, rate float64) (*tarWriteGeometryFit, string) {
	// Sparse narrow native stalls must sit on a predominantly streaming baseline, not supply gaps.
	signal := make([]float64, len(bins))
	native, supply := 0.0, 0.0
	for i, bin := range bins {
		signal[i] = bin.seconds
		native += bin.seconds
		supply += bin.supplySeconds
	}
	ordered := append([]float64(nil), signal...)
	sort.Float64s(ordered)
	baseline := ordered[len(ordered)/2]
	if baseline <= 0 || native <= 0 || width/baseline < rate*.6 || supply > native*.1 {
		return nil, "WRITE observations are slow, starved or have insufficient native streaming support"
	}
	nominalBins := nominal / width
	if float64(len(bins)) < nominalBins*8 {
		return nil, "At least eight observed native byte periods are required"
	}

	// Freeze frequency on the first byte half; the second half must identify the same harmonic.
	middle := len(signal) / 2
	period, power := tarWriteSpectrumPeriod(signal[:middle], nominalBins)
	later, laterPower := tarWriteSpectrumPeriod(signal[middle:], nominalBins)
	if period == 0 || later == 0 || math.Abs(later/period-1) > .03 || power*period < .05 || laterPower*later < .05 {
		return nil, "Native byte frequency is weak, unstable or unidentifiable"
	}
	correlation := tarWriteCorrelation(signal, int(math.Round(period)))
	half := tarWriteCorrelation(signal, int(math.Round(period/2)))
	if correlation < .15 || half >= correlation*.8 {
		return nil, "Autocorrelation does not distinguish the nominal full-wrap harmonic"
	}

	// Detector threshold is an experimental admission rule, not a specification-derived tolerance.
	points := make([]int, 0, 280)
	slow := 0
	for i := 1; i < len(signal)-1; i++ {
		if signal[i] <= baseline*3.5 {
			continue
		}
		slow++
		if float64(i) < period*.1 || float64(len(signal)-i) < period*.1 {
			continue // Initialization and terminal settlement do not identify an interior boundary.
		}
		if len(points) != 0 && i-points[len(points)-1] <= 2 {
			if signal[i] > signal[points[len(points)-1]] {
				points[len(points)-1] = i
			}
			continue
		}
		points = append(points, i)
	}
	if len(points) < 7 || float64(slow)/float64(len(signal)) > .1 {
		return nil, "Native stalls are absent, broad or consistent with continuous backhitching"
	}

	// Sequential boundary identity needs local spacing and a concentrated phase, not only a spectral peak.
	gaps := make([]float64, 0, len(points)-1)
	phaseX, phaseY := 0.0, 0.0
	for i, point := range points {
		angle := 2 * math.Pi * float64(point) / period
		phaseX += math.Cos(angle)
		phaseY += math.Sin(angle)
		if i == 0 {
			continue
		}
		gap := float64(point - points[i-1])
		if gap < .8*period || gap > 1.2*period {
			return nil, "Local native trough spacing cannot identify consecutive wraps"
		}
		gaps = append(gaps, gap)
	}
	if math.Hypot(phaseX, phaseY)/float64(len(points)) < .9 {
		return nil, "Native trough phase is not identifiable"
	}
	sort.Float64s(gaps)
	median := gaps[len(gaps)/2]
	deviations := make([]float64, len(gaps))
	for i, gap := range gaps {
		deviations[i] = math.Abs(gap - median)
	}
	sort.Float64s(deviations)
	return &tarWriteGeometryFit{points: points, period: period * width, correlation: correlation,
		spacingDeviation: deviations[len(deviations)*9/10] * width}, ""
}

func tarWriteSpectrumPeriod(signal []float64, nominal float64) (float64, float64) {
	// A bounded radix-two spectrum uses zero padding only to interpolate frequencies, not to add evidence.
	length := 1
	for length < len(signal)*2 {
		length *= 2
	}
	values := make([]complex128, length)
	mean, variance := 0.0, 0.0
	for _, value := range signal {
		mean += value
	}
	mean /= float64(len(signal))
	for i, value := range signal {
		centered := value - mean
		variance += centered * centered
		values[i] = complex(centered*(.5-.5*math.Cos(2*math.Pi*float64(i)/float64(len(signal)-1))), 0)
	}
	if variance == 0 {
		return 0, 0
	}
	for i, j := 1, 0; i < length; i++ {
		bit := length >> 1
		for ; j&bit != 0; bit >>= 1 {
			j ^= bit
		}
		j ^= bit
		if i < j {
			values[i], values[j] = values[j], values[i]
		}
	}
	for size := 2; size <= length; size *= 2 {
		root := cmplx.Rect(1, -2*math.Pi/float64(size))
		for offset := 0; offset < length; offset += size {
			factor := complex(1, 0)
			for j := 0; j < size/2; j++ {
				a, b := values[offset+j], values[offset+j+size/2]*factor
				values[offset+j], values[offset+j+size/2] = a+b, a-b
				factor *= root
			}
		}
	}
	best, power := 0, 0.0
	for i := 1; i < length/2; i++ {
		period := float64(length) / float64(i)
		if period < nominal/math.Sqrt2 || period > nominal*math.Sqrt2 {
			continue
		}
		p := real(values[i])*real(values[i]) + imag(values[i])*imag(values[i])
		if p > power {
			best, power = i, p
		}
	}
	if best == 0 {
		return 0, 0
	}
	// Sub-bin frequency error can miss a one-bin stall entirely. Refine only inside this
	// spectral peak's resolution cell, on the same half used to choose the frequency.
	low := max(1, int(math.Floor(float64(length)/(float64(best)+.75))))
	high := min(len(signal)-1, int(math.Ceil(float64(length)/(float64(best)-.75))))
	lag, correlation := 0, -1.0
	for i := low; i <= high; i++ {
		if value := tarWriteCorrelation(signal, i); value > correlation {
			lag, correlation = i, value
		}
	}
	return float64(lag), power / (variance * float64(len(signal)))
}

func tarWriteCorrelation(values []float64, lag int) float64 {
	if lag <= 0 || lag >= len(values) {
		return 0
	}
	x, y, xx, yy, xy := 0.0, 0.0, 0.0, 0.0, 0.0
	for i := lag; i < len(values); i++ {
		a, b := values[i-lag], values[i]
		x, y = x+a, y+b
		xx, yy, xy = xx+a*a, yy+b*b, xy+a*b
	}
	n := float64(len(values) - lag)
	denominator := math.Sqrt((xx - x*x/n) * (yy - y*y/n))
	if denominator <= 0 {
		return 0
	}
	return (xy - x*y/n) / denominator
}
