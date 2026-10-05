package apis

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/samuelncui/yatm/entity"
	"github.com/stretchr/testify/require"
)

func TestLiveLocationHidesIgnoredEntriesAndContinuesLexically(t *testing.T) {
	// Live browsing hides Ignore matches while dot directories and ordinary entries stay visible.
	ctx := context.Background()
	api, locations, root := setupLocationAPI(t)
	service := &filesService{api: api}
	dir := filepath.Join(root, "originals")
	require.NoError(t, os.Mkdir(dir, 0755))
	for _, name := range []string{".empty", "ignored", "visible"} {
		require.NoError(t, os.Mkdir(filepath.Join(dir, name), 0755))
	}
	require.NoError(t, os.WriteFile(filepath.Join(dir, "other.txt"), []byte("0123456789"), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "z.txt"), []byte("0123456789"), 0644))
	created, err := locations.Create(ctx, &entity.CreateLocationRequest{Location: &entity.Location{Name: "Documents", RootPath: dir,
		Config: &entity.LocationConfig{Ignore: &entity.IgnoreRules{Format: "gitignore", Text: "ignored/\nz.txt\n"}}}})
	require.NoError(t, err)
	id := created.Location.Id
	directory := &entity.FileOperationRef{Target: &entity.FileOperationRef_Location{Location: &entity.LocationEntryRef{LocationId: id}}}
	page, err := service.Search(ctx, &entity.SearchFilesRequest{Directory: directory, Limit: 2})
	require.NoError(t, err)
	require.Len(t, page.Entries, 2)
	require.Equal(t, ".empty", page.Entries[0].Path)
	require.Equal(t, "other.txt", page.Entries[1].Path)
	require.NotEmpty(t, page.NextCursor)
	last, err := service.Search(ctx, &entity.SearchFilesRequest{Directory: directory, Cursor: page.NextCursor, Limit: 2})
	require.NoError(t, err)
	require.Len(t, last.Entries, 1)
	require.Equal(t, "visible", last.Entries[0].Path)
	require.Nil(t, last.Entries[0].AssociatedFileId)
	require.NotNil(t, last.Entries[0].Reference)
	filtered, err := service.Search(ctx, &entity.SearchFilesRequest{Directory: directory, Query: `name:"Z.TXT"`, Limit: 1})
	require.NoError(t, err)
	require.Empty(t, filtered.Entries)

	// Ignore is visibility, not authorization: explicit resolution still reports the entry.
	entry, err := service.Get(ctx, &entity.GetFileRequest{Reference: &entity.FileOperationRef{Target: &entity.FileOperationRef_Location{Location: &entity.LocationEntryRef{LocationId: id, Path: "z.txt"}}}})
	require.NoError(t, err)
	require.Nil(t, entry.Detail.Entry.AssociatedFileId)
	require.NotNil(t, entry.Detail.Entry.Reference)

	// A directory that gains an entry keeps serving the same continuation; a replaced one does not.
	require.NoError(t, os.WriteFile(filepath.Join(dir, "new.txt"), []byte("new"), 0644))
	continued, err := service.Search(ctx, &entity.SearchFilesRequest{Directory: directory, Cursor: page.NextCursor, Limit: 2})
	require.NoError(t, err)
	require.Len(t, continued.Entries, 1)
	require.Equal(t, "visible", continued.Entries[0].Path)
	require.NoError(t, os.Rename(dir, dir+"-moved"))
	failed, err := service.Search(ctx, &entity.SearchFilesRequest{Directory: directory})
	require.Error(t, err)
	require.Nil(t, failed)
}

func TestExplicitIgnoredEntryAdmission(t *testing.T) {
	// Ignore hides a live entry without prohibiting explicit access or admission.
	ctx := context.Background()
	api, locations, root := setupLocationAPI(t)
	service := &filesService{api: api}
	dir := filepath.Join(root, "originals")
	require.NoError(t, os.Mkdir(dir, 0755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "page.html"), []byte("0123456789"), 0644))
	created, err := locations.Create(ctx, &entity.CreateLocationRequest{Location: &entity.Location{Name: "Documents", RootPath: dir,
		Config: &entity.LocationConfig{Ignore: &entity.IgnoreRules{Format: "gitignore", Text: "page.html\n"}}}})
	require.NoError(t, err)
	directory := &entity.FileOperationRef{Target: &entity.FileOperationRef_Location{Location: &entity.LocationEntryRef{LocationId: created.Location.Id}}}
	entry, err := service.Get(ctx, &entity.GetFileRequest{Reference: &entity.FileOperationRef{Target: &entity.FileOperationRef_Location{Location: &entity.LocationEntryRef{LocationId: created.Location.Id, Path: "page.html"}}}})
	require.NoError(t, err)
	require.Nil(t, entry.Detail.Entry.AssociatedFileId)
	// The ignored entry stays out of the listing while explicit access remains available.
	page, err := service.Search(ctx, &entity.SearchFilesRequest{Directory: directory})
	require.NoError(t, err)
	require.Empty(t, page.Entries)

	// Explicit admission bypasses only user Ignore and produces a real persistent identity.
	admitted, err := api.exe.AdmitLocationEntry(ctx, entry.Detail.Entry.Reference.GetLocation())
	require.NoError(t, err)
	admittedDetail, err := service.Get(ctx, &entity.GetFileRequest{Reference: entry.Detail.Entry.Reference})
	require.NoError(t, err)
	require.Equal(t, admitted.FileID, admittedDetail.Detail.Entry.GetAssociatedFileId())
	require.Empty(t, admittedDetail.Detail.ContentSignature)
	again, err := api.exe.AdmitLocationEntry(ctx, entry.Detail.Entry.Reference.GetLocation())
	require.NoError(t, err)
	require.Equal(t, admitted.FileID, again.FileID)

	// Symlink leaves can be listed, but entry resolution does not follow them.
	require.NoError(t, os.Symlink(dir, filepath.Join(dir, "link")))
	_, err = service.Get(ctx, &entity.GetFileRequest{Reference: &entity.FileOperationRef{Target: &entity.FileOperationRef_Location{Location: &entity.LocationEntryRef{LocationId: created.Location.Id, Path: "link/page.html"}}}})
	require.Error(t, err)
	_, err = service.Get(ctx, &entity.GetFileRequest{Reference: &entity.FileOperationRef{Target: &entity.FileOperationRef_Location{Location: &entity.LocationEntryRef{LocationId: created.Location.Id, Path: "../library.db"}}}})
	require.Error(t, err)
}
