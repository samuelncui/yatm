package executor

import (
	"fmt"
	"math"
	"sort"
	"time"

	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/internal/media/tape"
	"google.golang.org/protobuf/proto"
)

const tarWriteGeometryMaxBins = 65536

type tarWriteGeometryBin struct {
	seconds, supplySeconds float64
	bytes, low, high       uint64
}

// tarWriteGeometryLearning receives completed native facts on the observation consumer,
// never the I/O lane. It retains at most 65536 byte bins, not per-record telemetry or files.
type tarWriteGeometryLearning struct {
	profile                     *entity.TarMediaProfile
	prefix                      *entity.TapePositionMap
	bins                        []tarWriteGeometryBin
	width, bytes, cursor, start uint64
	sequence                    uint64
	wraps, firstWrap            uint32
	nominalPeriod, rate         float64
	lastEnd                     time.Time
	failed                      string
	terminal                    bool
}

func newTarWriteGeometryLearning(report *entity.InspectDeviceResponse, profile *entity.TarMediaProfile,
	start tape.Position, prefix *entity.TapePositionMap,
) (*tarWriteGeometryLearning, string) {
	if report.GetTape() == nil || report.GetTape().DensityCode == nil || profile == nil || profile.HardwareCompression ||
		profile.InitializationId == "" || profile.RecordMode == entity.TapeRecordMode_TAPE_RECORD_MODE_UNSPECIFIED ||
		profile.RecordBytes == 0 || !start.ObjectKnown || start.Partition != 0 {
		return nil, "Uncompressed identified written conditions and a partition-zero cursor are required"
	}
	density := report.GetTape().GetDensityCode()
	wraps, rate, _ := tarBuiltinMediaDefaults(density)
	capacity := tarNativeCapacity(density)
	if wraps == 0 || capacity == 0 {
		return nil, "Recorded density has no sourced native capacity/layout prior"
	}
	width := uint64(1 << 20)
	for width < capacity/32768 {
		width *= 2
	}
	value := &tarWriteGeometryLearning{profile: proto.Clone(profile).(*entity.TarMediaProfile),
		width: width, cursor: start.Object, start: start.Object, wraps: wraps, rate: rate,
		nominalPeriod: float64(capacity) / float64(wraps)}
	if prefix == nil {
		// At most start*RecordBytes precede this cursor; only a near-BOT start can identify wrap ordinal zero.
		if float64(start.Object)*float64(profile.RecordBytes) > value.nominalPeriod*.1 {
			return nil, "A mid-Tape APPEND requires retained prefix geometry to identify phase"
		}
		value.bins = make([]tarWriteGeometryBin, tarWriteGeometryMaxBins)
		return value, ""
	}

	// APPEND keeps the caller's complete full prefix and replaces only its final partial descriptor.
	if prefix.InitializationId != profile.InitializationId || len(prefix.Partitions) != 1 || prefix.Partitions[0] == nil ||
		prefix.Partitions[0].Partition != 0 || prefix.Partitions[0].GetEod() != start.Object || len(prefix.Wraps) == 0 {
		return nil, "APPEND prefix does not match initialization and actual starting EOD"
	}
	last := prefix.Wraps[len(prefix.Wraps)-1]
	if last == nil || last.Full || last.GetEndpoint() != start.Object || last.Partition != 0 ||
		last.EstimatedSpanObjects == nil || last.GetEstimatedSpanObjects() == 0 {
		return nil, "APPEND prefix must end in its actual partial wrap"
	}
	if prefix.EndpointConvention != entity.TapePositionEndpointConvention_TAPE_POSITION_ENDPOINT_CONVENTION_EXCLUSIVE &&
		prefix.EndpointConvention != entity.TapePositionEndpointConvention_TAPE_POSITION_ENDPOINT_CONVENTION_INCLUSIVE {
		return nil, "APPEND prefix endpoint convention is unknown"
	}
	for i, wrap := range prefix.Wraps {
		if wrap == nil || wrap.Partition != 0 || wrap.Wrap != uint32(i) || wrap.Endpoint == nil ||
			wrap.Direction != tarWriteWrapDirection(wrap.Wrap) || wrap.Band != wrap.Wrap/(wraps/4) || i < len(prefix.Wraps)-1 && !wrap.Full {
			return nil, "APPEND prefix traversal does not match the sourced layout"
		}
	}
	value.prefix = proto.Clone(prefix).(*entity.TapePositionMap)
	value.firstWrap = last.Wrap
	value.bins = make([]tarWriteGeometryBin, tarWriteGeometryMaxBins)
	return value, ""
}

