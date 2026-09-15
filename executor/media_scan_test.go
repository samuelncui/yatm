package executor

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/samuelncui/yatm/library"
	mediapkg "github.com/samuelncui/yatm/media"
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

	// A different final placement invalidates the observation even if the names and lengths match.
	require.NoError(t, os.WriteFile(index, []byte(strings.ReplaceAll(readInventoryFixture, "<startblock>40", "<startblock>41")), 0o600))
	_, err := session.readInventory(context.Background(), nil)
	require.ErrorContains(t, err, "inventory changed")
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
			require.Empty(t, session.inventoryDigest)
		})
	}
}

func TestTapeInventoryFinalizationRequiresNewCapture(t *testing.T) {
	for _, captured := range []bool{false, true} {
		t.Run(fmt.Sprintf("captured=%t", captured), func(t *testing.T) {
			// Only scripts and temporary directories participate; no real drive is accessed.
			root := t.TempDir()
			mountPoint := filepath.Join(root, "mounted")
			require.NoError(t, os.Mkdir(mountPoint, 0o700))
			index := filepath.Join(root, "ABC001.schema")
			require.NoError(t, os.WriteFile(index, []byte(readInventoryFixture), 0o600))
			identity := writeExecutorTestScript(t, "identity", `printf '%s\n' '{"barcode":"ABC001"}' > "$OUT"`)
			body := ":"
			if captured {
				fixture := filepath.Join(root, "final-index.xml")
				require.NoError(t, os.WriteFile(fixture, []byte(readInventoryFixture), 0o600))
				body = fmt.Sprintf("/bin/cp %s %s", strconv.Quote(fixture), strconv.Quote(index))
			}
			unmount := writeExecutorTestScript(t, "unmount", body)
			exe := New(nil, nil, nil, Paths{}, Scripts{ReadInfo: identity, Umount: unmount}, nil)
			session := &tapeReadSession{backend: exe.NewMediaBackend(1, nil).(*mediaBackend),
				media: &library.Media{Identity: "ABC001"}, device: "fixture-device", mountPoint: mountPoint,
				tapeDir: root, indexPath: index, recycleKey: func() {}}
			_, err := session.readInventory(context.Background(), nil)
			require.NoError(t, err)
			deadline := 2 * time.Second
			if captured {
				deadline = 10 * time.Second
			}
			ctx, cancel := context.WithTimeout(context.Background(), deadline)
			defer cancel()
			err = session.Finalize(ctx)
			if captured {
				require.NoError(t, err)
				return
			}
			// Cancellation may surface from script execution or waiting for capture; neither may publish the stale index.
			require.Error(t, err)
			require.NoFileExists(t, index)
		})
	}
}
