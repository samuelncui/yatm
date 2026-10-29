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
	"strconv"
	"strings"

	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/internal/media/readplan"
)

// ltfsReadIndexBytes bounds each retained token, live directory name and structural-name pool.
const ltfsReadIndexBytes = 8 << 20

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
	return parseLTFSIndex(ctx, reader, "", 0, yield)
}

// ParseLTFSReadIndex binds optional read geometry to the currently mounted volume and Index.
func ParseLTFSReadIndex(ctx context.Context, reader io.Reader, uuid string, generation uint64,
	yield func(*LTFSIndexEntry) error,
) error {
	if uuid == "" || generation == 0 {
		return fmt.Errorf("current LTFS Index identity is unavailable")
	}
	return parseLTFSIndex(ctx, reader, uuid, generation, yield)
}

func parseLTFSIndex(ctx context.Context, reader io.Reader, uuid string, generation uint64,
	yield func(*LTFSIndexEntry) error,
) error {
	// The existing inventory parser is also used with an expected mounted identity for read ordering.
	if reader == nil {
		return fmt.Errorf("parse LTFS index failed, reader is nil")
	}
	if yield == nil {
		return fmt.Errorf("parse LTFS index failed, yield is nil")
	}

	// Bound planning metadata while it is read, before XML allocates a complete oversized token.
	var bounded *ltfsReadIndexReader
	if uuid != "" {
		bounded = &ltfsReadIndexReader{reader: reader, ctx: ctx}
		reader = bounded
	}
	input := xml.NewDecoder(reader)
	decoder := input
	if bounded != nil {
		bounded.decoder = input
		decoder = xml.NewTokenDecoder(bounded)
	}
	directories := make([]string, 0, 8)
	directoryBytes := 0
	rootSeen := false
	uuidSeen, generationSeen := false, false
	indexSeen, depth := false, 0
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		if bounded != nil {
			bounded.limit = input.InputOffset() + ltfsReadIndexBytes
		}
		token, err := decoder.Token()
		if errors.Is(err, io.EOF) {
			if !rootSeen {
				return fmt.Errorf("parse LTFS index failed, root directory is missing")
			}
			if uuid != "" && (!indexSeen || depth != 0 || !uuidSeen || !generationSeen) {
				return fmt.Errorf("current LTFS Index identity is missing")
			}
			return nil
		}
		if err != nil {
			return fmt.Errorf("parse LTFS index failed, %w", err)
		}

		switch value := token.(type) {
		case xml.StartElement:
			// Only this document's direct Index fields can bind planning ranges to the mounted identity.
			if uuid != "" {
				if depth == 0 {
					if indexSeen || value.Name.Local != "ltfsindex" || value.Name.Space != "" {
						return fmt.Errorf("current LTFS Index root is invalid")
					}
					indexSeen = true
				}
				depth++
				if depth == 2 && (value.Name.Space != "" ||
					value.Name.Local != "volumeuuid" && value.Name.Local != "generationnumber" && value.Name.Local != "directory") {
					if err := decoder.Skip(); err != nil {
						return fmt.Errorf("skip LTFS Index extension failed, %w", err)
					}
					depth--
					continue
				}
			}

			switch value.Name.Local {
			case "volumeuuid", "generationnumber":
				if uuid == "" || depth != 2 || value.Name.Space != "" {
					continue
				}
				var text string
				if err := decoder.DecodeElement(&text, &value); err != nil {
					return fmt.Errorf("decode current LTFS Index identity failed, %w", err)
				}
				depth--
				if value.Name.Local == "volumeuuid" {
					if uuidSeen || text != uuid {
						return fmt.Errorf("current LTFS Index volume identity differs")
					}
					uuidSeen = true
				} else {
					actual, err := strconv.ParseUint(text, 10, 64)
					if generationSeen || err != nil || actual != generation {
						return fmt.Errorf("current LTFS Index generation differs")
					}
					generationSeen = true
				}
			case "directory":
				if uuid != "" && (!uuidSeen || !generationSeen) {
					return fmt.Errorf("current LTFS Index identity must precede file entries")
				}
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
				if uuid != "" {
					depth--
				}
				component, err := decodeLTFSName(name)
				if err != nil {
					return fmt.Errorf("decode LTFS directory name failed, %w", err)
				}
				if bounded != nil {
					if len(component) > ltfsReadIndexBytes-directoryBytes {
						return fmt.Errorf("LTFS planning directory names exceed the bounded metadata allowance")
					}
					directoryBytes += len(component)
				}
				directories[len(directories)-1] = component
				if len(directories) == 1 {
					if rootSeen {
						return fmt.Errorf("parse LTFS index failed, multiple root directories")
					}
					rootSeen = true
				}
			case "file":
				files := decoder
				if uuid != "" {
					bounded.limit = input.InputOffset() + ltfsReadIndexBytes
					files = xml.NewTokenDecoder(&ltfsReadFileTokens{decoder: decoder, input: input, start: input.InputOffset(), opening: &value})
					if _, err := files.Token(); err != nil {
						return err
					}
				}
				entry, err := decodeLTFSFile(files, value, directories)
				if err != nil {
					return err
				}
				if uuid != "" {
					depth--

					// A sparse file can have holes, but no physical extent may extend beyond its declared EOF.
					for _, extent := range entry.Storage.Metadata.GetLtfs().Extents {
						if extent.FileOffsetBytes > uint64(entry.Size) || extent.ByteCount > uint64(entry.Size)-extent.FileOffsetBytes {
							return fmt.Errorf("LTFS planning extent exceeds declared file size, path=%q", entry.Path)
						}
					}
				}
				if err := yield(entry); err != nil {
					return err
				}
			}
		case xml.EndElement:
			if uuid != "" {
				depth--
			}
			if value.Name.Local != "directory" {
				continue
			}
			if len(directories) == 0 {
				return fmt.Errorf("parse LTFS index failed, unexpected directory end")
			}
			if bounded != nil {
				directoryBytes -= len(directories[len(directories)-1])
			}
			directories[len(directories)-1] = ""
			directories = directories[:len(directories)-1]
		case xml.CharData:
			if uuid != "" && depth == 0 && strings.TrimSpace(string(value)) != "" {
				return fmt.Errorf("current LTFS Index has data outside its root")
			}
		}
	}
}

