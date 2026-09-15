package library

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/samuelncui/yatm/entity"
	"github.com/stretchr/testify/require"
)

func TestMoveFileRejectsInvalidParentWithoutLosingTree(t *testing.T) {
	// Logical moves must preserve the same acyclic directory-parent contract enforced by import.
	for _, test := range []struct {
		name   string
		parent func(*File, *File, *File) int64
	}{
		{name: "self", parent: func(root, _, _ *File) int64 { return root.ID }},
		{name: "descendant", parent: func(_, child, _ *File) int64 { return child.ID }},
		{name: "regular file", parent: func(_, _, regular *File) int64 { return regular.ID }},
		{name: "missing parent", parent: func(_, _, _ *File) int64 { return 99999 }},
	} {
		t.Run(test.name, func(t *testing.T) {
			// Create a small valid logical tree and an unrelated regular File.
			ctx := context.Background()
			lib := newJSONLTestLibrary(t)
			root, err := lib.MkdirAll(ctx, 0, "root", 0o755)
			require.NoError(t, err)
			child, err := lib.MkdirAll(ctx, root.ID, "child", 0o755)
			require.NoError(t, err)
			regular := &File{Name: "regular.txt"}
			require.NoError(t, lib.SaveFile(ctx, regular))

			// Reject the illegal destination before making the original root unreachable.
			root.ParentID = test.parent(root, child, regular)
			require.Error(t, lib.MoveFile(ctx, root))
			stored, err := lib.GetFile(ctx, root.ID)
			require.NoError(t, err)
			require.Zero(t, stored.ParentID)
		})
	}
}

func TestListParentsRetainsDeepRootAndRejectsInvalidAncestry(t *testing.T) {
	// Deep valid trees need complete breadcrumbs, including the root used by Trash classification.
	ctx := context.Background()
	lib := newJSONLTestLibrary(t)
	leaf, err := lib.MkdirAll(ctx, 0, strings.Repeat("folder/", 39)+"leaf", 0o755)
	require.NoError(t, err)
	parents, err := lib.ListParents(ctx, leaf.ID)
	require.NoError(t, err)
	require.Len(t, parents, 40)
	require.Zero(t, parents[0].ParentID)
	require.Equal(t, leaf.ID, parents[len(parents)-1].ID)

	// Malformed imported or manually edited catalogs must produce an error rather than a partial root.
	cycle := &File{ID: 1000, ParentID: 1000, Name: "cycle", Kind: entity.FileKind_FILE_KIND_DIRECTORY}
	require.NoError(t, lib.SaveFile(ctx, cycle))
	_, err = lib.ListParents(ctx, cycle.ID)
	require.ErrorContains(t, err, "cycle")
	var parentID int64
	for index := 0; index <= maxFilePathDepth; index++ {
		file := &File{ID: int64(2000 + index), ParentID: parentID, Name: fmt.Sprintf("deep-%d", index), Kind: entity.FileKind_FILE_KIND_DIRECTORY}
		require.NoError(t, lib.SaveFile(ctx, file))
		parentID = file.ID
	}
	_, err = lib.ListParents(ctx, parentID)
	require.ErrorContains(t, err, "exceeds")
}

func TestMoveFileToPathRollsBackNestedDirectories(t *testing.T) {
	// A nested rename may allocate directories before discovering the final destination is a descendant.
	ctx := context.Background()
	lib := newJSONLTestLibrary(t)
	root, err := lib.MkdirAll(ctx, 0, "root", 0o755)
	require.NoError(t, err)
	child, err := lib.MkdirAll(ctx, root.ID, "child", 0o755)
	require.NoError(t, err)

	// Keep the failed allocation, original root and child inside one rollback boundary.
	require.Error(t, lib.MoveFileToPath(ctx, root, "root/new/renamed"))
	created, err := lib.GetByName(ctx, root.ID, "new")
	require.NoError(t, err)
	require.Nil(t, created)
	stored, err := lib.GetFile(ctx, root.ID)
	require.NoError(t, err)
	require.Zero(t, stored.ParentID)
	require.Equal(t, "root", stored.Name)
	storedChild, err := lib.GetFile(ctx, child.ID)
	require.NoError(t, err)
	require.Equal(t, root.ID, storedChild.ParentID)
}

