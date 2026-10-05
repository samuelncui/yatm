package tools

import (
	"context"
	"fmt"
	"runtime/debug"

	"github.com/sirupsen/logrus"
)

func Wrap(ctx context.Context, f func()) {
	WrapWithLogger(ctx, logrus.StandardLogger(), f)
}

func WrapWithLogger(ctx context.Context, logger *logrus.Logger, f func()) {
	defer func() {
		e := recover()
		if e == nil {
			return
		}

		var err error
		switch v := e.(type) {
		case error:
			err = v
		default:
			err = fmt.Errorf("%v", err)
		}

		logger.WithContext(ctx).WithError(err).Errorf("panic: %s", debug.Stack())
	}()

	f()
}
