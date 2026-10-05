package apis

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/internal/executor"
	"github.com/samuelncui/yatm/internal/library"
	"github.com/samuelncui/yatm/internal/resource"
	"github.com/stretchr/testify/require"
)

func TestSharedFilesPureBrowseFilterAndExplicitCollection(t *testing.T) {
	// The same Files source supports uncollected entries and catalog associations without read side effects.
	api, locations, root := setupLocationAPI(t)
	ctx := context.Background()
	physical := filepath.Join(root, "originals")
	require.NoError(t, os.Mkdir(physical, 0755))
	for index := 0; index < 5; index++ {
		require.NoError(t, os.WriteFile(filepath.Join(physical, fmt.Sprintf("file-%d.txt", index)), []byte("test"), 0644))
	}
	created, err := locations.Create(ctx, &entity.CreateLocationRequest{Location: &entity.Location{Name: "Originals", RootPath: physical}})
	require.NoError(t, err)
	service := &filesService{api: api}
	directory := &entity.FileOperationRef{Target: &entity.FileOperationRef_Location{Location: &entity.LocationEntryRef{LocationId: created.Location.Id}}}
	page, err := service.Search(ctx, &entity.SearchFilesRequest{Directory: directory, Query: "name:file-* AND size:4", Limit: 2})
	require.NoError(t, err)
	require.Len(t, page.Entries, 2)
	require.Nil(t, page.Entries[0].AssociatedFileId)
	require.Nil(t, page.Entries[0].Status)
	require.Nil(t, page.Entries[0].SizeBytes)
	require.Empty(t, page.Entries[0].Operations)
	originals, err := api.lib.LocationOriginalsPage(ctx, created.Location.Id, "", 10)
	require.NoError(t, err)
	require.Empty(t, originals)
	next, err := service.Search(ctx, &entity.SearchFilesRequest{Directory: directory, Query: "name:file-* AND size:4", Cursor: page.NextCursor, Limit: 2})
	require.NoError(t, err)
	require.Equal(t, "file-2.txt", next.Entries[0].Name)
	_, err = service.Search(ctx, &entity.SearchFilesRequest{Directory: directory, Query: "name:other", Cursor: page.NextCursor})
	require.Error(t, err, "a cursor cannot be reused for a different query")

	// Explicit annotation admits and edits through one service; browsing remains pure.
	note := "important"
	updated, err := service.UpdateMetadata(ctx, &entity.UpdateFilesMetadataRequest{References: []*entity.FileOperationRef{page.Entries[0].Reference}, Note: &note, AddTags: []string{"travel"}})
	require.NoError(t, err)
	require.Len(t, updated.Entries, 1)
	annotated := updated.Entries[0]
	require.Positive(t, annotated.Entry.GetAssociatedFileId())
	require.Equal(t, "Originals", annotated.GetOriginal().GetSourceName())
	require.Equal(t, "file-0.txt", annotated.GetOriginal().GetPath())
	require.NotNil(t, annotated.GetOriginal().GetReference().GetLocation())
	require.Equal(t, entity.FilesCoverage_FILES_COVERAGE_UNSPECIFIED, annotated.Entry.Status.Current)
	filtered, err := service.Search(ctx, &entity.SearchFilesRequest{Directory: directory, Query: "tag:travel AND note:important", Limit: 1})
	require.NoError(t, err)
	require.Len(t, filtered.Entries, 1)
	require.Equal(t, "file-0.txt", filtered.Entries[0].Name)
	_, err = service.Search(ctx, &entity.SearchFilesRequest{Directory: directory, Query: "bad:value"})
	require.Error(t, err)

	// Pure Get leaves catalog revisions unchanged.
	before, err := api.lib.GetLocation(ctx, created.Location.Id)
	require.NoError(t, err)
	_, err = service.Get(ctx, &entity.GetFileRequest{Reference: &entity.FileOperationRef{Target: &entity.FileOperationRef_FileId{FileId: annotated.Entry.GetAssociatedFileId()}}})
	require.NoError(t, err)
	after, err := api.lib.GetLocation(ctx, created.Location.Id)
	require.NoError(t, err)
	require.Equal(t, before.Revision, after.Revision)
}

