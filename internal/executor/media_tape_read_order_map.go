package executor

import (
	"sort"
	"strconv"
	"strings"

	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/internal/media/tape"
)

// ltfsWrapDeviation bounds how far a full wrap's object count may differ from the partition's median,
// as a fraction 1/ltfsWrapDeviation of that median. Interpolation places objects by count, and LTFS
// ends every file with a short block, so a wrap of many small files holds more objects and misplaces
// them. A deviation f misplaces an interior object by at most f of a span: 0.5% is 0.48 seconds at the
// built-in 96-second full-span seek, the held-out LTFS positioning error. Recorded HP LTO-5 maps
// deviate by at most 0.06%.
const ltfsWrapDeviation = 200

// ltfsUniformWraps reports whether every full-wrap object count lies within the deviation bound of
// their median; it sorts lengths.
func ltfsUniformWraps(lengths []uint64) bool {
	sort.Slice(lengths, func(i, j int) bool { return lengths[i] < lengths[j] })
	median := ltfsMedian(lengths)
	for _, length := range lengths {
		if max(length, median)-min(length, median) > median/ltfsWrapDeviation {
			return false
		}
	}
	return true
}

// ltfsMedian returns the median of sorted, nonempty lengths.
func ltfsMedian(lengths []uint64) uint64 {
	median := lengths[len(lengths)/2]
	if len(lengths)%2 == 0 {
		before := lengths[len(lengths)/2-1]
		median = before + (median-before)/2
	}
	return median
}

// ltfsReadPositionMap derives this Session's map from the current reported wraps only.
func ltfsReadPositionMap(
	parameters *entity.TapeReadParameters, facts *ltfsReadFacts, snapshot *ltfsReadSnapshot,
) (*tapePositionMap, string) {
	if len(snapshot.endpoints) == 0 {
		return nil, "No current LTFS position map is available"
	}

	// The reported physical wrap numbers supply each partition's traversal under the same four-band model.
	layout := parameters.GetGeometry()
	if layout.GetNominalWraps() < 4 || layout.GetNominalWraps()%4 != 0 {
		return nil, "LTFS linear interpolation requires an explicit four-band nominal wrap count"
	}
	groups := make(map[uint32][]tape.WrapEndpoint, 2)
	for _, endpoint := range snapshot.endpoints {
		if endpoint.Partition > 1 || endpoint.Wrap >= layout.GetNominalWraps() {
			return nil, "LTFS reported wraps fall outside the observed two-partition layout"
		}
		groups[endpoint.Partition] = append(groups[endpoint.Partition], endpoint)
	}
	value := &tapePositionMap{source: snapshot.source + "; " + facts.partitionSource, eod: make(map[uint32]uint64, 2)}
	for _, partition := range []uint32{0, 1} {
		endpoints := groups[partition]
		if len(endpoints) == 0 {
			continue
		}
		if endpoints[0].Wrap%2 != 0 {
			return nil, "LTFS partition traversal does not begin on a forward wrap"
		}
		low := uint64(0)
		for index, endpoint := range endpoints {
			if index != 0 && endpoint.Wrap != endpoints[index-1].Wrap+1 {
				return nil, "LTFS reported partition traversal has missing or unordered wraps"
			}
			if endpoint.Object <= low {
				return nil, "LTFS reported boundaries are empty or reversed"
			}
			low = endpoint.Object
		}

		// A partition with no full reported wrap has no interpolation scale; retain that explicit gap.
		wraps, lengths := tapePartitionWraps(layout.GetNominalWraps(), endpoints)
		if len(lengths) != 0 && !ltfsUniformWraps(lengths) {
			return nil, "LTFS full wraps hold too unequal object counts for interpolation"
		}
		value.wraps = append(value.wraps, wraps...)
		value.eod[partition] = low
	}
	return value, ""
}

func ltfsReadDataEnd(facts *ltfsReadFacts, value *tapePositionMap,
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
	if !known {
		return nil, "LTFS data partition end is not bound to the mounted Index"
	}
	if eod := value.eod[partition]; eod != 0 {
		return &entity.TapeBytePosition{Partition: partition, LogicalObject: eod}, ""
	}
	return nil, "LTFS data partition EOD is unavailable"
}
