//go:build e2e

package e2e

import (
	"context"
	"io"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
)

func readHTTPContent(t *testing.T, ctx context.Context, url string) []byte {
	t.Helper()
	// Browser content and Preview delivery are HTTP protocol checks, not CLI file exports.
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	require.NoError(t, err)
	response, err := http.DefaultClient.Do(request)
	require.NoError(t, err)

	// Consume and close the response before reporting protocol or content failures.
	data, readErr := io.ReadAll(response.Body)
	closeErr := response.Body.Close()
	require.NoError(t, readErr)
	require.NoError(t, closeErr)
	require.Equal(t, http.StatusOK, response.StatusCode, string(data))
	return data
}
