package media

import (
	"context"
	"errors"
	"io"
	"math"
	"testing"

	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/internal/media/readplan"
	"github.com/samuelncui/yatm/internal/media/tape"
	"google.golang.org/protobuf/proto"
)

func tarReadModelFixture(t *testing.T, valid func() bool) *ReadCostModel {
	t.Helper()
	if valid == nil {
		valid = func() bool { return true }
	}
	model, err := NewReadCostModel(ReadModelProfile{ID: "software-only tar fixture", SnapshotValid: valid,
		Boundary: ReadEndpointExclusive, BOT: map[uint32]uint64{0: 0}, MinLPOS: 0, MaxLPOS: 10000,
		Wraps:        []ReadModelWrap{{Partition: 0, Wrap: 7, Band: 2, Direction: ReadWrapForward, Full: true, Endpoint: proto.Uint64(10000)}},
		Coefficients: ReadModelCoefficients{Base: 10}})
	if err != nil {
		t.Fatal(err)
	}
	return model
}

func tarReadTimingFixture() *entity.TapeReadTiming {
	return &entity.TapeReadTiming{BytesPerSecond: proto.Float64(4096), PostLocateReadOverheadSeconds: proto.Float64(0.5),
		FilemarkOverheadSeconds: proto.Float64(0.25)}
}

func TestTarReadFinishWithdrawsWithSessionState(t *testing.T) {
	for _, state := range []string{"failed", "active", "finalized", "consumption-deviation", "invalidated-map"} {
		t.Run(state, func(t *testing.T) {
			d, options, _ := tarPositionFixture(t, false, []int{4096}, []tarPositionMember{{name: "file", data: []byte("x")}})
			valid := true
			options.ReadModel = tarReadModelFixture(t, func() bool { return valid })
			options.ReadCompletion = &ReadCompletion{FullSpanSeconds: 10}
			options.Finish = func(context.Context) error { t.Fatal("cost evaluation performed physical release"); return nil }
			session, err := NewTarReadSession(d, options)
			if err != nil {
				t.Fatal(err)
			}
			s := session.(*tarReadSession)
			provider := &tarReadOrderProvider{session: s}
			from := readplan.State{Position: &entity.TapeBytePosition{LogicalObject: 100}}
			if _, known := provider.Finish(from); !known {
				t.Fatal("ready Session has no completion estimate")
			}
			switch state {
			case "failed":
				s.failed = io.ErrUnexpectedEOF
			case "active":
				s.active = true
			case "finalized":
				s.finalized = true
			case "consumption-deviation":
				s.readOrderDisabled = true
			case "invalidated-map":
				valid = false
			}
			if _, known := provider.Finish(from); known {
				t.Fatal("unavailable Session still quoted completion")
			}
			if d.observations != 0 {
				t.Fatal("completion estimate queried the device")
			}
		})
	}
}

func TestTarReadOrderFallbackDoesNotObservePosition(t *testing.T) {
	for _, mode := range []entity.TapeReadOrder{entity.TapeReadOrder_TAPE_READ_ORDER_LOGICAL, entity.TapeReadOrder_TAPE_READ_ORDER_AUTO} {
		// Missing positioning evidence preserves the baseline without an optional cursor query.
		d, options, _ := tarPositionFixture(t, false, []int{4096}, []tarPositionMember{{name: "first", data: []byte("x")}})
		d.positionErr = tape.ErrUncertain
		options.ReadOrder, options.ReadOrderReason = mode, "positioning remains unqualified"
		session, err := NewTarReadSession(d, options)
		if err != nil {
			t.Fatal(err)
		}
		provider, reason, err := session.(OrderedReadSession).ReadOrder(context.Background())
		if err != nil || provider != nil || reason == "" || d.observations != 0 {
			t.Fatalf("fallback=%v reason=%q err=%v observations=%d", provider, reason, err, d.observations)
		}
	}
}

