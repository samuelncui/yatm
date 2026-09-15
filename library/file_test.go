package library

import (
	"context"
	"fmt"
	"testing"

	"github.com/samuelncui/yatm/entity"
	"github.com/stretchr/testify/require"
)

func TestListWithSizeAggregatesPagedSubtrees(t *testing.T) {
	// Seed multiple direct roots and a subtree larger than one SQL page.
	_, lib := newTestLibrary(t)
	fixtures := []*File{
		{ID: 1, Name: "a.txt", Mode: 0o644, Size: 3},
		{ID: 2, Name: "b", Kind: entity.FileKind_FILE_KIND_DIRECTORY},
		{ID: 3, Name: "c", Kind: entity.FileKind_FILE_KIND_DIRECTORY},
		{ID: 4, ParentID: 2, Name: "nested", Kind: entity.FileKind_FILE_KIND_DIRECTORY},
		{ID: 5, ParentID: 4, Name: "deep.txt", Mode: 0o644, Size: 7},
	}
	require.NoError(t, lib.db.Create(fixtures).Error)
	seedTestArchivedFacts(t, lib.db, fixtures...)
	children := make([]*File, 0, batchSize+1)
	for index := 0; index <= batchSize; index++ {
		children = append(children, &File{
			ParentID: 2,
			Name:     fmt.Sprintf("file-%03d", index),
			Mode:     0o644,
			Size:     1,
		})
	}
	require.NoError(t, lib.db.CreateInBatches(children, batchSize).Error)
	seedTestArchivedFacts(t, lib.db, children...)

	// Aggregate every page and retain the direct-child name order.
	files, err := lib.ListWithSize(context.Background(), 0)
	require.NoError(t, err)
	require.Len(t, files, 3)
	require.Equal(t, "a.txt", files[0].Name)
	require.Equal(t, int64(3), files[0].Size)
	require.Equal(t, "b", files[1].Name)
	require.Equal(t, int64(batchSize+1+7), files[1].Size)
	require.Equal(t, "c", files[2].Name)
	require.Zero(t, files[2].Size)
}

func TestListWithSizeAggregatesDeepTree(t *testing.T) {
	// Seed a directory chain deep enough to exercise the explicit DFS stack.
	_, lib := newTestLibrary(t)
	const depth = 256
	files := make([]*File, 0, depth+2)
	files = append(files, &File{ID: 1, Name: "root", Kind: entity.FileKind_FILE_KIND_DIRECTORY})
	for id := int64(2); id <= depth+1; id++ {
		files = append(files, &File{
			ID:       id,
			ParentID: id - 1,
			Name:     fmt.Sprintf("directory-%03d", id),
			Kind:     entity.FileKind_FILE_KIND_DIRECTORY,
		})
	}
	files = append(files, &File{ID: depth + 2, ParentID: depth + 1, Name: "leaf", Mode: 0o644, Size: 17})
	require.NoError(t, lib.db.CreateInBatches(files, batchSize).Error)
	seedTestArchivedFacts(t, lib.db, files...)

	// Aggregate the complete chain without retaining a breadth-sized frontier.
	result, err := lib.ListWithSize(context.Background(), 0)
	require.NoError(t, err)
	require.Len(t, result, 1)
	require.Equal(t, "root", result[0].Name)
	require.Equal(t, int64(17), result[0].Size)
}
