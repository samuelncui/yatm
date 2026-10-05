package library

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/samuelncui/yatm/entity"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func TestSelectionSavedAndMixedOverlapReadsAreBatched(t *testing.T) {
	// Versioned leaves are a subset of the same physical and logical roots.
	db, lib := newTestLibrary(t)
	location := locationTestSource(t, lib)
	var expected []int64
	var unsaved []int64
	for i := 0; i < 241; i++ {
		file := &File{Name: fmt.Sprintf("file-%03d", i)}
		createFileRows(t, db, file)
		require.NoError(t, db.Create(&FileLocation{FileID: file.ID, LocationID: location.ID, Path: file.Name}).Error)
		if i%2 == 0 {
			require.NoError(t, db.Create(&FileVersion{FileID: file.ID, Signature: []byte("saved")}).Error)
			expected = append(expected, file.ID)
		} else {
			unsaved = append(unsaved, file.ID)
		}
	}
	selections := []*entity.FileSelection{
		{Target: &entity.FileSelection_Library{Library: &entity.LibrarySelection{}}, Scope: entity.FileScope_FILE_SCOPE_SAVED},
		{Target: &entity.FileSelection_Location{Location: &entity.LocationSelection{LocationId: location.ID}}},
		{Target: &entity.FileSelection_Library{Library: &entity.LibrarySelection{}}, Scope: entity.FileScope_FILE_SCOPE_ALL},
	}
	trace := &fileReadSQL{Interface: logger.Discard}
	db.Logger = trace
	var selected []int64
	require.NoError(t, lib.WalkFileSelections(context.Background(), selections, func(file *File, target string) error {
		require.Equal(t, file.Name, target)
		selected = append(selected, file.ID)
		return nil
	}))
	require.Equal(t, append(expected, unsaved...), selected)
	require.LessOrEqual(t, len(trace.queries), 60, "SAVED leaves and mixed overlap require batch reads, never one query per leaf/root")

	// An explicitly selected unsaved leaf is still excluded by SAVED.
	selections[0].GetLibrary().FileId = unsaved[0]
	selected = nil
	require.NoError(t, lib.WalkFileSelections(context.Background(), selections[:1], func(file *File, _ string) error {
		selected = append(selected, file.ID)
		return nil
	}))
	require.Empty(t, selected)
}

func TestSelectionOverlappingRootsDoNotRepeatTraversal(t *testing.T) {
	ctx := context.Background()
	db, lib := newTestLibrary(t)
	root := &File{Name: "root", Kind: entity.FileKind_FILE_KIND_DIRECTORY}
	createFileRows(t, db, root)
	child := &File{Name: "nested", ParentID: root.ID, Kind: entity.FileKind_FILE_KIND_DIRECTORY}
	createFileRows(t, db, child)
	var all, saved, unsaved []int64
	for i := 0; i < 241; i++ {
		file := &File{Name: fmt.Sprintf("file-%03d", i), ParentID: child.ID}
		createFileRows(t, db, file)
		all = append(all, file.ID)
		if i%2 == 0 {
			require.NoError(t, db.Create(&FileVersion{FileID: file.ID, Signature: []byte("saved")}).Error)
			saved = append(saved, file.ID)
		} else {
			unsaved = append(unsaved, file.ID)
		}
	}
	outside := &File{Name: "outside", ParentID: root.ID}
	createFileRows(t, db, outside)
	selectRoot := func(id int64, scope entity.FileScope) *entity.FileSelection {
		return &entity.FileSelection{Target: &entity.FileSelection_Library{Library: &entity.LibrarySelection{FileId: id}}, Scope: scope}
	}
	trace := &fileReadSQL{Interface: logger.Discard}
	db.Logger = trace
	// Nested roots come first; the later ancestor must not read their child pages again.
	collect := func(roots []*entity.FileSelection) ([]int64, int) {
		trace.queries = nil
		var ids []int64
		require.NoError(t, lib.WalkOriginalSelectionBatches(ctx, roots, func(files []*File, targets []string) error {
			require.LessOrEqual(t, len(files), batchSize)
			for i, file := range files {
				prefix := "root/nested/"
				if file.ID == outside.ID {
					prefix = "root/"
				}
				require.Equal(t, prefix+file.Name, targets[i])
				ids = append(ids, file.ID)
			}
			return nil
		}))
		visits := 0
		for _, query := range trace.queries {
			if strings.Contains(query, "parent_id =") {
				visits++
			}
			require.NotContains(t, query, "file_locations")
			require.NotContains(t, query, "positions")
		}
		return ids, visits
	}
	base := []*entity.FileSelection{selectRoot(child.ID, entity.FileScope_FILE_SCOPE_ALL), selectRoot(root.ID, entity.FileScope_FILE_SCOPE_ALL)}
	ids, visits := collect(base)
	require.Equal(t, append(append([]int64{}, all...), outside.ID), ids)
	var duplicate []*entity.FileSelection
	for i := 0; i < 100; i++ {
		duplicate = append(duplicate, base[0])
	}
	for i := 0; i < 100; i++ {
		duplicate = append(duplicate, base[1])
	}
	again, repeated := collect(duplicate)
	require.Equal(t, ids, again)
	require.Equal(t, visits, repeated, "duplicate roots must not multiply directory page visits")

	// SAVED owns only versioned leaves; ALL must still visit directories for unbacked leaves.
	mixed := []*entity.FileSelection{selectRoot(child.ID, entity.FileScope_FILE_SCOPE_SAVED), base[1]}
	ids, visits = collect(mixed)
	want := append(append(append([]int64{}, saved...), unsaved...), outside.ID)
	require.Equal(t, want, ids)
	duplicate = nil
	for i := 0; i < 100; i++ {
		duplicate = append(duplicate, mixed[0])
	}
	for i := 0; i < 100; i++ {
		duplicate = append(duplicate, mixed[1])
	}
	again, repeated = collect(duplicate)
	require.Equal(t, ids, again)
	require.Equal(t, visits, repeated)
}

