//go:build e2e

package e2e

import (
	"bytes"
	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/library"
	"github.com/stretchr/testify/require"
	"sort"
	"testing"
)

func requireTapeWriteOrder(
	t *testing.T,
	items []*entity.ArchiveItem,
	positions map[string]*library.Position,
) {
	t.Helper()
	// Compare only files with real extents, keeping each partition independent.
	type orderedFile struct {
		path  string
		order []byte
	}
	expected := make(map[byte][]string)
	physical := make(map[byte][]orderedFile)
	for _, item := range items {
		position := positions[item.File.TargetPath]
		require.NotNil(t, position, item.File.TargetPath)
		if len(position.StorageOrder) == 0 {
			continue
		}
		partition := position.StorageOrder[0]
		expected[partition] = append(expected[partition], item.File.TargetPath)
		physical[partition] = append(physical[partition], orderedFile{
			path: item.File.TargetPath, order: position.StorageOrder,
		})
	}

	// Within each LTFS partition, the physical extents must follow the Archive request order.
	for partition, want := range expected {
		files := physical[partition]
		sort.Slice(files, func(i, j int) bool {
			return bytes.Compare(files[i].order, files[j].order) < 0
		})
		got := make([]string, 0, len(files))
		for _, file := range files {
			got = append(got, file.path)
		}
		require.Equal(t, want, got, "partition %c", partition)
	}
}
