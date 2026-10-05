//go:build linux || darwin

package executor

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/samuelncui/yatm/internal/library"
	"github.com/stretchr/testify/require"
)

func TestObserveTrackingUsesExistingStatWithoutOpeningFile(t *testing.T) {
	// The existing observation remains sufficient even when reopening is impossible.
	path := filepath.Join(t.TempDir(), "removed-after-stat")
	require.NoError(t, os.WriteFile(path, nil, 0o600))
	info, err := os.Stat(path)
	require.NoError(t, err)
	require.NoError(t, os.Remove(path))
	keys := ObserveTracking(&library.Location{ExecutorID: "local"}, info)
	require.Len(t, keys, 1)
	require.Equal(t, library.TrackingNative, keys[0].Kind)
	require.Contains(t, keys[0].Scope, "local/fs/")
	require.NotEmpty(t, keys[0].KeyValue)
}
