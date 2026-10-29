package tape

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

func TestCommandObservationsAreBoundedAndIssueNoCommands(t *testing.T) {
	// An unread telemetry channel cannot hold the device lane or send a measurement operation.
	var calls atomic.Int64
	transport := newSerializedTransport(func(Command) (Response, error) {
		calls.Add(1)
		return Response{Completed: true}, nil
	}, func() error { return nil })
	observations := transport.observeCommands(RecordMode{MaximumBytes: 512})
	if calls.Load() != 0 || transport.observeCommands(RecordMode{}) != nil {
		t.Fatal("subscription changed I/O or allowed a second owner")
	}
	records := Records{Transport: transport, Mode: RecordMode{MaximumBytes: 512}, Timeout: time.Second}
	for range 300 {
		if _, err := records.Read(context.Background(), make([]byte, 512)); err != nil {
			t.Fatal(err)
		}
	}
	observations.Stop()
	if calls.Load() != 300 || observations.Dropped() != 44 {
		t.Fatalf("commands=%d dropped=%d", calls.Load(), observations.Dropped())
	}
	var count uint64
	for value := range observations.Values() {
		count++
		// An immediate fake syscall may finish within one clock tick; do not invent positive elapsed time.
		if value.Sequence != count || !value.Known || value.Bytes != 512 || value.StartedAt.IsZero() || value.Elapsed < 0 {
			t.Fatalf("invalid native observation: %+v", value)
		}
	}
	if count != 256 {
		t.Fatalf("unbounded/short queue: %d", count)
	}
	if err := records.Rewind(context.Background()); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 301 {
		t.Fatal("stopping observation prevented ordinary I/O")
	}
}

func TestCommandObservationsExcludeAdmissionWaitAndJoinNativeSettlement(t *testing.T) {
	// Hold admission longer than the native operation and detach while a later native call is active.
	started, release := make(chan struct{}), make(chan struct{})
	transport := newSerializedTransport(func(Command) (Response, error) {
		close(started)
		<-release
		return Response{Completed: true}, nil
	}, func() error { return nil })
	observations := transport.observeCommands(RecordMode{})
	<-transport.lane
	completed := make(chan error, 1)
	go func() {
		_, err := transport.Execute(context.Background(), Command{CDB: []byte{1, 0, 0, 0, 0, 0}, Direction: NoData, Timeout: time.Second})
		completed <- err
	}()
	time.Sleep(40 * time.Millisecond)
	released := time.Now()
	transport.lane <- struct{}{}
	<-started
	stopped := make(chan struct{})
	go func() { observations.Stop(); close(stopped) }()
	select {
	case <-stopped:
		t.Fatal("stopped before native owner settled")
	default:
	}
	close(release)
	if err := <-completed; err != nil {
		t.Fatal(err)
	}
	<-stopped
	value := <-observations.Values()
	if value.StartedAt.Before(released) || value.Elapsed >= time.Since(released) {
		t.Fatalf("timing includes command admission wait: %+v", value)
	}
}

func TestCommandObservationLogicalFacts(t *testing.T) {
	// Reading a short record and a filemark must retain actual counts; an uncertain command never trains.
	mode := RecordMode{MaximumBytes: 512}
	observations := &CommandObservations{mode: mode}
	read := Command{CDB: []byte{8, 0, 0, 2, 0, 0}, Data: make([]byte, 512)}
	for _, test := range []struct {
		name     string
		command  Command
		response Response
		known    bool
		bytes    int
	}{
		{name: "full", command: read, response: Response{Completed: true}, known: true, bytes: 512},
		{name: "unexplained residual", command: read, response: Response{Completed: true, Residual: 1}},
		{name: "locate", command: Command{CDB: []byte{0x2b, 2, 0, 0, 0, 1, 3, 0, 0, 0}}, response: Response{Completed: true}, known: true},
		{name: "unqualified locate form", command: Command{CDB: []byte{0x2b, 1, 0, 0, 0, 1, 3, 0, 0, 0}}, response: Response{Completed: true}},
	} {
		t.Run(test.name, func(t *testing.T) {
			value := CommandObservation{Opcode: test.command.CDB[0]}
			observations.decode(&value, test.command, test.response)
			if value.Known != test.known || value.Bytes != test.bytes {
				t.Fatalf("observation=%+v", value)
			}
			if test.name == "locate" && value.Position.Object != 259 {
				t.Fatalf("LOCATE target=%+v", value.Position)
			}
		})
	}

	// Failed commands keep a sequence entry so the consumer cannot bridge over an unknown movement.
	observations.values = make(chan CommandObservation, 1)
	observations.record(read, Response{Completed: true}, errors.New("native failure"), time.Now(), time.Second)
	if value := <-observations.Values(); value.Known || value.Sequence != 1 {
		t.Fatalf("failed command became a learning sample: %+v", value)
	}
}

func TestCommandObservationDiscardRetainsNativeReadFacts(t *testing.T) {
	transport := newSerializedTransport(func(Command) (Response, error) {
		return Response{Completed: true}, nil
	}, func() error { return nil })
	mode := RecordMode{MaximumBytes: 512}
	observations := transport.observeCommands(mode)
	records := Records{Transport: transport, Mode: mode, Timeout: time.Second}
	buffer := make([]byte, 512)
	for _, ctx := range []context.Context{context.Background(), WithReadDiscard(context.Background())} {
		if _, err := records.Read(ctx, buffer); err != nil {
			t.Fatal(err)
		}
	}
	observations.Stop()
	first, second := <-observations.Values(), <-observations.Values()
	if !first.Known || !second.Known || first.Bytes != 512 || second.Bytes != 512 || first.Discard || !second.Discard {
		t.Fatalf("discard labeling changed native read facts: first=%+v second=%+v", first, second)
	}
}