func TestTarReadOrderTracksExactAnchorsAndSharedRecord(t *testing.T) {
	// One actual record can satisfy a later or earlier member with no motion or measured timing.
	d, options, members := tarPositionFixture(t, false, []int{4096}, []tarPositionMember{
		{name: "first", data: []byte("one")}, {name: "second", data: []byte("two")}})
	options.ReadOrder, options.ReadModel = entity.TapeReadOrder_TAPE_READ_ORDER_AUTO, tarReadModelFixture(t, nil)
	session, _ := NewTarReadSession(d, options)
	provider, _, _ := session.(OrderedReadSession).ReadOrder(context.Background())
	first := tarPositionRequest(t, d, members[0])
	ranges, ok := provider.Ranges(first)
	if !ok || len(ranges) != 1 || !proto.Equal(ranges[0].Start, first.Storage.Metadata.GetTar().Header) ||
		!proto.Equal(ranges[0].End, first.Storage.Metadata.GetTar().End) || ranges[0].Stream != tarPrefix(1) {
		t.Fatalf("exact range=%v known=%t", ranges, ok)
	}
	readPositionMember(t, session, first, members[0].data)

	// Current reports the host-next object separately from the retained containing record.
	before := d.observations
	current, known, err := provider.Current(context.Background())
	if err != nil || !known || current.Position.LogicalObject != 102 || current.Cache.LogicalObject != 101 ||
		current.Stream != tarPrefix(1) || d.observations != before {
		t.Fatalf("current=%v known=%t err=%v", current, known, err)
	}
	intervals, _ := provider.Ranges(tarPositionRequest(t, d, members[1]))
	transition, known := provider.Evaluate(current, intervals[0])
	if !known || transition.Method != readplan.MethodCache || transition.Cost != (0) || !sameTarReadState(current, transition.Next) {
		t.Fatalf("cached transition=%v known=%t", transition, known)
	}
	reads, locates := d.reads, len(d.locates)
	readPositionMember(t, session, tarPositionRequest(t, d, members[1]), members[1].data)
	readPositionMember(t, session, first, members[0].data)
	if d.reads != reads || len(d.locates) != locates {
		t.Fatalf("shared record reread: reads=%d/%d locates=%d/%d", d.reads, reads, len(d.locates), locates)
	}
}

func TestTarReadOrderCurrentDoesNotInventStreamOrCache(t *testing.T) {
	// An arbitrary constructor may observe an object, but it cannot turn that into a file or cached bytes.
	d, options, _ := tarPositionFixture(t, false, []int{4096}, []tarPositionMember{{name: "first", data: []byte("x")}})
	options.ReadOrder, options.ReadModel = entity.TapeReadOrder_TAPE_READ_ORDER_AUTO, tarReadModelFixture(t, nil)
	session, _ := NewTarReadSession(d, options)
	provider, _, _ := session.(OrderedReadSession).ReadOrder(context.Background())
	state, known, err := provider.Current(context.Background())
	if err != nil || !known || state.Position.LogicalObject != 100 || state.Stream != "" || state.Cache != nil || d.observations != 1 {
		t.Fatalf("state=%v known=%t err=%v observations=%d", state, known, err, d.observations)
	}
}

