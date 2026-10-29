package media

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"io"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/samuelncui/acp"
	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/internal/media/tape"
	"google.golang.org/protobuf/proto"
)

type tarPositionDevice struct {
	*tarTestDevice
	base                                 uint64
	bits                                 uint32
	reads, rewinds, spaces, observations int
	locates                              []uint64
	positionErr, locateErr, readErr      error
	readFailure                          int
	unknown                              bool
	reported                             *tape.Position
	cleanup                              int
}

func (d *tarPositionDevice) Read(ctx context.Context, data []byte) (tape.RecordResult, error) {
	d.reads++
	if d.reads == d.readFailure {
		return tape.RecordResult{}, d.readErr
	}
	return d.tarTestDevice.Read(ctx, data)
}
func (d *tarPositionDevice) Rewind(ctx context.Context) error {
	d.rewinds++
	return d.tarTestDevice.Rewind(ctx)
}
func (d *tarPositionDevice) Space(ctx context.Context, code byte, count int32) error {
	d.spaces++
	return d.tarTestDevice.Space(ctx, code, count)
}
func (d *tarPositionDevice) AddressBits() uint32 { return d.bits }
func (d *tarPositionDevice) Position(context.Context) (tape.Position, error) {
	// Independent fixture coordinates count physical records and filemarks, not tar bytes.
	d.observations++
	if d.positionErr != nil {
		return tape.Position{}, d.positionErr
	}
	if d.reported != nil {
		return *d.reported, nil
	}
	file := uint64(0)
	for _, record := range d.records[:d.cursor] {
		if record.mark {
			file++
		}
	}
	position := tape.Position{Object: d.base + uint64(d.cursor), ObjectKnown: !d.unknown, File: file, FileKnown: true}
	if d.unknown {
		position.Partition = math.MaxUint32
		position.Object = math.MaxUint64
	}
	return position, nil
}
func (d *tarPositionDevice) Locate(_ context.Context, partition uint32, object uint64) error {
	// An issued rejection leaves the physical cursor unchanged and remains distinguishable from uncertainty.
	d.locates = append(d.locates, object)
	if d.locateErr != nil {
		return d.locateErr
	}
	if partition != 0 || object < d.base || object-d.base > uint64(len(d.records)) {
		return errors.New("fixture LOCATE outside recorded objects")
	}
	d.cursor = int(object - d.base)
	return nil
}

type tarPositionMember struct {
	name       string
	data       []byte
	attrs      map[string]string
	start, end uint64
}

func tarPositionFixture(t *testing.T, fixed bool, lengths []int, members []tarPositionMember) (*tarPositionDevice, TarSessionOptions, []tarPositionMember) {
	t.Helper()
	// Serialize independent standard tar framing, recording boundaries only after its actual padding.
	var content bytes.Buffer
	writer := tar.NewWriter(&content)
	for i := range members {
		members[i].name = tarPrefix(1) + "/" + members[i].name
		members[i].start = uint64(content.Len())
		header := &tar.Header{Name: members[i].name, Size: int64(len(members[i].data)), Mode: 0640, ModTime: time.Unix(1000, 0), Format: tar.FormatPAX}
		if members[i].attrs != nil {
			header.PAXRecords = map[string]string{}
			for name, value := range members[i].attrs {
				header.PAXRecords["SCHILY.xattr."+name] = value
			}
		}
		if err := writer.WriteHeader(header); err != nil {
			t.Fatal(err)
		}
		if _, err := writer.Write(members[i].data); err != nil {
			t.Fatal(err)
		}
		if err := writer.Flush(); err != nil {
			t.Fatal(err)
		}
		members[i].end = uint64(content.Len())
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}

	// Deliberately vary record lengths independently of the configured maximum and logical tar blocks.
	d := &tarPositionDevice{tarTestDevice: &tarTestDevice{records: []tarTestRecord{{mark: true}}}, base: 100, bits: 64}
	data := content.Bytes()
	for i := 0; len(data) > 0; i++ {
		size := min(lengths[i%len(lengths)], len(data))
		record := bytes.Clone(data[:size])
		data = data[size:]
		if fixed && len(record) < lengths[0] {
			record = append(record, make([]byte, lengths[0]-len(record))...)
		}
		d.records = append(d.records, tarTestRecord{data: record})
	}
	d.records = append(d.records, tarTestRecord{mark: true})
	maximum := 4096
	if fixed {
		maximum = lengths[0]
	}
	options := tarTestOptions(t, fixed, maximum)
	options.Navigator = d
	options.Cleanup = func(context.Context) error { d.cleanup++; return nil }
	return d, options, members
}