func (w *tarWriteGeometryLearning) Observe(value tape.CommandObservation) {
	if w == nil || w.failed != "" {
		return
	}
	if w.sequence != 0 && value.Sequence != w.sequence+1 {
		w.failed = "Native command observation sequence has a gap"
		return
	}
	w.sequence = value.Sequence
	if value.Opcode != 0x0a {
		if w.bytes != 0 && value.Opcode == 0x10 && !value.Known {
			w.terminal = true
		}
		if w.bytes != 0 && value.Opcode != 0x34 && value.Opcode != 0x10 {
			w.failed = "A nonsequential command interrupted WRITE geometry observation"
		}
		return
	}
	if !value.Known {
		// Preserve known prefix evidence before a terminal EW; no accepted bytes are invented.
		w.terminal = true
		return
	}
	if w.terminal || value.Bytes <= 0 || value.Bytes > int(w.profile.RecordBytes) ||
		value.Elapsed <= 0 || value.StartedAt.IsZero() || w.cursor == math.MaxUint64 {
		w.failed = "WRITE byte acceptance, sequential cursor or native timing is incomplete"
		return
	}
	gap := 0.0
	if !w.lastEnd.IsZero() {
		gap = max(0, value.StartedAt.Sub(w.lastEnd).Seconds())
	}
	w.lastEnd = value.StartedAt.Add(value.Elapsed)
	end := w.bytes + uint64(value.Bytes)
	if end < w.bytes || (end-1)/w.width >= uint64(len(w.bins)) {
		w.failed = "WRITE geometry exceeds the fixed byte-bin budget"
		return
	}

	// Conserve native duration over accepted bytes; retain supply/admission gaps separately.
	for w.bytes < end {
		index := w.bytes / w.width
		stop := min(end, (index+1)*w.width)
		bin := &w.bins[index]
		if bin.bytes == 0 {
			bin.low = w.cursor
		}
		bin.high = w.cursor + 1
		bin.bytes += stop - w.bytes
		bin.seconds += value.Elapsed.Seconds() * float64(stop-w.bytes) / float64(value.Bytes)
		bin.supplySeconds += gap
		gap = 0
		w.bytes = stop
	}
	w.cursor++
}

func (w *tarWriteGeometryLearning) Finish(eod tape.Position, dropped uint64) (*entity.TapePositionMap, string) {
	if w == nil {
		return nil, "WRITE geometry observation is unavailable"
	}
	defer func() { w.bins = nil }() // Only the derived map survives finalization.
	if w.failed != "" {
		return nil, w.failed
	}
	if dropped != 0 || !eod.ObjectKnown || eod.Partition != 0 || eod.Object < w.cursor || eod.Object-w.cursor > 2 {
		return nil, "A lossless WRITE trace and settled actual EOD are required"
	}
	count := int(w.bytes / w.width) // An incomplete final bin cannot supply a rate.
	if count < 32 {
		return nil, "WRITE byte coverage is too short to identify recurrence"
	}
	fit, reason := fitTarWriteGeometry(w.bins[:count], float64(w.width), w.nominalPeriod, w.rate)
	if fit == nil {
		return nil, reason
	}
	first := (float64(fit.points[0]) + .5) * float64(w.width)
	if w.prefix == nil {
		if first < fit.period*.5 || first > fit.period*1.5 {
			return nil, "The first native trough cannot identify the initial wrap phase"
		}
	} else {
		low := uint64(0)
		if len(w.prefix.Wraps) > 1 {
			low = w.prefix.Wraps[len(w.prefix.Wraps)-2].GetEndpoint()
			if w.prefix.EndpointConvention == entity.TapePositionEndpointConvention_TAPE_POSITION_ENDPOINT_CONVENTION_INCLUSIVE {
				low++
			}
		}
		span := w.prefix.Wraps[len(w.prefix.Wraps)-1].GetEstimatedSpanObjects()
		if w.start < low || w.start-low > span || math.Abs(first-fit.period*(1-float64(w.start-low)/float64(span))) > fit.period*.25 {
			return nil, "APPEND native phase does not agree with the retained partial wrap"
		}
	}
	if uint64(w.firstWrap)+uint64(len(fit.points))+1 > uint64(w.wraps) {
		return nil, "WRITE candidates exceed the sourced traversal"
	}
	return w.positionMap(eod.Object, fit), ""
}

