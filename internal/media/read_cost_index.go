package media

import (
	"encoding/binary"
	"math/bits"

	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/internal/media/readplan"
)

// ReadIndex exposes exact integer/normalized coordinates in the wrap and band/direction groups.
// Candidate neighbourhoods are heuristic; complete directed costs still decide route adoption.
func (m *ReadCostModel) ReadIndex(position *entity.TapeBytePosition) (readplan.Index, bool) {
	if !m.Valid() {
		return readplan.Index{}, false
	}
	interval, numerator, known := m.position(position)
	if !known {
		return readplan.Index{}, false
	}
	key, zone := m.readCoordinateKey(interval, numerator)
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

func readLPOSKey(lpos uint64) []byte {
	key := make([]byte, 8)
	binary.BigEndian.PutUint64(key, lpos)
	return key
}

func (m *ReadCostModel) readCoordinateKey(interval readModelInterval, numerator uint64) ([]byte, byte) {
	if !m.normalized {
		lpos := readLPOS(m.Longitudinal(), numerator, interval.span)
		return readLPOSKey(lpos), readLPOSZone(m.Longitudinal(), lpos)
	}
	return readFractionKey(numerator, interval.span), readNormalizedZone(numerator, interval.span)
}

func readFractionKey(numerator, denominator uint64) []byte {
	// Distinct uint64-denominator fractions differ by more than 2^-128. One extra bit represents one.
	key := make([]byte, 17)
	if numerator == denominator {
		key[0] = 1
		return key
	}
	high, remainder := bits.Div64(numerator, 0, denominator)
	low, _ := bits.Div64(remainder, 0, denominator)
	binary.BigEndian.PutUint64(key[1:9], high)
	binary.BigEndian.PutUint64(key[9:], low)
	return key
}
