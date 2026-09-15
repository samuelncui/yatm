package tools

import (
	"context"
	"fmt"
)

type root[O any] interface {
	Unpack() O
}

type message[R, O any] interface {
	Pack() R
	ToOneof() O
}

type router[P root[PO], R root[RO], PO, RO any] struct {
	methods []method[R, PO]
}

func NewRouter[P root[PO], R root[RO], PO, RO any](methods ...method[R, PO]) func(context.Context, P) (R, error) {
	r := &router[P, R, PO, RO]{methods: methods}
	return r.exec
}

func (r *router[P, R, PO, RO]) exec(ctx context.Context, param P) (R, error) {
	// Unpack once so method selection and invocation observe the same value.
	oneof := param.Unpack()
	for _, candidate := range r.methods {
		result, matched, err := candidate.exec(ctx, oneof)
		if !matched {
			continue
		}
		return result, err
	}

	return ZeroValue[R](), fmt.Errorf("no method found for param %T", oneof)
}

type method[R, PO any] interface {
	exec(context.Context, PO) (R, bool, error)
}

type methodImpl[PM message[P, PO], RM message[R, RO], P root[PO], R root[RO], PO, RO any] struct {
	f func(context.Context, PM) (RM, error)
}

func Method[
	PM message[P, PO], RM message[R, RO],
	P root[PO], R root[RO],
	PO, RO any,
](f func(context.Context, PM) (RM, error)) *methodImpl[PM, RM, P, R, PO, RO] {
	return &methodImpl[PM, RM, P, R, PO, RO]{f: f}
}

func (impl *methodImpl[PM, RM, P, R, PO, RO]) exec(ctx context.Context, oneof PO) (R, bool, error) {
	param, ok := any(oneof).(PM)
	if !ok {
		return ZeroValue[R](), false, nil
	}

	reply, err := impl.f(ctx, param)
	if err != nil {
		return ZeroValue[R](), true, err
	}
	return reply.Pack(), true, nil
}

type ActionReply struct{}

func (ActionReply) Unpack() ActionReply  { return ActionReply{} }
func (ActionReply) Pack() ActionReply    { return ActionReply{} }
func (ActionReply) ToOneof() ActionReply { return ActionReply{} }

func NewActionRouter[P root[PO], PO any](methods ...method[ActionReply, PO]) func(context.Context, P) error {
	router := NewRouter[P, ActionReply, PO, ActionReply](methods...)
	return func(ctx context.Context, param P) error {
		_, err := router(ctx, param)
		return err
	}
}

func ActionMethod[
	PM message[P, PO],
	P root[PO],
	PO any,
](f func(context.Context, PM) error) *methodImpl[PM, ActionReply, P, ActionReply, PO, ActionReply] {
	return Method[PM, ActionReply, P, ActionReply, PO, ActionReply](func(ctx context.Context, param PM) (ActionReply, error) {
		return ActionReply{}, f(ctx, param)
	})
}
