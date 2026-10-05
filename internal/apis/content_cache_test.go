//go:build linux || darwin

package apis

import (
	"context"
	"testing"

	"github.com/samuelncui/acp"
	"github.com/stretchr/testify/require"
)

// cacheContentSignature hashes one file and refreshes its disposable ACP cache entry.
// Library tests seed the cache this way before admitting or comparing live files.
func cacheContentSignature(t *testing.T, ctx context.Context, filename string) {
	t.Helper()

	item := &singleFileItem{filename: filename}
	stream, err := acp.NewStream(ctx, item.onResults, acp.WithHashPolicy(acp.HashCachedOrReadRefresh))
	require.NoError(t, err)
	require.NoError(t, stream.Submit(item))
	require.NoError(t, stream.Close())
	require.NoError(t, stream.Wait())
	require.NoError(t, item.failure)
	require.NotNil(t, item.result)
	require.Len(t, item.result.SHA256, 32)
}

// singleFileItem is one targetless item whose outcome the results callback records.
type singleFileItem struct {
	filename string
	result   *acp.Result
	failure  error
}

func (i *singleFileItem) Source() string { return i.filename }

func (i *singleFileItem) Targets() []string { return nil }

// onResults records the one terminal outcome of the item.
func (i *singleFileItem) onResults(results []acp.Result) error {
	for _, result := range results {
		item, ok := result.Job.(*singleFileItem)
		if !ok {
			continue
		}
		if result.Err != nil {
			item.failure = result.Err
			continue
		}
		copied := result
		item.result = &copied
	}
	return nil
}
