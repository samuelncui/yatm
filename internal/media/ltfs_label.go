package media

import (
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/google/uuid"
)

const ltfsLabelMaximumBytes = 512 << 10

// LTFSLabelFacts contains common volume facts and the physically observed partition mapping.
type LTFSLabelFacts struct {
	VolumeUUID     string
	BlockBytes     uint32
	Compression    bool
	IndexPartition string
	DataPartition  string
	Partitions     map[string]uint32
}

type ltfsLabel struct {
	version, creator, formatTime string
	volumeUUID, partition        string
	index, data                  string
	blockBytes                   uint32
	compression                  bool
}

// ParseLTFSLabels validates complete XML records read from physical partitions zero and one.
// Their array positions, never their letters or roles, establish the numeric partition mapping.
func ParseLTFSLabels(labels [2][]byte) (*LTFSLabelFacts, error) {
	// Decode both records before publishing any shared facts or physical addresses.
	var parsed [2]ltfsLabel
	for partition, data := range labels {
		value, err := parseLTFSLabel(data)
		if err != nil {
			return nil, fmt.Errorf("parse LTFS label on physical partition %d failed, %w", partition, err)
		}
		parsed[partition] = value
	}

	// SNIA LTFS 2.4 §8.1 permits only the local partition to differ between the two Labels.
	first, second := parsed[0], parsed[1]
	if first.partition == second.partition {
		return nil, fmt.Errorf("LTFS labels repeat local partition %q", first.partition)
	}
	first.partition, second.partition = "", ""
	if first != second {
		return nil, fmt.Errorf("LTFS labels disagree on common volume facts")
	}
	return &LTFSLabelFacts{
		VolumeUUID: first.volumeUUID, BlockBytes: first.blockBytes, Compression: first.compression,
		IndexPartition: first.index, DataPartition: first.data,
		Partitions: map[string]uint32{parsed[0].partition: 0, parsed[1].partition: 1},
	}, nil
}

func parseLTFSLabel(data []byte) (ltfsLabel, error) {
	// The cap limits optional parsing work; it is not the LTFS format's maximum Label size.
	if len(data) == 0 || len(data) > ltfsLabelMaximumBytes {
		return ltfsLabel{}, fmt.Errorf("LTFS label record size %d is outside the supported bound", len(data))
	}
	fields, version, err := decodeLTFSLabel(data)
	if err != nil {
		return ltfsLabel{}, err
	}
	for _, name := range []string{"creator", "formattime", "volumeuuid", "location/partition",
		"partitions/index", "partitions/data", "blocksize", "compression"} {
		if fields[name] == "" {
			return ltfsLabel{}, fmt.Errorf("LTFS label field %q is missing or empty", name)
		}
	}

	// Reference LTFS 4c0e6faa src/libltfs/ltfs.h admits Label versions 1.0.0 through 2.99.99.
	parts := strings.Split(version, ".")
	if len(parts) != 3 {
		return ltfsLabel{}, fmt.Errorf("LTFS label version %q is unsupported", version)
	}
	var numbers [3]uint64
	for i, part := range parts {
		value, err := strconv.ParseUint(part, 10, 8)
		if err != nil || value > 99 {
			return ltfsLabel{}, fmt.Errorf("LTFS label version %q is unsupported", version)
		}
		numbers[i] = value
	}
	if numbers[0] < 1 || numbers[0] > 2 {
		return ltfsLabel{}, fmt.Errorf("LTFS label version %q is unsupported", version)
	}

	// Preserve explicit identity and policy; permissive UUID spellings or absent false are not evidence.
	identifier, err := uuid.Parse(fields["volumeuuid"])
	if err != nil || len(fields["volumeuuid"]) != 36 {
		return ltfsLabel{}, fmt.Errorf("LTFS label volume UUID %q is invalid", fields["volumeuuid"])
	}
	blockBytes, err := strconv.ParseUint(fields["blocksize"], 10, 32)
	if err != nil || blockBytes == 0 {
		return ltfsLabel{}, fmt.Errorf("LTFS label block size %q is invalid", fields["blocksize"])
	}
	var compression bool
	switch fields["compression"] {
	case "true", "1":
		compression = true
	case "false", "0":
	default:
		return ltfsLabel{}, fmt.Errorf("LTFS label compression %q is invalid", fields["compression"])
	}

	// Each Label must name one of the same two distinct roles; physical order remains independent.
	partition, index, dataPartition := fields["location/partition"], fields["partitions/index"], fields["partitions/data"]
	for _, value := range []string{partition, index, dataPartition} {
		if len(value) != 1 || value[0] < 'a' || value[0] > 'z' {
			return ltfsLabel{}, fmt.Errorf("LTFS label partition %q is invalid", value)
		}
	}
	if index == dataPartition || partition != index && partition != dataPartition {
		return ltfsLabel{}, fmt.Errorf("LTFS label partition roles are ambiguous")
	}
	return ltfsLabel{
		version: fmt.Sprintf("%d.%d.%d", numbers[0], numbers[1], numbers[2]),
		creator: fields["creator"], formatTime: fields["formattime"], volumeUUID: identifier.String(),
		partition: partition, index: index, data: dataPartition, blockBytes: uint32(blockBytes), compression: compression,
	}, nil
}

