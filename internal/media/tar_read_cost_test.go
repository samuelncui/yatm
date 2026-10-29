package media

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"io"
	"math"
	"testing"
	"time"

	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/internal/media/readplan"
	"github.com/samuelncui/yatm/internal/media/tape"
	"google.golang.org/protobuf/proto"
)

func TestTarReadStreamFileRequiresTheCanonicalArchiveName(t *testing.T) {
	// Fixed lowercase hex preserves the exact archive domain without formatting a second string per quote.
	for _, value := range []uint64{0, 1, 15, 16, 0xffffffffffffffff} {
		file, known := tarReadStreamFile(tarPrefix(value))
		if !known || file != value {
			t.Fatalf("archive %x decoded as %x, known=%v", value, file, known)
		}
	}
	for _, stream := range []string{"", "archives/1", "archives/00000000000000001", "archives/000000000000000A",
		"archives/+000000000000001", "elsewhere0000000000000001", "archives/00000000000000g1"} {
		if _, known := tarReadStreamFile(stream); known {
			t.Fatalf("noncanonical archive %q was accepted", stream)
		}
	}
}

func tarCostRange(file, start, end uint64, offset uint32) readplan.Range {
	return readplan.Range{Start: &entity.TapeBytePosition{LogicalObject: start},
		End: &entity.TapeBytePosition{LogicalObject: end, OffsetBytes: offset}, Stream: tarPrefix(file)}
}

func TestTarReadModelPricesOnlyCoveredMemberRecords(t *testing.T) {
	// A final partial wrap keeps its actual EOD while its coordinate uses a full-wrap denominator.
	profile, _ := readModelFixture()
	profile.FullWrapSeconds = proto.Float64(10)
	profile.Coefficients.Longitudinal = 0
	profile.Wraps = profile.Wraps[:2]
	profile.Wraps[0].Endpoint = proto.Uint64(100)
	profile.Wraps[1].Endpoint = proto.Uint64(150)
	profile.Wraps[1].Full = false
	profile.Wraps[1].EstimatedSpanObjects = proto.Uint64(100)
	model, err := NewReadCostModel(profile)
	if err != nil {
		t.Fatal(err)
	}
	s := &tarReadSession{options: TarSessionOptions{Mode: tape.RecordMode{MaximumBytes: 4096},
		ReadTiming: tarReadTimingFixture(), ReadModel: model}, navigator: &tarPositionDevice{bits: 64}}
	state := readplan.State{Position: readOrderPosition(150), Stream: tarPrefix(1)}
	if _, known := model.AccessScore(state.Position, readOrderPosition(149)); !known {
		t.Fatal("EOD must remain a known host-next cursor")
	}

	// The last real record is readable; the cursor and its extrapolated continuation are not data.
	for _, test := range []struct {
		name   string
		target readplan.Range
		known  bool
	}{
		{"last record", tarCostRange(1, 149, 150, 0), true},
		{"cross wrap", tarCostRange(1, 99, 101, 0), true},
		{"starts at EOD", tarCostRange(1, 150, 151, 0), false},
		{"end inside nonexistent record", tarCostRange(1, 149, 150, 1), false},
		{"beyond EOD", tarCostRange(1, 149, 151, 0), false},
	} {
		t.Run(test.name, func(t *testing.T) {
			if known := s.modelCoversReadRange(test.target); known != test.known {
				t.Fatalf("coverage=%t want=%t", known, test.known)
			}
			if _, known := s.evaluateRead(state, test.target); known != test.known {
				t.Fatalf("quote=%t want=%t", known, test.known)
			}
		})
	}

	// A declared hole cannot be hidden by individually known beginning/end coordinates.
	model.intervals[0][1].low = 101
	if s.modelCoversReadRange(tarCostRange(1, 99, 102, 0)) {
		t.Fatal("missing interior coverage was priced")
	}
}

