package tape

import (
	"context"
	"errors"
	"testing"
	"time"
)

type opaqueRecordTransport struct{ calls int }

func (t *opaqueRecordTransport) Execute(context.Context, Command) (Response, error) {
	// An external Execute-only implementation has no contract to invoke private Command fields.
	t.calls++
	return Response{Completed: true}, nil
}

func (*opaqueRecordTransport) Close() error { return nil }

func TestObservedWriteRequiresAnExplicitTransportIssueBoundary(t *testing.T) {
	// Method existence on Records cannot qualify an opaque Transport that silently ignores the hook.
	transport := new(opaqueRecordTransport)
	records := Records{Transport: transport, Mode: RecordMode{MaximumBytes: 512}, Timeout: time.Second}
	callbacks := 0
	result, err := records.WriteObserved(context.Background(), make([]byte, 512), func() error { callbacks++; return nil })
	if !errors.Is(err, ErrUnsupported) || !result.AcceptedKnown || result.Bytes != 0 || transport.calls != 0 || callbacks != 0 {
		t.Fatalf("opaque observed write=%+v err=%v calls=%d callbacks=%d", result, err, transport.calls, callbacks)
	}

	// Ordinary WRITE remains valid on the established Execute contract and issues exactly one record.
	result, err = records.Write(context.Background(), make([]byte, 512))
	if err != nil || !result.AcceptedKnown || result.Bytes != 512 || transport.calls != 1 || callbacks != 0 {
		t.Fatalf("ordinary write=%+v err=%v calls=%d callbacks=%d", result, err, transport.calls, callbacks)
	}

	// A nil hook asks for ordinary WRITE; it does not introduce an optional transport requirement.
	result, err = records.WriteObserved(context.Background(), make([]byte, 512), nil)
	if err != nil || !result.AcceptedKnown || result.Bytes != 512 || transport.calls != 2 || callbacks != 0 {
		t.Fatalf("nil-hook write=%+v err=%v calls=%d callbacks=%d", result, err, transport.calls, callbacks)
	}
}
