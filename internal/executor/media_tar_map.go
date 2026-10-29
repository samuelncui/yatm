package executor

import (
	"math"
	"sort"

	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/internal/dataformat"
	"github.com/samuelncui/yatm/internal/library"
	mediapkg "github.com/samuelncui/yatm/internal/media"
	"github.com/samuelncui/yatm/internal/media/tape"
	"google.golang.org/protobuf/proto"
)

// compileTarMediaReadModel uses a current report first, or retained coverage owned by this initialization.
// The returned observation is independently publishable even when elapsed-time inputs are incomplete.
func compileTarMediaReadModel(parameters *entity.TapeReadParameters, stored *library.Media,
	geometry *tape.WrapMap, boundary mediapkg.ReadEndpointConvention, valid func() bool,
) (*mediapkg.ReadCostModel, *entity.TapePositionMap, string) {
	// The caller proves the label and frozen profile before accepting geometry into this session.
	if stored == nil || stored.Profile.GetTape().GetTar() == nil || valid == nil || !valid() {
		return nil, nil, "A current identified tar Media session is unavailable"
	}
	if err := entity.ValidateTarMediaProfile(stored.Profile.GetTape()); err != nil {
		return nil, nil, err.Error()
	}
	profile := stored.Profile.GetTape().Tar
	current := geometry != nil && geometry.Valid() && geometry.AllWraps && len(geometry.Endpoints) != 0
	layout := parameters.GetGeometry()
	if current && layout != nil && layout.NominalWraps != nil {
		observed, reason := deriveTarPositionMap(parameters, profile, geometry, boundary)
		if observed == nil {
			return nil, nil, reason
		}
		if err := library.ValidateTapePositionMap(stored, observed); err != nil {
			return nil, nil, err.Error()
		}
		model, reason := compileTarPositionMap(parameters, profile, observed,
			func() bool { return valid() && geometry.Valid() })
		return model, observed, reason
	}

	// A saved map supplies no lifetime of its own and cannot change this read attempt's immutable owner.
	value := stored.TapePositionMap
	if value == nil {
		return nil, nil, "No current or retained Tape position map is available"
	}
	// Retained maps declare their own endpoint interpretation; boundary describes only this live report protocol.
	if err := library.ValidateTapePositionMap(stored, value); err != nil {
		return nil, nil, err.Error()
	}
	modelValid := valid
	if current {
		// Missing drive layout cannot discard a self-described map or override contradictory current coverage.
		if reason := tarPositionMapReportReason(value, geometry, boundary); reason != "" {
			return nil, nil, reason
		}
		modelValid = func() bool { return valid() && geometry.Valid() }
	}
	model, reason := compileTarPositionMap(parameters, profile, value, modelValid)
	return model, nil, reason
}

func tarPositionMapReportReason(value *entity.TapePositionMap, geometry *tape.WrapMap,
	boundary mediapkg.ReadEndpointConvention,
) string {
	// Compare coverage only; the validated retained map continues to own its orientation and coordinate scale.
	if boundary != mediapkg.ReadEndpointExclusive && boundary != mediapkg.ReadEndpointInclusive {
		return "Reported wrap endpoint interpretation is not established"
	}
	if len(value.Wraps) > len(geometry.Endpoints) ||
		value.Partitions[0].Eod != nil && *value.Partitions[0].Eod > geometry.Endpoints[len(geometry.Endpoints)-1].Object {
		return "Current reported EOD does not cover the retained Tape position map"
	}
	low := uint64(0)
	for index, endpoint := range geometry.Endpoints {
		if endpoint.Partition != 0 || endpoint.Wrap != uint32(index) {
			return "Linear interpolation requires contiguous partition-zero wraps beginning at zero"
		}
		last := index == len(geometry.Endpoints)-1
		high := endpoint.Object
		if !last && boundary == mediapkg.ReadEndpointInclusive {
			if high == math.MaxUint64 {
				return "A reported wrap endpoint exceeds the address domain"
			}
			high++
		}
		if high <= low {
			return "Reported wrap boundaries are empty or reversed"
		}
		low = high
		if index >= len(value.Wraps) {
			continue
		}

		// Full boundaries must agree exactly; an earlier partial EOD may remain a bounded prefix of this wrap.
		wrap := value.Wraps[index]
		if wrap.Partition != 0 || wrap.Wrap != uint32(index) {
			return "Retained Tape position coverage does not match reported wrap traversal"
		}
		if wrap.Endpoint == nil {
			continue
		}
		retained := *wrap.Endpoint
		if wrap.Full && value.EndpointConvention == entity.TapePositionEndpointConvention_TAPE_POSITION_ENDPOINT_CONVENTION_INCLUSIVE {
			retained++ // The retained map validator has already rejected inclusive overflow.
		}
		if retained > high || wrap.Full && (last || retained != high) {
			return "Current reported wrap boundaries contradict the retained Tape position map"
		}
	}
	return ""
}

