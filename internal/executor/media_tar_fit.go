package executor

import (
	"math"
	"strings"
	"time"

	"github.com/samuelncui/yatm/entity"
	mediapkg "github.com/samuelncui/yatm/internal/media"
	"google.golang.org/protobuf/proto"
)

// Implementation details of the iteratively reweighted least-squares L1 solve: a fixed iteration
// bound, a relative coefficient-change stop and a residual floor that keeps weights finite.
const (
	tapeFitIterations    = 64
	tapeFitTolerance     = 1e-9
	tapeFitResidualFloor = 1e-6
)

const (
	tapeFitLocate = iota
	tapeFitRead
	tapeFitUnload
)

type tapeFitRow struct {
	x      [7]float64
	y      float64
	job    int64
	action observationAction
}

// tapeReadRefit reduces retained observations to the columns each action's model needs.
type tapeReadRefit struct {
	base    *entity.TapeReadParameters
	unit    mediapkg.ReadLongitudinal
	nominal uint32
	rows    [3][]tapeFitRow
	count   uint64
}

func newTapeReadRefit(base *entity.TapeReadParameters) *tapeReadRefit {
	refit := &tapeReadRefit{base: base, nominal: base.GetGeometry().GetNominalWraps()}
	refit.unit.Normalized = base.GetPositioning().LongitudinalSecondsPerWrap != nil
	return refit
}

func (r *tapeReadRefit) add(value *tapeReadObservation) {
	r.count++
	if value.DeviceSeconds < 0 || math.IsNaN(value.DeviceSeconds) || math.IsInf(value.DeviceSeconds, 0) {
		return
	}
	row := tapeFitRow{y: value.DeviceSeconds, job: value.JobID, action: value.Action}
	switch value.Action {
	case observationLocate:
		features, ok := r.features(value)
		if !ok {
			return
		}
		row.x = features
		r.rows[tapeFitLocate] = append(r.rows[tapeFitLocate], row)
	case observationRead, observationFirstRead, observationFilemarkRead:
		// seconds = bytes / rate, plus the first-READ or filemark fixed term.
		row.x[0] = float64(value.Bytes)
		if value.Action == observationFirstRead {
			row.x[1] = 1
		} else if value.Action == observationFilemarkRead {
			row.x[2] = 1
		}
		r.rows[tapeFitRead] = append(r.rows[tapeFitRead], row)
	case observationUnload:
		// seconds = fixed work + physical fraction of one full-span rewind.
		from := value.From
		if from.Offset == nil || from.Span == nil || *from.Span == 0 || *from.Offset > *from.Span {
			return
		}
		row.x[0], row.x[1] = 1, float64(*from.Offset)/float64(*from.Span)
		r.rows[tapeFitUnload] = append(r.rows[tapeFitUnload], row)
	}
}

// features resolves retained points with the current generation layout and longitudinal unit.
func (r *tapeReadRefit) features(value *tapeReadObservation) ([7]float64, bool) {
	unit := r.unit
	if !unit.Normalized {
		if value.MinLPOS == nil || value.MaxLPOS == nil {
			return [7]float64{}, false
		}
		unit.MinLPOS, unit.MaxLPOS = *value.MinLPOS, *value.MaxLPOS
	}
	from, ok := r.point(value.From)
	if !ok {
		return [7]float64{}, false
	}
	to, ok := r.point(value.To)
	if !ok {
		return [7]float64{}, false
	}
	return mediapkg.ReadAccessFeatures(from, to, unit)
}

func (r *tapeReadRefit) point(value observationPoint) (mediapkg.ReadPoint, bool) {
	if value.Partition == nil || value.Wrap == nil || value.Offset == nil || value.Span == nil ||
		r.nominal < 4 || r.nominal%4 != 0 {
		return mediapkg.ReadPoint{}, false
	}
	band, direction := tapeWrapLayout(*value.Wrap, r.nominal)
	return mediapkg.ReadPoint{Partition: *value.Partition, Wrap: *value.Wrap, Band: band,
		Direction: mediapkg.ReadWrapDirection(direction), Offset: *value.Offset, Span: *value.Span}, true
}

