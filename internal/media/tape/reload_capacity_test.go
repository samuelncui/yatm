package tape

import (
	"context"
	"encoding/hex"
	"errors"
	"reflect"
	"testing"

	"github.com/samuelncui/yatm/entity"
)

func TestReloadCapacityTransportAcceptsExpectedHeldResponse(t *testing.T) {
	// Replay the native held response through both production transport owners.
	f := newReloadFixture()
	sense, err := hex.DecodeString("7000020000000010000000003a040000944e00000000000000")
	if err != nil {
		t.Fatal(err)
	}
	f.heldResponse = &Response{Completed: true, Status: 2, DriverStatus: 8, Sense: sense}
	transport := capacityTestTransport(f.execute, func() (*serializedTransport, error) {
		t.Fatal("refresh opened the optional capacity attachment")
		return nil, ErrUnsupported
	}, func() error { f.closed++; return nil })
	d, err := NewDevice(context.Background(), Identity{Configured: "fixture"}, transport)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Cleanup(context.Background()) })
	original := append([]byte(nil), f.mode...)
	f.onMotion = func(load bool) {
		if load {
			f.mode[14] = 4
		}
	}

	// The expected held state permits one LOAD and the usual same-descriptor observation.
	if err := d.RefreshPositionMap(context.Background()); err != nil {
		t.Fatalf("refresh=%v motions=%v", err, reloadMotions(f))
	}
	if !reflect.DeepEqual(reloadMotions(f), [][]byte{reloadCommand(false), reloadCommand(true)}) || f.wrapQueries != 2 {
		t.Fatalf("motions=%v wrap_queries=%d", reloadMotions(f), f.wrapQueries)
	}

	// Accepted held evidence must leave the shared gate open for owned control restoration.
	if err := d.Cleanup(context.Background()); err != nil || !reflect.DeepEqual(f.mode, original) || f.closed != 1 {
		t.Fatalf("cleanup=%v mode=%x want=%x closed=%d", err, f.mode, original, f.closed)
	}
}

func TestReloadCapacityTransportRejectsUnconfirmedHeldState(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*Response)
		cause  error
		cancel bool
	}{
		{name: "medium absent", change: func(r *Response) { r.Sense[13] = 0 }},
		{name: "other not ready", change: func(r *Response) { r.Sense[12], r.Sense[13] = 4, 1 }},
		{name: "GOOD", change: func(r *Response) { r.Status, r.DriverStatus, r.Sense = 0, 0, nil }},
		{name: "missing completion", change: func(r *Response) { r.Completed = false }},
		{name: "deferred", change: func(r *Response) { r.Sense[0] = 0x71 }},
		{name: "host failure", change: func(r *Response) { r.HostStatus = 3 }},
		{name: "driver failure", change: func(r *Response) { r.DriverStatus = 4 }},
		{name: "filemark", change: func(r *Response) { r.Sense[2] |= 0x80 }},
		{name: "EOM", change: func(r *Response) { r.Sense[2] |= 0x40 }},
		{name: "ILI", change: func(r *Response) { r.Sense[2] |= 0x20 }},
		{name: "residual", change: func(r *Response) { r.Residual = 1 }},
		{name: "transport uncertainty", cause: &UncertainError{Cause: ErrMalformed}},
		{name: "joined cancellation", cause: errors.Join(context.Canceled, &CommandError{})},
		{name: "caller cancellation", cancel: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Only the explicit held TUR receives this altered native response or concurrent failure.
			f := newReloadFixture()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			transport := capacityTestTransport(func(command Command) (Response, error) {
				response, err := f.execute(command)
				if command.CDB[0] == 0 && f.held {
					response.DriverStatus = 8
					if tc.change != nil {
						tc.change(&response)
					}
					if tc.cancel {
						cancel()
					}
					err = errors.Join(err, tc.cause)
				}
				return response, err
			}, func() (*serializedTransport, error) { return nil, ErrUnsupported }, func() error { f.closed++; return nil })
			d, err := NewDevice(ctx, Identity{Configured: "fixture"}, transport)
			if err != nil {
				t.Fatal(err)
			}

			// Neither a misleading sense tuple nor a joined failure authorizes LOAD or map reuse.
			if err := d.RefreshPositionMap(ctx); err == nil || len(reloadMotions(f)) != 1 || d.navigation != nil || d.geometry != nil {
				t.Fatalf("refresh=%v motions=%v", err, reloadMotions(f))
			}

			// Cleanup never schedules compensating motion after the rejected discriminator.
			_ = d.Cleanup(context.Background())
			if f.closed != 1 || len(reloadMotions(f)) != 1 {
				t.Fatalf("closed=%d motions=%v", f.closed, reloadMotions(f))
			}
		})
	}
}

func TestCapacityTransportOrdinaryHeldTURStillClosesAdmission(t *testing.T) {
	// An ordinary readiness query has no authority to reinterpret the held-state discriminator.
	issues := 0
	transport := capacityTestTransport(func(Command) (Response, error) {
		issues++
		return Response{Completed: true, Status: 2, DriverStatus: 8,
			Sense: fixedSense(0x70, 2, 0x3a, 4, false, 0, 0)}, nil
	}, func() (*serializedTransport, error) { return nil, ErrUnsupported }, func() error { return nil })
	t.Cleanup(func() { _ = transport.Close() })
	if _, err := query(context.Background(), transport, []byte{0, 0, 0, 0, 0, 0}, 0); Reason(err) != entity.DeviceReason_DEVICE_REASON_NO_MEDIUM {
		t.Fatalf("ordinary TUR=%v", err)
	}

	// The shared terminal gate still refuses later primary work before native submission.
	if _, err := transport.Execute(context.Background(), capacityTestWrite()); Reason(err) != entity.DeviceReason_DEVICE_REASON_NO_MEDIUM || issues != 1 {
		t.Fatalf("write=%v issues=%d", err, issues)
	}
}