func TestOriginalObservationDistinguishesMissingAndUnavailable(t *testing.T) {
	api, locations, root := setupLocationAPI(t)
	ctx := context.Background()
	physical := filepath.Join(root, "originals")
	require.NoError(t, os.Mkdir(physical, 0755))
	name := filepath.Join(physical, "file.txt")
	require.NoError(t, os.WriteFile(name, []byte("content"), 0644))
	created, err := locations.Create(ctx, &entity.CreateLocationRequest{Location: &entity.Location{Name: "Originals", RootPath: physical}})
	require.NoError(t, err)
	entry, err := api.exe.ObserveLocationEntry(ctx, created.Location.Id, "file.txt")
	require.NoError(t, err)
	original, err := api.exe.AdmitLocationEntry(ctx, entry.Reference)
	require.NoError(t, err)
	file, err := api.lib.GetFile(ctx, original.FileID)
	require.NoError(t, err)
	observe := func() *entity.FileContentSummary {
		require.NoError(t, api.lib.HydrateFileContent(ctx, file))
		_ = api.exe.ObserveFileContent(ctx, file.ID, file.ContentSummary)
		return file.ContentSummary
	}
	present := observe()
	require.Equal(t, entity.OriginalAvailability_ORIGINAL_AVAILABILITY_PRESENT, present.OriginalAvailability)
	require.True(t, present.CurrentObservationValid)
	require.False(t, present.SignatureKnown, "a metadata read does not hash content")
	require.NoError(t, os.WriteFile(name, []byte("replacement content"), 0644))
	changed := observe()
	require.Equal(t, entity.OriginalAvailability_ORIGINAL_AVAILABILITY_PRESENT, changed.OriginalAvailability)
	require.False(t, changed.CurrentObservationValid)
	require.NoError(t, os.Remove(name))
	require.Equal(t, entity.OriginalAvailability_ORIGINAL_AVAILABILITY_MISSING, observe().OriginalAvailability)
	require.NoError(t, os.Rename(physical, physical+"-offline"))
	require.Equal(t, entity.OriginalAvailability_ORIGINAL_AVAILABILITY_UNAVAILABLE, observe().OriginalAvailability)
	original, err = api.lib.GetFileLocation(ctx, file.ID)
	require.NoError(t, err)
	require.NotNil(t, original, "observation never deletes persistent organization")
}

func TestSparseLocationQueryObservesEachEntryOnce(t *testing.T) {
	api, locations, root := setupLocationAPI(t)
	ctx := context.Background()
	physical := filepath.Join(root, "sparse")
	require.NoError(t, os.Mkdir(physical, 0755))
	for index := 0; index < 600; index++ {
		require.NoError(t, os.WriteFile(filepath.Join(physical, fmt.Sprintf("file-%03d", index)), nil, 0644))
	}
	created, err := locations.Create(ctx, &entity.CreateLocationRequest{Location: &entity.Location{Name: "Sparse", RootPath: physical}})
	require.NoError(t, err)
	seen := make(map[string]bool)
	page, err := api.exe.ListLocationEntriesMatching(ctx, &entity.LocationEntriesQuery{LocationId: created.Location.Id, Limit: 1}, func(entries []*entity.LocationEntry) ([]int, error) {
		require.LessOrEqual(t, len(entries), 256)
		var matches []int
		for index, entry := range entries {
			require.False(t, seen[entry.Path], "a sparse query must not re-enumerate earlier pages")
			seen[entry.Path] = true
			if entry.Path == "file-550" {
				matches = append(matches, index)
			}
		}
		return matches, nil
	})
	require.NoError(t, err)
	require.Len(t, seen, 600)
	require.Len(t, page.Entries, 1)
	require.Equal(t, "file-550", page.Entries[0].Path)
	require.Empty(t, page.NextCursor)
}

func TestOriginalDetailFollowsTheCurrentRegistration(t *testing.T) {
	// A File's original reference resolves against the current Location registration.
	root, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	db, err := resource.OpenSQLite(filepath.Join(root, "library.db"))
	require.NoError(t, err)
	lib := library.New(db)
	require.NoError(t, lib.AutoMigrate())
	ctx := context.Background()
	for _, dir := range []string{"before", "after"} {
		require.NoError(t, os.Mkdir(filepath.Join(root, dir), 0755))
	}
	require.NoError(t, os.WriteFile(filepath.Join(root, "before", "file.txt"), []byte("before"), 0644))
	location := &library.Location{Name: "Bound", ExecutorID: "local", RootPath: filepath.Join(root, "before")}
	require.NoError(t, lib.CreateLocation(ctx, location))
	exe := executor.New(db, lib, nil, executor.Paths{Access: []executor.AccessRange{{Root: root}}, Work: filepath.Join(root, "work")}, executor.Scripts{}, nil)
	live, err := exe.ObserveLocationEntry(ctx, location.ID, "file.txt")
	require.NoError(t, err)
	original, err := exe.AdmitLocationEntry(ctx, live.Reference)
	require.NoError(t, err)
	api := New(lib, exe)
	location.RootPath = filepath.Join(root, "after")
	_, err = lib.UpdateLocation(ctx, location)
	require.NoError(t, err)

	service := &filesService{api: api}
	detail := func() *entity.FilesDetail {
		entry, err := service.Get(ctx, &entity.GetFileRequest{Reference: &entity.FileOperationRef{Target: &entity.FileOperationRef_FileId{FileId: original.FileID}}})
		require.NoError(t, err)
		return entry.Detail
	}

	// The moved registration does not contain the path, so it has no observed content reference.
	entry := detail()
	require.Nil(t, entry.ContentReference)
	require.Equal(t, "file.txt", entry.Original.GetPath())

	// Recreating the path under the current root restores its observed content reference.
	require.NoError(t, os.WriteFile(filepath.Join(root, "after", "file.txt"), []byte("after"), 0644))
	entry = detail()
	require.NotNil(t, entry.ContentReference)
	require.Equal(t, original.Path, entry.ContentReference.GetLocation().Path)
}
