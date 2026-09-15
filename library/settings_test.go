package library

import (
	"bytes"
	"context"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/samuelncui/yatm/entity"
	"github.com/stretchr/testify/require"
)

func TestLibrarySettingsPersistWithOptimisticRevision(t *testing.T) {
	// An absent setting preserves visibility without creating state on read.
	ctx := context.Background()
	db, lib := newTestLibrary(t)
	initial, err := lib.GetLibrarySettings(ctx)
	require.NoError(t, err)
	require.True(t, initial.IncludeUnbackedFiles)
	require.Zero(t, initial.Revision)
	var count int64
	require.NoError(t, db.Model(&LibrarySettings{}).Count(&count).Error)
	require.Zero(t, count)

	// A stale writer cannot override either the first persisted setting or a later edit.
	saved, err := lib.UpdateLibrarySettings(ctx, false, initial.Revision)
	require.NoError(t, err)
	require.Positive(t, saved.Revision)
	_, err = lib.UpdateLibrarySettings(ctx, true, initial.Revision)
	require.ErrorIs(t, err, ErrOnlineConflict)
	read, err := New(db).GetLibrarySettings(ctx)
	require.NoError(t, err)
	require.False(t, read.IncludeUnbackedFiles)
	require.Equal(t, saved.Revision, read.Revision)
	updated, err := lib.UpdateLibrarySettings(ctx, true, saved.Revision)
	require.NoError(t, err)
	require.Greater(t, updated.Revision, saved.Revision)
	_, err = lib.UpdateLibrarySettings(ctx, false, saved.Revision)
	require.ErrorIs(t, err, ErrOnlineConflict)
}

func TestSavedScopeFiltersBeforeListingSearchAndSelectionPages(t *testing.T) {
	// Put more than a page of unbacked names before two saved files and a retained empty directory.
	ctx := context.Background()
	db, lib := newTestLibrary(t)
	for index := 0; index < 105; index++ {
		require.NoError(t, lib.SaveFile(ctx, &File{Name: fmt.Sprintf("a-%03d", index), Kind: entity.FileKind_FILE_KIND_REGULAR}))
	}
	directory := &File{Name: "m-empty", Kind: entity.FileKind_FILE_KIND_DIRECTORY}
	first := &File{Name: "z-first", Kind: entity.FileKind_FILE_KIND_REGULAR, Note: "kept"}
	second := &File{Name: "z-second", Kind: entity.FileKind_FILE_KIND_REGULAR}
	for _, file := range []*File{directory, first, second} {
		require.NoError(t, lib.SaveFile(ctx, file))
	}
	for _, file := range []*File{first, second} {
		require.NoError(t, db.Create(&FileVersion{FileID: file.ID, Signature: []byte(file.Name)}).Error)
	}
	setting, err := lib.UpdateLibrarySettings(ctx, false, 0)
	require.NoError(t, err)

	// Saved visibility is version existence, including versions with no surviving Position.
	page, err := lib.ListFiles(ctx, Root.ID, entity.FileScope_FILE_SCOPE_DEFAULT, "", 2)
	require.NoError(t, err)
	require.Equal(t, entity.FileScope_FILE_SCOPE_SAVED, page.Scope)
	require.Equal(t, []int64{directory.ID, first.ID}, []int64{page.Files[0].ID, page.Files[1].ID})
	require.NotEmpty(t, page.NextCursor)
	last, err := lib.ListFiles(ctx, Root.ID, page.Scope, page.NextCursor, 2)
	require.NoError(t, err)
	require.Len(t, last.Files, 1)
	require.Equal(t, second.ID, last.Files[0].ID)
	require.Empty(t, last.NextCursor)
	_, err = lib.ListFiles(ctx, Root.ID, entity.FileScope_FILE_SCOPE_ALL, page.NextCursor, 2)
	require.Error(t, err)

	// Search applies the same database predicate before selecting each page.
	search, err := lib.SearchFilesIn(ctx, "type:file", "", 1, entity.FileScope_FILE_SCOPE_DEFAULT, 0, 0)
	require.NoError(t, err)
	require.Len(t, search.Results, 1)
	require.Equal(t, first.ID, search.Results[0].File.ID)
	search, err = lib.SearchFilesIn(ctx, "type:file", search.NextCursor, 1, entity.FileScope_FILE_SCOPE_SAVED, 0, 0)
	require.NoError(t, err)
	require.Len(t, search.Results, 1)
	require.Equal(t, second.ID, search.Results[0].File.ID)
	require.Empty(t, search.NextCursor)

	// A frozen selection retains its scope when the persistent display preference changes.
	selection := &entity.FileSelection{Target: &entity.FileSelection_Library{Library: &entity.LibrarySelection{FileId: Root.ID}}}
	require.NoError(t, lib.FreezeSelections(ctx, []*entity.FileSelection{selection}))
	_, err = lib.UpdateLibrarySettings(ctx, true, setting.Revision)
	require.NoError(t, err)
	var selected []int64
	require.NoError(t, lib.WalkFileSelections(ctx, []*entity.FileSelection{selection}, func(file *File, target string) error {
		require.NotEmpty(t, target)
		selected = append(selected, file.ID)
		return nil
	}))
	require.Equal(t, []int64{first.ID, second.ID}, selected)
	all, err := lib.ListFiles(ctx, Root.ID, entity.FileScope_FILE_SCOPE_ALL, "", 200)
	require.NoError(t, err)
	require.Len(t, all.Files, 108)
	kept, err := lib.GetFile(ctx, first.ID)
	require.NoError(t, err)
	require.Equal(t, "kept", kept.Note)
}

