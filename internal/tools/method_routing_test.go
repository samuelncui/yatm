package tools

import (
	"context"
	"errors"
	"testing"
)

type routeInput interface{ routeInput() }

type routeRequest struct {
	value   routeInput
	unpacks int
}

func (r *routeRequest) Unpack() routeInput {
	r.unpacks++
	return r.value
}

type firstRequest struct{}

func (*firstRequest) routeInput()           {}
func (r *firstRequest) ToOneof() routeInput { return r }

type secondRequest struct{}

func (*secondRequest) routeInput()           {}
func (r *secondRequest) ToOneof() routeInput { return r }

func TestActionRouterDispatchesAndUnpacksOnce(t *testing.T) {
	called := ""
	router := NewActionRouter[*routeRequest](
		ActionMethod(func(context.Context, *firstRequest) error {
			called = "first"
			return nil
		}),
		ActionMethod(func(context.Context, *secondRequest) error {
			called = "second"
			return nil
		}),
	)
	request := &routeRequest{value: &secondRequest{}}

	if err := router(context.Background(), request); err != nil {
		t.Fatalf("route request: %v", err)
	}
	if called != "second" {
		t.Fatalf("called %q, want second", called)
	}
	if request.unpacks != 1 {
		t.Fatalf("unpack count = %d, want 1", request.unpacks)
	}
}

func TestActionRouterRejectsUnsupportedRequest(t *testing.T) {
	router := NewActionRouter[*routeRequest](
		ActionMethod(func(context.Context, *firstRequest) error { return nil }),
	)

	if err := router(context.Background(), &routeRequest{value: &secondRequest{}}); err == nil {
		t.Fatal("expected unsupported request error")
	}
}

func TestActionRouterPreservesHandlerError(t *testing.T) {
	want := errors.New("handler failed")
	router := NewActionRouter[*routeRequest](
		ActionMethod(func(context.Context, *firstRequest) error { return want }),
	)

	if err := router(context.Background(), &routeRequest{value: &firstRequest{}}); !errors.Is(err, want) {
		t.Fatalf("route error = %v, want %v", err, want)
	}
}
