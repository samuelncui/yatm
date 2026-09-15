//go:build linux || darwin

package apis

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/google/uuid"
	"github.com/samuelncui/acp"
	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/library"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"
)

func admissionLocation(t *testing.T) (*API, *locationService, string, int64) {
	t.Helper()
	api, service, root := setupOnlineAPI(t)
	dir := filepath.Join(root, "originals")
	require.NoError(t, os.Mkdir(dir, 0755))
	created, err := service.Create(context.Background(), &entity.CreateLocationRequest{Location: &entity.Location{Name: "Documents", RootPath: dir}})
	require.NoError(t, err)
	settings, err := api.lib.GetLibrarySettings(context.Background())
	require.NoError(t, err)
	settings.AutoCollectFiles = true
	_, err = api.lib.UpdateFileSettings(context.Background(), settings)
	require.NoError(t, err)
	return api, service, dir, created.Location.Id
}

func admittedPage(t *testing.T, service *locationService, id int64) map[string]*entity.LocationEntry {
	t.Helper()
	page, err := service.ListEntries(context.Background(), &entity.ListLocationEntriesRequest{LocationId: id})
	require.NoError(t, err)
	require.Empty(t, page.CollectionError)
	var refs []*entity.FileOperationRef
	for _, entry := range page.Entries {
		refs = append(refs, &entity.FileOperationRef{Target: &entity.FileOperationRef_Location{Location: entry.Reference}})
	}
	if len(refs) > 0 {
		_, err = (&filesService{api: service.api}).Collect(context.Background(), &entity.CollectFilesRequest{References: refs, Automatic: true})
		require.NoError(t, err)
		page, err = service.ListEntries(context.Background(), &entity.ListLocationEntriesRequest{LocationId: id})
		require.NoError(t, err)
	}
	entries := map[string]*entity.LocationEntry{}
	for _, entry := range page.Entries {
		entries[entry.Path] = entry
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
	api, service, dir, id := admissionLocation(t)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "before.txt"), []byte("original"), 0644))
	first := admittedPage(t, service, id)["before.txt"]
	require.NotNil(t, first.File)
	note := "preserved annotation"
	require.NoError(t, api.lib.EditFileMetadata(context.Background(), []int64{first.File.Id}, library.FileMetadataEdit{Note: &note, AddTags: []string{"review"}}))
	require.NoError(t, os.Rename(filepath.Join(dir, "before.txt"), filepath.Join(dir, "after.txt")))
	after := admittedPage(t, service, id)["after.txt"]
	require.Equal(t, first.File.Id, after.File.Id)
	require.Equal(t, note, after.File.Note)
	require.Contains(t, after.File.Tags, "review")
	require.Empty(t, after.Original.Signature)
	previous, err := api.lib.GetFileLocationAtPath(context.Background(), id, "before.txt")
	require.NoError(t, err)
	require.Nil(t, previous)
}

func TestLiveAdmissionNativeRoundPrecedesCopiedUUID(t *testing.T) {
	// A copied UUID in an earlier list row must not preempt the real renamed inode in a later row.
	api, service, dir, id := admissionLocation(t)
	value := uuid.NewString()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "original.txt"), []byte("original"), 0644))
	trackingAttribute(t, filepath.Join(dir, "original.txt"), value)
	first := admittedPage(t, service, id)["original.txt"]
	note := "original owner"
	require.NoError(t, api.lib.EditFileMetadata(context.Background(), []int64{first.File.Id}, library.FileMetadataEdit{Note: &note}))
	require.NoError(t, os.Rename(filepath.Join(dir, "original.txt"), filepath.Join(dir, "z-renamed.txt")))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "a-copy.txt"), []byte("different independent copy"), 0644))
	trackingAttribute(t, filepath.Join(dir, "a-copy.txt"), value)
	page := admittedPage(t, service, id)
	require.Equal(t, first.File.Id, page["z-renamed.txt"].File.Id)
	require.Equal(t, note, page["z-renamed.txt"].File.Note)
	require.NotEqual(t, first.File.Id, page["a-copy.txt"].File.Id)
	require.Empty(t, page["a-copy.txt"].File.Note)
}

func TestLiveAdmissionPathReplacementClearsContentAndWinsIdentity(t *testing.T) {
	// Path continuity wins even when the previous object survives elsewhere with its tracking UUID.
	_, service, dir, id := admissionLocation(t)
	filename := filepath.Join(dir, "original.txt")
	require.NoError(t, os.WriteFile(filename, []byte("before"), 0644))
	trackingAttribute(t, filename, uuid.NewString())
	copyer, err := acp.New(context.Background(), acp.AccurateJob(filename, nil), acp.WithHash(true), acp.WithSignatureCache(true))
	require.NoError(t, err)
	require.NoError(t, copyer.WaitErr())
	first := admittedPage(t, service, id)["original.txt"]
	require.NotEmpty(t, first.Original.Signature)
	info, err := os.Stat(filename)
	require.NoError(t, err)
	require.NoError(t, os.Rename(filename, filepath.Join(dir, "a-moved.txt")))
	require.NoError(t, os.WriteFile(filename, []byte("after!"), info.Mode()))
	require.NoError(t, os.Chtimes(filename, info.ModTime(), info.ModTime()))
	page := admittedPage(t, service, id)
	require.Equal(t, first.File.Id, page["original.txt"].File.Id)
	require.Empty(t, page["original.txt"].Original.Signature)
	require.Empty(t, page["original.txt"].Original.Sha256)
	require.NotEqual(t, first.File.Id, page["a-moved.txt"].File.Id)
}