func deriveTarPositionMap(parameters *entity.TapeReadParameters, profile *entity.TarMediaProfile,
	geometry *tape.WrapMap, boundary mediapkg.ReadEndpointConvention,
) (*entity.TapePositionMap, string) {
	// Assigning bands requires the explicit four-band layout rather than the number of populated wraps.
	layout := parameters.GetGeometry()
	if profile == nil || layout == nil || layout.NominalWraps == nil || layout.GetNominalWraps() < 4 ||
		layout.GetNominalWraps()%4 != 0 {
		return nil, "The four-band interpolation layout requires an explicit nominal wrap count"
	}
	if geometry == nil || !geometry.Valid() || !geometry.AllWraps || len(geometry.Endpoints) < 2 ||
		uint64(len(geometry.Endpoints)) > uint64(layout.GetNominalWraps()) {
		return nil, "A current complete reported-wrap map with a full interval is unavailable"
	}
	if boundary != mediapkg.ReadEndpointExclusive && boundary != mediapkg.ReadEndpointInclusive {
		return nil, "Reported wrap endpoint interpretation is not established"
	}

	// Reported logical addresses remain exact; the layout hypothesis supplies only orientation and bands.
	wraps := make([]*entity.TapePositionWrap, len(geometry.Endpoints))
	lengths := make([]uint64, 0, len(wraps)-1)
	low := uint64(0)
	for index, endpoint := range geometry.Endpoints {
		if endpoint.Partition != 0 || endpoint.Wrap != uint32(index) {
			return nil, "Linear interpolation requires contiguous partition-zero wraps beginning at zero"
		}
		last := index == len(wraps)-1
		high := endpoint.Object
		if !last && boundary == mediapkg.ReadEndpointInclusive {
			if high == math.MaxUint64 {
				return nil, "A reported wrap endpoint exceeds the address domain"
			}
			high++
		}
		if high <= low {
			return nil, "Reported wrap boundaries are empty or reversed"
		}
		band, direction := tapeWrapLayout(endpoint.Wrap, layout.GetNominalWraps())
		wraps[index] = &entity.TapePositionWrap{Partition: endpoint.Partition, Wrap: endpoint.Wrap,
			Band: band, Direction: direction,
			Full: !last, Endpoint: proto.Uint64(endpoint.Object), EndpointUncertaintyObjects: proto.Uint64(0)}
		if !last {
			lengths = append(lengths, high-low)
			low = high
		}
	}

	// The final interpolation denominator never extends actual EOD or becomes a synthetic address.
	sort.Slice(lengths, func(i, j int) bool { return lengths[i] < lengths[j] })
	median := lengths[len(lengths)/2]
	if len(lengths)%2 == 0 {
		before := lengths[len(lengths)/2-1]
		median = before + (median-before)/2
	}
	eod := geometry.Endpoints[len(wraps)-1].Object
	wraps[len(wraps)-1].EstimatedSpanObjects = proto.Uint64(max(eod-low, median))
	observedAtNS, err := dataformat.Nanoseconds(geometry.ObservedAt)
	if err != nil {
		return nil, err.Error()
	}
	value := &entity.TapePositionMap{InitializationId: profile.InitializationId,
		Partitions: []*entity.TapePositionPartition{{Eod: proto.Uint64(eod)}}, Wraps: wraps,
		EndpointConvention: entity.TapePositionEndpointConvention(boundary), Source: geometry.Source,
		ObservedAtNs: observedAtNS}
	if layout.MinLpos != nil {
		value.MinLpos = proto.Uint64(*layout.MinLpos)
	}
	if layout.MaxLpos != nil {
		value.MaxLpos = proto.Uint64(*layout.MaxLpos)
	}
	return value, ""
}

// tapeWrapLayout derives a wrap's band and direction from the generation's nominal wrap count:
// four equal bands, and even wraps written forward. Callers ensure at least four nominal wraps.
func tapeWrapLayout(wrap, nominalWraps uint32) (uint32, entity.TapePositionDirection) {
	if wrap%2 != 0 {
		return wrap / (nominalWraps / 4), entity.TapePositionDirection_TAPE_POSITION_DIRECTION_REVERSE
	}
	return wrap / (nominalWraps / 4), entity.TapePositionDirection_TAPE_POSITION_DIRECTION_FORWARD
}
