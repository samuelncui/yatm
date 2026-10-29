package executor

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/samuelncui/acp"
	"github.com/stretchr/testify/require"
)

func TestCopyPagesPreservesIndependentFailures(t *testing.T) {
	for _, test := range []struct {
		name           string
		page, pipeline bool
	}{{"page", true, false}, {"pipeline", false, true}, {"both", true, true}, {"success", false, false}} {
		t.Run(test.name, func(t *testing.T) {
			// Real ACP result delivery races with loading the next caller-owned page.
			path := filepath.Join(t.TempDir(), "source")
			require.NoError(t, os.WriteFile(path, []byte("content"), 0600))
			pageErr := errors.New("load next Archive page failed")
			pipelineErr := errors.New("persist copy results failed")
			delivered := make(chan struct{})
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			engine, err := acp.NewStream(ctx, func([]acp.Result) error {
				close(delivered)
				if test.pipeline {
					return pipelineErr
				}
				return nil
			}, acp.WithResultBatch(1), acp.WithHashPolicy(acp.HashRead))
			require.NoError(t, err)
			t.Cleanup(func() { _ = engine.Close() })

			// The second page returns its own failure after the first result has reached ACP's callback.
			calls := 0
			err = RunCopyPages(ctx, engine, func(ctx context.Context) ([]acp.Item, error) {
				calls++
				if calls == 1 {
					return []acp.Item{&acp.SimpleJob{Path: path}}, nil
				}
				select {
				case <-delivered:
				case <-ctx.Done():
					return nil, ctx.Err()
				}
				if test.page {
					return nil, pageErr
				}
				return nil, io.EOF
			})
			require.Equal(t, test.page, errors.Is(err, pageErr))
			require.Equal(t, test.pipeline, errors.Is(err, pipelineErr))
			if test.page && !test.pipeline {
				require.Same(t, pageErr, err)
			}
			if !test.page && !test.pipeline {
				require.NoError(t, err)
			}
		})
	}
}
