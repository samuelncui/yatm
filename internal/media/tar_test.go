package media

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"io"
	"io/fs"
	"strings"
	"testing"
	"time"

	"github.com/samuelncui/acp"
	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/internal/media/tape"
)

type tarTestRecord struct {
	data []byte
	mark bool
}
type tarTestDevice struct {
	records                           []tarTestRecord
	cursor, writes, marks             int
	warningAt, unknownAt, readErrorAt int
	writeErrorAt                      int
	writeErr                          error
	rejectWrite                       bool
	completionErr                     error
}

func (d *tarTestDevice) Read(_ context.Context, output []byte) (tape.RecordResult, error) {
	if d.readErrorAt != 0 && d.cursor+1 == d.readErrorAt {
		return tape.RecordResult{}, errors.New("injected data read error")
	}
	if d.cursor == len(d.records) {
		return tape.RecordResult{AcceptedKnown: true, EOD: true}, nil
	}
	record := d.records[d.cursor]
	d.cursor++
	if record.mark {
		return tape.RecordResult{AcceptedKnown: true, Filemark: true}, nil
	}
	if len(record.data) > len(output) {
		return tape.RecordResult{}, io.ErrShortBuffer
	}
	copy(output, record.data)
	return tape.RecordResult{AcceptedKnown: true, Bytes: len(record.data)}, nil
}
func (d *tarTestDevice) Write(_ context.Context, data []byte) (tape.RecordResult, error) {
	d.writes++
	if d.writes == d.unknownAt {
		return tape.RecordResult{}, errors.New("injected unknown acceptance")
	}
	if d.writes == d.writeErrorAt && d.rejectWrite {
		return tape.RecordResult{AcceptedKnown: true}, d.writeErr
	}
	d.records = append(d.records, tarTestRecord{data: bytes.Clone(data)})
	result := tape.RecordResult{AcceptedKnown: true, Bytes: len(data)}
	if d.writes == d.writeErrorAt {
		return result, d.writeErr
	}
	if d.writes == d.warningAt {
		result.EarlyWarning = true
		return result, tape.ErrEarlyWarning
	}
	return result, nil
}
func (d *tarTestDevice) Filemarks(_ context.Context, count uint32) error {
	if d.completionErr != nil && d.completionErr != tape.ErrEarlyWarning {
		return d.completionErr
	}
	d.marks += int(count)
	for range count {
		d.records = append(d.records, tarTestRecord{mark: true})
	}
	return d.completionErr
}
func (d *tarTestDevice) Sync(context.Context) error   { return nil }
func (d *tarTestDevice) Rewind(context.Context) error { d.cursor = 0; return nil }
func (d *tarTestDevice) Space(_ context.Context, code byte, count int32) error {
	if code != 1 || count < 0 {
		return errors.New("unsupported fixture navigation")
	}
	for count > 0 {
		if d.cursor == len(d.records) {
			return io.EOF
		}
		if d.records[d.cursor].mark {
			count--
		}
		d.cursor++
	}
	return nil
}

type tarTestInfo struct {
	name string
	size int64
}

func (i tarTestInfo) Name() string       { return i.name }
func (i tarTestInfo) Size() int64        { return i.size }
func (i tarTestInfo) Mode() fs.FileMode  { return 0640 }
func (i tarTestInfo) ModTime() time.Time { return time.Unix(1000, 123456789) }
func (i tarTestInfo) IsDir() bool        { return false }
func (i tarTestInfo) Sys() any           { return nil }

func tarTestOptions(t *testing.T, fixed bool, recordBytes int) TarSessionOptions {
	t.Helper()
	mode := tape.RecordMode{MaximumBytes: recordBytes}
	if fixed {
		mode.FixedBytes = recordBytes
	}
	return TarSessionOptions{Media: &Descriptor{Kind: entity.MediaKind_MEDIA_KIND_TAPE}, Mode: mode,
		TapeFile: 1, Cleanup: func(context.Context) error { return nil }}
}