func tarPositionAt(t *testing.T, d *tarPositionDevice, offset uint64) *entity.TapeBytePosition {
	t.Helper()
	// Expected anchors come from the fixture's physical record lengths, independently of reader state.
	for i, record := range d.records[1:] {
		if record.mark {
			if offset == 0 {
				return &entity.TapeBytePosition{LogicalObject: d.base + 1 + uint64(i)}
			}
			break
		}
		if offset < uint64(len(record.data)) {
			return &entity.TapeBytePosition{LogicalObject: d.base + 1 + uint64(i), OffsetBytes: uint32(offset)}
		}
		offset -= uint64(len(record.data))
	}
	t.Fatal("fixture offset is outside its content Tape file")
	return nil
}

func tarPositionRequest(t *testing.T, d *tarPositionDevice, member tarPositionMember) ReadRequest {
	t.Helper()
	locator := &entity.TarMetadata{TapeFile: 1, HeaderOffsetBytes: member.start, Header: tarPositionAt(t, d, member.start), End: tarPositionAt(t, d, member.end)}
	return ReadRequest{Path: member.name, Storage: &entity.StoragePosition{Order: entity.TarStorageOrder(locator), Metadata: &entity.StorageMetadata{Backend: &entity.StorageMetadata_Tar{Tar: locator}}}}
}

