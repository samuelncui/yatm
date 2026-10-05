//go:build linux || darwin

package apis

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/google/uuid"
	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/internal/library"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"
)

func admissionLocation(t *testing.T) (*API, *locationService, string, int64) {
	t.Helper()
	api, service, root := setupLocationAPI(t)
	dir := filepath.Join(root, "originals")
	require.NoError(t, os.Mkdir(dir, 0755))
	created, err := service.Create(context.Background(), &entity.CreateLocationRequest{Location: &entity.Location{Name: "Documents", RootPath: dir}})
	require.NoError(t, err)
	return api, service, dir, created.Location.Id
}

func admittedPage(t *testing.T, locations *locationService, id int64) map[string]*entity.FilesDetail {
	t.Helper()
	ctx := context.Background()
	api := locations.api
	service := &filesService{api: api}
	directory := &entity.FileOperationRef{Target: &entity.FileOperationRef_Location{Location: &entity.LocationEntryRef{LocationId: id}}}
	page, err := listFiles(t, ctx, service, &entity.ListFilesRequest{Directory: directory, Include: []entity.FilesInclude{entity.FilesInclude_FILES_INCLUDE_OPERATIONS}})
	require.NoError(t, err)
	var refs []*entity.LocationEntryRef
	for _, entry := range page.Entries {
		refs = append(refs, entry.Reference.GetLocation())
	}
	if len(refs) > 0 {
		_, err = api.exe.AdmitLocationEntries(ctx, refs)
		require.NoError(t, err)
		page, err = listFiles(t, ctx, service, &entity.ListFilesRequest{Directory: directory, Include: []entity.FilesInclude{entity.FilesInclude_FILES_INCLUDE_OPERATIONS}})
		require.NoError(t, err)
	}
	entries := map[string]*entity.FilesDetail{}
	for _, entry := range page.Entries {
		detail, err := service.Get(ctx, &entity.GetFileRequest{Reference: entry.Reference})
		require.NoError(t, err)
		entries[entry.Path] = detail.Detail
	}
	return entries
}

func trackingAttribute(t *testing.T, filename, value string) {
	t.Helper()
	name := "user.yatm.tracking_uuid"
	if runtime.GOOS == "darwin" {
		name = "yatm.tracking_uuid"
	}
	require.NoError(t, unix.Setxattr(filename, name, []byte(value), 0))
}

func TestLiveAdmissionRenamePreservesTagsAndNote(t *testing.T) {
	// Explicit collection after a pure browse follows external renames before creating new identities.
	api, locations, dir, id := admissionLocation(t)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "before.txt"), []byte("original"), 0644))
	first := admittedPage(t, locations, id)["before.txt"]
	require.Positive(t, first.Entry.GetAssociatedFileId())
	note := "preserved annotation"
	require.NoError(t, api.lib.EditFileMetadata(context.Background(), []int64{first.Entry.GetAssociatedFileId()}, library.FileMetadataEdit{Note: &note, AddTags: []string{"review"}}))
	require.NoError(t, os.Rename(filepath.Join(dir, "before.txt"), filepath.Join(dir, "after.txt")))
	after := admittedPage(t, locations, id)["after.txt"]
	require.Equal(t, first.Entry.GetAssociatedFileId(), after.Entry.GetAssociatedFileId())
	require.Equal(t, note, after.Organization.Note)
	require.Contains(t, after.Organization.Tags, "review")
	require.Empty(t, after.ContentSignature)
	previous, err := api.lib.GetFileLocationAtPath(context.Background(), id, "before.txt")
	require.NoError(t, err)
	require.Nil(t, previous)
}

func TestLiveAdmissionIgnoresOldUUIDAttributes(t *testing.T) {
	// Obsolete copied markers remain untouched and cannot alter native continuity.
	api, locations, dir, id := admissionLocation(t)
	value := uuid.NewString()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "original.txt"), []byte("original"), 0644))
	trackingAttribute(t, filepath.Join(dir, "original.txt"), value)
	first := admittedPage(t, locations, id)["original.txt"]
	note := "original owner"
	require.NoError(t, api.lib.EditFileMetadata(context.Background(), []int64{first.Entry.GetAssociatedFileId()}, library.FileMetadataEdit{Note: &note}))
	require.NoError(t, os.Rename(filepath.Join(dir, "original.txt"), filepath.Join(dir, "z-renamed.txt")))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "a-copy.txt"), []byte("different independent copy"), 0644))
	trackingAttribute(t, filepath.Join(dir, "a-copy.txt"), value)
	page := admittedPage(t, locations, id)
	require.Equal(t, first.Entry.GetAssociatedFileId(), page["z-renamed.txt"].Entry.GetAssociatedFileId())
	require.Equal(t, note, page["z-renamed.txt"].Organization.Note)
	require.NotEqual(t, first.Entry.GetAssociatedFileId(), page["a-copy.txt"].Entry.GetAssociatedFileId())
	require.Empty(t, page["a-copy.txt"].Organization.Note)
}

func TestLiveAdmissionPathReplacementClearsContentAndWinsIdentity(t *testing.T) {
	// Path continuity wins even when the previous inode survives at another path.
	_, locations, dir, id := admissionLocation(t)
	filename := filepath.Join(dir, "original.txt")
	require.NoError(t, os.WriteFile(filename, []byte("before"), 0644))
	trackingAttribute(t, filename, uuid.NewString())
	cacheContentSignature(t, context.Background(), filename)
	first := admittedPage(t, locations, id)["original.txt"]
	require.NotEmpty(t, first.ContentSignature)
	info, err := os.Stat(filename)
	require.NoError(t, err)
	require.NoError(t, os.Rename(filename, filepath.Join(dir, "a-moved.txt")))
	require.NoError(t, os.WriteFile(filename, []byte("after!"), info.Mode()))
	require.NoError(t, os.Chtimes(filename, info.ModTime(), info.ModTime()))
	page := admittedPage(t, locations, id)
	require.Equal(t, first.Entry.GetAssociatedFileId(), page["original.txt"].Entry.GetAssociatedFileId())
	require.Empty(t, page["original.txt"].ContentSignature)
	require.NotEqual(t, first.Entry.GetAssociatedFileId(), page["a-moved.txt"].Entry.GetAssociatedFileId())
}
