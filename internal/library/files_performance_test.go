package library

import (
	"context"
	"fmt"
	"testing"

	"github.com/samuelncui/yatm/entity"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm/logger"
)

func TestFileCompatibilityProjectionBatchesFacts(t *testing.T) {
	// Mix original, saved-only and unlinked rows across the internal batch boundary.
	db, lib := newTestLibrary(t)
	location := locationTestSource(t, lib)
	rows := make([]*File, 201)
	for i := range rows {
		rows[i] = &File{Name: fmt.Sprintf("file-%03d", i)}
		createFileRows(t, db, rows[i])
		if i%3 == 0 {
			require.NoError(t, db.Create(&FileLocation{FileID: rows[i].ID, LocationID: location.ID, Path: rows[i].Name, Size: 10}).Error)
		}
		if i%3 != 2 {
			require.NoError(t, db.Create(&FileVersion{FileID: rows[i].ID, Signature: []byte("older"), Size: 20}).Error)
			require.NoError(t, db.Create(&FileVersion{FileID: rows[i].ID, Signature: []byte("newer"), Size: 30}).Error)
		}
	}

	// Originals win over history; one original and one fallback read serve each batch.
	trace := &fileReadSQL{Interface: logger.Discard}
	db.Logger = trace
	require.NoError(t, hydrateFileViews(db, rows...))
	require.Len(t, trace.queries, 6)
	for i, row := range rows {
		require.Equal(t, []int64{10, 30, 0}[i%3], row.Size)
	}
}

func TestFilesQueryReusesCompilationAcrossBatches(t *testing.T) {
	// Compile once, then make reparsing observable without adding production instrumentation.
	db, lib := newTestLibrary(t)
	query, err := lib.CompileFilesQuery("name:match*")
	require.NoError(t, err)
	query.Text = "unsupported:must-not-be-reparsed"
	rows := make([]LiveQueryRow, 129)
	for i := range rows {
		rows[i] = LiveQueryRow{Name: fmt.Sprintf("match-%d", i)}
	}
	trace := &fileReadSQL{Interface: logger.Discard}
	db.Logger = trace
	for range 3 {
		matched, err := lib.MatchLiveQuery(context.Background(), query, rows)
		require.NoError(t, err)
		require.Len(t, matched, len(rows))
	}
	require.Len(t, trace.queries, 9, "each 129-row batch needs three bounded SQL statements and no recompilation")
	_, err = lib.ListFileQueryRows(context.Background(), 0, entity.FileScope_FILE_SCOPE_ALL, false, query, "", 10)
	require.NoError(t, err)
}

func TestSelectionMembershipUsesBatchAncestors(t *testing.T) {
	// Unrelated descendants do not need to be materialized to check one association batch.
	db, lib := newTestLibrary(t)
	root := &File{Name: "selected", Kind: entity.FileKind_FILE_KIND_DIRECTORY}
	other := &File{Name: "other", Kind: entity.FileKind_FILE_KIND_DIRECTORY}
	createFileRows(t, db, root, other)
	ids := make([]int64, 256)
	for i := range ids {
		file := &File{Name: fmt.Sprintf("leaf-%03d", i), ParentID: root.ID}
		if i%2 != 0 {
			file.ParentID = other.ID
		}
		createFileRows(t, db, file)
		ids[i] = file.ID
	}
	trace := &fileReadSQL{Interface: logger.Discard}
	db.Logger = trace
	matched, err := lib.MatchSelectionFiles(context.Background(), ids, []*entity.FileSelection{{Target: &entity.FileSelection_Library{Library: &entity.LibrarySelection{FileId: root.ID}}, Scope: entity.FileScope_FILE_SCOPE_ALL}})
	require.NoError(t, err)
	require.Len(t, trace.queries, 1)
	require.Len(t, matched, 128)
	for i, id := range ids {
		require.Equal(t, i%2 == 0, matched[id])
	}
	// The recursive term follows indexed parent IDs, never all descendants of a selected root.
	require.Contains(t, trace.queries[0], "parent.id = ancestors.parent_id")
}
