//go:build linux || darwin

package executor

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/samuelncui/yatm/internal/library"
	"github.com/stretchr/testify/require"
)

func TestTrackerScopesNativeKeysByFilesystemOnce(t *testing.T) {
	// Every entry of one filesystem shares one resolved scope; the observation is never reopened.
	root := t.TempDir()
	tracker := NewTracker()
	var scope string
	for _, name := range []string{"a", "b", "c"} {
		path := filepath.Join(root, name)
		require.NoError(t, os.WriteFile(path, nil, 0o600))
		info, err := Lstat(path)
		require.NoError(t, err)
		require.NoError(t, os.Remove(path))
		keys := tracker.Observe(path, info)
		if len(keys) == 0 {
			t.Skip("temporary directory filesystem has no stable identity")
		}
		require.Len(t, keys, 1)
		require.Equal(t, library.TrackingNative, keys[0].Kind)
		require.True(t, strings.HasPrefix(keys[0].Scope, "fs:") || strings.HasPrefix(keys[0].Scope, "zfs:"), keys[0].Scope)
		require.Len(t, keys[0].KeyValue, 8)
		if scope == "" {
			scope = keys[0].Scope
		}
		require.Equal(t, scope, keys[0].Scope)
	}
	require.Len(t, tracker.scopes, 1)
}

func TestLstatMatchesOrdinaryLstatFacts(t *testing.T) {
	// The identity-bearing observation reports the same facts as os.Lstat for every entry kind.
	root := t.TempDir()
	file := filepath.Join(root, "file")
	require.NoError(t, os.WriteFile(file, []byte("content"), 0o640))
	require.NoError(t, os.Chmod(file, 0o2640))
	link := filepath.Join(root, "link")
	require.NoError(t, os.Symlink("file", link))
	for _, path := range []string{root, file, link} {
		want, err := os.Lstat(path)
		require.NoError(t, err)
		got, err := Lstat(path)
		require.NoError(t, err)
		require.Equal(t, want.Name(), got.Name())
		require.Equal(t, want.Mode(), got.Mode())
		require.Equal(t, want.Size(), got.Size())
		require.True(t, want.ModTime().Equal(got.ModTime()))
		if keys := NewTracker().Observe(path, want); keys != nil {
			require.Equal(t, keys[0].KeyValue, NewTracker().Observe(path, got)[0].KeyValue)
		}
	}
	_, err := Lstat(filepath.Join(root, "missing"))
	require.ErrorIs(t, err, os.ErrNotExist)
}