func TestMixedPhysicalAndLogicalSelectionsDeduplicateWithFrozenRevision(t *testing.T) {
	// Two physical entries retain independent File identities while sharing overlapping selection roots.
	ctx := context.Background()
	db, lib := newTestLibrary(t)
	location := onlineTestSource(t, lib)
	location, err := lib.PublishOnline(ctx, location.ID, location.Revision, 1, onlineTestManifest(
		onlineTestPosition("dir/a.txt", "same"), onlineTestPosition("dir/b.txt", "same")))
	require.NoError(t, err)
	files, err := lib.OnlineFilesPage(ctx, location.ID, "", 10)
	require.NoError(t, err)
	require.Len(t, files, 2)
	require.NoError(t, db.Create(&FileVersion{FileID: files[0].FileID, Signature: []byte("saved-other-content")}).Error)
	selections := []*entity.FileSelection{
		{Target: &entity.FileSelection_Library{Library: &entity.LibrarySelection{FileId: Root.ID}}, Scope: entity.FileScope_FILE_SCOPE_SAVED},
		{Target: &entity.FileSelection_Location{Location: &entity.LocationSelection{LocationId: location.ID, Path: "dir/"}}},
		{Target: &entity.FileSelection_Library{Library: &entity.LibrarySelection{FileId: files[1].FileID}}, Scope: entity.FileScope_FILE_SCOPE_ALL},
	}
	require.NoError(t, lib.FreezeSelections(ctx, selections))
	require.Equal(t, "dir", selections[1].GetLocation().Path)
	require.Equal(t, location.Revision, selections[1].GetLocation().Revision)

	// Saved logical roots cannot suppress unbacked entries explicitly selected from a Location.
	var selected []int64
	require.NoError(t, lib.WalkFileSelections(ctx, selections, func(file *File, target string) error {
		require.Contains(t, target, "Unforged/Everyday/dir/")
		selected = append(selected, file.ID)
		return nil
	}))
	require.Equal(t, []int64{files[0].FileID, files[1].FileID}, selected)

	// Rebinding or publishing the physical tree invalidates the frozen physical selection.
	_, err = lib.PublishOnline(ctx, location.ID, location.Revision, 2, onlineTestManifest())
	require.NoError(t, err)
	require.ErrorIs(t, lib.WalkFileSelections(ctx, selections[1:2], func(*File, string) error {
		t.Fatal("stale physical pages must not emit files")
		return nil
	}), ErrOnlineConflict)
}

