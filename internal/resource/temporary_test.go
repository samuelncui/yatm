package resource

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestTemporaryDBOwnsOnlyItsDirectory(t *testing.T) {
	// Concurrent requests with the same prefix must have independent files and pools.
	root := t.TempDir()
	first, err := OpenTemporaryDB(root, "operation-")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, first.Close()) })
	second, err := OpenTemporaryDB(root, "operation-")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, second.Close()) })
	require.NotEqual(t, first.Directory, second.Directory)
	connection, err := first.DB.DB()
	require.NoError(t, err)
	path := first.Directory
	require.NoError(t, first.DB.Exec("CREATE TABLE item(id INTEGER PRIMARY KEY)").Error)

	// Closing removes the owned database and prevents handle reuse without affecting its peer.
	require.NoError(t, first.Close())
	require.NoError(t, first.Close())
	require.Error(t, connection.Ping())
	_, err = os.Stat(path)
	require.ErrorIs(t, err, os.ErrNotExist)
	require.NoError(t, second.DB.Exec("CREATE TABLE item(id INTEGER PRIMARY KEY)").Error)
	entries, err := os.ReadDir(root)
	require.NoError(t, err)
	require.Len(t, entries, 1)
	require.Equal(t, filepath.Base(second.Directory), entries[0].Name())
}
