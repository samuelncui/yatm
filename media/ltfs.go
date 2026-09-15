package media

import (
	"context"
	"encoding/binary"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"math"
	"net/url"
	"path"
	"strings"

	"github.com/samuelncui/yatm/entity"
)

type ltfsXMLName struct {
	Value          string `xml:",chardata"`
	PercentEncoded bool   `xml:"percentencoded,attr"`
}

type ltfsXMLExtent struct {
	Partition  string `xml:"partition"`
	StartBlock uint64 `xml:"startblock"`
	ByteOffset uint64 `xml:"byteoffset"`
	ByteCount  uint64 `xml:"bytecount"`
	FileOffset uint64 `xml:"fileoffset"`
}

type ltfsXMLFile struct {
	Name       ltfsXMLName `xml:"name"`
	Length     uint64      `xml:"length"`
	ExtentInfo struct {
		Extents []ltfsXMLExtent `xml:"extent"`
	} `xml:"extentinfo"`
}

// LTFSIndexEntry is one physical file decoded from a captured LTFS index.
type LTFSIndexEntry struct {
	Path    string
	Size    int64
	Storage *entity.StoragePosition
}

// ParseLTFSIndex streams validated physical files without retaining the manifest.
func ParseLTFSIndex(ctx context.Context, reader io.Reader, yield func(*LTFSIndexEntry) error) error {
	if reader == nil {
		return fmt.Errorf("parse LTFS index failed, reader is nil")
	}
	if yield == nil {
		return fmt.Errorf("parse LTFS index failed, yield is nil")
	}

	decoder := xml.NewDecoder(reader)
	directories := make([]string, 0, 8)
	rootSeen := false
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		token, err := decoder.Token()
		if errors.Is(err, io.EOF) {
			if !rootSeen {
				return fmt.Errorf("parse LTFS index failed, root directory is missing")
			}
			return nil
		}
		if err != nil {
			return fmt.Errorf("parse LTFS index failed, %w", err)
		}

		switch value := token.(type) {
		case xml.StartElement:
			switch value.Name.Local {
			case "directory":
				directories = append(directories, "")
			case "name":
				if len(directories) == 0 {
					continue
				}
				if directories[len(directories)-1] != "" {
					return fmt.Errorf("parse LTFS index failed, directory has multiple names")
				}
				var name ltfsXMLName
				if err := decoder.DecodeElement(&name, &value); err != nil {
					return fmt.Errorf("decode LTFS directory name failed, %w", err)
				}
				component, err := decodeLTFSName(name)
				if err != nil {
					return fmt.Errorf("decode LTFS directory name failed, %w", err)
				}
				directories[len(directories)-1] = component
				if len(directories) == 1 {
					if rootSeen {
						return fmt.Errorf("parse LTFS index failed, multiple root directories")
					}
					rootSeen = true
				}
			case "file":
				entry, err := decodeLTFSFile(decoder, value, directories)
				if err != nil {
					return err
				}
				if err := yield(entry); err != nil {
					return err
				}
			}
		case xml.EndElement:
			if value.Name.Local != "directory" {
				continue
			}
			if len(directories) == 0 {
				return fmt.Errorf("parse LTFS index failed, unexpected directory end")
			}
			directories = directories[:len(directories)-1]
		}
	}
}

func decodeLTFSFile(
	decoder *xml.Decoder,
	start xml.StartElement,
	directories []string,
) (*LTFSIndexEntry, error) {
	if len(directories) == 0 {
		return nil, fmt.Errorf("decode LTFS file failed, root directory is missing")
	}
	for _, directory := range directories {
		if directory == "" {
			return nil, fmt.Errorf("decode LTFS file failed, directory name is missing")
		}
	}
	var file ltfsXMLFile
	if err := decoder.DecodeElement(&file, &start); err != nil {
		return nil, fmt.Errorf("decode LTFS file failed, %w", err)
	}
	name, err := decodeLTFSName(file.Name)
	if err != nil {
		return nil, fmt.Errorf("decode LTFS file name failed, %w", err)
	}
	parts := append([]string(nil), directories[1:]...)
	filePath := path.Join(append(parts, name)...)
	if err := entity.ValidateRelativePath(filePath); err != nil {
		return nil, fmt.Errorf("decode LTFS file path failed, %w", err)
	}
	if file.Length > math.MaxInt64 {
		return nil, fmt.Errorf("decode LTFS file failed, path=%q length=%d exceeds int64", filePath, file.Length)
	}

	metadata := &entity.LTFSMetadata{Extents: make([]*entity.LTFSExtent, 0, len(file.ExtentInfo.Extents))}
	for _, extent := range file.ExtentInfo.Extents {
		if len(extent.Partition) != 1 {
			return nil, fmt.Errorf("decode LTFS extent failed, path=%q partition=%q is not one byte", filePath, extent.Partition)
		}
		metadata.Extents = append(metadata.Extents, &entity.LTFSExtent{
			Partition: extent.Partition, StartBlock: extent.StartBlock, ByteOffset: extent.ByteOffset,
			ByteCount: extent.ByteCount, FileOffset: extent.FileOffset,
		})
	}
	if file.Length > 0 && len(metadata.Extents) == 0 {
		return nil, fmt.Errorf("decode LTFS extent failed, path=%q extent is missing", filePath)
	}
	return &LTFSIndexEntry{
		Path: filePath, Size: int64(file.Length),
		Storage: &entity.StoragePosition{
			Order: makeLTFSStorageOrder(metadata.Extents), Metadata: metadata.Pack(),
		},
	}, nil
}

func decodeLTFSName(name ltfsXMLName) (string, error) {
	value := name.Value
	if name.PercentEncoded {
		decoded, err := url.PathUnescape(value)
		if err != nil {
			return "", err
		}
		value = decoded
	}
	if value == "" || value == "." || value == ".." || strings.ContainsAny(value, "/\\\x00") {
		return "", fmt.Errorf("invalid LTFS path component, value=%q", value)
	}
	return value, nil
}

func makeLTFSStorageOrder(extents []*entity.LTFSExtent) []byte {
	if len(extents) == 0 {
		return []byte{}
	}
	first := extents[0]
	for _, extent := range extents[1:] {
		if extent.FileOffset < first.FileOffset {
			first = extent
		}
	}
	order := make([]byte, 17)
	order[0] = first.Partition[0]
	binary.BigEndian.PutUint64(order[1:9], first.StartBlock)
	binary.BigEndian.PutUint64(order[9:17], first.ByteOffset)
	return order
}
