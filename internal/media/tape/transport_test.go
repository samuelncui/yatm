package tape

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync/atomic"
	"testing"
	"time"
)

func TestTransportSerializesAndRetainsActiveCommandThroughCancellation(t *testing.T) {
	// A cancelled caller cannot release an active ioctl or let Close race its descriptor.
	entered, settle := make(chan struct{}), make(chan struct{})
	var active, closed atomic.Int32
	transport := newSerializedTransport(func(Command) (Response, error) {
		if active.Add(1) != 1 {
			t.Error("commands overlapped")
		}
		close(entered)
		<-settle
		active.Add(-1)
		return Response{Completed: true}, nil
	}, func() error { closed.Add(1); return nil })
	ctx, cancel := context.WithCancel(context.Background())
	command := Command{CDB: []byte{0, 0, 0, 0, 0, 0}, Direction: NoData, Timeout: time.Second}
	finished := make(chan error, 1)
	go func() { _, err := transport.Execute(ctx, command); finished <- err }()
	<-entered
	cancel()
	closeDone := make(chan error, 1)
	go func() { closeDone <- transport.Close() }()

	// Admission cancellation ends without issuing a second command; active cancellation must settle.
	if _, err := transport.Execute(ctx, command); !errors.Is(err, context.Canceled) {
		t.Fatalf("queued cancellation=%v", err)
	}
	select {
	case <-finished:
		t.Fatal("active command returned before settlement")
	case <-time.After(20 * time.Millisecond):
	}
	if closed.Load() != 0 {
		t.Fatal("active descriptor closed")
	}
	close(settle)
	if err := <-finished; !errors.Is(err, context.Canceled) {
		t.Fatalf("active cancellation=%v", err)
	}
	if err := <-closeDone; err != nil {
		t.Fatal(err)
	}
	if _, err := transport.Execute(context.Background(), command); !errors.Is(err, io.ErrClosedPipe) {
		t.Fatalf("closed Execute=%v", err)
	}
	if err := transport.Close(); err != nil || closed.Load() != 1 {
		t.Fatalf("Close=%v, count=%d", err, closed.Load())
	}
}

func TestSemanticUncertaintyStopsFutureCommands(t *testing.T) {
	for _, response := range []Response{
		{},
		{Completed: true, HostStatus: 3},
		{Completed: true, Residual: -1},
		{Completed: true, Status: 2, Sense: fixedSense(0x71, 0, 0, 2, true, 0x40, 0)},
		{Completed: true, Status: 2, Sense: []byte{0x70}},
		{Completed: true, Status: 2, Sense: fixedSense(0x70, 6, 0x29, 0, false, 0, 0)},
	} {
		// Transport and sense/residual failures poison the serialized lane before its release.
		calls := 0
		transport := newSerializedTransport(func(Command) (Response, error) { calls++; return response, nil }, func() error { return nil })
		command := Command{CDB: []byte{0, 0, 0, 0, 0, 0}, Direction: NoData, Timeout: time.Second}
		if _, err := transport.Execute(context.Background(), command); !errors.Is(err, ErrUncertain) {
			t.Fatalf("uncertain response=%v", err)
		}
		if _, err := transport.Execute(context.Background(), command); !errors.Is(err, ErrUncertain) || calls != 1 {
			t.Fatalf("next Execute=%v, calls=%d", err, calls)
		}
		_ = transport.Close()
	}
}

func TestCleanUnsupportedOptionalQueryDoesNotPoisonTransport(t *testing.T) {
	// An optional query rejection has a known outcome and leaves safe query admission available.
	calls := 0
	transport := newSerializedTransport(func(Command) (Response, error) {
		calls++
		if calls == 1 {
			return Response{Completed: true, Status: 2, Sense: fixedSense(0x70, 5, 0x20, 0, false, 0, 0)}, nil
		}
		return Response{Completed: true}, nil
	}, func() error { return nil })
	defer transport.Close()
	command := Command{CDB: []byte{0, 0, 0, 0, 0, 0}, Direction: NoData, Timeout: time.Second}
	response, err := transport.Execute(context.Background(), command)
	if err != nil || Reason(CheckResponse(response)).String() != "DEVICE_REASON_UNSUPPORTED" {
		t.Fatalf("optional rejection=%v, response=%v", err, response)
	}
	if _, err := transport.Execute(context.Background(), command); err != nil || calls != 2 {
		t.Fatalf("later query=%v, calls=%d", err, calls)
	}
}

func TestCleanUnsupportedRequiresEveryCauseToBeKnownRejection(t *testing.T) {
	for _, asc := range []byte{0x20, 0x24} {
		// Wrapped and joined clean rejections remain optional; any other cause remains a failure.
		failure := &CommandError{Response: Response{Completed: true, Status: 2}, Sense: Sense{Key: 5, ASC: asc}}
		for _, cause := range []error{ErrUnsupported, failure, fmt.Errorf("query failed, %w", failure), errors.Join(ErrUnsupported, failure)} {
			if !CleanUnsupported(cause) {
				t.Fatalf("clean rejection refused, cause=%v", cause)
			}
		}
		for _, cause := range []error{context.Canceled, context.DeadlineExceeded, ErrUncertain, ErrMalformed, errors.New("hardware failure"), &UncertainError{Cause: failure}} {
			if CleanUnsupported(errors.Join(failure, cause)) {
				t.Fatalf("mixed rejection accepted, cause=%v", cause)
			}
		}
	}

	// Unsupported-opcode sense does not excuse deferred, boundary or transport evidence.
	for _, change := range []func(*CommandError){
		func(e *CommandError) { e.Response.Completed = false },
		func(e *CommandError) { e.Response.HostStatus = 1 },
		func(e *CommandError) { e.Response.MessageStatus = 1 },
		func(e *CommandError) { e.Response.DriverStatus = 1 },
		func(e *CommandError) { e.Sense.Deferred = true },
		func(e *CommandError) { e.Sense.Filemark = true },
		func(e *CommandError) { e.Sense.EOM = true },
		func(e *CommandError) { e.Sense.ILI = true },
	} {
		failure := &CommandError{Response: Response{Completed: true, Status: 2}, Sense: Sense{Key: 5, ASC: 0x20}}
		change(failure)
		if CleanUnsupported(failure) {
			t.Fatalf("incoherent unsupported response accepted: %+v", failure)
		}
	}
	if CleanUnsupported(nil) {
		t.Fatal("successful command classified as unsupported")
	}
}
