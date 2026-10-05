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
func (r *firstRequest) Pack() *routeRequest { return &routeRequest{value: r} }
func (r *firstRequest) ToOneof() routeInput { return r }

type secondRequest struct{}

func (*secondRequest) routeInput()           {}
func (r *secondRequest) Pack() *routeRequest { return &routeRequest{value: r} }
func (r *secondRequest) ToOneof() routeInput { return r }

type routeOutput interface{ routeOutput() }

type routeReply struct{ value routeOutput }

func (r *routeReply) Unpack() routeOutput { return r.value }

type firstReply struct{}

func (*firstReply) routeOutput()           {}
func (r *firstReply) Pack() *routeReply    { return &routeReply{value: r} }
func (r *firstReply) ToOneof() routeOutput { return r }

func TestRouterDispatchesAndUnpacksOnce(t *testing.T) {
	router := NewRouter[*routeRequest, *routeReply, routeInput, routeOutput](
		Method(func(context.Context, *firstRequest) (*firstReply, error) {
			return &firstReply{}, nil
		}),
	)
	request := &routeRequest{value: &firstRequest{}}

	reply, err := router(context.Background(), request)
	if err != nil {
		t.Fatalf("route request: %v", err)
	}
	if _, ok := reply.Unpack().(*firstReply); !ok {
		t.Fatalf("unexpected reply type %T", reply.Unpack())
	}
	if request.unpacks != 1 {
		t.Fatalf("unpack count = %d, want 1", request.unpacks)
	}
}

func TestRouterRejectsUnsupportedRequest(t *testing.T) {
	router := NewRouter[*routeRequest, *routeReply, routeInput, routeOutput](
		Method(func(context.Context, *firstRequest) (*firstReply, error) {
			return &firstReply{}, nil
		}),
	)

	_, err := router(context.Background(), &routeRequest{value: &secondRequest{}})
	if err == nil {
		t.Fatal("expected unsupported request error")
	}
}

func TestRouterPreservesHandlerError(t *testing.T) {
	want := errors.New("handler failed")
	router := NewRouter[*routeRequest, *routeReply, routeInput, routeOutput](
		Method(func(context.Context, *firstRequest) (*firstReply, error) {
			return nil, want
		}),
	)

	_, err := router(context.Background(), &routeRequest{value: &firstRequest{}})
	if !errors.Is(err, want) {
		t.Fatalf("route error = %v, want %v", err, want)
	}
}