func writeTarTestMember(t *testing.T, session WriteSession, name string, data []byte, attrs map[string][]byte) {
	t.Helper()
	// Exercise the same lazy target endpoint boundary ACP uses.
	ref, err := session.Target(name, int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	w, err := ref.Content.Open(context.Background(), acp.ContentMetadata{Info: tarTestInfo{name: name, size: int64(len(data))}, Xattrs: attrs})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write(data); err != nil {
		t.Fatal(err)
	}
	if err := w.Finish(nil); err != nil {
		t.Fatal(err)
	}
}

// tarTestWriteSession retains target receipts only for bounded fixtures. Production stores them in Archive items.
type tarTestWriteSession struct {
	*tarWriteSession
	targets []*tarTestTarget
}

type tarTestTarget struct {
	ref  TargetRef
	path string
	size int64
}

func newTarTestWriteSession(device TarRecordDevice, options TarSessionOptions) (*tarTestWriteSession, error) {
	session, err := NewTarWriteSession(device, options)
	if err != nil {
		return nil, err
	}
	return &tarTestWriteSession{tarWriteSession: session.(*tarWriteSession)}, nil
}

func (s *tarTestWriteSession) Target(path string, size int64) (TargetRef, error) {
	ref, err := s.tarWriteSession.Target(path, size)
	if err != nil {
		return ref, err
	}
	target := &tarTestTarget{ref: ref, path: path}
	s.targets = append(s.targets, target)
	return TargetRef{Content: target}, nil
}

func (t *tarTestTarget) Open(ctx context.Context, metadata acp.ContentMetadata) (acp.ContentWriter, error) {
	if metadata.Info != nil {
		t.size = metadata.Info.Size()
	}
	return t.ref.Content.Open(ctx, metadata)
}

func (t *tarTestTarget) storagePosition() *entity.StoragePosition { return t.ref.Storage() }

func tarTestCompleted(t *testing.T, session *tarTestWriteSession, result *WriteResult) []*CompletedFile {
	t.Helper()
	// Replay caller-held receipts through the same final validator used by Archive, with no runtime inventory.
	if result.Inventory != nil {
		t.Fatal("tar result unexpectedly exposes an independent inventory")
	}
	var files []*CompletedFile
	for _, target := range session.targets {
		staged := target.ref.Storage()
		if staged == nil {
			continue
		}
		storage, err := result.FinalizeStorage(staged)
		if err != nil {
			t.Fatal(err)
		}
		if storage != nil {
			files = append(files, &CompletedFile{Path: result.PathPrefix + "/" + target.path, Size: target.size, Storage: storage})
		}
	}
	return files
}

func tarTestMetadata(t *testing.T, file fs.File) acp.ContentMetadata {
	t.Helper()
	// Metadata belongs to the opened file, including its optional raw PAX attributes.
	info, err := file.Stat()
	if err != nil {
		t.Fatal(err)
	}
	attrs, err := file.(acp.XattrReader).Xattrs()
	if err != nil {
		t.Fatal(err)
	}
	return acp.ContentMetadata{Info: info, Xattrs: attrs}
}

func tarTestBytes(d *tarTestDevice) []byte {
	var data []byte
	for _, record := range d.records {
		data = append(data, record.data...)
	}
	return data
}

func TestTarPackedRoundTrip(t *testing.T) {
	for _, fixed := range []bool{false, true} {
		t.Run(map[bool]string{false: "variable", true: "fixed"}[fixed], func(t *testing.T) {
			// Two members and long UTF-8 paths share records and retain raw empty/binary attrs.
			d := &tarTestDevice{records: []tarTestRecord{{mark: true}}}
			options := tarTestOptions(t, fixed, 16<<10)
			s, err := newTarTestWriteSession(d, options)
			if err != nil {
				t.Fatal(err)
			}
			name := strings.Repeat("目录/", 40) + "file"
			attrs := map[string][]byte{"user.empty": {}, "user.binary": {0, '\n', 255}, "acp.signature": []byte("cache")}
			writeTarTestMember(t, s, name, []byte("first"), attrs)
			writeTarTestMember(t, s, "second", []byte("second"), nil)
			if d.writes != 0 {
				t.Fatalf("members flushed physical records, writes=%d", d.writes)
			}
			result, err := s.Finalize(context.Background(), nil)
			if err != nil {
				t.Fatal(err)
			}
			files := tarTestCompleted(t, s, result)
			if len(files) != 2 || d.marks != 1 || result.VerifiedPrefix {
				t.Fatalf("unexpected completion: files=%d, marks=%d", len(files), d.marks)
			}
			if fixed && len(d.records[1].data) != options.Mode.FixedBytes {
				t.Fatal("fixed record was short")
			}

			// The selected source reads actual metadata and settles padding at Close.
			read, err := NewTarReadSession(d, options)
			if err != nil {
				t.Fatal(err)
			}
			for index, file := range files {
				ref, err := read.Source(ReadRequest{Path: file.Path, Storage: file.Storage})
				if err != nil {
					t.Fatal(err)
				}
				r, err := ref.Content.Open(context.Background())
				if err != nil {
					t.Fatal(err)
				}
				metadata := tarTestMetadata(t, r)
				data, err := io.ReadAll(r)
				if err != nil {
					t.Fatal(err)
				}
				if err := r.Close(); err != nil {
					t.Fatal(err)
				}
				if index == 0 && (!bytes.Equal(data, []byte("first")) || !bytes.Equal(metadata.Xattrs["user.binary"], attrs["user.binary"])) {
					t.Fatal("member data/xattrs changed")
				}
				if index == 0 {
					if _, exists := metadata.Xattrs["user.empty"]; !exists {
						t.Fatal("empty xattr lost")
					}
				}
				if index == 0 && !bytes.Equal(metadata.Xattrs["acp.signature"], attrs["acp.signature"]) {
					t.Fatal("codec filtered a raw attribute")
				}
			}

			// Fresh inventory enumerates the physical files, including observed placement.
			var inventory []*InventoryEntry
			if err := read.WalkInventory(context.Background(), func(entry *InventoryEntry) error { inventory = append(inventory, entry); return nil }); err != nil {
				t.Fatal(err)
			}
			if len(inventory) != 2 || inventory[0].Storage.Metadata.GetTar().HeaderOffsetBytes != files[0].Storage.Metadata.GetTar().HeaderOffsetBytes {
				t.Fatal("fresh inventory locator differs")
			}
			if err := read.Finalize(context.Background(), nil); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestTarWarningStopsDataAndFiltersBufferedMembers(t *testing.T) {
	for _, fixed := range []bool{false, true} {
		t.Run(map[bool]string{false: "variable", true: "fixed"}[fixed], func(t *testing.T) {
			// One accepted record contains a complete first member and an incomplete second member.
			d := &tarTestDevice{warningAt: 1}
			s, err := newTarTestWriteSession(d, tarTestOptions(t, fixed, 4096))
			if err != nil {
				t.Fatal(err)
			}
			writeTarTestMember(t, s, "first", []byte("first"), nil)
			ref, _ := s.Target("large", 8192)
			w, err := ref.Content.Open(context.Background(), acp.ContentMetadata{Info: tarTestInfo{name: "large", size: 8192}})
			if err != nil {
				t.Fatal(err)
			}
			_, writeErr := w.Write(make([]byte, 8192))
			if !errors.Is(writeErr, ErrTargetNoSpace) {
				t.Fatalf("warning was not a target stop: %v", writeErr)
			}
			_ = w.Finish(writeErr)
			result, err := s.Finalize(context.Background(), writeErr)
			if err != nil {
				t.Fatal(err)
			}
			files := tarTestCompleted(t, s, result)
			if len(files) != 1 || !strings.HasSuffix(files[0].Path, "/first") || d.writes != 1 || d.marks != 1 {
				t.Fatalf("warning submitted suffix or wrong completion, files=%d writes=%d marks=%d", len(files), d.writes, d.marks)
			}
		})
	}
}

func TestTarSourceFailureDiscardsSuffix(t *testing.T) {
	// A complete member entirely buffered in a fixed-record suffix cannot publish after source failure.
	d := &tarTestDevice{}
	s, err := newTarTestWriteSession(d, tarTestOptions(t, true, 16<<10))
	if err != nil {
		t.Fatal(err)
	}
	writeTarTestMember(t, s, "buffered", []byte("complete"), nil)
	ref, _ := s.Target("incomplete", 4096)
	w, err := ref.Content.Open(context.Background(), acp.ContentMetadata{Info: tarTestInfo{name: "incomplete", size: 4096}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write([]byte("partial")); err != nil {
		t.Fatal(err)
	}
	cause := errors.New("source failed")
	_ = w.Finish(cause)

	// Failed content ends the attempt without flushing records or manufacturing a filemark.
	result, err := s.Finalize(context.Background(), cause)
	if !errors.Is(err, ErrFinalizeUnusable) || !errors.Is(err, cause) || result != nil || d.writes != 0 || d.marks != 0 {
		t.Fatalf("interrupted stream produced completion, result=%v err=%v writes=%d marks=%d", result, err, d.writes, d.marks)
	}
}

func TestTarFinalizationGates(t *testing.T) {
	for _, failure := range []string{"unknown-record", "filemark", "cleanup"} {
		t.Run(failure, func(t *testing.T) {
			// No success inventory survives an uncertain record, failed boundary or failed required cleanup.
			d := &tarTestDevice{}
			options := tarTestOptions(t, false, 4096)
			cleaned := false
			options.Cleanup = func(context.Context) error {
				cleaned = true
				if failure == "cleanup" {
					return errors.New("cleanup failed")
				}
				return nil
			}
			if failure == "unknown-record" {
				d.unknownAt = 1
			}
			if failure == "filemark" {
				d.completionErr = errors.New("filemark failed")
			}
			s, err := newTarTestWriteSession(d, options)
			if err != nil {
				t.Fatal(err)
			}
			writeTarTestMember(t, s, "file", []byte("file"), nil)
			result, err := s.Finalize(context.Background(), nil)
			if !errors.Is(err, ErrFinalizeUnusable) || result != nil || !cleaned {
				t.Fatalf("failed gate produced usable result, result=%v err=%v cleaned=%t", result, err, cleaned)
			}
		})
	}
}

func TestTarInventoryTruncatedTailAndReadError(t *testing.T) {
	// Public tar framing remains independently recoverable before a clean-filemark truncated member.
	d := &tarTestDevice{records: []tarTestRecord{{mark: true}}, warningAt: 1}
	s, err := newTarTestWriteSession(d, tarTestOptions(t, false, 4096))
	if err != nil {
		t.Fatal(err)
	}
	writeTarTestMember(t, s, "first", []byte("saved"), nil)
	ref, _ := s.Target("incomplete", 10000)
	w, err := ref.Content.Open(context.Background(), acp.ContentMetadata{Info: tarTestInfo{name: "incomplete", size: 10000}})
	if err != nil {
		t.Fatal(err)
	}
	_, cause := w.Write(make([]byte, 10000))
	_ = w.Finish(cause)
	if _, err := s.Finalize(context.Background(), cause); err != nil {
		t.Fatal(err)
	}

	// Clean truncation excludes only the damaged tail; actual I/O failure invalidates enumeration.
	read, _ := NewTarReadSession(d, tarTestOptions(t, false, 4096))
	count := 0
	if err := read.WalkInventory(context.Background(), func(*InventoryEntry) error { count++; return nil }); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("incomplete member was inventoried, count=%d", count)
	}
	d.readErrorAt = 2
	if err := read.WalkInventory(context.Background(), func(*InventoryEntry) error { return nil }); err == nil {
		t.Fatal("actual read error accepted as truncation")
	}
}

func TestTarMetadataAndUnsupportedExtensions(t *testing.T) {
	// Invalid/oversized metadata rejects the member before its first header reaches the device.
	for _, attrs := range []map[string][]byte{{"bad=name": {}}, {"user.large": make([]byte, 1<<20)}, {"user.huge": make([]byte, 1<<20+1)}} {
		d := &tarTestDevice{}
		s, _ := newTarTestWriteSession(d, tarTestOptions(t, false, 512))
		ref, _ := s.Target("file", 0)
		if _, err := ref.Content.Open(context.Background(), acp.ContentMetadata{Info: tarTestInfo{name: "file"}, Xattrs: attrs}); err == nil {
			t.Fatal("invalid xattr admitted")
		}
		if d.writes != 0 {
			t.Fatal("rejected metadata wrote headers")
		}
		if _, err := s.Finalize(context.Background(), nil); err != nil {
			t.Fatal(err)
		}
	}

	// Standard archives with global state or links cannot become independently addressed regular members.
	for _, kind := range []byte{tar.TypeXGlobalHeader, tar.TypeSymlink} {
		var archive bytes.Buffer
		writer := tar.NewWriter(&archive)
		header := &tar.Header{Name: tarPrefix(1) + "/file", Typeflag: kind, Linkname: "other", Format: tar.FormatPAX}
		if kind == tar.TypeXGlobalHeader {
			header = &tar.Header{Typeflag: kind, PAXRecords: map[string]string{"comment": "global"}}
		}
		if err := writer.WriteHeader(header); err != nil {
			t.Fatal(err)
		}
		if err := writer.Close(); err != nil {
			t.Fatal(err)
		}
		d := &tarTestDevice{records: []tarTestRecord{{mark: true}, {data: archive.Bytes()}, {mark: true}}}
		read, _ := NewTarReadSession(d, tarTestOptions(t, false, 16<<10))
		if err := read.WalkInventory(context.Background(), func(*InventoryEntry) error { return nil }); err == nil {
			t.Fatal("unsupported member accepted")
		}
	}
}