func TestTarReadActionsPriceCompleteRecordsAndFilemarks(t *testing.T) {
	tests := []struct {
		name                  string
		state                 readplan.State
		target                readplan.Range
		method                readplan.Method
		estimate, uncertainty time.Duration
		next, cache           uint64
	}{
		{"same file gap", readplan.State{Position: readOrderPosition(101), Stream: tarPrefix(1)}, tarCostRange(1, 103, 104, 512), readplan.MethodContinue, 4 * time.Second, 1010 * time.Millisecond, 105, 104},
		{"two append boundaries", readplan.State{Position: readOrderPosition(101), Stream: tarPrefix(1)}, tarCostRange(3, 110, 111, 512), readplan.MethodContinue, 9500 * time.Millisecond, 3010 * time.Millisecond, 112, 111},
		{"cached header and unread tail", readplan.State{Position: readOrderPosition(102), Cache: readOrderPosition(101), Stream: tarPrefix(1)}, tarCostRange(1, 101, 103, 512), readplan.MethodCache, 2 * time.Second, 1010 * time.Millisecond, 104, 103},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			// The quote includes skipped records, while each FILEMARK consumes an object and no data bytes.
			s := &tarReadSession{options: TarSessionOptions{Mode: tape.RecordMode{MaximumBytes: 4096}, ReadTiming: tarReadTimingFixture()}}
			transition, known := s.evaluateRead(test.state, test.target)
			if !known || transition.Method != test.method || transition.Cost != test.estimate ||
				transition.Next.Position.LogicalObject != test.next || transition.Next.Cache.LogicalObject != test.cache || transition.Next.Stream != test.target.Stream {
				t.Fatalf("transition=%+v known=%t", transition, known)
			}
			if s.records != nil {
				t.Fatal("pure evaluation changed the reader")
			}
		})
	}
}

func TestTarReadActionsCacheOnlyNeedsNoTiming(t *testing.T) {
	// A cache hit preserves the physical cursor even for duplicate and backwards same-record members.
	s := &tarReadSession{options: TarSessionOptions{Mode: tape.RecordMode{MaximumBytes: 4096}}}
	state := readplan.State{Position: readOrderPosition(102), Cache: readOrderPosition(101), Stream: tarPrefix(1)}
	target := tarCostRange(1, 101, 101, 2048)
	transition, known := s.evaluateRead(state, target)
	if !known || transition.Method != readplan.MethodCache || transition.Cost != 0 || !sameTarReadState(state, transition.Next) {
		t.Fatalf("transition=%v known=%t", transition, known)
	}
	transition.Next.Position.LogicalObject++
	if state.Position.LogicalObject != 102 {
		t.Fatal("prediction mutated caller state")
	}

	// Object equality alone cannot prove cache ownership in another physical file.
	target.Stream = tarPrefix(2)
	if _, known := s.evaluateRead(state, target); known {
		t.Fatal("different stream reused cached bytes")
	}
}

func TestTarReadActionsMissingInputsAffectOnlyTheirMethod(t *testing.T) {
	// Continue works with no positioning model or post-LOCATE measurement; cache needs neither READ input.
	s := &tarReadSession{options: TarSessionOptions{Mode: tape.RecordMode{MaximumBytes: 4096}, ReadTiming: tarReadTimingFixture()}}
	s.options.ReadTiming.PostLocateReadOverheadSeconds = nil
	state := readplan.State{Position: readOrderPosition(101), Stream: tarPrefix(1)}
	if transition, known := s.evaluateRead(state, tarCostRange(1, 102, 103, 0)); !known || transition.Method != readplan.MethodContinue {
		t.Fatalf("continue=%v known=%t", transition, known)
	}
	s.options.ReadTiming.FilemarkOverheadSeconds = nil
	if _, known := s.evaluateRead(state, tarCostRange(2, 102, 103, 0)); known {
		t.Fatal("missing FILEMARK timing priced crossing")
	}
	if _, known := s.evaluateRead(state, tarCostRange(1, 102, 103, 0)); !known {
		t.Fatal("missing FILEMARK timing disabled same-file read")
	}
	state.Cache, state.Position = readOrderPosition(101), readOrderPosition(102)
	if transition, known := s.evaluateRead(state, tarCostRange(1, 101, 101, 1024)); !known || transition.Method != readplan.MethodCache {
		t.Fatalf("cache=%v known=%t", transition, known)
	}
}