func TestSelectionRootValidationBatchesSharedAncestors(t *testing.T) {
	db, lib := newTestLibrary(t)
	ctx := context.Background()
	parent := int64(0)
	var ancestors []*File
	for i := 0; i < 6; i++ {
		file := &File{Name: fmt.Sprintf("level-%d", i), ParentID: parent, Kind: entity.FileKind_FILE_KIND_DIRECTORY}
		createFileRows(t, db, file)
		ancestors = append(ancestors, file)
		parent = file.ID
	}
	var selections []*entity.FileSelection
	for i := 0; i < 241; i++ {
		file := &File{Name: fmt.Sprintf("leaf-%03d", i), ParentID: parent}
		createFileRows(t, db, file)
		selections = append(selections, &entity.FileSelection{Target: &entity.FileSelection_Library{Library: &entity.LibrarySelection{FileId: file.ID}}})
	}
	// Selecting an ancestor too must reuse it at every descendant depth.
	selections = append(selections, &entity.FileSelection{Target: &entity.FileSelection_Library{Library: &entity.LibrarySelection{FileId: ancestors[2].ID}}})
	queries, rows := 0, int64(0)
	require.NoError(t, db.Callback().Query().After("gorm:query").Register("selection-root-cost", func(tx *gorm.DB) {
		queries++
		rows += tx.RowsAffected
		require.LessOrEqual(t, len(tx.Statement.Vars), batchSize)
	}))
	require.NoError(t, lib.ValidateOriginalSelections(ctx, selections))
	require.LessOrEqual(t, queries, 8)
	require.EqualValues(t, 247, rows, "each root and ancestor must be read exactly once")
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	require.ErrorIs(t, lib.ValidateOriginalSelections(cancelled, selections), context.Canceled)
}