func decodeLTFSLabel(data []byte) (map[string]string, string, error) {
	// Track known paths so duplicate fields and nested scalar content cannot replace authoritative facts.
	fields := map[string]string{"creator": "", "formattime": "", "volumeuuid": "", "location/partition": "",
		"partitions/index": "", "partitions/data": "", "blocksize": "", "compression": ""}
	seen := make(map[string]bool, len(fields)+3)
	decoder := xml.NewDecoder(bytes.NewReader(bytes.TrimPrefix(data, []byte{0xef, 0xbb, 0xbf})))
	path, version := "", ""
	var value strings.Builder
	for {
		token, err := decoder.Token()
		if errors.Is(err, io.EOF) {
			if !seen["ltfslabel"] || path != "" {
				return nil, "", fmt.Errorf("LTFS label root is missing or incomplete")
			}
			return fields, version, nil
		}
		if err != nil {
			return nil, "", fmt.Errorf("decode LTFS label XML failed, %w", err)
		}

		// Unknown child elements follow the reference reader's extension handling, without supplying facts.
		switch element := token.(type) {
		case xml.StartElement:
			key := strings.TrimPrefix(path+"/"+element.Name.Local, "ltfslabel/")
			if path == "" {
				if element.Name != (xml.Name{Local: "ltfslabel"}) || seen["ltfslabel"] {
					return nil, "", fmt.Errorf("LTFS label requires one unqualified ltfslabel root")
				}
				for _, attribute := range element.Attr {
					if attribute.Name == (xml.Name{Local: "version"}) {
						if seen["@version"] {
							return nil, "", fmt.Errorf("LTFS label has multiple versions")
						}
						version, seen["@version"] = attribute.Value, true
					}
				}
				path, seen["ltfslabel"] = "ltfslabel", true
				continue
			}
			if _, scalar := fields[strings.TrimPrefix(path, "ltfslabel/")]; scalar {
				return nil, "", fmt.Errorf("LTFS label scalar field contains nested elements")
			}
			_, scalar := fields[key]
			if element.Name.Space != "" || !scalar && key != "location" && key != "partitions" {
				if err := decoder.Skip(); err != nil {
					return nil, "", fmt.Errorf("skip LTFS label extension failed, %w", err)
				}
				continue
			}
			if seen[key] {
				return nil, "", fmt.Errorf("LTFS label field %q is repeated", key)
			}
			seen[key], path = true, "ltfslabel/"+key
			value.Reset()
		case xml.EndElement:
			if key := strings.TrimPrefix(path, "ltfslabel/"); seen[key] {
				if _, scalar := fields[key]; scalar {
					fields[key] = value.String()
				}
			}
			if index := strings.LastIndexByte(path, '/'); index >= 0 {
				path = path[:index]
			} else {
				path = ""
			}
		case xml.CharData:
			if _, scalar := fields[strings.TrimPrefix(path, "ltfslabel/")]; scalar {
				value.Write(element)
			} else if len(bytes.Trim(element, " \t\r\n")) != 0 {
				return nil, "", fmt.Errorf("LTFS label has data outside a scalar field")
			}
		}
	}
}
