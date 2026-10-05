package executor

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	mediapkg "github.com/samuelncui/yatm/internal/media"
	"github.com/stretchr/testify/require"
)

const readInventoryFixture = `<ltfsindex><directory><name>ABC001</name><contents>
<file><name>file.txt</name><length>7</length><extentinfo>
<extent><fileoffset>0</fileoffset><partition>b</partition><startblock>40</startblock><byteoffset>3</byteoffset><bytecount>7</bytecount></extent>
</extentinfo></file></contents></directory></ltfsindex>`

func TestTapeInventoryUsesFreshPathsAndPhysicalExtents(t *testing.T) {
	// The current captured index, not Library inventory, defines the files and their read order.
	root := t.TempDir()
	index := filepath.Join(t.TempDir(), "ABC001.schema")
	require.NoError(t, os.WriteFile(index, []byte(readInventoryFixture), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(root, "file.txt"), []byte("content"), 0o600))
	session := &tapeReadSession{mountPoint: root, indexPath: index}
	var entries []*mediapkg.InventoryEntry
	require.NoError(t, session.WalkInventory(context.Background(), func(entry *mediapkg.InventoryEntry) error {
		entries = append(entries, entry)
		return nil
	}))
	require.Len(t, entries, 1)
	require.Equal(t, "file.txt", entries[0].Path)
	require.Equal(t, int64(7), entries[0].Info.Size())
	require.Len(t, entries[0].Storage.Order, 17)
	require.Equal(t, uint64(40), entries[0].Storage.Metadata.GetLtfs().Extents[0].StartBlock)

}

func TestTapeInventoryRejectsMissingAndLinkedPaths(t *testing.T) {
	for _, linked := range []bool{false, true} {
		t.Run(fmt.Sprintf("linked=%t", linked), func(t *testing.T) {
			root := t.TempDir()
			index := filepath.Join(t.TempDir(), "ABC001.schema")
			require.NoError(t, os.WriteFile(index, []byte(readInventoryFixture), 0o600))
			if linked {
				outside := filepath.Join(t.TempDir(), "outside.txt")
				require.NoError(t, os.WriteFile(outside, []byte("content"), 0o600))
				require.NoError(t, os.Symlink(outside, filepath.Join(root, "file.txt")))
			}
			session := &tapeReadSession{mountPoint: root, indexPath: index}
			called := false
			err := session.WalkInventory(context.Background(), func(*mediapkg.InventoryEntry) error { called = true; return nil })
			require.Error(t, err)
			require.False(t, called)
		})
	}
}

func TestTapeReadFinalizeDoesNotRequireASecondInventoryCapture(t *testing.T) {
	// Only scripts and temporary directories participate; no real drive is accessed.
	root := t.TempDir()
	mountPoint := filepath.Join(root, "mounted")
	require.NoError(t, os.Mkdir(mountPoint, 0o700))
	index := filepath.Join(root, "ABC001.schema")
	require.NoError(t, os.WriteFile(index, []byte(readInventoryFixture), 0o600))
	identity := writeExecutorTestScript(t, "identity", `printf '%s\n' '{"barcode":"ABC001"}' > "$OUT"`)
	unmount := writeExecutorTestScript(t, "unmount", ":")
	exe := New(nil, nil, nil, Paths{}, Scripts{ReadInfo: identity, Umount: unmount}, nil)
	session := &tapeReadSession{backend: exe.NewMediaBackend(1, nil, nil).(*mediaBackend),
		media: &mediapkg.Descriptor{Identity: "ABC001"}, device: "fixture-device", mountPoint: mountPoint,
		tapeDir: root, indexPath: index, recycleKey: func() {}}
	require.NoError(t, session.readInventory(context.Background(), nil))
	require.NoError(t, session.Finalize(context.Background()))
}
