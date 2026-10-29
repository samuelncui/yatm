package media

import (
	"bytes"
	"context"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/internal/media/readplan"
)

func TestTarReadSequenceDenseRetainedRecordKeepsLogical(t *testing.T) {
	// Software-only facts cover the real provider/planner/source path, without qualifying a drive model.
	d, options, members := tarPositionFixture(t, false, []int{4096}, []tarPositionMember{
		{name: "first", data: []byte("one")}, {name: "second", data: []byte("two")}})
	d.cursor = 1
	options.TapeFile, options.ReadInitialFileKnown, options.ReadInitialPosition = 1, true, readOrderPosition(101)
	options.ReadOrder = entity.TapeReadOrder_TAPE_READ_ORDER_AUTO
	options.ReadTiming, options.ReadModel = tarReadTimingFixture(), tarReadModelFixture(t, nil)
	session, err := NewTarReadSession(d, options)
	if err != nil {
		t.Fatal(err)
	}
	requests := []ReadRequest{tarPositionRequest(t, d, members[0]), tarPositionRequest(t, d, members[1]), tarPositionRequest(t, d, members[0])}
	ids := tarReadSequenceActual(t, session, requests, [][]byte{members[0].data, members[1].data, members[0].data}, "LOGICAL:")
	if !reflect.DeepEqual(ids, []int64{1, 2, 3}) || d.reads != 1 || len(d.locates) != 0 || d.observations != 0 {
		t.Fatalf("ids=%v reads=%d locates=%v observations=%d", ids, d.reads, d.locates, d.observations)
	}
}

func TestTarReadSequenceSparseInitialCursorAdoptsAutoAndReadsExactMembers(t *testing.T) {
	// Explicit synthetic geometry/timing supply a software-only route comparison, not factory qualification.
	members := make([]tarPositionMember, 60)
	for i := range members {
		members[i] = tarPositionMember{name: fmt.Sprintf("member-%02d", i), data: bytes.Repeat([]byte{byte(i)}, 3584)}
	}
	d, options, members := tarPositionFixture(t, false, []int{4096}, members)
	options.ReadOrder = entity.TapeReadOrder_TAPE_READ_ORDER_AUTO
	options.ReadTiming, options.ReadModel = tarReadTimingFixture(), tarReadModelFixture(t, nil)
	options.ReadModel.coefficients.Longitudinal = 1
	session, err := NewTarReadSession(d, options)
	if err != nil {
		t.Fatal(err)
	}

	// A real completed source establishes the non-BOT initial byte cursor and retained record.
	readPositionMember(t, session, tarPositionRequest(t, d, members[38]), members[38].data)
	state := session.(*tarReadSession).readState()
	if state.Position.LogicalObject != 140 || state.Cache.LogicalObject != 139 {
		t.Fatalf("initial=%v", state)
	}
	d.reads, d.observations, d.locates = 0, 0, nil
	requests := []ReadRequest{tarPositionRequest(t, d, members[0]), tarPositionRequest(t, d, members[20]), tarPositionRequest(t, d, members[40])}
	ids := tarReadSequenceActual(t, session, requests, [][]byte{members[0].data, members[20].data, members[40].data}, "AUTO:")
	if !reflect.DeepEqual(ids, []int64{3, 2, 1}) || d.reads != 4 || !reflect.DeepEqual(d.locates, []uint64{121, 101}) || d.observations != 0 {
		t.Fatalf("ids=%v reads=%d locates=%v observations=%d", ids, d.reads, d.locates, d.observations)
	}
}

func tarReadSequenceActual(t *testing.T, session InventorySession, requests []ReadRequest, contents [][]byte, reason string) []int64 {
	t.Helper()
	// Freeze exact ranges through the real provider before the complete SQLite plan is constructed.
	provider, _, err := session.(OrderedReadSession).ReadOrder(context.Background())
	if err != nil || provider == nil {
		t.Fatalf("provider=%v err=%v", provider, err)
	}
	spans := make([]readplan.Span, len(requests))
	for i, request := range requests {
		ranges, known := provider.Ranges(request)
		if !known {
			t.Fatalf("request %d range unavailable", i)
		}
		spans[i] = readplan.Span{ID: int64(i + 1), Ranges: ranges}
	}
	device := session.(*tarReadSession).device.(*tarPositionDevice)
	reads, locates, observations := device.reads, len(device.locates), device.observations
	root := t.TempDir()
	sequence, err := readplan.Plan(context.Background(), root, provider, sequenceTestInput(spans))
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := sequence.Close(); err != nil {
			t.Error(err)
		}
		requireSequenceRootEmpty(t, root)
	}()
	if !strings.HasPrefix(sequence.Reason(), reason) {
		t.Fatalf("reason=%q expected=%q", sequence.Reason(), reason)
	}
	if device.reads != reads || len(device.locates) != locates || device.observations != observations {
		t.Fatal("planning issued physical reads or navigation")
	}

	// Execute the returned whole-selection IDs, comparing every planned next state with actual settled state.
	var ids []int64
	state, known, err := provider.Current(context.Background())
	if err != nil || !known {
		t.Fatalf("initial state=%v known=%t err=%v", state, known, err)
	}
	for {
		batch, err := sequence.Next(context.Background(), 1)
		if err != nil {
			t.Fatal(err)
		}
		if len(batch) == 0 {
			break
		}
		id := batch[0]
		if id < 1 || id > int64(len(requests)) {
			t.Fatalf("unexpected returned ID=%d", id)
		}
		transition, known := provider.Evaluate(state, spans[id-1].Ranges[0])
		if !known {
			t.Fatalf("ID=%d lost complete transition from %v", id, state)
		}
		readPositionMember(t, session, requests[id-1], contents[id-1])
		actual, known, err := provider.Current(context.Background())
		if err != nil || !known || !sameTarReadState(actual, transition.Next) || session.(*tarReadSession).readOrderDisabled {
			t.Fatalf("ID=%d predicted=%v actual=%v known=%t disabled=%t err=%v", id, transition.Next, actual, known, session.(*tarReadSession).readOrderDisabled, err)
		}
		state = actual
		ids = append(ids, id)
	}
	if len(ids) != len(requests) {
		t.Fatalf("returned IDs=%v expected count=%d", ids, len(requests))
	}
	return ids
}
