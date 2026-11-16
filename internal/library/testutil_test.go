package library

import (
	"context"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/internal/resource"
	"github.com/samuelncui/yatm/internal/treeops"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func openTestLibraryDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := resource.OpenSQLite(filepath.Join(t.TempDir(), "library.db"))
	require.NoError(t, err)
	return db
}

func newTestLibrary(t *testing.T) (*gorm.DB, *Library) {
	t.Helper()
	db := openTestLibraryDB(t)
	lib := New(db)
	require.NoError(t, lib.PrepareTemporary(t.TempDir()))
	require.NoError(t, lib.AutoMigrate())
	return db, lib
}

// listTestChildren reads one complete directory level through the Files listing.
func listTestChildren(t testing.TB, l *Library, parentID int64) []*File {
	t.Helper()
	page, err := l.ListFileRows(context.Background(), parentID, entity.FileScope_FILE_SCOPE_ALL, false, "", "", maxFileSearchLimit)
	require.NoError(t, err)
	require.Empty(t, page.NextCursor)
	return page.Files
}

func readTestPosition(l *Library, id int64) (*Position, error) {
	position := new(Position)
	if err := l.db.First(position, id).Error; err != nil {
		return nil, err
	}
	return position, nil
}

func createFileRows(t testing.TB, db *gorm.DB, files ...*File) {
	t.Helper()
	for _, file := range files {
		require.NoError(t, createFileRow(db, file))
	}
}

func mergeFiles(ctx context.Context, lib *Library, targetID int64, sourceIDs ...int64) error {
	_, err := lib.MergeFileMembers(ctx, targetID, func(yield func([]int64) error) error { return yield(sourceIDs) }, false)
	return err
}

// trashFiles runs the shared logical Remove operation for the selected Files.
func trashFiles(ctx context.Context, lib *Library, ids ...int64) error {
	refs := make([]string, 0, len(ids))
	for _, id := range ids {
		refs = append(refs, strconv.FormatInt(id, 10))
	}
	_, err := lib.organizeTree(ctx, treeops.Request{Kind: treeops.Delete, Sources: refs})
	return err
}
