package executor

import (
	"context"
	"errors"
	"io"

	"github.com/hashicorp/go-multierror"
	"github.com/samuelncui/acp"
)

// RunCopyPages feeds bounded caller-owned pages and always drains accepted ACP work.
// Callers own item policy and must drain their result writer after this returns.
func RunCopyPages(ctx context.Context, engine *acp.StreamCopyer, next func(context.Context) ([]acp.Item, error)) (rerr error) {
	// Drain accepted work without losing an independent page or Submit failure to the pipeline's final error.
	defer func() {
		closeErr := engine.Close()
		for _, err := range []error{engine.Wait(), closeErr} {
			if err == nil || errors.Is(rerr, err) {
				continue
			}
			if rerr == nil {
				rerr = err
			} else {
				rerr = multierror.Append(rerr, err).ErrorOrNil()
			}
		}
	}()

	// A failed page is never submitted; accepted earlier items still settle through the deferred drain.
	for {
		items, err := next(ctx)
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		if err := engine.Submit(items...); err != nil {
			return err
		}
	}
}
