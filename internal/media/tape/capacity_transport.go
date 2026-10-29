package tape

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"syscall"
	"time"

	"github.com/samuelncui/yatm/entity"
)

// capacityTransport owns one lazy query attachment and a brief attempt-wide terminal gate.
// Primary and query lanes each bound one pregranted lifetime; the mutex never spans native I/O.
// The media session owns stopping its observer before completion/controls; Close joins raw lifetimes.
type capacityTransport struct {
	mu        sync.Mutex
	closed    bool
	failure   error
	lane      chan struct{}
	open      func() (*serializedTransport, error)
	query     *serializedTransport
	withdrawn bool
	closeOnce sync.Once
	closeErr  error
}

func newCapacityTransport(primary *serializedTransport, open func() (*serializedTransport, error)) *capacityTransport {
	lane := make(chan struct{}, 1)
	lane <- struct{}{}
	t := &capacityTransport{lane: lane, open: open}
	primary.grant = t.grant
	primary.finish = t.finishPrimary
	return t
}

func exactCapacityCommand(command Command) bool {
	return command.Direction == FromDevice && len(command.Data) == MaxQueryBytes &&
		command.Timeout >= time.Millisecond && command.Timeout <= capacityObservationTimeout &&
		bytes.Equal(command.CDB, []byte{0x4d, 0, 0x4c, 0, 0, 0, 0, 0x10, 0, 0})
}

func (t *capacityTransport) grant() error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.failure != nil {
		return t.failure
	}
	if t.closed {
		return io.ErrClosedPipe
	}
	return nil
}

func (t *capacityTransport) fail(err error) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.failure == nil {
		t.failure = err
	} else {
		// Each lane has at most one pregranted lifetime; retain a later uncertainty/cleanup cause too.
		t.failure = errors.Join(t.failure, err)
	}
	return t.failure
}

func (t *capacityTransport) finishPrimary(command Command, response Response, err error) error {
	// Preparation uses the primary lane, but an optional settled query failure cannot poison WRITE.
	if exactCapacityCommand(command) && (onlyCapacityQueryError(err) || CleanUnsupported(err)) {
		return err
	}

	// Existing known control rejections still permit restoration; loss of the medium does not.
	if err == nil && response.Completed {
		failure := CheckResponse(response)
		reason := Reason(failure)
		if reason == entity.DeviceReason_DEVICE_REASON_NO_MEDIUM || reason == entity.DeviceReason_DEVICE_REASON_NOT_READY {
			// Explicit control flows can validate an expected settled readiness response.
			if command.validate != nil {
				failure = command.validate(response)
			}
			err = failure
		}
	}
	if err != nil {
		t.fail(err)
	}
	return err
}

func (t *capacityTransport) finishQuery(command Command, response Response, err error) error {
	// Settled query-local failures retain the attachment and writer for subsequent attempts.
	if err == nil {
		err = checkCommandResponse(command, response)
	}
	if onlyCapacityQueryError(err) {
		return err
	}

	// Native uncertainty closes shared admission; malformed counter data is only an unavailable observation.
	if err == nil {
		if response.Residual < 0 || int(response.Residual) > len(command.Data) {
			err = &UncertainError{Cause: ErrMalformed}
		} else {
			_, _, err = decodeCapacity(command.Data[:len(command.Data)-int(response.Residual)])
			if err == ErrMalformed {
				return err
			}
		}
	}
	if CleanUnsupported(err) {
		t.withdrawn = true
	} else if err != nil {
		t.fail(err)
	}
	return err
}

func (t *capacityTransport) tryExecute(ctx context.Context, command Command, onIssue func() error) (Response, bool, error) {
	// Attachment belongs to the same nonqueued lifetime as the query; Close cannot overtake it.
	if !exactCapacityCommand(command) {
		return Response{}, false, fmt.Errorf("command is outside the private capacity allowlist")
	}
	select {
	case <-ctx.Done():
		return Response{}, false, ctx.Err()
	case <-t.lane:
	default:
		return Response{}, false, nil
	}
	defer func() { t.lane <- struct{}{} }()
	if err := ctx.Err(); err != nil {
		return Response{}, false, err
	}
	if err := t.grant(); err != nil {
		return Response{}, false, err
	}
	if t.withdrawn {
		return Response{}, false, ErrUnsupported
	}
	if t.query == nil {
		query, err := t.open()
		if err != nil {
			if capacityAttachmentResourceMiss(err) {
				return Response{}, false, err
			}
			t.withdrawn = true
			if cleanCapacityAttachmentAbsence(err) {
				return Response{}, false, fmt.Errorf("capacity attachment unavailable: %w", ErrUnsupported)
			}
			return Response{}, false, t.fail(err)
		}
		if query == nil {
			return Response{}, false, t.fail(fmt.Errorf("capacity attachment is absent: %w", ErrMalformed))
		}
		query.grant, query.finish = t.grant, t.finishQuery
		t.query = query
	}

	// This second grant is immediately before the callback; no gate spans open or native execution.
	return t.query.TryExecute(ctx, command, onIssue)
}

func capacityAttachmentResourceMiss(err error) bool {
	// Only a clean local allocation failure permits another attachment attempt; cleanup is terminal.
	if errors.Is(err, ErrUncertain) {
		return false
	}
	if _, joined := err.(interface{ Unwrap() []error }); joined {
		return false
	}
	if wrapped, ok := err.(interface{ Unwrap() error }); ok {
		return capacityAttachmentResourceMiss(wrapped.Unwrap())
	}
	return err == syscall.EMFILE || err == syscall.ENFILE || err == syscall.ENOMEM
}

func cleanCapacityAttachmentAbsence(err error) bool {
	// A joined cleanup cause is never optional, even if another cause was a permission rejection.
	if _, joined := err.(interface{ Unwrap() []error }); joined {
		return false
	}
	if wrapped, ok := err.(interface{ Unwrap() error }); ok {
		return cleanCapacityAttachmentAbsence(wrapped.Unwrap())
	}
	return err == ErrUnsupported || err == syscall.EACCES || err == syscall.EPERM
}

func (t *capacityTransport) Close() error {
	// Permanently close grants first, then join lazy attachment/native execution before closing its fd.
	t.closeOnce.Do(func() {
		t.mu.Lock()
		t.closed = true
		t.mu.Unlock()
		<-t.lane
		defer func() { t.lane <- struct{}{} }()
		if t.query != nil {
			if err := t.query.Close(); err != nil {
				t.closeErr = &UncertainError{Cause: err}
			}
		}
		t.mu.Lock()
		if errors.Is(t.failure, ErrUncertain) {
			t.closeErr = errors.Join(t.closeErr, t.failure)
		}
		t.mu.Unlock()
	})
	return t.closeErr
}