// model refits every action from the base parameters, or returns nil when no coefficient is identifiable.
func (r *tapeReadRefit) model(device string, now time.Time) *entity.TapeLearnedReadParameters {
	parameters := proto.Clone(r.base).(*entity.TapeReadParameters)
	var coefficients [3][7]float64
	var updated [3]bool

	// LOCATE keeps the seven CERN terms in the base parameters' longitudinal unit.
	if p := parameters.Positioning; p != nil && len(r.rows[tapeFitLocate]) != 0 {
		slope := &p.LongitudinalSecondsPerLpos
		if r.unit.Normalized {
			slope = &p.LongitudinalSecondsPerWrap
		}
		fields := [7]**float64{&p.BaseSeconds, &p.WrapChangeSeconds, &p.BandChangeSeconds,
			&p.LandingZoneChangeSeconds, &p.DirectionChangeSeconds, &p.StepBackSeconds, slope}
		for i, field := range fields {
			coefficients[tapeFitLocate][i] = valueOrZero(*field)
		}
		fitted, identified := tapeFitL1(r.rows[tapeFitLocate], 7, coefficients[tapeFitLocate])
		for i, field := range fields {
			if identified[i] && (i < 6 || fitted[i] >= 0) {
				*field, coefficients[tapeFitLocate][i], updated[tapeFitLocate] = proto.Float64(fitted[i]), fitted[i], true
			}
		}
	}

	// READ prices seconds per byte; the rate is its reciprocal.
	if rows := r.rows[tapeFitRead]; len(rows) != 0 {
		reading := parameters.Reading
		if reading == nil {
			reading = &entity.TapeReadTiming{}
		}
		current := [7]float64{0, reading.GetPostLocateReadOverheadSeconds(), reading.GetFilemarkOverheadSeconds()}
		if reading.GetBytesPerSecond() > 0 {
			current[0] = 1 / reading.GetBytesPerSecond()
		}
		coefficients[tapeFitRead] = current
		fitted, identified := tapeFitL1(rows, 3, current)
		if identified[0] && fitted[0] > 0 && !math.IsInf(1/fitted[0], 0) {
			reading.BytesPerSecond, coefficients[tapeFitRead][0], updated[tapeFitRead] = proto.Float64(1/fitted[0]), fitted[0], true
		}
		for i, field := range []**float64{nil, &reading.PostLocateReadOverheadSeconds, &reading.FilemarkOverheadSeconds} {
			if field != nil && identified[i] && fitted[i] >= 0 {
				*field, coefficients[tapeFitRead][i], updated[tapeFitRead] = proto.Float64(fitted[i]), fitted[i], true
			}
		}
		if updated[tapeFitRead] {
			parameters.Reading = reading
		}
	}

	// UNLOAD keeps its own fixed work and full-span slope, never LOCATE coefficients.
	if rows := r.rows[tapeFitUnload]; len(rows) != 0 {
		completion := parameters.Completion
		if completion == nil {
			completion = &entity.TapeReadCompletion{}
		}
		current := [7]float64{completion.GetFixedSeconds(), completion.GetFullSpanSeconds()}
		coefficients[tapeFitUnload] = current
		fitted, identified := tapeFitL1(rows, 2, current)
		if identified[0] && fitted[0] >= 0 {
			completion.FixedSeconds, coefficients[tapeFitUnload][0], updated[tapeFitUnload] = proto.Float64(fitted[0]), fitted[0], true
		}
		if identified[1] && fitted[1] > 0 {
			completion.FullSpanSeconds, coefficients[tapeFitUnload][1], updated[tapeFitUnload] = proto.Float64(fitted[1]), fitted[1], true
		}
		if updated[tapeFitUnload] {
			parameters.Completion = completion
		}
	}
	if !updated[tapeFitLocate] && !updated[tapeFitRead] && !updated[tapeFitUnload] {
		return nil
	}

	// Residuals over the retained observations summarize the published fit.
	count, total, worst := 0, 0.0, 0.0
	predicted := [3]bool{parameters.Positioning != nil, parameters.GetReading().GetBytesPerSecond() > 0, parameters.Completion != nil}
	for class, rows := range r.rows {
		if !predicted[class] {
			continue
		}
		for _, row := range rows {
			residual := row.y
			for i, value := range row.x {
				residual -= value * coefficients[class][i]
			}
			residual = math.Abs(residual)
			count, total, worst = count+1, total+residual, max(worst, residual)
		}
	}
	if count == 0 || math.IsNaN(total) || math.IsInf(total, 0) {
		return nil
	}
	actions := make([]string, 0, 3)
	for class, name := range []string{"LOCATE", "READ", "UNLOAD"} {
		if updated[class] {
			actions = append(actions, name)
		}
	}
	parameters.SourceReference = "Passive completed native I/O; Job-weighted L1 refit over retained observations; measured actions: " +
		strings.Join(actions, ", ") + "; other parameters retain prior values"
	return &entity.TapeLearnedReadParameters{Device: device, Parameters: parameters, FitVersion: "native-l1-1",
		TrainingObservations: uint32(min(r.count, math.MaxUint32)), MaeSeconds: total / float64(count),
		MaxErrorSeconds: worst, UpdatedAtNs: now.UnixNano()}
}