func (w *tarWriteGeometryLearning) positionMap(eod uint64, fit *tarWriteGeometryFit) *entity.TapePositionMap {
	value := &entity.TapePositionMap{InitializationId: w.profile.InitializationId,
		EndpointConvention: entity.TapePositionEndpointConvention_TAPE_POSITION_ENDPOINT_CONVENTION_EXCLUSIVE,
		Partitions:         []*entity.TapePositionPartition{{Eod: proto.Uint64(eod)}},
		ObservedAtNs:       time.Now().UnixNano(), Source: fmt.Sprintf(
			"passive native WRITE byte recurrence; first-half frequency %.6g bytes, ACF %.3f; empirical endpoint allowances; nominal density layout selects harmonic only",
			fit.period, fit.correlation)}
	if w.prefix != nil {
		value.EndpointConvention = w.prefix.EndpointConvention
		value.Wraps = append(value.Wraps, w.prefix.Wraps[:len(w.prefix.Wraps)-1]...)
		value.MinLpos, value.MaxLpos = w.prefix.MinLpos, w.prefix.MaxLpos
	}
	low := uint64(0)
	if len(value.Wraps) != 0 {
		low = value.Wraps[len(value.Wraps)-1].GetEndpoint()
		if value.EndpointConvention == entity.TapePositionEndpointConvention_TAPE_POSITION_ENDPOINT_CONVENTION_INCLUSIVE {
			low++
		}
	}
	// Actual object progression, including short variable records, determines the byte-to-object scale.
	lengths := make([]uint64, 0, len(fit.points)-1)
	for i := 1; i < len(fit.points); i++ {
		a, b := w.bins[fit.points[i-1]], w.bins[fit.points[i]]
		lengths = append(lengths, b.low+(b.high-b.low)/2-(a.low+(a.high-a.low)/2))
	}
	sort.Slice(lengths, func(i, j int) bool { return lengths[i] < lengths[j] })
	span := lengths[len(lengths)/2]
	for i, point := range fit.points {
		bin := w.bins[point]
		boundary := bin.low + (bin.high-bin.low)/2
		// An empirical 5% phase allowance covers buffering plus observed local period variation.
		// It is not a tolerance specified by the LTO format or a portable physical error guarantee.
		allowance := uint64(math.Ceil(max(.05, fit.spacingDeviation*2/fit.period) * float64(span)))
		endpoint := boundary
		if value.EndpointConvention == entity.TapePositionEndpointConvention_TAPE_POSITION_ENDPOINT_CONVENTION_INCLUSIVE {
			endpoint--
		}
		wrap := w.firstWrap + uint32(i)
		value.Wraps = append(value.Wraps, &entity.TapePositionWrap{Wrap: wrap, Band: wrap / (w.wraps / 4),
			Direction: tarWriteWrapDirection(wrap), Full: true, Endpoint: proto.Uint64(endpoint),
			EndpointUncertaintyObjects: proto.Uint64(max(allowance, bin.high-bin.low))})
		low = boundary
	}
	wrap := w.firstWrap + uint32(len(fit.points))
	value.Wraps = append(value.Wraps, &entity.TapePositionWrap{Wrap: wrap, Band: wrap / (w.wraps / 4),
		Direction: tarWriteWrapDirection(wrap), Endpoint: proto.Uint64(eod), EndpointUncertaintyObjects: proto.Uint64(0),
		EstimatedSpanObjects: proto.Uint64(max(span, eod-low))})
	return value
}

func tarWriteWrapDirection(wrap uint32) entity.TapePositionDirection {
	if wrap%2 != 0 {
		return entity.TapePositionDirection_TAPE_POSITION_DIRECTION_REVERSE
	}
	return entity.TapePositionDirection_TAPE_POSITION_DIRECTION_FORWARD
}

func tarNativeCapacity(density uint32) uint64 {
	// Native decimal capacities: HP Gen3/Gen6 specifications and Quantum TB00068A-v03 p4.
	// These choose a harmonic search window, never exact per-cartridge object spacing.
	switch density {
	case 0x40:
		return 100e9
	case 0x42:
		return 200e9
	case 0x44:
		return 400e9
	case 0x46:
		return 800e9
	case 0x58:
		return 1500e9
	case 0x5a:
		return 2500e9
	case 0x5c:
		return 6000e9
	case 0x5d:
		return 9000e9
	case 0x5e:
		return 12000e9
	case 0x60:
		return 18000e9
	}
	return 0
}
