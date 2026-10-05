package executor

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	mediapkg "github.com/samuelncui/yatm/internal/media"
	"github.com/stretchr/testify/require"
)

func TestVolumeInventoryCancellationStopsCurrentPage(t *testing.T) {
	// Cancel after one accepted entry in a flat directory, without a recursive boundary.
	root := t.TempDir()
	for _, name := range []string{"one", "two", "three"} {
		require.NoError(t, os.WriteFile(filepath.Join(root, name), []byte(name), 0600))
	}
	session := &volumeReadSession{volume: &mediapkg.Volume{Root: root}}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	accepted := 0
	err := session.WalkInventory(ctx, func(*mediapkg.InventoryEntry) error {
		accepted++
		cancel()
		return nil
	})

	// Keep the accepted item while returning cancellation before any further publication.
	require.ErrorIs(t, err, context.Canceled)
	require.Equal(t, 1, accepted)
}

func TestVolumeInventoryReleasesDirectoriesAfterYieldFailure(t *testing.T) {
	// Keep several directory handles open while a nested callback reports an item failure.
	root := t.TempDir()
	dir := filepath.Join(root, "a", "b", "c")
	require.NoError(t, os.MkdirAll(dir, 0700))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "file"), []byte("data"), 0600))
	before, err := os.ReadDir("/dev/fd")
	if err != nil {
		t.Skip("descriptor inventory unavailable")
	}
	session := &volumeReadSession{volume: &mediapkg.Volume{Root: root}}
	failure := errors.New("item failed")
	for range 8 {
		err := session.WalkInventory(context.Background(), func(*mediapkg.InventoryEntry) error { return failure })
		require.ErrorIs(t, err, failure)
	}

	// Descriptors must be released by unwinding, without relying on a garbage collection.
	after, err := os.ReadDir("/dev/fd")
	require.NoError(t, err)
	require.LessOrEqual(t, len(after), len(before))
}
