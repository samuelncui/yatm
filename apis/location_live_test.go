package apis

import (
	"context"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/samuelncui/yatm/entity"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protojson"
)

func TestLiveLocationListsUnadmittedEntriesAndRejectsStalePages(t *testing.T) {
	// Live browsing includes empty/ignored/dot directories without admitting their children.
	ctx := context.Background()
	_, service, root := setupOnlineAPI(t)
	dir := filepath.Join(root, "originals")
	require.NoError(t, os.Mkdir(dir, 0755))
	for _, name := range []string{".empty", "ignored"} {
		require.NoError(t, os.Mkdir(filepath.Join(dir, name), 0755))
	}
	require.NoError(t, os.WriteFile(filepath.Join(dir, "z.txt"), []byte("0123456789"), 0644))
	created, err := service.Create(ctx, &entity.CreateLocationRequest{Location: &entity.Location{Name: "Documents", RootPath: dir,
		Ignore: &entity.IgnoreRules{Format: "gitignore", Text: "ignored/\nz.txt\n"}}})
	require.NoError(t, err)
	id := created.Location.Id
	page, err := service.ListEntries(ctx, &entity.ListLocationEntriesRequest{LocationId: id, Limit: 2})
	require.NoError(t, err)
	require.Len(t, page.Entries, 2)
	require.Equal(t, ".empty", page.Entries[0].Path)
	require.Equal(t, "ignored", page.Entries[1].Path)
	require.NotEmpty(t, page.NextCursor)
	last, err := service.ListEntries(ctx, &entity.ListLocationEntriesRequest{LocationId: id, Cursor: page.NextCursor, Limit: 2})
	require.NoError(t, err)
	require.Len(t, last.Entries, 1)
	require.Nil(t, last.Entries[0].File)
	require.NotNil(t, last.Entries[0].Reference)
	filtered, err := service.ListEntries(ctx, &entity.ListLocationEntriesRequest{LocationId: id, NameFilter: "Z.TXT", Limit: 1})
	require.NoError(t, err)
	require.Len(t, filtered.Entries, 1)

	// A changed directory invalidates continuation; unavailable directories return no cached rows.
	require.NoError(t, os.WriteFile(filepath.Join(dir, "new.txt"), []byte("new"), 0644))
	_, err = service.ListEntries(ctx, &entity.ListLocationEntriesRequest{LocationId: id, Cursor: page.NextCursor, Limit: 2})
	require.Error(t, err)
	require.NoError(t, os.Rename(dir, dir+"-moved"))
	failed, err := service.ListEntries(ctx, &entity.ListLocationEntriesRequest{LocationId: id})
	require.Error(t, err)
	require.Nil(t, failed)
}

func TestUnadmittedLiveContentAndExplicitIgnoredAdmission(t *testing.T) {
	// No signature, File ID or analysis is necessary for safe HEAD/Range access.
	ctx := context.Background()
	api, service, root := setupOnlineAPI(t)
	dir := filepath.Join(root, "originals")
	require.NoError(t, os.Mkdir(dir, 0755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "page.html"), []byte("0123456789"), 0644))
	created, err := service.Create(ctx, &entity.CreateLocationRequest{Location: &entity.Location{Name: "Documents", RootPath: dir,
		Ignore: &entity.IgnoreRules{Format: "gitignore", Text: "page.html\n"}}})
	require.NoError(t, err)
	entry, err := service.GetEntry(ctx, &entity.GetLocationEntryRequest{LocationId: created.Location.Id, Path: "page.html"})
	require.NoError(t, err)
	require.Nil(t, entry.File)
	encoded, err := protojson.Marshal(entry.Reference)
	require.NoError(t, err)
	target := "/locations/" + strconv.FormatInt(created.Location.Id, 10) + "/content?" + url.Values{"ref": {base64.RawURLEncoding.EncodeToString(encoded)}}.Encode()
	router := api.Uploader()
	request := httptest.NewRequest(http.MethodGet, target, nil)
	request.Header.Set("Range", "bytes=2-4")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	require.Equal(t, http.StatusPartialContent, response.Code)
	require.Equal(t, "234", response.Body.String())
	require.Contains(t, response.Header().Get("Content-Disposition"), "attachment")
	response = httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodHead, target, nil))
	require.Equal(t, http.StatusOK, response.Code)
	require.Empty(t, response.Body.String())

	// Explicit admission bypasses only user Ignore and produces a real persistent identity.
	admitted, err := service.Admit(ctx, entry.Reference)
	require.NoError(t, err)
	require.Positive(t, admitted.File.Id)
	require.Empty(t, admitted.Original.Signature)
	again, err := service.Admit(ctx, entry.Reference)
	require.NoError(t, err)
	require.Equal(t, admitted.File.Id, again.File.Id)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "page.html"), []byte("different"), 0644))
	response = httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, target, nil))
	require.Equal(t, http.StatusConflict, response.Code)

	// Symlink leaves can be listed, but neither content nor ancestor traversal follows them.
	require.NoError(t, os.Symlink(dir, filepath.Join(dir, "link")))
	_, err = service.GetEntry(ctx, &entity.GetLocationEntryRequest{LocationId: created.Location.Id, Path: "link/page.html"})
	require.Error(t, err)
	_, err = service.GetEntry(ctx, &entity.GetLocationEntryRequest{LocationId: created.Location.Id, Path: "../library.db"})
	require.Error(t, err)
}
