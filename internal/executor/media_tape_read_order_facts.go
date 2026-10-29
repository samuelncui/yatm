package executor

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"maps"
	"runtime"
	"strconv"
	"strings"

	"github.com/google/uuid"
	mediapkg "github.com/samuelncui/yatm/internal/media"
	"golang.org/x/sys/unix"
)

type ltfsReadFacts struct {
	blockBytes      uint32
	compression     bool
	uuid            string
	generation      uint64
	partitions      map[string]uint32
	partitionSource string
	indexSymbol     string
	dataSymbol      string
}

// readLTFSReadFacts validates mounted facts against pre-mount Labels, or uses the mounted MAM selectors.
// This function never opens the raw device while LTFS owns it.
func readLTFSReadFacts(ctx context.Context, labels *mediapkg.LTFSLabelFacts,
	read func(string, int) ([]byte, error),
) (*ltfsReadFacts, error) {
	// These values come from the volume label/index; the current drive DCE does not establish history.
	values := make(map[string]string, 5)
	for _, name := range []string{"volumeBlocksize", "volumeCompression", "volumeUUID", "indexGeneration", "partitionMap"} {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		value, err := read("ltfs."+name, 1024)
		if err != nil {
			return nil, fmt.Errorf("read LTFS %s failed, %w", name, err)
		}
		values[name] = string(value)
	}
	blockBytes, err := strconv.ParseUint(values["volumeBlocksize"], 10, 32)
	if err != nil || blockBytes == 0 {
		return nil, fmt.Errorf("LTFS volume block size is unavailable")
	}
	if values["volumeCompression"] != "false" {
		return nil, fmt.Errorf("LTFS volume-label compression is enabled or unknown")
	}
	identity, err := uuid.Parse(values["volumeUUID"])
	if err != nil {
		return nil, fmt.Errorf("LTFS volume UUID is unavailable, %w", err)
	}
	generation, err := strconv.ParseUint(values["indexGeneration"], 10, 64)
	if err != nil || generation == 0 {
		return nil, fmt.Errorf("LTFS index generation is unavailable")
	}

	// A qualified SG pair supplies physical addresses; mounted facts bind it to this volume and Index.
	if labels != nil {
		if labels.VolumeUUID != identity.String() || labels.BlockBytes != uint32(blockBytes) || labels.Compression ||
			values["partitionMap"] != "I:"+labels.IndexPartition+",D:"+labels.DataPartition {
			return nil, fmt.Errorf("LTFS mounted facts conflict with the pre-mount Labels")
		}
		return &ltfsReadFacts{blockBytes: uint32(blockBytes), uuid: identity.String(), generation: generation,
			partitions: maps.Clone(labels.Partitions), partitionSource: "pre-mount SG LTFS Labels",
			indexSymbol: labels.IndexPartition, dataSymbol: labels.DataPartition}, nil
	}

	// IBM virtual MAM selectors use the mounted label's real symbol-to-number conversion.
	// Whole, bounded snapshots must match uniquely; ambiguous or changing observations stay unknown.
	snapshots := make(map[string][]byte, 4)
	for _, selector := range []string{"IP", "DP", "00", "01"} {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		value, err := read("ltfs.vendor.IBM.mediaMAM."+selector, 65536)
		if err != nil {
			return nil, fmt.Errorf("read LTFS physical partition mapping failed, selector=%q, %w", selector, err)
		}
		if _, valid := ltfsPartitionMAMCapacity(value); !valid {
			return nil, fmt.Errorf("LTFS partition MAM is incomplete, selector=%q", selector)
		}
		snapshots[selector] = value
	}
	partitions, err := ltfsPhysicalPartitions(values["partitionMap"], snapshots)
	if err != nil {
		return nil, err
	}
	return &ltfsReadFacts{blockBytes: uint32(blockBytes), uuid: identity.String(), generation: generation,
		partitions: partitions, partitionSource: "mounted LTFS MAM selectors",
		indexSymbol: values["partitionMap"][2:3], dataSymbol: values["partitionMap"][6:7]}, nil
}

func mountedLTFSAttribute(root, name string, limit int) ([]byte, error) {
	// Linux namespaces its virtual attributes; Darwin exposes their unprefixed LTFS names.
	if runtime.GOOS != "darwin" {
		name = "user." + name
	}
	value := make([]byte, limit)
	n, err := unix.Getxattr(root, name, value)
	if err != nil {
		return nil, err
	}
	if n <= 0 || n > len(value) {
		return nil, fmt.Errorf("LTFS attribute exceeds its observation bound, name=%q", name)
	}
	return value[:n], nil
}

func ltfsPartitionMAMCapacity(value []byte) (uint64, bool) {
	// READ ATTRIBUTE framing and partition maximum capacity establish a complete partition observation.
	if len(value) < 4 || uint64(binary.BigEndian.Uint32(value[:4]))+4 != uint64(len(value)) {
		return 0, false
	}
	capacity := uint64(0)
	for offset := 4; offset < len(value); {
		if len(value)-offset < 5 {
			return 0, false
		}
		id := binary.BigEndian.Uint16(value[offset : offset+2])
		length := int(binary.BigEndian.Uint16(value[offset+3 : offset+5]))
		if length > len(value)-offset-5 {
			return 0, false
		}
		if id == 0x0001 {
			if capacity != 0 || length != 8 || binary.BigEndian.Uint64(value[offset+5:offset+13]) == 0 {
				return 0, false
			}
			capacity = binary.BigEndian.Uint64(value[offset+5 : offset+13])
		}
		offset += 5 + length
	}
	return capacity, capacity != 0
}

func ltfsPhysicalPartitions(roles string, snapshots map[string][]byte) (map[string]uint32, error) {
	// Role letters are arbitrary label identifiers; they never imply physical partition zero or one.
	parts := strings.Split(roles, ",")
	if len(parts) != 2 || len(parts[0]) != 3 || len(parts[1]) != 3 ||
		!strings.HasPrefix(parts[0], "I:") || !strings.HasPrefix(parts[1], "D:") || parts[0][2] == parts[1][2] {
		return nil, fmt.Errorf("LTFS partition roles are unavailable")
	}
	// Cartridge-wide volatile fields cannot identify a partition. Require a stable partition-specific difference.
	first, firstValid := ltfsPartitionMAMCapacity(snapshots["00"])
	second, secondValid := ltfsPartitionMAMCapacity(snapshots["01"])
	if !firstValid || !secondValid || first == second {
		return nil, fmt.Errorf("LTFS physical partition capacities are indistinguishable or unavailable")
	}
	result := make(map[string]uint32, 2)
	for index, role := range []string{"IP", "DP"} {
		switch {
		case bytes.Equal(snapshots[role], snapshots["00"]):
			result[parts[index][2:]] = 0
		case bytes.Equal(snapshots[role], snapshots["01"]):
			result[parts[index][2:]] = 1
		default:
			return nil, fmt.Errorf("LTFS symbolic and physical partition observations do not match")
		}
	}
	if result[parts[0][2:]] == result[parts[1][2:]] {
		return nil, fmt.Errorf("LTFS partition observations are not one-to-one")
	}
	return result, nil
}