func BenchmarkCommandObservations(b *testing.B) {
	// Reuse one physical record buffer. The full queue is the production overload case: it must
	// add bounded bookkeeping without allocation or waiting for a telemetry consumer.
	for _, enabled := range []bool{false, true} {
		name := "disabled"
		if enabled {
			name = "full_queue"
		}
		b.Run(name, func(b *testing.B) {
			transport := newSerializedTransport(func(Command) (Response, error) {
				return Response{Completed: true}, nil
			}, func() error { return nil })
			mode := RecordMode{MaximumBytes: 512 * 1024}
			var observations *CommandObservations
			if enabled {
				observations = transport.observeCommands(mode)
				defer observations.Stop()
			}
			records := Records{Transport: transport, Mode: mode, Timeout: time.Second}
			buffer := make([]byte, mode.MaximumBytes)
			b.ReportAllocs()
			b.ResetTimer()
			for b.Loop() {
				if _, err := records.Read(context.Background(), buffer); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func TestCommandObservationUnloadAndRestoration(t *testing.T) {
	// Only exact synchronous unload and nonmoving owned-control forms preserve usable facts.
	mode := sessionMode(make([]byte, 16), nil, nil, 512)
	modeCDB := []byte{0x55, 0x10, 0, 0, 0, 0, 0, 0, byte(len(mode)), 0}
	keyCDB := []byte{0xb5, 0x20, 0, 0x10, 0, 0, 0, 0, 0, 20, 0, 0}
	for _, test := range []struct {
		name     string
		command  Command
		response Response
		err      error
		known    bool
	}{
		{"unload", Command{CDB: []byte{0x1b, 0, 0, 0, 0, 0}, Direction: NoData}, Response{Completed: true}, nil, true},
		{"immediate", Command{CDB: []byte{0x1b, 1, 0, 0, 0, 0}, Direction: NoData}, Response{Completed: true}, nil, false},
		{"load", Command{CDB: []byte{0x1b, 0, 0, 0, 1, 0}, Direction: NoData}, Response{Completed: true}, nil, false},
		{"hold", Command{CDB: []byte{0x1b, 0, 0, 0, 8, 0}, Direction: NoData}, Response{Completed: true}, nil, false},
		{"residual", Command{CDB: []byte{0x1b, 0, 0, 0, 0, 0}, Direction: NoData}, Response{Completed: true, Residual: 1}, nil, false},
		{"unsettled", Command{CDB: []byte{0x1b, 0, 0, 0, 0, 0}, Direction: NoData}, Response{}, nil, false},
		{"native failure", Command{CDB: []byte{0x1b, 0, 0, 0, 0, 0}, Direction: NoData}, Response{Completed: true}, errors.New("native failure"), false},
		{"mode restore", Command{CDB: modeCDB, Direction: ToDevice, Data: mode}, Response{Completed: true}, nil, true},
		{"mode residual", Command{CDB: modeCDB, Direction: ToDevice, Data: mode}, Response{Completed: true, Residual: 1}, nil, false},
		{"external key control", Command{CDB: keyCDB, Direction: ToDevice, Data: make([]byte, 20)}, Response{Completed: true}, nil, false},
		{"key residual", Command{CDB: keyCDB, Direction: ToDevice, Data: make([]byte, 20)}, Response{Completed: true, Residual: 1}, nil, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			// Feed the same completed native response boundary used by the serialized owner.
			observations := &CommandObservations{values: make(chan CommandObservation, 1)}
			observations.record(test.command, test.response, test.err, time.Now(), 2*time.Second)
			value := <-observations.Values()
			if value.Known != test.known || value.Opcode != test.command.CDB[0] || value.Elapsed != 2*time.Second {
				t.Fatalf("invalid control observation: %+v", value)
			}
		})
	}

	// Partition changes, malformed parameter lists and key installation are not nonmoving restoration evidence.
	partition := append(append([]byte(nil), mode...), []byte{0x11, 6, 0, 0, 0, 0, 0, 0}...)
	partitionCDB := append([]byte(nil), modeCDB...)
	partitionCDB[8] = byte(len(partition))
	installed := make([]byte, 20)
	installed[6] = 2
	for _, command := range []Command{
		{CDB: partitionCDB, Direction: ToDevice, Data: partition},
		{CDB: modeCDB, Direction: ToDevice, Data: mode[:15]},
		{CDB: keyCDB, Direction: ToDevice, Data: installed},
	} {
		if nonmovingRestoration(command) {
			t.Fatal("unknown control change preserved the cursor")
		}
	}
}

func TestCommandObservationNormalUnloadUsesOnlyRequestedCommand(t *testing.T) {
	// Passive completion telemetry observes the existing native unload without additional device I/O.
	calls := 0
	transport := newSerializedTransport(func(command Command) (Response, error) {
		calls++
		if len(command.CDB) != 6 || command.CDB[0] != 0x1b || command.CDB[1] != 0 || command.CDB[4] != 0 {
			t.Fatalf("unexpected native command: %x", command.CDB)
		}
		return Response{Completed: true}, nil
	}, func() error { return nil })
	observations := transport.observeCommands(RecordMode{})
	records := Records{Transport: transport, Timeout: time.Second}
	if err := records.Unload(context.Background()); err != nil {
		t.Fatal(err)
	}
	observations.Stop()
	value := <-observations.Values()
	if calls != 1 || value.Opcode != 0x1b || !value.Known || value.StartedAt.IsZero() || value.Elapsed < 0 {
		t.Fatalf("invalid native unload observation: calls=%d value=%+v", calls, value)
	}
	if _, more := <-observations.Values(); more {
		t.Fatal("unload telemetry issued an extra command")
	}
	if err := transport.Close(); err != nil {
		t.Fatal(err)
	}
}
