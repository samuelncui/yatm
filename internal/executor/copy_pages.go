package executor

import (
	"context"
	"errors"
	"io"

	"github.com/samuelncui/acp"
)

// RunCopyPages feeds bounded caller-owned pages and always drains accepted ACP work.
// Callers own item policy and must drain their result writer after this returns.
func RunCopyPages(ctx context.Context, engine *acp.StreamCopyer, next func(context.Context) ([]acp.Item, error)) (rerr error) {
	// Wait owns pipeline failures; linear target exhaustion may be reported only by Submit.
	defer func() {
		closeErr := engine.Close()
		if waitErr := engine.Wait(); waitErr != nil {
			rerr = waitErr
		}
		rerr = errors.Join(rerr, closeErr)
	}()
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