func readPositionMember(t *testing.T, session InventorySession, request ReadRequest, want []byte) acp.ContentMetadata {
	t.Helper()
	// The endpoint must validate its header before yielding bytes and acknowledge physical completion at Close.
	source, err := session.Source(request)
	if err != nil {
		t.Fatal(err)
	}
	reader, err := source.Content.Open(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	metadata := tarTestMetadata(t, reader)
	data, err := io.ReadAll(reader)
	if err != nil || !bytes.Equal(data, want) || metadata.Info.Size() != int64(len(want)) {
		t.Fatalf("data=%q metadata=%v err=%v", data, metadata.Info, err)
	}
	if err := reader.Close(); err != nil {
		t.Fatal(err)
	}
	return metadata
}

func TestTarReadPositionRandomOrder(t *testing.T) {
	for _, fixed := range []bool{false, true} {
		t.Run(map[bool]string{false: "short variable", true: "fixed"}[fixed], func(t *testing.T) {
			// A long local PAX chain and large payload cross several differently sized records.
			lengths := []int{700, 1300, 900, 512}
			if fixed {
				lengths = []int{1024}
			}
			d, options, members := tarPositionFixture(t, fixed, lengths, []tarPositionMember{
				{name: "first", data: []byte("first")},
				{name: strings.Repeat("long/", 35) + "middle", data: bytes.Repeat([]byte("payload"), 600), attrs: map[string]string{"user.empty": "", "user.binary": string([]byte{0, 255, '\n'})}},
				{name: "last", data: []byte("last")},
			})
			session, err := NewTarReadSession(d, options)
			if err != nil {
				t.Fatal(err)
			}
			if d.reads+d.rewinds+d.spaces+d.observations+len(d.locates) != 0 {
				t.Fatal("constructor issued physical operations")
			}

			// Selected reads in nonlogical order use exact anchors rather than a BOT scan or nominal byte division.
			for _, index := range []int{2, 0, 1, 2} {
				metadata := readPositionMember(t, session, tarPositionRequest(t, d, members[index]), members[index].data)
				for name, value := range members[index].attrs {
					observed, exists := metadata.Xattrs[name]
					if !exists || !bytes.Equal(observed, []byte(value)) {
						t.Fatalf("PAX attribute %q differs: %q", name, observed)
					}
				}
			}
			if d.observations != 1 {
				t.Fatalf("per-member position queries=%d", d.observations)
			}
			if len(d.locates) == 0 || d.rewinds+d.spaces != 0 {
				t.Fatalf("locates=%v rewinds=%d spaces=%d", d.locates, d.rewinds, d.spaces)
			}
			if err := session.Finalize(context.Background(), nil); err != nil || d.cleanup != 1 {
				t.Fatalf("cleanup=%d err=%v", d.cleanup, err)
			}
		})
	}
}

func TestTarReadPositionRetainsEarlierSharedHeader(t *testing.T) {
	// All three member headers share one record; opening an earlier header must not reread or move Tape.
	d, options, members := tarPositionFixture(t, false, []int{4096}, []tarPositionMember{{name: "a", data: []byte("a")}, {name: "b", data: []byte("bb")}, {name: "c", data: []byte("ccc")}})
	session, err := NewTarReadSession(d, options)
	if err != nil {
		t.Fatal(err)
	}
	for _, index := range []int{1, 0, 2} {
		readPositionMember(t, session, tarPositionRequest(t, d, members[index]), members[index].data)
	}
	if d.reads != 1 || len(d.locates) != 1 || d.observations != 1 || d.rewinds+d.spaces != 0 {
		t.Fatalf("reads=%d locates=%v observations=%d", d.reads, d.locates, d.observations)
	}
}

func TestTarReadPositionUsesNextUnreadRecord(t *testing.T) {
	// A completed member ends exactly at the next physical record; the adjacent header needs no LOCATE.
	d, options, members := tarPositionFixture(t, false, []int{1024}, []tarPositionMember{{name: "a", data: []byte("a")}, {name: "b", data: []byte("b")}})
	session, err := NewTarReadSession(d, options)
	if err != nil {
		t.Fatal(err)
	}
	for _, member := range members {
		readPositionMember(t, session, tarPositionRequest(t, d, member), member.data)
	}
	if d.reads != 2 || len(d.locates) != 1 || d.observations != 1 {
		t.Fatalf("reads=%d locates=%v observations=%d", d.reads, d.locates, d.observations)
	}
}

func TestTarReadPositionFallbackBeforeLocate(t *testing.T) {
	for _, kind := range []string{"missing navigator", "missing anchors", "32 bit range", "unknown position", "unsupported position", "command rejection"} {
		t.Run(kind, func(t *testing.T) {
			// Keep otherwise proven sequential access available when optional seek evidence is ineligible.
			d, options, members := tarPositionFixture(t, false, []int{700, 900}, []tarPositionMember{{name: "first", data: []byte("saved")}})
			request := tarPositionRequest(t, d, members[0])
			switch kind {
			case "missing navigator":
				options.Navigator = nil
			case "missing anchors":
				request.Storage = tarStorage(1, members[0].start)
				d.positionErr = errors.Join(tape.ErrMalformed, tape.ErrUncertain)
			case "32 bit range":
				d.bits = 32
				d.base = math.MaxUint32
				request = tarPositionRequest(t, d, members[0])
				d.positionErr = errors.Join(tape.ErrMalformed, tape.ErrUncertain)
			case "unknown position":
				d.unknown = true
			case "unsupported position":
				d.positionErr = tape.ErrUnsupported
			case "command rejection":
				d.positionErr = &tape.CommandError{Response: tape.Response{Completed: true, Status: 2}, Sense: tape.Sense{Key: 5, ASC: 0x20}}
			}
			session, err := NewTarReadSession(d, options)
			if err != nil {
				t.Fatal(err)
			}
			readPositionMember(t, session, request, members[0].data)
			if (kind == "missing anchors" || kind == "32 bit range") && d.observations != 0 {
				t.Fatal("ineligible selected read queried optional positioning")
			}
			if len(d.locates) != 0 || d.rewinds != 1 || d.spaces != 1 {
				t.Fatalf("locates=%v rewinds=%d spaces=%d", d.locates, d.rewinds, d.spaces)
			}
		})
	}
}

func TestTarReadPositionFailureStopsUnstartedSources(t *testing.T) {
	for _, kind := range []string{"LOCATE rejection", "read error", "mixed observation failure"} {
		t.Run(kind, func(t *testing.T) {
			// Rejected motion is Session-wide unavailability, not fabricated uncertainty or file corruption.
			d, options, members := tarPositionFixture(t, false, []int{512}, []tarPositionMember{{name: "file", data: bytes.Repeat([]byte("x"), 1200)}})
			cause := errors.New("injected reader failure")
			switch kind {
			case "LOCATE rejection":
				cause = tape.ErrUnsupported
				d.locateErr = cause
			case "read error":
				d.readFailure = 2
				d.readErr = cause
			case "mixed observation failure":
				d.positionErr = errors.Join(tape.ErrUnsupported, cause)
			}
			session, err := NewTarReadSession(d, options)
			if err != nil {
				t.Fatal(err)
			}
			source, err := session.Source(tarPositionRequest(t, d, members[0]))
			if err != nil {
				t.Fatal(err)
			}
			reader, err := source.Content.Open(context.Background())
			if kind == "read error" {
				if err != nil {
					t.Fatal(err)
				}
				_, err = io.ReadAll(reader)
				if closeErr := reader.Close(); !errors.Is(closeErr, cause) {
					t.Fatalf("Close error=%v", closeErr)
				}
			}
			if !errors.Is(err, cause) || errors.Is(err, tape.ErrUncertain) {
				t.Fatalf("first failure=%v", err)
			}
			if kind == "LOCATE rejection" && !errors.Is(err, ErrReadUnavailable) {
				t.Fatalf("LOCATE failure is not Session-wide: %v", err)
			}

			// Reopening an unstarted source preserves the cause and emits no query, motion or data command.
			commands := d.reads + d.rewinds + d.spaces + d.observations + len(d.locates)
			_, nextErr := source.Content.Open(context.Background())
			if !errors.Is(nextErr, ErrReadUnavailable) || !errors.Is(nextErr, cause) || d.reads+d.rewinds+d.spaces+d.observations+len(d.locates) != commands {
				t.Fatalf("next error=%v", nextErr)
			}
			if err := session.Finalize(context.Background(), nil); err != nil || d.cleanup != 1 {
				t.Fatalf("known cleanup error=%v", err)
			}
		})
	}
}

func TestTarReadPositionCommandFailureScope(t *testing.T) {
	for _, test := range []struct {
		name        string
		sense       tape.Sense
		unavailable bool
	}{
		{name: "no medium", sense: tape.Sense{Key: 2, ASC: 0x3a}, unavailable: true},
		{name: "not ready", sense: tape.Sense{Key: 2, ASC: 4}, unavailable: true},
		{name: "medium error", sense: tape.Sense{Key: 3, ASC: 0x11}},
	} {
		t.Run(test.name, func(t *testing.T) {
			// Device-wide rejection stops the lane; an actual medium fault remains the first member observation.
			d, options, members := tarPositionFixture(t, false, []int{512}, []tarPositionMember{{name: "file", data: []byte("saved")}})
			cause := &tape.CommandError{Response: tape.Response{Completed: true, Status: 2}, Sense: test.sense}
			d.readFailure, d.readErr = 1, cause
			session, err := NewTarReadSession(d, options)
			if err != nil {
				t.Fatal(err)
			}
			source, err := session.Source(tarPositionRequest(t, d, members[0]))
			if err != nil {
				t.Fatal(err)
			}
			reader, err := source.Content.Open(context.Background())
			if reader != nil || !errors.Is(err, cause) || errors.Is(err, ErrReadUnavailable) != test.unavailable || errors.Is(err, tape.ErrUncertain) {
				t.Fatalf("first READ failure=%v reader=%v", err, reader)
			}

			// No unstarted source may retry after either failure, and known cleanup remains independent.
			commands := d.reads + d.rewinds + d.spaces + d.observations + len(d.locates)
			_, err = source.Content.Open(context.Background())
			if !errors.Is(err, ErrReadUnavailable) || !errors.Is(err, cause) || d.reads+d.rewinds+d.spaces+d.observations+len(d.locates) != commands {
				t.Fatalf("stopped lane issued a command: %v", err)
			}
			if err := session.Finalize(context.Background(), nil); err != nil || d.cleanup != 1 {
				t.Fatalf("known cleanup error=%v", err)
			}
		})
	}
}

func TestTarReadPositionRejectsActualRecordBoundsAndEnd(t *testing.T) {
	for _, kind := range []string{"header outside short record", "wrong path", "wrong end"} {
		t.Run(kind, func(t *testing.T) {
			// Frozen locators must match actual record lengths, the local header path and exclusive end.
			d, options, members := tarPositionFixture(t, false, []int{512}, []tarPositionMember{{name: "a", data: []byte("a")}, {name: "b", data: []byte("b")}})
			request := tarPositionRequest(t, d, members[1])
			locator := request.Storage.Metadata.GetTar()
			switch kind {
			case "header outside short record":
				locator.Header.OffsetBytes = uint32(locator.HeaderOffsetBytes)
			case "wrong path":
				locator.Header = tarPositionAt(t, d, members[0].start)
			case "wrong end":
				locator.End.OffsetBytes = 512
			}
			session, err := NewTarReadSession(d, options)
			if err != nil {
				t.Fatal(err)
			}
			source, err := session.Source(request)
			if err != nil {
				t.Fatal(err)
			}
			reader, err := source.Content.Open(context.Background())
			if kind == "wrong end" {
				if err != nil {
					t.Fatal(err)
				}
				if _, err := io.ReadAll(reader); err != nil {
					t.Fatal(err)
				}
				err = reader.Close()
			} else if reader != nil {
				t.Fatal("invalid header exposed payload")
			}
			if err == nil {
				t.Fatal("invalid physical locator was accepted")
			}
			commands := d.reads + d.rewinds + d.spaces + d.observations + len(d.locates)
			_, err = source.Content.Open(context.Background())
			if !errors.Is(err, ErrReadUnavailable) || d.reads+d.rewinds+d.spaces+d.observations+len(d.locates) != commands {
				t.Fatalf("failed lane retried: %v", err)
			}
		})
	}
}

func TestTarReadPositionInventoryAnchors(t *testing.T) {
	for _, unknown := range []bool{false, true} {
		t.Run(map[bool]string{false: "exact", true: "unknown"}[unknown], func(t *testing.T) {
			// Only qualified observed object coordinates add anchors; logical inventory remains complete without them.
			d, options, members := tarPositionFixture(t, false, []int{700, 1300, 512}, []tarPositionMember{{name: "a", data: []byte("a")}, {name: "b", data: bytes.Repeat([]byte("b"), 800)}})
			d.unknown = unknown
			session, err := NewTarReadSession(d, options)
			if err != nil {
				t.Fatal(err)
			}
			var entries []*InventoryEntry
			if err := session.WalkInventory(context.Background(), func(entry *InventoryEntry) error { entries = append(entries, entry); return nil }); err != nil {
				t.Fatal(err)
			}
			if len(entries) != len(members) {
				t.Fatalf("entries=%d", len(entries))
			}
			for i, entry := range entries {
				locator := entry.Storage.Metadata.GetTar()
				if err := entity.ValidateTarMetadata(locator, uint32(options.Mode.MaximumBytes)); err != nil {
					t.Fatal(err)
				}
				if unknown {
					if locator.Header != nil || locator.End != nil {
						t.Fatal("unknown address emitted anchors")
					}
					continue
				}
				expected := tarPositionRequest(t, d, members[i]).Storage.Metadata.GetTar()
				if !proto.Equal(locator, expected) {
					t.Fatalf("locator=%v expected=%v", locator, expected)
				}
			}
		})
	}
}

func TestTarReadPositionUnknownObjectKeepsExactFileOrdinal(t *testing.T) {
	// Unknown object flags invalidate garbage partition/object fields, but not an independently exact file ID.
	d, options, _ := tarPositionFixture(t, false, []int{1024}, []tarPositionMember{{name: "a", data: []byte("a")}})
	d.reported = &tape.Position{Partition: math.MaxUint32, Object: math.MaxUint64, File: 2, FileKnown: true}
	session, err := NewTarReadSession(d, options)
	if err != nil {
		t.Fatal(err)
	}
	emitted := 0
	err = session.WalkInventory(context.Background(), func(*InventoryEntry) error { emitted++; return nil })
	if !errors.Is(err, ErrReadUnavailable) || emitted != 0 || d.reads != 0 {
		t.Fatalf("ordinal mismatch err=%v emitted=%d reads=%d", err, emitted, d.reads)
	}
}

func TestTarReadPositionInventoryRejectsBeginningAfterSpace(t *testing.T) {
	// Controlled navigation to content file one cannot remain at partition beginning, even without a file count.
	d, options, members := tarPositionFixture(t, false, []int{512}, []tarPositionMember{{name: "file", data: []byte("saved")}})
	d.reported = &tape.Position{Beginning: true, ObjectKnown: true}
	session, err := NewTarReadSession(d, options)
	if err != nil {
		t.Fatal(err)
	}
	emitted := false
	err = session.WalkInventory(context.Background(), func(*InventoryEntry) error { emitted = true; return nil })
	if !errors.Is(err, ErrReadUnavailable) || emitted || d.reads != 0 || d.rewinds != 1 || d.spaces != 1 {
		t.Fatalf("contradictory inventory error=%v emitted=%t reads=%d rewinds=%d spaces=%d", err, emitted, d.reads, d.rewinds, d.spaces)
	}

	// Contradictory evidence stops later sources before any query, motion or read can retry it.
	source, err := session.Source(tarPositionRequest(t, d, members[0]))
	if err != nil {
		t.Fatal(err)
	}
	commands := d.reads + d.rewinds + d.spaces + d.observations + len(d.locates)
	_, err = source.Content.Open(context.Background())
	if !errors.Is(err, ErrReadUnavailable) || d.reads+d.rewinds+d.spaces+d.observations+len(d.locates) != commands {
		t.Fatalf("stopped inventory lane issued a command: %v", err)
	}
}
