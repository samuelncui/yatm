package executor

import (
	"context"
	"fmt"
	"runtime"
	"strconv"

	"github.com/google/uuid"
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

// readLTFSReadFacts reads the mounted LTFS facts and applies the LTFS partition convention.
// This function never opens the raw device while LTFS owns it.
func readLTFSReadFacts(ctx context.Context, read func(string, int) ([]byte, error)) (*ltfsReadFacts, error) {
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

	// Every known formatter makes index partition a physical 0 and data partition b physical 1.
	// A different map keeps the read LOGICAL; a wrong convention can only make extents unknown.
	if values["partitionMap"] != "I:a,D:b" {
		return nil, fmt.Errorf("LTFS partition map %q does not follow the a=0, b=1 convention", values["partitionMap"])
	}
	return &ltfsReadFacts{blockBytes: uint32(blockBytes), uuid: identity.String(), generation: generation,
		partitions: map[string]uint32{"a": 0, "b": 1}, partitionSource: "LTFS convention a=0, b=1",
		indexSymbol: "a", dataSymbol: "b"}, nil
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
