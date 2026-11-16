package media

import (
	"encoding/binary"
	"math"

	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/internal/media/readplan"
)

// ReadIndex exposes normalized coordinates in the wrap and band/direction groups; the big-endian bits
// of a nonnegative float sort like its value. Candidate neighbourhoods are heuristic; complete
// directed costs still decide route adoption.
func (m *ReadCostModel) ReadIndex(position *entity.TapeBytePosition) (readplan.Index, bool) {
	if !m.Valid() {
		return readplan.Index{}, false
	}
	interval, numerator, known := m.position(position)
	if !known {
		return readplan.Index{}, false
	}
	key := make([]byte, 8)
	binary.BigEndian.PutUint64(key, math.Float64bits(readFraction(numerator, interval.span)))
	zone := readNormalizedZone(numerator, interval.span)
	groups := [][]byte{readIntervalGroup(position.Partition, interval.index, zone),
		readAggregateGroup(position.Partition, interval.band, interval.direction, zone)}
	return readplan.Index{Groups: groups, Position: key}, true
}

func readIntervalGroup(partition, index uint32, zone byte) []byte {
	group := make([]byte, 10)
	binary.BigEndian.PutUint32(group[1:5], partition)
	binary.BigEndian.PutUint32(group[5:9], index)
	group[9] = zone
	return group
}

func readAggregateGroup(partition, band uint32, direction ReadWrapDirection, zone byte) []byte {
	group := make([]byte, 11)
	group[0], group[9], group[10] = 1, byte(direction), zone
	binary.BigEndian.PutUint32(group[1:5], partition)
	binary.BigEndian.PutUint32(group[5:9], band)
	return group
}