func TestLocationMigrationMergesRootsAndNeverReappliesYAML(t *testing.T) {
	// Identical legacy roots become one registration without scanning its files.
	ctx := context.Background()
	db, lib := newTestLibrary(t)
	root := filepath.Join(t.TempDir(), "legacy")
	require.NoError(t, lib.MigrateLocationPaths(ctx, "local", root, root))
	var locations []*Location
	require.NoError(t, db.Find(&locations).Error)
	require.Len(t, locations, 1)
	location := locations[0]
	require.NotEmpty(t, location.BindingToken)
	require.True(t, location.RestoreTarget)
	require.Equal(t, entity.OnlineBinding_CONFIRMED, location.Binding)
	var indexed int64
	require.NoError(t, db.Model(&FileLocation{}).Count(&indexed).Error)
	require.Zero(t, indexed)

	// Settings edits and deletions survive restarts even when legacy YAML is still present.
	location.Name, location.RestoreTarget = "Renamed destination", false
	require.NoError(t, db.Save(location).Error)
	require.NoError(t, lib.MigrateLocationPaths(ctx, "local", root, root))
	reloaded, err := lib.GetOnlineSource(ctx, location.ID)
	require.NoError(t, err)
	require.Equal(t, location.Name, reloaded.Name)
	require.False(t, reloaded.RestoreTarget)
	require.NoError(t, lib.DeleteOnlineSource(ctx, location.ID, reloaded.Revision))
	require.NoError(t, lib.MigrateLocationPaths(ctx, "local", root, root))
	require.NoError(t, db.Find(&locations).Error)
	require.Empty(t, locations)
	messages, err := lib.LocationMigrationMessages(ctx, "local")
	require.NoError(t, err)
	require.Len(t, messages, 2)
}

func TestPhysicalSelectionsKeepCaseDistinctDirectoryBoundaries(t *testing.T) {
	// Published paths from case-sensitive filesystems may contain both directory names.
	ctx := context.Background()
	_, lib := newTestLibrary(t)
	location := onlineTestSource(t, lib)
	location, err := lib.PublishOnline(ctx, location.ID, location.Revision, 1, onlineTestManifest(
		onlineTestPosition("Photos/upper.txt", "upper"), onlineTestPosition("photos/lower.txt", "lower")))
	require.NoError(t, err)
	selection := &entity.FileSelection{Target: &entity.FileSelection_Location{Location: &entity.LocationSelection{
		LocationId: location.ID, Path: "photos",
	}}}
	require.NoError(t, lib.FreezeSelections(ctx, []*entity.FileSelection{selection}))

	// Selecting one physical directory must not include its differently cased sibling.
	var targets []string
	require.NoError(t, lib.WalkFileSelections(ctx, []*entity.FileSelection{selection}, func(_ *File, target string) error {
		targets = append(targets, target)
		return nil
	}))
	require.Equal(t, []string{"Unforged/Everyday/photos/lower.txt"}, targets)
}

func TestLibrarySettingsExportRetainsHiddenFilesAndRollsBackWithImport(t *testing.T) {
	// Saved-only presentation must not filter a complete metadata backup.
	ctx := context.Background()
	_, lib := newTestLibrary(t)
	file := &File{Name: "unbacked.txt", Kind: entity.FileKind_FILE_KIND_REGULAR, Note: "do not lose"}
	require.NoError(t, lib.SaveFile(ctx, file))
	_, err := lib.UpdateFileSettings(ctx, &LibrarySettings{IncludeUnbackedFiles: false, AutoCollectFiles: false, ConfirmPermanentDelete: false})
	require.NoError(t, err)
	var backup bytes.Buffer
	require.NoError(t, lib.Export(ctx, &backup, []entity.LibraryEntityType{entity.LibraryEntityType_FILE}))
	_, imported := newTestLibrary(t)
	require.NoError(t, imported.Import(ctx, bytes.NewReader(backup.Bytes())))
	settings, err := imported.GetLibrarySettings(ctx)
	require.NoError(t, err)
	require.False(t, settings.IncludeUnbackedFiles)
	require.False(t, settings.AutoCollectFiles)
	require.False(t, settings.ConfirmPermanentDelete)
	kept, err := imported.GetFile(ctx, file.ID)
	require.NoError(t, err)
	require.Equal(t, file.Note, kept.Note)

	// A malformed body cannot leave its header preference applied outside the replacement transaction.
	changed, err := imported.UpdateFileSettings(ctx, &LibrarySettings{IncludeUnbackedFiles: true, AutoCollectFiles: true, ConfirmPermanentDelete: true, Revision: settings.Revision})
	require.NoError(t, err)
	broken := append(append([]byte{}, backup.Bytes()...), []byte("{invalid record}\n")...)
	require.Error(t, imported.Import(ctx, bytes.NewReader(broken)))
	after, err := imported.GetLibrarySettings(ctx)
	require.NoError(t, err)
	require.True(t, after.IncludeUnbackedFiles)
	require.True(t, after.AutoCollectFiles)
	require.True(t, after.ConfirmPermanentDelete)
	require.Equal(t, changed.Revision, after.Revision)
	kept, err = imported.GetFile(ctx, file.ID)
	require.NoError(t, err)
	require.Equal(t, file.Note, kept.Note)
}
