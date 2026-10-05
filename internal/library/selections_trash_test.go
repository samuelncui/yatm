package library

import (
	"context"
	"testing"

	"github.com/samuelncui/yatm/entity"
	"github.com/stretchr/testify/require"
)

func TestOriginalSelectionsExcludeTrashWithoutChangingRestoreSources(t *testing.T) {
	// Explicit Trash descendants are rejected, while a whole-Library walk prunes the subtree.
	db, lib := newTestLibrary(t)
	ctx := context.Background()
	createFileRows(t, db, &File{ID: TrashFileID, Name: ".Trash", Kind: entity.FileKind_FILE_KIND_DIRECTORY})
	trashDirectory := &File{Name: "removed", ParentID: TrashFileID, Kind: entity.FileKind_FILE_KIND_DIRECTORY}
	createFileRows(t, db, trashDirectory)
	removed := &File{Name: "old.txt", ParentID: trashDirectory.ID, Kind: entity.FileKind_FILE_KIND_REGULAR}
	active := &File{Name: "active.txt", Kind: entity.FileKind_FILE_KIND_REGULAR}
	createFileRows(t, db, removed, active)
	selectFile := func(id int64) []*entity.FileSelection {
		return []*entity.FileSelection{{Target: &entity.FileSelection_Library{Library: &entity.LibrarySelection{FileId: id}}, Scope: entity.FileScope_FILE_SCOPE_ALL}}
	}
	for _, id := range []int64{TrashFileID, trashDirectory.ID, removed.ID} {
		require.ErrorContains(t, lib.ValidateOriginalSelections(ctx, selectFile(id)), "Trash")
	}
	var originals []int64
	require.NoError(t, lib.WalkOriginalSelections(ctx, selectFile(0), func(file *File, _ string) error {
		originals = append(originals, file.ID)
		return nil
	}))
	require.Equal(t, []int64{active.ID}, originals)

	// Removing an original from future Scan/Backup must not hide its saved Restore identity.
	var restoreSources []int64
	require.NoError(t, lib.WalkFileSelections(ctx, selectFile(removed.ID), func(file *File, _ string) error {
		restoreSources = append(restoreSources, file.ID)
		return nil
	}))
	require.Equal(t, []int64{removed.ID}, restoreSources)
}
