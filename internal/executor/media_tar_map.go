package executor

import (
	"sort"

	"github.com/samuelncui/yatm/entity"
	mediapkg "github.com/samuelncui/yatm/internal/media"
	"github.com/samuelncui/yatm/internal/media/tape"
	"google.golang.org/protobuf/proto"
)

// compileTarMediaReadModel derives this Session's map from a current complete report only.
// The returned map reports its source even when elapsed-time inputs are incomplete.
func compileTarMediaReadModel(parameters *entity.TapeReadParameters, media *mediapkg.Descriptor,
	geometry *tape.WrapMap, valid func() bool,
) (*mediapkg.ReadCostModel, *tapePositionMap, string) {
	// The caller proves the label and validated frozen profile before accepting geometry into this session.
	if !geometry.Complete() {
		return nil, nil, "No current Tape position map is available"
	}
	if reason := tarReportedWrapsReason(geometry); reason != "" {
		return nil, nil, reason
	}
	observed, reason := deriveTarPositionMap(parameters, geometry)
	if observed == nil {
		return nil, nil, reason
	}
	model, reason := compileTarPositionMap(parameters, media.Profile.GetTape().Tar, observed,
		func() bool { return valid() && geometry.Valid() })
	return model, observed, reason
}

// tarReportedWrapsReason qualifies the current report's contiguous, increasing partition-zero traversal.
func tarReportedWrapsReason(geometry *tape.WrapMap) string {
	low := uint64(0)
	for index, endpoint := range geometry.Endpoints {
		if endpoint.Partition != 0 || endpoint.Wrap != uint32(index) {
			return "Linear interpolation requires contiguous partition-zero wraps beginning at zero"
		}
		if endpoint.Object <= low {
			return "Reported wrap boundaries are empty or reversed"
		}
		low = endpoint.Object
	}
	return ""
}

// tapePositionMap is the derived geometry of the cartridge loaded for one read Session; it is never stored.
type tapePositionMap struct {
	source string
	wraps  []mediapkg.ReadModelWrap
	// eod is each mapped partition's actual end of data.
	eod map[uint32]uint64
}

// tapePartitionWraps lays one partition's consecutive reported endpoints out under the four-band
// model. Reported logical addresses remain exact; the layout supplies only orientation and bands.
// It returns the sorted full-wrap object counts; with at least one, the final wrap's interpolation
// denominator is the greater of its observed length and their median, never extending actual EOD.
func tapePartitionWraps(nominalWraps uint32, endpoints []tape.WrapEndpoint) ([]mediapkg.ReadModelWrap, []uint64) {
	wraps := make([]mediapkg.ReadModelWrap, len(endpoints))
	lengths := make([]uint64, 0, len(endpoints)-1)
	low := uint64(0)
	for index, endpoint := range endpoints {
		last := index == len(endpoints)-1
		band, direction := tapeWrapLayout(endpoint.Wrap, nominalWraps)
		wraps[index] = mediapkg.ReadModelWrap{Partition: endpoint.Partition, Wrap: endpoint.Wrap, Band: band,
			Direction: direction, Full: !last, Endpoint: proto.Uint64(endpoint.Object)}
		if !last {
			lengths = append(lengths, endpoint.Object-low)
			low = endpoint.Object
		}
	}
	if len(lengths) != 0 {
		sort.Slice(lengths, func(i, j int) bool { return lengths[i] < lengths[j] })
		eod := endpoints[len(endpoints)-1].Object
		wraps[len(wraps)-1].EstimatedSpanObjects = proto.Uint64(max(eod-low, ltfsMedian(lengths)))
	}
	return wraps, lengths
}

func deriveTarPositionMap(parameters *entity.TapeReadParameters, geometry *tape.WrapMap) (*tapePositionMap, string) {
	// Assigning bands requires the explicit four-band layout rather than the number of populated wraps.
	// The caller qualifies the complete reported traversal before deriving its map.
	layout := parameters.GetGeometry()
	if layout.GetNominalWraps() < 4 || layout.GetNominalWraps()%4 != 0 {
		return nil, "The four-band interpolation layout requires an explicit nominal wrap count"
	}
	if len(geometry.Endpoints) < 2 || uint64(len(geometry.Endpoints)) > uint64(layout.GetNominalWraps()) {
		return nil, "A current complete reported-wrap map with a full interval is unavailable"
	}
	wraps, _ := tapePartitionWraps(layout.GetNominalWraps(), geometry.Endpoints)
	return &tapePositionMap{source: geometry.Source, wraps: wraps,
		eod: map[uint32]uint64{0: geometry.Endpoints[len(wraps)-1].Object}}, ""
}

// tapeWrapLayout derives a wrap's band and direction from the generation's nominal wrap count:
// four equal bands, and even wraps written forward. Callers ensure at least four nominal wraps.
func tapeWrapLayout(wrap, nominalWraps uint32) (uint32, mediapkg.ReadWrapDirection) {
	if wrap%2 != 0 {
		return wrap / (nominalWraps / 4), mediapkg.ReadWrapReverse
	}
	return wrap / (nominalWraps / 4), mediapkg.ReadWrapForward
}