func TestTarReadOrderShortRecordBoundaryAndIneligibleLocators(t *testing.T) {
	// Planning retains the exact short-record endpoint rather than manufacturing its byte length.
	d, options, members := tarPositionFixture(t, false, []int{512}, []tarPositionMember{{name: "first", data: []byte("x")}})
	options.ReadOrder, options.ReadModel = entity.TapeReadOrder_TAPE_READ_ORDER_AUTO, tarReadModelFixture(t, nil)
	session, _ := NewTarReadSession(d, options)
	provider, _, _ := session.(OrderedReadSession).ReadOrder(context.Background())
	request := tarPositionRequest(t, d, members[0])
	intervals, ok := provider.Ranges(request)
	if !ok || !proto.Equal(intervals[0].End, request.Storage.Metadata.GetTar().End) {
		t.Fatalf("range=%v", intervals)
	}

	// Missing and unrepresentable optional coordinates cannot enter optimized planning.
	request.Storage.Metadata.GetTar().Header, request.Storage.Metadata.GetTar().End = nil, nil
	if _, ok := provider.Ranges(request); ok {
		t.Fatal("sequential locator acquired physical range")
	}
	request = tarPositionRequest(t, d, members[0])
	d.bits = 32
	request.Storage.Metadata.GetTar().Header.LogicalObject = math.MaxUint32
	request.Storage.Metadata.GetTar().End.LogicalObject = math.MaxUint32
	request.Storage.Metadata.GetTar().End.OffsetBytes = 1
	if _, ok := provider.Ranges(request); ok {
		t.Fatal("read-ahead address overflow accepted")
	}
}

func TestTarReadOrderUncertainCurrentStopsPreparation(t *testing.T) {
	d, options, _ := tarPositionFixture(t, false, []int{4096}, []tarPositionMember{{name: "first", data: []byte("x")}})
	options.ReadOrder, options.ReadModel = entity.TapeReadOrder_TAPE_READ_ORDER_AUTO, tarReadModelFixture(t, nil)
	d.positionErr = tape.ErrUncertain
	session, _ := NewTarReadSession(d, options)
	provider, _, _ := session.(OrderedReadSession).ReadOrder(context.Background())
	if _, known, err := provider.Current(context.Background()); known || !errors.Is(err, ErrReadUnavailable) || !errors.Is(err, tape.ErrUncertain) {
		t.Fatalf("current known=%t err=%v", known, err)
	}
}

func TestTarReadOrderRejectsAnInvalidatedModelBeforeObserving(t *testing.T) {
	// Snapshot invalidation affects provider admission before any cursor query.
	d, options, _ := tarPositionFixture(t, false, []int{4096}, []tarPositionMember{{name: "first", data: []byte("x")}})
	valid := true
	options.ReadOrder, options.ReadModel = entity.TapeReadOrder_TAPE_READ_ORDER_AUTO, tarReadModelFixture(t, func() bool { return valid })
	session, _ := NewTarReadSession(d, options)
	valid = false
	provider, reason, err := session.(OrderedReadSession).ReadOrder(context.Background())
	if err != nil || provider != nil || reason == "" || d.observations != 0 {
		t.Fatalf("provider=%v reason=%q err=%v", provider, reason, err)
	}
}