const ltfsReadShortXMLBytes = 128

// ltfsReadIndexReader bounds planning input and XML nesting, not the total streamed Index size.
type ltfsReadIndexReader struct {
	reader      io.Reader
	ctx         context.Context
	decoder     *xml.Decoder
	frames      []int
	frameCount  int
	strings     map[string]string
	stringBytes int
	read, limit int64
}

func (r *ltfsReadIndexReader) Read(data []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	if r.read >= r.limit {
		return 0, fmt.Errorf("LTFS planning metadata exceeds the bounded input allowance")
	}
	n, err := r.reader.Read(data[:min(int64(len(data)), r.limit-r.read)])
	r.read += int64(n)
	return n, err
}

func (r *ltfsReadIndexReader) Token() (xml.Token, error) {
	// Raw tokens let the planning decoder own validation without first growing a second XML stack.
	if err := r.ctx.Err(); err != nil {
		return nil, err
	}
	token, err := r.decoder.RawToken()
	if err != nil {
		return nil, err
	}
	switch value := token.(type) {
	case xml.StartElement:
		// Namespace undo records also occupy decoder frames; account them before its stack grows.
		count := 1
		long := len(value.Name.Space) > ltfsReadShortXMLBytes || len(value.Name.Local) > ltfsReadShortXMLBytes
		for _, attribute := range value.Attr {
			if attribute.Name.Space == "xmlns" || attribute.Name.Space == "" && attribute.Name.Local == "xmlns" {
				count++
				long = long || len(attribute.Name.Local) > ltfsReadShortXMLBytes || len(attribute.Value) > ltfsReadShortXMLBytes
			}
		}
		if count > readplan.MaxRanges-r.frameCount {
			return nil, fmt.Errorf("LTFS planning nesting exceeds the supported depth")
		}

		// Popped frames retain strings. Bound long values without reboxing ordinary short-name tokens.
		if long {
			if value.Name.Space, err = r.retain(value.Name.Space); err != nil {
				return nil, err
			}
			if value.Name.Local, err = r.retain(value.Name.Local); err != nil {
				return nil, err
			}
			for i := range value.Attr {
				attribute := &value.Attr[i]
				if attribute.Name.Space != "xmlns" && (attribute.Name.Space != "" || attribute.Name.Local != "xmlns") {
					continue
				}
				if attribute.Name.Local, err = r.retain(attribute.Name.Local); err != nil {
					return nil, err
				}
				if attribute.Value, err = r.retain(attribute.Value); err != nil {
					return nil, err
				}
			}
			token = value
		}
		r.frames = append(r.frames, count)
		r.frameCount += count
	case xml.EndElement:
		if len(r.frames) != 0 {
			r.frameCount -= r.frames[len(r.frames)-1]
			r.frames = r.frames[:len(r.frames)-1]
		}
	}
	return token, nil
}