func TestTarReadActionsRejectUnknownAndOverflow(t *testing.T) {
	tests := []struct {
		name   string
		state  readplan.State
		target readplan.Range
		change func(*tarReadSession)
	}{
		{name: "unknown stream", state: readplan.State{Position: readOrderPosition(101)}, target: tarCostRange(1, 102, 103, 0)},
		{name: "backward beyond cache", state: readplan.State{Position: readOrderPosition(103), Stream: tarPrefix(1)}, target: tarCostRange(1, 101, 102, 0)},
		{name: "more marks than objects", state: readplan.State{Position: readOrderPosition(101), Stream: tarPrefix(1)}, target: tarCostRange(3, 102, 103, 0)},
		{name: "byte overflow", state: readplan.State{Position: readOrderPosition(0), Stream: tarPrefix(1)}, target: tarCostRange(1, math.MaxUint64-1, math.MaxUint64, 0)},
		{name: "host-next overflow", state: readplan.State{Position: readOrderPosition(101), Stream: tarPrefix(1)}, target: tarCostRange(1, 102, math.MaxUint64, 1)},
		{name: "invalid offset", state: readplan.State{Position: readOrderPosition(101), Stream: tarPrefix(1)}, target: tarCostRange(1, 102, 103, 4096)},
		{name: "infinite rate", state: readplan.State{Position: readOrderPosition(101), Stream: tarPrefix(1)}, target: tarCostRange(1, 102, 103, 0), change: func(s *tarReadSession) { s.options.ReadTiming.BytesPerSecond = proto.Float64(math.Inf(1)) }},
		{name: "duration overflow", state: readplan.State{Position: readOrderPosition(101), Stream: tarPrefix(1)}, target: tarCostRange(1, 102, 103, 0), change: func(s *tarReadSession) {
			s.options.ReadTiming.BytesPerSecond = proto.Float64(math.SmallestNonzeroFloat64)
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			s := &tarReadSession{options: TarSessionOptions{Mode: tape.RecordMode{MaximumBytes: 4096}, ReadTiming: tarReadTimingFixture()}}
			if test.change != nil {
				test.change(s)
			}
			if transition, known := s.evaluateRead(test.state, test.target); known {
				t.Fatalf("unqualified transition=%v", transition)
			}
		})
	}
}

func TestTarReadActionsChargePostLocateOnce(t *testing.T) {
	// A complete distant seek includes positioning plus first READ overhead and containing-record bytes.
	s := &tarReadSession{options: TarSessionOptions{Mode: tape.RecordMode{MaximumBytes: 4096}, ReadTiming: tarReadTimingFixture(), ReadModel: tarReadModelFixture(t, nil)}, navigator: &tarPositionDevice{bits: 64}}
	from := readplan.State{Position: readOrderPosition(101), Stream: tarPrefix(1)}
	transition, known := s.evaluateRead(from, tarCostRange(1, 120, 121, 0))
	if !known || transition.Method != readplan.MethodSeek || transition.Cost != 11500*time.Millisecond {
		t.Fatalf("seek=%v known=%t", transition, known)
	}

	// A subsequent file does not itself imply a new seek or another post-LOCATE overhead.
	next, known := s.evaluateRead(transition.Next, tarCostRange(2, 122, 123, 0))
	if !known || next.Method != readplan.MethodContinue || next.Cost != 1250*time.Millisecond {
		t.Fatalf("next=%v known=%t", next, known)
	}
}

