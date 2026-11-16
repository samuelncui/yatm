package executor

import (
	"context"
	"fmt"

	"github.com/samuelncui/yatm/internal/media/tape"
)

// fakeTransport scripts a Tape transport. execute answers each command and may delegate to the
// wrapped Transport, which also answers when execute is nil. Close reaches the wrapped Transport
// when one exists and otherwise returns closeErr.
type fakeTransport struct {
	tape.Transport
	execute  func(context.Context, tape.Command) (tape.Response, error)
	closed   int
	closeErr error
}

func (f *fakeTransport) Execute(ctx context.Context, command tape.Command) (tape.Response, error) {
	if f.execute == nil {
		return f.Transport.Execute(ctx, command)
	}
	return f.execute(ctx, command)
}

func (f *fakeTransport) Close() error {
	f.closed++
	if f.Transport != nil {
		return f.Transport.Close()
	}
	return f.closeErr
}

// fakeNativeTransport exposes the same complete optional method set as Linux SG over any Transport.
type fakeNativeTransport struct {
	tape.Transport
	busy bool
}

func (f *fakeNativeTransport) EnsureTransfer(ctx context.Context, _ int) error {
	return ctx.Err()
}

func (f *fakeNativeTransport) ExecuteObserved(ctx context.Context, command tape.Command, onIssue func() error) (tape.Response, error) {
	if err := ctx.Err(); err != nil {
		return tape.Response{}, err
	}
	if onIssue != nil {
		if err := onIssue(); err != nil {
			return tape.Response{}, err
		}
	}
	return f.Execute(ctx, command)
}

func (f *fakeNativeTransport) TryExecute(ctx context.Context, command tape.Command, onIssue func() error) (tape.Response, bool, error) {
	if err := ctx.Err(); err != nil {
		return tape.Response{}, false, err
	}
	if f.busy {
		return tape.Response{}, false, nil
	}
	if onIssue != nil {
		if err := onIssue(); err != nil {
			return tape.Response{}, false, err
		}
	}
	response, err := f.Execute(ctx, command)
	return response, true, err
}

// fakeBoundedTransport reserves transfers up to a fixed adapter limit, like Linux SG readback.
type fakeBoundedTransport struct {
	tape.Transport
	limit int
}

func (f *fakeBoundedTransport) EnsureTransfer(_ context.Context, size int) error {
	if size > f.limit {
		return fmt.Errorf("SG reserved buffer %d is smaller than selected record %d", f.limit, size)
	}
	return nil
}
