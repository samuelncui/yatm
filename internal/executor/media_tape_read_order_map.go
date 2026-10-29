package executor

import (
	"math"
	"sort"
	"strconv"
	"strings"

	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/internal/library"
	"github.com/samuelncui/yatm/internal/media/tape"
	"google.golang.org/protobuf/proto"
)

func ltfsReadPositionMap(parameters *entity.TapeReadParameters, stored *library.Media,
	facts *ltfsReadFacts, snapshot *ltfsReadSnapshot,
) (*entity.TapePositionMap, bool, string) {
	// A retained map belongs to the exact mounted initialization and captured Index generation.
	if len(snapshot.endpoints) == 0 {
		value := stored.TapePositionMap
		if strings.HasPrefix(value.GetSource(), "A3/1F/45; ") &&
			value.GetEndpointConvention() == entity.TapePositionEndpointConvention_TAPE_POSITION_ENDPOINT_CONVENTION_INCLUSIVE &&
			tape.REOWPEndpointConvention(snapshot.report) == entity.TapePositionEndpointConvention_TAPE_POSITION_ENDPOINT_CONVENTION_EXCLUSIVE {
			return nil, false, "Retained HP LTO-5 REOWP geometry needs count-based endpoint refresh"
		}
		if value == nil || value.LtfsUuid != facts.uuid || value.GetLtfsIndexGeneration() != facts.generation {
			return nil, false, "No current or matching retained LTFS position map is available"
		}
		if err := library.ValidateTapePositionMap(stored, value); err != nil {
			return nil, false, err.Error()
		}
		return value, false, ""
	}

	// Unknown endpoint interpretation cannot be repaired by assuming an extra logical object.
	if snapshot.boundary != entity.TapePositionEndpointConvention_TAPE_POSITION_ENDPOINT_CONVENTION_INCLUSIVE &&
		snapshot.boundary != entity.TapePositionEndpointConvention_TAPE_POSITION_ENDPOINT_CONVENTION_EXCLUSIVE {
		return nil, false, "LTFS reported endpoint interpretation is not established"
	}

	// The reported physical wrap numbers supply each partition's traversal under the same four-band model.
	layout := parameters.GetGeometry()
	if layout.GetNominalWraps() < 4 || layout.GetNominalWraps()%4 != 0 {
		return nil, false, "LTFS linear interpolation requires an explicit four-band nominal wrap count"
	}
	groups := make(map[uint32][]tape.WrapEndpoint, 2)
	for _, endpoint := range snapshot.endpoints {
		if endpoint.Partition > 1 || endpoint.Wrap >= layout.GetNominalWraps() {
			return nil, false, "LTFS reported wraps fall outside the observed two-partition layout"
		}
		groups[endpoint.Partition] = append(groups[endpoint.Partition], endpoint)
	}
	value := &entity.TapePositionMap{LtfsUuid: facts.uuid, LtfsIndexGeneration: proto.Uint64(facts.generation),
		EndpointConvention: snapshot.boundary,
		Source:             snapshot.source + "; " + facts.partitionSource, ObservedAtNs: snapshot.observedAt.UnixNano(),
		MinLpos: layout.MinLpos, MaxLpos: layout.MaxLpos}
	for _, partition := range []uint32{0, 1} {
		endpoints := groups[partition]
		if len(endpoints) == 0 {
			continue
		}
		if endpoints[0].Wrap%2 != 0 {
			return nil, false, "LTFS partition traversal does not begin on a forward wrap"
		}
		low := uint64(0)
		lengths := make([]uint64, 0, len(endpoints)-1)
		first := len(value.Wraps)
		for index, endpoint := range endpoints {
			if index != 0 && endpoint.Wrap != endpoints[index-1].Wrap+1 {
				return nil, false, "LTFS reported partition traversal has missing or unordered wraps"
			}
			last, high := index == len(endpoints)-1, endpoint.Object
			if !last && snapshot.boundary == entity.TapePositionEndpointConvention_TAPE_POSITION_ENDPOINT_CONVENTION_INCLUSIVE {
				if high == math.MaxUint64 {
					return nil, false, "LTFS reported endpoint exceeds the logical address domain"
				}
				high++
			}
			if high <= low {
				return nil, false, "LTFS reported boundaries are empty or reversed"
			}
			band, direction := tapeWrapLayout(endpoint.Wrap, layout.GetNominalWraps())
			value.Wraps = append(value.Wraps, &entity.TapePositionWrap{Partition: partition, Wrap: endpoint.Wrap,
				Band: band, Direction: direction,
				Full: !last, Endpoint: proto.Uint64(endpoint.Object), EndpointUncertaintyObjects: proto.Uint64(0)})
			if !last {
				lengths = append(lengths, high-low)
				low = high
			}
		}

		// A partition with no full reported wrap has no interpolation scale; retain that explicit gap.
		eod := endpoints[len(endpoints)-1].Object
		value.Partitions = append(value.Partitions, &entity.TapePositionPartition{Partition: partition, Eod: proto.Uint64(eod)})
		if len(lengths) == 0 {
			continue
		}
		sort.Slice(lengths, func(i, j int) bool { return lengths[i] < lengths[j] })
		median := lengths[len(lengths)/2]
		if len(lengths)%2 == 0 {
			before := lengths[len(lengths)/2-1]
			median = before + (median-before)/2
		}
		value.Wraps[first+len(endpoints)-1].EstimatedSpanObjects = proto.Uint64(max(eod-low, median))
	}
	if err := library.ValidateTapePositionMap(stored, value); err != nil {
		return nil, false, err.Error()
	}
	return value, true, ""
}

func ltfsReadDataEnd(facts *ltfsReadFacts, value *entity.TapePositionMap,
	read func(string, int) ([]byte, error),
) (*entity.TapeBytePosition, string) {
	// Normal closed LTFS starts from its Index-partition Index; dirty completion appends at DP EOD.
	// indexLocation is a stored Index pointer, never the mounted head or a dirty-state probe.
	location, err := read("ltfs.indexLocation", 128)
	if err != nil {
		return nil, "LTFS current Index location is unavailable"
	}
	parts := strings.Split(string(location), ":")
	if len(parts) != 2 || len(facts.indexSymbol) != 1 || parts[0] != facts.indexSymbol {
		return nil, "LTFS current Index is not on the Index partition"
	}
	if block, err := strconv.ParseUint(parts[1], 10, 64); err != nil || block == 0 {
		return nil, "LTFS current Index location is invalid"
	}
	partition, known := facts.partitions[facts.dataSymbol]
	if !known || value.GetLtfsUuid() != facts.uuid || value.GetLtfsIndexGeneration() != facts.generation {
		return nil, "LTFS data partition end is not bound to the mounted Index"
	}
	for _, end := range value.Partitions {
		if end.Partition == partition && end.Eod != nil && end.GetEod() != 0 {
			return &entity.TapeBytePosition{Partition: partition, LogicalObject: end.GetEod()}, ""
		}
	}
	return nil, "LTFS data partition EOD is unavailable"
}