func TestSelectionIndexedLocationSkipsCoveredPrefixes(t *testing.T) {
	ctx := context.Background()
	db, lib := newTestLibrary(t)
	location := locationTestSource(t, lib)
	var expected []int64
	for i := 0; i < 241; i++ {
		file := &File{Name: fmt.Sprintf("nested-%03d", i)}
		createFileRows(t, db, file)
		require.NoError(t, db.Create(&FileLocation{FileID: file.ID, LocationID: location.ID, Path: fmt.Sprintf("a/file-%03d", i)}).Error)
		expected = append(expected, file.ID)
	}
	for _, name := range []string{"a!", "a0", "z"} {
		file := &File{Name: name}
		createFileRows(t, db, file)
		require.NoError(t, db.Create(&FileLocation{FileID: file.ID, LocationID: location.ID, Path: name}).Error)
		expected = append(expected, file.ID)
	}
	selectPath := func(name string) *entity.FileSelection {
		return &entity.FileSelection{Target: &entity.FileSelection_Location{Location: &entity.LocationSelection{LocationId: location.ID, Path: name}}}
	}
	roots := []*entity.FileSelection{selectPath("a"), selectPath("")}
	for i := 0; i < 100; i++ {
		roots = append(roots, selectPath("a"), selectPath(""))
	}
	fileRows := int64(0)
	require.NoError(t, db.Callback().Query().After("gorm:query").Register("selection-index-cost", func(tx *gorm.DB) {
		if tx.Statement.Table == "files" {
			fileRows += tx.RowsAffected
		}
	}))
	var selected []int64
	require.NoError(t, lib.WalkOriginalSelections(ctx, roots, func(file *File, target string) error {
		require.Equal(t, file.Name, target)
		selected = append(selected, file.ID)
		return nil
	}))
	require.Equal(t, expected, selected)
	require.EqualValues(t, len(expected), fileRows, "covered prefixes never rehydrate their File rows")
	// A complete earlier root must not hide an invalid explicit indexed path.
	require.ErrorContains(t, lib.WalkOriginalSelections(ctx, []*entity.FileSelection{selectPath(""), selectPath("missing")}, func(*File, string) error { return nil }), "no indexed files")
}

func TestSelectionExplicitSavedLeavesKeepDownstreamBatches(t *testing.T) {
	ctx := context.Background()
	db, lib := newTestLibrary(t)
	var selections []*entity.FileSelection
	var expected []int64
	for i := 0; i < 241; i++ {
		file := &File{Name: fmt.Sprintf("file-%03d", i)}
		createFileRows(t, db, file)
		selections = append(selections, &entity.FileSelection{Target: &entity.FileSelection_Library{Library: &entity.LibrarySelection{FileId: file.ID}}, Scope: entity.FileScope_FILE_SCOPE_SAVED})
		if i%2 == 0 {
			require.NoError(t, db.Create(&FileVersion{FileID: file.ID, Signature: []byte("saved")}).Error)
			expected = append(expected, file.ID)
		}
	}
	trace := &fileReadSQL{Interface: logger.Discard}
	db.Logger = trace
	var selected []int64
	var sizes []int
	require.NoError(t, lib.WalkOriginalSelectionBatches(ctx, selections, func(files []*File, targets []string) error {
		sizes = append(sizes, len(files))
		for i, file := range files {
			require.Equal(t, file.Name, targets[i])
			selected = append(selected, file.ID)
		}
		return nil
	}))
	require.Equal(t, expected, selected)
	require.Equal(t, []int{100, 21}, sizes)
	require.Len(t, trace.queries, 6, "three identity batches and three SAVED eligibility batches")
}

func TestSelectionProjectionRetainsOriginalAndSavedSize(t *testing.T) {
	ctx := context.Background()
	db, lib := newTestLibrary(t)
	location := locationTestSource(t, lib)
	current := &File{Name: "current"}
	saved := &File{Name: "saved"}
	createFileRows(t, db, current, saved)
	require.NoError(t, db.Create(&FileLocation{FileID: current.ID, LocationID: location.ID, Path: current.Name, Size: 5, Mode: 0644}).Error)
	require.NoError(t, db.Create(&FileVersion{FileID: current.ID, Signature: []byte("old"), Size: 99}).Error)
	require.NoError(t, db.Create(&FileVersion{FileID: saved.ID, Signature: []byte("saved"), Size: 9, Mode: 0644}).Error)
	// FileTreeSize needs the original-first compatibility projection after raw traversal.
	size, err := lib.FileTreeSize(ctx, 0, entity.FileScope_FILE_SCOPE_ALL)
	require.NoError(t, err)
	require.EqualValues(t, 14, size)
	selection := []*entity.FileSelection{{Target: &entity.FileSelection_Library{Library: &entity.LibrarySelection{FileId: saved.ID}}, Scope: entity.FileScope_FILE_SCOPE_SAVED}}
	require.NoError(t, lib.WalkFileSelections(ctx, selection, func(file *File, target string) error {
		require.EqualValues(t, 9, file.Size)
		require.EqualValues(t, 0644, file.Mode)
		require.Equal(t, "saved", target)
		return nil
	}))
}