func TestTarReadOrderWithdrawsAfterSafeConsumptionDeviation(t *testing.T) {
	for _, mode := range []string{"different host-next", "unknown host-next", "unanchored member"} {
		t.Run(mode, func(t *testing.T) {
			// Keep actual member bytes intact while withdrawing only the optional consumption model.
			d, options, members := tarPositionFixture(t, false, []int{4096}, []tarPositionMember{
				{name: "first", data: []byte("one")}, {name: "second", data: []byte("two")}})
			options.ReadOrder, options.ReadModel = entity.TapeReadOrder_TAPE_READ_ORDER_AUTO, tarReadModelFixture(t, nil)
			session, _ := NewTarReadSession(d, options)
			provider, _, _ := session.(OrderedReadSession).ReadOrder(context.Background())
			request := tarPositionRequest(t, d, members[0])
			if mode == "unanchored member" {
				locator := request.Storage.Metadata.GetTar()
				locator.Header, locator.End = nil, nil
				request.Storage.Order = entity.TarStorageOrder(locator)
			}
			source, _ := session.Source(request)
			reader, err := source.Content.Open(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if _, err := io.Copy(io.Discard, reader); err != nil {
				t.Fatal(err)
			}
			state := session.(*tarReadSession)
			switch mode {
			case "different host-next":
				state.records.nextObject++
			case "unknown host-next":
				state.records.nextKnown = false
			}
			if err := reader.Close(); err != nil {
				t.Fatal(err)
			}

			// Withdrawal keeps the existing verified-byte reading path available.
			next := tarPositionRequest(t, d, members[1])
			if _, known := provider.Ranges(next); known {
				t.Fatal("withdrawn ranges remain available")
			}
			if current, known, err := provider.Current(context.Background()); known || current.Position != nil || err != nil {
				t.Fatalf("current=%v known=%t err=%v", current, known, err)
			}
			if _, known := usableReadScore(provider, readOrderPosition(102), readOrderPosition(200)); known {
				t.Fatal("withdrawn score remains available")
			}
			readPositionMember(t, session, next, members[1].data)
			if !state.readOrderDisabled || state.failed != nil {
				t.Fatalf("disabled=%t failed=%v", state.readOrderDisabled, state.failed)
			}
		})
	}
}

func TestTarReadOrderCurrentChecksExplicitFileBeforeUnknownObjectFallback(t *testing.T) {
	tests := []struct {
		name                 string
		position             tape.Position
		contradiction, known bool
	}{
		{name: "beginning with known object", position: tape.Position{Beginning: true, ObjectKnown: true, Object: 101}, contradiction: true},
		{name: "beginning with unknown object", position: tape.Position{Beginning: true}, contradiction: true},
		{name: "different file with known object", position: tape.Position{FileKnown: true, File: 2, ObjectKnown: true, Object: 101}, contradiction: true},
		{name: "different file with unknown object", position: tape.Position{FileKnown: true, File: 2, Partition: math.MaxUint32, Object: math.MaxUint64}, contradiction: true},
		{name: "unknown file with known object", position: tape.Position{File: 999, ObjectKnown: true, Object: 101}, known: true},
		{name: "unknown file with unknown object", position: tape.Position{File: 999, Partition: math.MaxUint32, Object: math.MaxUint64}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			// A completed label supplies file1; unknown optional object fields cannot hide contrary physical evidence.
			d, options, members := tarPositionFixture(t, false, []int{4096}, []tarPositionMember{{name: "first", data: []byte("x")}})
			d.cursor, d.reported = 1, &test.position
			options.TapeFile, options.ReadInitialFileKnown = 1, true
			options.ReadOrder, options.ReadModel = entity.TapeReadOrder_TAPE_READ_ORDER_AUTO, tarReadModelFixture(t, nil)
			session, err := NewTarReadSession(d, options)
			if err != nil {
				t.Fatal(err)
			}
			provider, _, err := session.(OrderedReadSession).ReadOrder(context.Background())
			if err != nil || provider == nil {
				t.Fatalf("provider=%v err=%v", provider, err)
			}
			state, known, err := provider.Current(context.Background())
			if test.contradiction {
				if !errors.Is(err, ErrReadUnavailable) || known {
					t.Fatalf("state=%v known=%t err=%v", state, known, err)
				}
				source, sourceErr := session.Source(tarPositionRequest(t, d, members[0]))
				if sourceErr != nil {
					t.Fatal(sourceErr)
				}
				if _, err := source.Content.Open(context.Background()); !errors.Is(err, ErrReadUnavailable) || d.observations != 1 || d.reads != 0 || len(d.locates) != 0 {
					t.Fatalf("next error=%v observations=%d reads=%d locates=%v", err, d.observations, d.reads, d.locates)
				}
				return
			}

			// An unspecified File flag cannot override known file1, and valid object evidence remains usable.
			if err != nil || known != test.known || session.(*tarReadSession).failed != nil {
				t.Fatalf("state=%v known=%t err=%v", state, known, err)
			}
			if known && (state.Position.LogicalObject != 101 || state.Stream != tarPrefix(1) || state.Cache != nil) {
				t.Fatalf("current=%v", state)
			}
		})
	}
}
