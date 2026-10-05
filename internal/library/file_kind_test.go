package library

import (
	"context"
	"io/fs"
	"testing"

	"github.com/samuelncui/yatm/entity"
	"github.com/stretchr/testify/require"
)

func TestSaveFilePreservesKindDespitePresentationMode(t *testing.T) {
	for _, test := range []struct {
		name string
		kind entity.FileKind
		mode uint32
	}{
		{"regular with directory presentation", entity.FileKind_FILE_KIND_REGULAR, uint32(fs.ModeDir | 0o755)},
		{"directory with regular presentation", entity.FileKind_FILE_KIND_DIRECTORY, 0o644},
	} {
		t.Run(test.name, func(t *testing.T) {
			// Persist an explicit logical kind before changing transient display facts.
			ctx := context.Background()
			_, lib := newTestLibrary(t)
			file := &File{Name: "before", Kind: test.kind}
			require.NoError(t, lib.SaveFile(ctx, file))
			loaded, err := lib.GetFile(ctx, file.ID)
			require.NoError(t, err)

			// A logical rename must not turn presentation data into stored identity.
			loaded.Name, loaded.Mode = "after", test.mode
			require.NoError(t, lib.SaveFile(ctx, loaded))
			require.Equal(t, test.kind, loaded.Kind)
			stored, err := lib.GetFile(ctx, file.ID)
			require.NoError(t, err)
			require.Equal(t, "after", stored.Name)
			require.Equal(t, test.kind, stored.Kind)
		})
	}
}

func TestMkdirAndTrashSetExplicitDirectoryKind(t *testing.T) {
	// Both nested construction and repeat lookup use logical directory identity.
	ctx := context.Background()
	db, lib := newTestLibrary(t)
	directory, err := lib.MkdirAll(ctx, Root.ID, "collection/nested", 0o700)
	require.NoError(t, err)
	require.Equal(t, entity.FileKind_FILE_KIND_DIRECTORY, directory.Kind)
	parent, err := lib.GetFile(ctx, directory.ParentID)
	require.NoError(t, err)
	require.Equal(t, entity.FileKind_FILE_KIND_DIRECTORY, parent.Kind)
	reused, err := lib.MkdirAll(ctx, Root.ID, "collection/nested", 0o755)
	require.NoError(t, err)
	require.Equal(t, directory.ID, reused.ID)

	// Trash creation stores a directory before creating its child checkpoint.
	checkpoint, err := lib.newTrash(ctx, db)
	require.NoError(t, err)
	require.Equal(t, entity.FileKind_FILE_KIND_DIRECTORY, checkpoint.Kind)
	trash, err := lib.GetFile(ctx, TrashFileID)
	require.NoError(t, err)
	require.Equal(t, entity.FileKind_FILE_KIND_DIRECTORY, trash.Kind)

	// System-field repair restores the explicit kind without replacing annotations.
	trash.Kind, trash.Mode, trash.Note = entity.FileKind_FILE_KIND_REGULAR, 0, "keep"
	require.NoError(t, lib.SaveFile(ctx, trash))
	_, err = lib.newTrash(ctx, db)
	require.NoError(t, err)
	trash, err = lib.GetFile(ctx, TrashFileID)
	require.NoError(t, err)
	require.Equal(t, entity.FileKind_FILE_KIND_DIRECTORY, trash.Kind)
	require.Equal(t, "keep", trash.Note)
}

func TestMoveFileUsesKindForDirectoryCollisions(t *testing.T) {
	for _, test := range []struct {
		name  string
		kind  entity.FileKind
		mode  uint32
		merge bool
	}{
		{"directory with regular presentation", entity.FileKind_FILE_KIND_DIRECTORY, 0o644, true},
		{"regular with directory presentation", entity.FileKind_FILE_KIND_REGULAR, uint32(fs.ModeDir | 0o755), false},
	} {
		t.Run(test.name, func(t *testing.T) {
			// Give the source and destination the same name under different parents.
			ctx := context.Background()
			_, lib := newTestLibrary(t)
			parent, err := lib.MkdirAll(ctx, Root.ID, "parent", 0o755)
			require.NoError(t, err)
			target, err := lib.MkdirAll(ctx, Root.ID, "item", 0o755)
			require.NoError(t, err)
			source := &File{ParentID: parent.ID, Name: "item", Kind: test.kind, Note: "source"}
			require.NoError(t, lib.SaveFile(ctx, source))
			sourceID := source.ID

			// Only two explicit directory identities may merge, regardless of display mode.
			source.ParentID, source.Mode = Root.ID, test.mode
			err = lib.MoveFile(ctx, source)
			if !test.merge {
				require.ErrorContains(t, err, "target already exists")
				stored, err := lib.GetFile(ctx, sourceID)
				require.NoError(t, err)
				require.Equal(t, parent.ID, stored.ParentID)
				require.Equal(t, test.kind, stored.Kind)
				return
			}
			require.NoError(t, err)
			require.Equal(t, target.ID, source.ID)
			require.Equal(t, entity.FileKind_FILE_KIND_DIRECTORY, source.Kind)
			require.Equal(t, "source", source.Note)
			_, err = lib.GetFile(ctx, sourceID)
			require.ErrorIs(t, err, ErrFileNotFound)
		})
	}
}

func TestImportedFilesReuseExplicitDirectoryKinds(t *testing.T) {
	// Each admitted File is independent, but its logical ancestor directories are reused.
	ctx := context.Background()
	db, lib := newTestLibrary(t)
	first, err := createImportedFile(db, "Unforged/Documents/first.txt")
	require.NoError(t, err)
	second, err := createImportedFile(db, "Unforged/Documents/second.txt")
	require.NoError(t, err)
	require.NotEqual(t, first.ID, second.ID)
	require.Equal(t, first.ParentID, second.ParentID)
	require.Equal(t, entity.FileKind_FILE_KIND_REGULAR, first.Kind)
	require.Equal(t, entity.FileKind_FILE_KIND_REGULAR, second.Kind)

	// All persisted ancestors are directories without a Mode-derived save hook.
	parents, err := lib.ListParents(ctx, first.ParentID)
	require.NoError(t, err)
	require.Len(t, parents, 2)
	for _, parent := range parents {
		require.Equal(t, entity.FileKind_FILE_KIND_DIRECTORY, parent.Kind)
	}
}