func TestTarLogicalReadContinuesAndCachesWithoutModel(t *testing.T) {
	// Explicit post-label facts permit LOGICAL execution to read ahead without a geometry model.
	d, options, members := tarPositionFixture(t, false, []int{4096}, []tarPositionMember{
		{name: "first", data: []byte("one")}, {name: "second", data: []byte("two")}})
	d.cursor = 1
	options.TapeFile, options.ReadInitialFileKnown, options.ReadInitialPosition = 1, true, readOrderPosition(101)
	options.ReadTiming = tarReadTimingFixture()
	options.ReadTiming.PostLocateReadOverheadSeconds = nil
	session, _ := NewTarReadSession(d, options)
	readPositionMember(t, session, tarPositionRequest(t, d, members[1]), members[1].data)
	readPositionMember(t, session, tarPositionRequest(t, d, members[0]), members[0].data)
	if d.reads != 1 || len(d.locates) != 0 || d.rewinds != 0 || d.spaces != 0 || d.observations != 0 {
		t.Fatalf("reads=%d locates=%v rewind=%d space=%d observations=%d", d.reads, d.locates, d.rewinds, d.spaces, d.observations)
	}
}

func TestTarReadRuntimeChoosesContinueAndThenRealSeek(t *testing.T) {
	// Each member occupies one full record, followed by one legal short trailer record.
	members := []tarPositionMember{{name: "first", data: bytes.Repeat([]byte("a"), 3584)},
		{name: "second", data: bytes.Repeat([]byte("b"), 3584)}, {name: "third", data: bytes.Repeat([]byte("c"), 3584)},
		{name: "fourth", data: bytes.Repeat([]byte("d"), 3584)}}
	d, options, members := tarPositionFixture(t, false, []int{4096}, members)
	d.cursor = 1
	options.TapeFile, options.ReadInitialFileKnown, options.ReadInitialPosition = 1, true, readOrderPosition(101)
	options.ReadTiming, options.ReadModel = tarReadTimingFixture(), tarReadModelFixture(t, nil)
	options.ReadModel.coefficients.Base = 0.01
	session, _ := NewTarReadSession(d, options)
	readPositionMember(t, session, tarPositionRequest(t, d, members[0]), members[0].data)
	if d.reads != 1 || len(d.locates) != 0 {
		t.Fatalf("first reads=%d locates=%v", d.reads, d.locates)
	}
	readPositionMember(t, session, tarPositionRequest(t, d, members[3]), members[3].data)
	if d.reads != 2 || len(d.locates) != 1 || d.locates[0] != 104 {
		t.Fatalf("selected reads=%d locates=%v", d.reads, d.locates)
	}
	if session.(*tarReadSession).readOrderDisabled {
		t.Fatal("exact supported record consumption withdrew model")
	}
}

func TestTarReadInitialFactsAreExplicitAndCopied(t *testing.T) {
	d, options, _ := tarPositionFixture(t, false, []int{4096}, []tarPositionMember{{name: "first", data: []byte("x")}})
	options.TapeFile, options.ReadInitialFileKnown = 1, true
	options.ReadTiming = tarReadTimingFixture()
	session, _ := NewTarReadSession(d, options)
	s := session.(*tarReadSession)
	if state := s.readState(); state.Stream != tarPrefix(1) || state.Position != nil || state.Cache != nil {
		t.Fatalf("initial=%v", state)
	}
	options.ReadTiming.BytesPerSecond = proto.Float64(1)
	if s.options.ReadTiming.GetBytesPerSecond() != 4096 {
		t.Fatal("caller mutated session timing")
	}

	// A supplied host-next identity is copied, while an arbitrary constructor invents no facts.
	options.ReadInitialPosition = readOrderPosition(101)
	session, _ = NewTarReadSession(d, options)
	options.ReadInitialPosition.LogicalObject = 999
	if session.(*tarReadSession).readState().Position.LogicalObject != 101 {
		t.Fatal("caller mutated initial position")
	}
	options.ReadInitialFileKnown = false
	options.ReadInitialPosition = nil
	session, _ = NewTarReadSession(d, options)
	if state := session.(*tarReadSession).readState(); state.Position != nil || state.Stream != "" || state.Cache != nil {
		t.Fatalf("arbitrary constructor state=%v", state)
	}
}

