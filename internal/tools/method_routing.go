package tools

import (
	"context"
	"fmt"
)

type root[O any] interface {
	Unpack() O
}

// variant lets ActionMethod infer the oneof interface from a generated variant type.
type variant[O any] interface {
	ToOneof() O
}

// NewActionRouter dispatches a request oneof to the first handler registered for its variant.
func NewActionRouter[P root[PO], PO any](methods ...func(context.Context, PO) (bool, error)) func(context.Context, P) error {
	return func(ctx context.Context, param P) error {
		// Unpack once so method selection and invocation observe the same value.
		oneof := param.Unpack()
		for _, method := range methods {
			if matched, err := method(ctx, oneof); matched {
				return err
			}
		}
		return fmt.Errorf("no method found for param %T", oneof)
	}
}

// ActionMethod adapts a handler for one generated variant type to NewActionRouter.
func ActionMethod[PM variant[PO], PO any](f func(context.Context, PM) error) func(context.Context, PO) (bool, error) {
	return func(ctx context.Context, oneof PO) (bool, error) {
		param, ok := any(oneof).(PM)
		if !ok {
			return false, nil
		}
		return true, f(ctx, param)
	}
}
