// Package tape owns raw sequential-device transport and passive observations. Callers must hold
// the Executor's device lease before opening a transport; configured LTFS adapters remain separate.
package tape

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"sync/atomic"
	"time"
)

// Direction uses SG's one-way transfer forms. No command transfers in both directions.
type Direction int32

const (
	NoData           Direction = -1
	ToDevice         Direction = -2
	FromDevice       Direction = -3
	MaxTransferBytes           = 0xffffff
	MaxQueryBytes              = 4096
)

// Command owns its buffers until Execute returns, including after context cancellation.
type Command struct {
	CDB         []byte
	Direction   Direction
	Data        []byte
	Timeout     time.Duration
	validate    func(Response) error
	onIssue     func() error
	readDiscard bool
}

// Response preserves native status and residual evidence; residuals alone do not prove write acceptance.
type Response struct {
	Completed                bool
	Status                   byte
	MessageStatus            byte
	HostStatus, DriverStatus uint16
	Residual                 int32
	Sense                    []byte
}

// Transport serializes primary commands. An in-flight command settles before cancellation returns;
// unknown physical outcomes remain errors and cannot authorize a later command or publication.
type Transport interface {
	Execute(context.Context, Command) (Response, error)
	Close() error
}

var (
	ErrUnsupported = errors.New("raw Tape transport is unsupported")
	ErrUncertain   = errors.New("Tape command outcome is unknown")
	ErrMalformed   = errors.New("malformed Tape command response")
)

// UncertainError retains the underlying transport failure and signals retained device unavailability.
type UncertainError struct{ Cause error }

func (e *UncertainError) Error() string   { return fmt.Sprintf("%s: %v", ErrUncertain, e.Cause) }
func (e *UncertainError) Unwrap() []error { return []error{ErrUncertain, e.Cause} }

// serializedTransport owns admission, synchronous command lifetime and terminal cleanup.
// The syscall remains synchronous: no cancellation goroutine can release an active descriptor.
type serializedTransport struct {
	lane         chan struct{}
	execute      func(Command) (Response, error)
	close        func() error
	closed       bool
	uncertain    error
	closeOnce    sync.Once
	closeErr     error
	grant        func() error
	finish       func(Command, Response, error) error
	observations atomic.Pointer[CommandObservations]
}

func newSerializedTransport(execute func(Command) (Response, error), close func() error) *serializedTransport {
	lane := make(chan struct{}, 1)
	lane <- struct{}{}
	return &serializedTransport{lane: lane, execute: execute, close: close}
}

func (t *serializedTransport) Execute(ctx context.Context, command Command) (Response, error) {
	response, _, err := t.executeCommand(ctx, command, true)
	return response, err
}

// ExecuteObserved explicitly guarantees the callback's issue boundary and abort semantics.
// Opaque Execute-only transports cannot establish this optional accounting contract.
func (t *serializedTransport) ExecuteObserved(ctx context.Context, command Command, onIssue func() error) (Response, error) {
	command.onIssue = onIssue
	response, _, err := t.executeCommand(ctx, command, true)
	return response, err
}

// TryExecute admits a passive observation only when the existing command lane is immediately free.
// It never queues ahead of data; an admitted command retains the lane until physical settlement.
func (t *serializedTransport) TryExecute(ctx context.Context, command Command, onIssue func() error) (Response, bool, error) {
	if !passiveCommand(command) {
		return Response{}, false, fmt.Errorf("optional command is outside passive allowlist")
	}
	command.onIssue = onIssue
	return t.executeCommand(ctx, command, false)
}

func (t *serializedTransport) executeCommand(ctx context.Context, command Command, wait bool) (Response, bool, error) {
	// Reject invalid bounds before taking the command lane or exposing buffers to the kernel.
	if len(command.CDB) < 6 || len(command.CDB) > 16 || len(command.Data) > MaxTransferBytes {
		return Response{}, false, fmt.Errorf("invalid Tape command or transfer size")
	}
	if command.Timeout < time.Millisecond || command.Timeout > 24*time.Hour {
		return Response{}, false, fmt.Errorf("invalid Tape command timeout")
	}
	if command.Direction != NoData && command.Direction != ToDevice && command.Direction != FromDevice {
		return Response{}, false, fmt.Errorf("invalid Tape transfer direction")
	}
	if command.Direction == NoData && len(command.Data) != 0 {
		return Response{}, false, fmt.Errorf("no-data Tape command has a transfer buffer")
	}

	// Cancellation applies to admission; once admitted, this owner waits for the ioctl to settle.
	if wait {
		select {
		case <-ctx.Done():
			return Response{}, false, ctx.Err()
		case <-t.lane:
		}
	} else {
		select {
		case <-ctx.Done():
			return Response{}, false, ctx.Err()
		case <-t.lane:
		default:
			return Response{}, false, nil
		}
	}
	defer func() { t.lane <- struct{}{} }()
	if err := ctx.Err(); err != nil {
		return Response{}, false, err
	}
	if t.closed {
		return Response{}, false, io.ErrClosedPipe
	}
	if t.uncertain != nil {
		return Response{}, false, t.uncertain
	}
	if t.grant != nil {
		if err := t.grant(); err != nil {
			return Response{}, false, err
		}
	}

	// Submission/accounting observes the actual issue boundary, never enqueue or command completion.
	if command.onIssue != nil {
		if err := command.onIssue(); err != nil {
			return Response{}, false, err
		}
	}
	observations := t.observations.Load()
	var started time.Time
	if observations != nil {
		started = time.Now()
	}
	response, err := t.execute(command)
	var elapsed time.Duration
	if observations != nil {
		elapsed = time.Since(started)
	}
	if !response.Completed && err == nil {
		err = &UncertainError{Cause: fmt.Errorf("Tape completion status is absent")}
	}

	// Validate physical outcome before releasing the lane to a subsequent command.
	if response.Completed {
		validationErr := checkCommandResponse(command, response)
		if response.Residual < 0 || int64(response.Residual) > int64(len(command.Data)) {
			validationErr = &UncertainError{Cause: ErrMalformed}
		}
		if command.validate != nil {
			validationErr = errors.Join(validationErr, command.validate(response))
		}
		if errors.Is(validationErr, ErrUncertain) || onlyCapacityQueryError(validationErr) {
			err = errors.Join(err, validationErr)
		}
	}
	if t.finish != nil {
		err = t.finish(command, response, err)
	}
	if errors.Is(err, ErrUncertain) {
		t.uncertain = err
	}
	if observations != nil {
		observations.record(command, response, err, started, elapsed)
	}
	return response, true, errors.Join(err, ctx.Err())
}

func (t *serializedTransport) Close() error {
	// Closing waits behind every active command and never sends a reset, unload or filemark.
	t.closeOnce.Do(func() {
		<-t.lane
		defer func() { t.lane <- struct{}{} }()
		t.closed = true
		t.closeErr = errors.Join(t.close(), t.uncertain)
	})
	return t.closeErr
}
