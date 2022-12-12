package tools

import (
	"context"
	"fmt"
	"runtime/debug"

	"github.com/sirupsen/logrus"
)

func Wrap(ctx context.Context, f func()) {
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

		logrus.WithContext(ctx).WithError(err).Errorf("panic: %s", debug.Stack())
	}()

	f()
}