func (r *ltfsReadIndexReader) retain(value string) (string, error) {
	if len(value) <= ltfsReadShortXMLBytes {
		// A short prefix can share a long qualified-name allocation from encoding/xml.
		return strings.Clone(value), nil
	}
	if retained, ok := r.strings[value]; ok {
		return retained, nil
	}
	// Reserve map overhead as well as bytes; these are planning limits, not XML format limits.
	size := len(value) + 128
	if len(r.strings) == readplan.MaxRanges || size > ltfsReadIndexBytes-r.stringBytes {
		return "", fmt.Errorf("LTFS planning XML frames exceed the bounded metadata allowance")
	}
	if r.strings == nil {
		r.strings = make(map[string]string)
	}
	r.strings[value] = value
	r.stringBytes += size
	return value, nil
}

// ltfsReadFileTokens bounds one planning entry before DecodeElement can retain an unbounded extent slice.
type ltfsReadFileTokens struct {
	decoder *xml.Decoder
	input   *xml.Decoder
	start   int64
	extents int
	opening *xml.StartElement
}

func (t *ltfsReadFileTokens) Token() (xml.Token, error) {
	// The nested decoder must observe its own opening token to validate the matching closing element.
	if t.opening != nil {
		opening := *t.opening
		t.opening = nil
		return opening, nil
	}

	// Only planning metadata is bounded; the ordinary inventory parser keeps its original contract.
	token, err := t.decoder.Token()
	if err != nil {
		return nil, err
	}
	if t.input.InputOffset()-t.start > ltfsReadIndexBytes {
		return nil, fmt.Errorf("LTFS planning file exceeds the bounded metadata allowance")
	}
	if start, ok := token.(xml.StartElement); ok && start.Name.Local == "extent" {
		t.extents++
		if t.extents > readplan.MaxRanges {
			return nil, fmt.Errorf("LTFS planning file exceeds the supported extent count")
		}
	}
	return token, nil
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

	metadata := &entity.LtfsMetadata{Extents: make([]*entity.LtfsExtent, 0, len(file.ExtentInfo.Extents))}
	for _, extent := range file.ExtentInfo.Extents {
		if len(extent.Partition) != 1 {
			return nil, fmt.Errorf("decode LTFS extent failed, path=%q partition=%q is not one byte", filePath, extent.Partition)
		}
		metadata.Extents = append(metadata.Extents, &entity.LtfsExtent{
			Partition: extent.Partition, StartBlock: extent.StartBlock, ByteOffset: extent.ByteOffset,
			ByteCount: extent.ByteCount, FileOffsetBytes: extent.FileOffset,
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

func makeLTFSStorageOrder(extents []*entity.LtfsExtent) []byte {
	if len(extents) == 0 {
		return []byte{}
	}
	first := extents[0]
	for _, extent := range extents[1:] {
		if extent.FileOffsetBytes < first.FileOffsetBytes {
			first = extent
		}
	}
	order := make([]byte, 17)
	order[0] = first.Partition[0]
	binary.BigEndian.PutUint64(order[1:9], first.StartBlock)
	binary.BigEndian.PutUint64(order[9:17], first.ByteOffset)
	return order
}