func TestTarReadThroughFailureStopsLaneAndStillCleansUp(t *testing.T) {
	// The chosen Continue action must preserve the existing physical read failure and cleanup lifecycle.
	d, options, members := tarPositionFixture(t, false, []int{4096}, []tarPositionMember{{name: "first", data: bytes.Repeat([]byte("a"), 8192)}})
	d.cursor, d.readFailure, d.readErr = 1, 2, tape.ErrUncertain
	options.TapeFile, options.ReadInitialFileKnown, options.ReadInitialPosition = 1, true, readOrderPosition(101)
	options.ReadTiming = tarReadTimingFixture()
	session, _ := NewTarReadSession(d, options)
	source, _ := session.Source(tarPositionRequest(t, d, members[0]))
	reader, err := source.Content.Open(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, err = io.Copy(io.Discard, reader); !errors.Is(err, tape.ErrUncertain) {
		t.Fatalf("read error=%v", err)
	}
	if err = reader.Close(); !errors.Is(err, tape.ErrUncertain) {
		t.Fatalf("close error=%v", err)
	}
	reads := d.reads
	if _, err = source.Content.Open(context.Background()); !errors.Is(err, ErrReadUnavailable) || d.reads != reads {
		t.Fatalf("next error=%v reads=%d/%d", err, d.reads, reads)
	}
	if err = session.Finalize(context.Background(), nil); err != nil || d.cleanup != 1 {
		t.Fatalf("finalize=%v cleanup=%d", err, d.cleanup)
	}
}

func tarReadAppendFixture(t *testing.T) (*tarPositionDevice, TarSessionOptions, []tarPositionMember, ReadRequest) {
	t.Helper()
	// Build two independently terminated archives separated by a real APPEND filemark.
	d, options, members := tarPositionFixture(t, false, []int{4096}, []tarPositionMember{{name: "first", data: bytes.Repeat([]byte("a"), 3584)}})
	var content bytes.Buffer
	writer := tar.NewWriter(&content)
	name := tarPrefix(2) + "/next"
	if err := writer.WriteHeader(&tar.Header{Name: name, Mode: 0640, Size: 3, Format: tar.FormatPAX}); err != nil {
		t.Fatal(err)
	}
	if _, err := writer.Write([]byte("two")); err != nil {
		t.Fatal(err)
	}
	if err := writer.Flush(); err != nil {
		t.Fatal(err)
	}
	end := uint32(content.Len())
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	object := d.base + uint64(len(d.records))
	d.records = append(d.records, tarTestRecord{data: bytes.Clone(content.Bytes())}, tarTestRecord{mark: true})
	d.cursor = 1
	options.TapeFile, options.ReadInitialFileKnown, options.ReadInitialPosition = 1, true, readOrderPosition(101)
	options.ReadTiming = tarReadTimingFixture()
	options.Navigator = nil
	locator := &entity.TarMetadata{TapeFile: 2, Header: readOrderPosition(object), End: &entity.TapeBytePosition{LogicalObject: object, OffsetBytes: end}}
	request := ReadRequest{Path: name, Storage: &entity.StoragePosition{Order: entity.TarStorageOrder(locator), Metadata: locator.Pack()}}
	return d, options, members, request
}

func TestTarReadThroughAppendUsesActualFilemarkAndResetsRelativeBytes(t *testing.T) {
	// Reading across APPEND consumes the first file's actual short trailer, FILEMARK and next file bytes.
	d, options, members, next := tarReadAppendFixture(t)
	session, _ := NewTarReadSession(d, options)
	readPositionMember(t, session, tarPositionRequest(t, d, members[0]), members[0].data)
	readPositionMember(t, session, next, []byte("two"))
	state := session.(*tarReadSession)
	if d.reads != 4 || len(d.locates) != 0 || d.rewinds != 0 || d.spaces != 0 || state.file != 2 || state.records.offset != 1024 || state.readOrderDisabled {
		t.Fatalf("reads=%d locates=%v file=%d offset=%d disabled=%t", d.reads, d.locates, state.file, state.records.offset, state.readOrderDisabled)
	}
	if current := state.readState(); current.Stream != tarPrefix(2) || current.Position.LogicalObject != 105 || current.Cache.LogicalObject != 104 {
		t.Fatalf("current=%v", current)
	}
}

func TestTarReadThroughAppendRejectsCorruptionAndUnexpectedEOD(t *testing.T) {
	for _, mode := range []string{"corrupt trailer", "missing filemark", "read failure"} {
		t.Run(mode, func(t *testing.T) {
			// A failed boundary never silently becomes a predicted new file or permits another source.
			d, options, members, next := tarReadAppendFixture(t)
			switch mode {
			case "corrupt trailer":
				d.records[2].data[1000] = 1
			case "missing filemark":
				d.records = d.records[:3]
			case "read failure":
				d.readFailure, d.readErr = 2, tape.ErrUncertain
			}
			session, _ := NewTarReadSession(d, options)
			readPositionMember(t, session, tarPositionRequest(t, d, members[0]), members[0].data)
			source, _ := session.Source(next)
			if reader, err := source.Content.Open(context.Background()); err == nil || reader != nil {
				t.Fatalf("open reader=%v err=%v", reader, err)
			}
			reads := d.reads
			if _, err := source.Content.Open(context.Background()); !errors.Is(err, ErrReadUnavailable) || d.reads != reads {
				t.Fatalf("next error=%v reads=%d/%d", err, d.reads, reads)
			}
			if len(d.locates) != 0 {
				t.Fatalf("failed continuation retried through LOCATE: %v", d.locates)
			}
			if err := session.Finalize(context.Background(), nil); err != nil || d.cleanup != 1 {
				t.Fatalf("cleanup=%d err=%v", d.cleanup, err)
			}
		})
	}
}

func TestTarReadContinueChecksPhysicalHeaderAndEnd(t *testing.T) {
	for _, mode := range []string{"header", "end"} {
		t.Run(mode, func(t *testing.T) {
			// Cost selection cannot override the parsed target header or its exact closing byte anchor.
			d, options, members := tarPositionFixture(t, false, []int{4096}, []tarPositionMember{{name: "first", data: []byte("x")}})
			d.cursor = 1
			options.TapeFile, options.ReadInitialFileKnown, options.ReadInitialPosition = 1, true, readOrderPosition(101)
			options.ReadTiming, options.ReadModel = tarReadTimingFixture(), tarReadModelFixture(t, nil)
			request := tarPositionRequest(t, d, members[0])
			locator := request.Storage.Metadata.GetTar()
			if mode == "header" {
				locator.Header.LogicalObject++
				locator.End.LogicalObject++
			} else {
				locator.End.OffsetBytes += 512
			}
			request.Storage.Order = entity.TarStorageOrder(locator)
			session, _ := NewTarReadSession(d, options)
			source, err := session.Source(request)
			if err != nil {
				t.Fatal(err)
			}
			reader, err := source.Content.Open(context.Background())
			if mode == "header" {
				if err == nil || reader != nil {
					t.Fatalf("header accepted reader=%v err=%v", reader, err)
				}
			} else {
				if err != nil {
					t.Fatal(err)
				}
				if _, err := io.Copy(io.Discard, reader); err != nil {
					t.Fatal(err)
				}
				if err := reader.Close(); err == nil {
					t.Fatal("corrupt end accepted")
				}
			}
			if session.(*tarReadSession).failed == nil || len(d.locates) != 0 {
				t.Fatalf("failure=%v locates=%v", session.(*tarReadSession).failed, d.locates)
			}
		})
	}
}

func TestTarReadActionRetainsActualFinalShortRecord(t *testing.T) {
	// The writer's last record may be short, but host-next and exact member bytes still match the prediction.
	d, options, members := tarPositionFixture(t, false, []int{4096}, []tarPositionMember{{name: "first", data: bytes.Repeat([]byte("a"), 6000)}})
	d.cursor = 1
	options.TapeFile, options.ReadInitialFileKnown, options.ReadInitialPosition = 1, true, readOrderPosition(101)
	options.ReadTiming = tarReadTimingFixture()
	session, _ := NewTarReadSession(d, options)
	readPositionMember(t, session, tarPositionRequest(t, d, members[0]), members[0].data)
	state := session.(*tarReadSession)
	if d.reads != 2 || state.records.length >= 4096 || state.readState().Position.LogicalObject != 103 || state.readOrderDisabled {
		t.Fatalf("reads=%d actual length=%d state=%v disabled=%t", d.reads, state.records.length, state.readState(), state.readOrderDisabled)
	}
}

func TestTarReadExternalShortSubdivisionWithdrawsOnlyEstimates(t *testing.T) {
	// Ordinary reads retain actual external record lengths; unsupported subdivision invalidates elapsed-time accounting.
	d, options, members := tarPositionFixture(t, false, []int{512}, []tarPositionMember{{name: "first", data: []byte("one")}, {name: "next", data: []byte("two")}})
	d.cursor = 1
	options.TapeFile, options.ReadInitialFileKnown, options.ReadInitialPosition = 1, true, readOrderPosition(101)
	options.ReadTiming = tarReadTimingFixture()
	session, _ := NewTarReadSession(d, options)
	readPositionMember(t, session, tarPositionRequest(t, d, members[0]), members[0].data)
	if !session.(*tarReadSession).readOrderDisabled || session.(*tarReadSession).failed != nil {
		t.Fatal("unsupported subdivision did not withdraw only estimates")
	}
	readPositionMember(t, session, tarPositionRequest(t, d, members[1]), members[1].data)
}

func TestTarReadThroughAppendPermitsCleanTruncatedTail(t *testing.T) {
	// APPEND and inventory permit a readable unfinished member ending at a proven FILEMARK.
	d, options, members, next := tarReadAppendFixture(t)
	var unfinished bytes.Buffer
	writer := tar.NewWriter(&unfinished)
	if err := writer.WriteHeader(&tar.Header{Name: tarPrefix(1) + "/unfinished", Mode: 0640, Size: 10000, Format: tar.FormatPAX}); err != nil {
		t.Fatal(err)
	}
	if _, err := writer.Write(bytes.Repeat([]byte("u"), 3584)); err != nil {
		t.Fatal(err)
	}
	if unfinished.Len() != 4096 {
		t.Fatalf("unfinished full record bytes=%d", unfinished.Len())
	}
	d.records[2].data = bytes.Clone(unfinished.Bytes())
	session, _ := NewTarReadSession(d, options)
	readPositionMember(t, session, tarPositionRequest(t, d, members[0]), members[0].data)
	readPositionMember(t, session, next, []byte("two"))
	if d.reads != 4 || len(d.locates) != 0 || session.(*tarReadSession).failed != nil {
		t.Fatalf("reads=%d locates=%v failed=%v", d.reads, d.locates, session.(*tarReadSession).failed)
	}
}

func TestTarReadOrderScoresContainingRecordWithoutChangingAnchors(t *testing.T) {
	// Exact member offsets do not prevent coordinate ranking or indexed candidate construction.
	d, options, members := tarPositionFixture(t, false, []int{4096}, []tarPositionMember{{name: "first", data: []byte("one")}, {name: "next", data: []byte("two")}})
	options.ReadOrder, options.ReadModel = entity.TapeReadOrder_TAPE_READ_ORDER_AUTO, tarReadModelFixture(t, nil)
	session, _ := NewTarReadSession(d, options)
	provider, _, _ := session.(OrderedReadSession).ReadOrder(context.Background())
	intervals, known := provider.Ranges(tarPositionRequest(t, d, members[1]))
	if !known || intervals[0].Start.OffsetBytes == 0 {
		t.Fatalf("nonzero target offset missing: %v", intervals)
	}
	before := proto.Clone(intervals[0].Start)
	if _, known := provider.(*tarReadOrderProvider).ReadIndex(intervals[0].Start); !known {
		t.Fatal("exact offset disabled index")
	}
	if !proto.Equal(before, intervals[0].Start) {
		t.Fatal("ranking mutated exact member anchor")
	}
}

func TestTarReadTimingAloneDoesNotReplaceEligibleSeekWithGapReads(t *testing.T) {
	// READ-rate evidence alone cannot prove that discarding a forward gap beats an available LOCATE.
	d, options, members := tarPositionFixture(t, false, []int{4096}, []tarPositionMember{
		{name: "first", data: bytes.Repeat([]byte("a"), 3584)}, {name: "gap", data: bytes.Repeat([]byte("b"), 3584)},
		{name: "selected", data: bytes.Repeat([]byte("c"), 3584)}})
	d.cursor = 1
	options.TapeFile, options.ReadInitialFileKnown, options.ReadInitialPosition = 1, true, readOrderPosition(101)
	options.ReadTiming = tarReadTimingFixture()
	session, _ := NewTarReadSession(d, options)
	readPositionMember(t, session, tarPositionRequest(t, d, members[2]), members[2].data)
	if d.reads != 1 || len(d.locates) != 1 || d.locates[0] != 103 {
		t.Fatalf("reads=%d locates=%v", d.reads, d.locates)
	}
}

func TestTarReadTimingAloneDoesNotReplaceEligibleSeekAcrossAppend(t *testing.T) {
	// A missing positioning quote also cannot justify reading the prior trailer and FILEMARK.
	d, options, members, next := tarReadAppendFixture(t)
	options.Navigator = d
	session, _ := NewTarReadSession(d, options)
	readPositionMember(t, session, tarPositionRequest(t, d, members[0]), members[0].data)
	readPositionMember(t, session, next, []byte("two"))
	if d.reads != 2 || len(d.locates) != 1 || d.locates[0] != 104 {
		t.Fatalf("reads=%d locates=%v", d.reads, d.locates)
	}
}

func TestTarReadCompleteQuotesCanSelectReadThroughGap(t *testing.T) {
	// With both complete quotes, reading a small gap is cheaper than the qualified ten-second seek.
	d, options, members := tarPositionFixture(t, false, []int{4096}, []tarPositionMember{
		{name: "first", data: bytes.Repeat([]byte("a"), 3584)}, {name: "gap", data: bytes.Repeat([]byte("b"), 3584)},
		{name: "selected", data: bytes.Repeat([]byte("c"), 3584)}})
	d.cursor = 1
	options.TapeFile, options.ReadInitialFileKnown, options.ReadInitialPosition = 1, true, readOrderPosition(101)
	options.ReadTiming, options.ReadModel = tarReadTimingFixture(), tarReadModelFixture(t, nil)
	session, _ := NewTarReadSession(d, options)
	readPositionMember(t, session, tarPositionRequest(t, d, members[2]), members[2].data)
	if d.reads != 3 || len(d.locates) != 0 {
		t.Fatalf("reads=%d locates=%v", d.reads, d.locates)
	}
}