func TestMoveFileToPathCreatesValidNestedDestination(t *testing.T) {
	// A normal nested move keeps the File identity and user metadata while allocating its target directories.
	ctx := context.Background()
	lib := newJSONLTestLibrary(t)
	file := &File{Name: "before.txt", Note: "keep organization"}
	require.NoError(t, lib.SaveFile(ctx, file))
	id := file.ID
	require.NoError(t, lib.MoveFileToPath(ctx, file, "folder/sub/after.txt"))
	require.Equal(t, id, file.ID)
	require.Equal(t, "after.txt", file.Name)

	// The old path disappears, and resolving the complete new path reaches the same File.
	stored, err := lib.GetByPath(ctx, 0, "folder/sub/after.txt")
	require.NoError(t, err)
	require.Equal(t, id, stored.ID)
	require.Equal(t, "keep organization", stored.Note)
	old, err := lib.GetByName(ctx, 0, "before.txt")
	require.NoError(t, err)
	require.Nil(t, old)
}

func TestMoveFileToPathPreservesWhitespaceInsideComponents(t *testing.T) {
	// Whitespace inside a validated full target path belongs to the logical name component.
	ctx := context.Background()
	lib := newJSONLTestLibrary(t)
	file := &File{Name: "before.txt"}
	require.NoError(t, lib.SaveFile(ctx, file))
	require.NoError(t, lib.MoveFileToPath(ctx, file, "folder /after.txt"))

	// Resolving the directory directly avoids any presentation-level path normalization.
	parent, err := lib.GetFile(ctx, file.ParentID)
	require.NoError(t, err)
	require.Equal(t, "folder ", parent.Name)
	unexpected, err := lib.GetByName(ctx, 0, "folder")
	require.NoError(t, err)
	require.Nil(t, unexpected)
}

func TestMoveFileRequiresExistingSource(t *testing.T) {
	// A move is not an upsert, including when nested destination directories would otherwise be allocated.
	ctx := context.Background()
	lib := newJSONLTestLibrary(t)
	file := &File{ID: 99999, Name: "missing.txt"}
	require.ErrorIs(t, lib.MoveFile(ctx, file), ErrFileNotFound)
	require.ErrorIs(t, lib.MoveFileToPath(ctx, file, "new/missing.txt"), ErrFileNotFound)

	// Both entry points leave an empty catalog rather than inventing the requested source identity.
	rows, err := lib.List(ctx, 0)
	require.NoError(t, err)
	require.Empty(t, rows)
}

func TestMkdirAllCurrentDirectoryAndParentValidation(t *testing.T) {
	// Current-directory requests return an existing identity and never create a literal dot directory.
	ctx := context.Background()
	lib := newJSONLTestLibrary(t)
	root, err := lib.MkdirAll(ctx, 0, ".", 0o755)
	require.NoError(t, err)
	require.Zero(t, root.ID)
	directory, err := lib.MkdirAll(ctx, 0, "folder", 0o755)
	require.NoError(t, err)
	same, err := lib.MkdirAll(ctx, directory.ID, ".", 0o755)
	require.NoError(t, err)
	require.Equal(t, directory.ID, same.ID)
	var dots int64
	require.NoError(t, lib.db.Model(ModelFile).Where("name = ?", ".").Count(&dots).Error)
	require.Zero(t, dots)

	// Invalid parents are rejected even for a no-op path; regular content cannot contain children.
	regular := &File{Name: "regular.txt"}
	require.NoError(t, lib.SaveFile(ctx, regular))
	for _, parentID := range []int64{regular.ID, 99999} {
		for _, name := range []string{".", "child"} {
			_, err := lib.MkdirAll(ctx, parentID, name, 0o755)
			require.Error(t, err)
		}
	}
	for _, name := range []string{"", "..", "../escape", "new/../escape", "/absolute", "new\\child"} {
		_, err := lib.MkdirAll(ctx, directory.ID, name, 0o755)
		require.Error(t, err, name)
	}
	children, err := lib.List(ctx, directory.ID)
	require.NoError(t, err)
	require.Empty(t, children)
}

func TestLogicalMutationRejectsExistingAncestorCycle(t *testing.T) {
	// Defensive ancestry checks must stop even when inspecting a previously invalid catalog.
	ctx := context.Background()
	lib := newJSONLTestLibrary(t)
	cycle := &File{ID: 100, ParentID: 100, Name: "cycle", Kind: entity.FileKind_FILE_KIND_DIRECTORY}
	require.NoError(t, lib.SaveFile(ctx, cycle))
	file := &File{Name: "kept.txt"}
	require.NoError(t, lib.SaveFile(ctx, file))

	// Both move and mkdir report the cycle without attaching new nodes to it.
	file.ParentID = cycle.ID
	require.ErrorContains(t, lib.MoveFile(ctx, file), "cycle")
	_, err := lib.MkdirAll(ctx, cycle.ID, "new", 0o755)
	require.ErrorContains(t, err, "cycle")
	stored, err := lib.GetFile(ctx, file.ID)
	require.NoError(t, err)
	require.Zero(t, stored.ParentID)
}