func valueOrZero(value *float64) float64 {
	if value == nil {
		return 0
	}
	return *value
}

// tapeFitL1 minimizes weighted absolute residuals. Each Job carries equal total weight within an
// action, so one abnormal Job among N moves the fit by about 1/N. A column that is not independent
// in the weighted design is not identifiable: it keeps its current coefficient and is not reported.
func tapeFitL1(rows []tapeFitRow, columns int, current [7]float64) ([7]float64, [7]bool) {
	// Weight each record by the inverse of its Job's record count for the same action.
	type key struct {
		job    int64
		action observationAction
	}
	counts := make(map[key]int)
	for _, row := range rows {
		counts[key{row.job, row.action}]++
	}
	weights := make([]float64, len(rows))
	target := make([]float64, len(rows))
	for i, row := range rows {
		weights[i], target[i] = 1/float64(counts[key{row.job, row.action}]), row.y
	}

	// Identifiability is decided once on the Job weights; fixed columns move into the target.
	var use [7]bool
	for i := range columns {
		use[i] = true
	}
	_, identified := tapeLeastSquares(rows, use, target, weights)
	for i, row := range rows {
		for column := range columns {
			if !identified[column] {
				target[i] -= row.x[column] * current[column]
			}
		}
	}
	coefficients, independent := tapeLeastSquares(rows, identified, target, weights)
	if independent != identified {
		return current, [7]bool{}
	}

	// Reweight by inverse absolute residual until coefficients settle or the bound is reached.
	scaled := make([]float64, len(rows))
	for range tapeFitIterations {
		for i, row := range rows {
			residual := target[i]
			for column, value := range row.x {
				residual -= value * coefficients[column]
			}
			scaled[i] = weights[i] / max(math.Abs(residual), tapeFitResidualFloor)
		}
		next, independent := tapeLeastSquares(rows, identified, target, scaled)
		if independent != identified {
			break
		}
		settled := true
		for column := range columns {
			if math.Abs(next[column]-coefficients[column]) > tapeFitTolerance*math.Abs(next[column]) {
				settled = false
			}
		}
		coefficients = next
		if settled {
			break
		}
	}
	result := current
	for column := range columns {
		if identified[column] {
			result[column] = coefficients[column]
		}
	}
	return result, identified
}

// tapeLeastSquares solves weighted least squares over the used columns with scaled, reorthogonalized
// modified Gram-Schmidt. A column below the scaled-rank threshold is reported as not independent.
func tapeLeastSquares(rows []tapeFitRow, use [7]bool, target, weights []float64) ([7]float64, [7]bool) {
	roots := make([]float64, len(rows))
	for i, weight := range weights {
		roots[i] = math.Sqrt(weight)
	}

	// Scale each column to unit norm, then orthogonalize twice against the accepted columns.
	var basis [][]float64
	var order []int
	var upper [7][7]float64
	var scales [7]float64
	for column := range 7 {
		if !use[column] {
			continue
		}
		values, scale := make([]float64, len(rows)), 0.0
		for i, row := range rows {
			values[i] = roots[i] * row.x[column]
			scale += values[i] * values[i]
		}
		scale = math.Sqrt(scale)
		if scale == 0 || math.IsInf(scale, 0) || math.IsNaN(scale) {
			continue
		}
		for i := range values {
			values[i] /= scale
		}
		var projections [7]float64
		for range 2 {
			for k, before := range basis {
				projection := 0.0
				for i, value := range values {
					projection += value * before[i]
				}
				projections[k] += projection
				for i := range values {
					values[i] -= projection * before[i]
				}
			}
		}
		norm := 0.0
		for _, value := range values {
			norm += value * value
		}
		norm = math.Sqrt(norm)
		if norm < 0.01 {
			continue
		}
		k := len(basis)
		for i := range values {
			values[i] /= norm
		}
		for before := range k {
			upper[before][k] = projections[before]
		}
		upper[k][k], scales[k] = norm, scale
		basis, order = append(basis, values), append(order, column)
	}

	// Back substitution returns coefficients in original feature units.
	var solved [7]float64
	for k := len(basis) - 1; k >= 0; k-- {
		value := 0.0
		for i, base := range basis[k] {
			value += base * roots[i] * target[i]
		}
		for next := k + 1; next < len(basis); next++ {
			value -= upper[k][next] * solved[next]
		}
		solved[k] = value / upper[k][k]
	}
	var coefficients [7]float64
	var independent [7]bool
	for k, column := range order {
		coefficients[column] = solved[k] / scales[k]
		independent[column] = !math.IsNaN(coefficients[column]) && !math.IsInf(coefficients[column], 0)
	}
	return coefficients, independent
}
