package apis

import (
	"context"
	"crypto/sha256"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/executor"
	"github.com/samuelncui/yatm/library"
	"github.com/samuelncui/yatm/resource"
	"github.com/stretchr/testify/require"
)

func setupOnlineAPI(t *testing.T) (*API, *locationService, string) {
	// Use an isolated permitted namespace with explicit runtime resources below it.
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	db, err := resource.OpenSQLite(filepath.Join(root, "library.db"))
	require.NoError(t, err)
	l := library.New(db)
	require.NoError(t, l.AutoMigrate())
	// These API fixtures exercise explicit operations; auto-collection has dedicated Job coverage.
	_, err = l.UpdateFileSettings(context.Background(), &library.LibrarySettings{IncludeUnbackedFiles: true, AutoCollectFiles: false, ConfirmPermanentDelete: true})
	require.NoError(t, err)
	exe := executor.New(db, l, nil, executor.Paths{Source: root, Work: filepath.Join(root, "work")}, executor.Scripts{}, nil)
	exe.SetOnlineRuntimePaths(filepath.Join(root, "library.db"))
	api := New(l, exe)
	return api, &locationService{api: api}, root
}

func TestOnlineRegistrationBoundariesAndConfiguration(t *testing.T) {
	// Whole allowed-root registration persists required exclusions, not a blanket work exclusion.
	_, service, root := setupOnlineAPI(t)
	ctx := context.Background()
	created, err := service.Create(ctx, &entity.CreateLocationRequest{Location: &entity.Location{Name: "Whole root", RootPath: root, Ignore: &entity.IgnoreRules{Format: "gitignore", Text: "future/"}}})
	require.NoError(t, err)
	require.Contains(t, created.RequiredExclusions, "library.db")
	require.Contains(t, created.RequiredExclusions, "work/jobs")
	require.NotContains(t, created.RequiredExclusions, "work")
	_, err = service.Create(ctx, &entity.CreateLocationRequest{Location: &entity.Location{Name: "Duplicate", RootPath: root}})
	require.Error(t, err)

	// Known symlinks and lexical path escapes are refused at the registration boundary.
	out := t.TempDir()
	require.NoError(t, os.Symlink(out, filepath.Join(root, "linked")))
	for _, invalid := range []string{out, filepath.Join(root, "linked")} {
		_, err := service.Create(ctx, &entity.CreateLocationRequest{Location: &entity.Location{Name: "Invalid", RootPath: invalid}})
		require.Error(t, err)
	}

	// Nested roots remain distinct registrations with an explicit duplicate-index warning.
	sub := filepath.Join(root, "documents")
	require.NoError(t, os.Mkdir(sub, 0755))
	nested, err := service.Create(ctx, &entity.CreateLocationRequest{Location: &entity.Location{Name: "Documents", RootPath: sub}})
	require.NoError(t, err)
	require.NotEmpty(t, nested.Warnings)
	require.Equal(t, entity.OnlineBinding_CONFIRMED, nested.Location.Binding)
}

func TestOnlineContentIdentityRangeAndActiveContent(t *testing.T) {
	// Publish a real online original through the same metadata-only Library transaction as Sync.
	api, service, root := setupOnlineAPI(t)
	ctx := context.Background()
	sourceRoot := filepath.Join(root, "source")
	require.NoError(t, os.Mkdir(sourceRoot, 0755))
	created, err := service.Create(ctx, &entity.CreateLocationRequest{Location: &entity.Location{Name: "Originals", RootPath: sourceRoot}})
	require.NoError(t, err)
	filename := filepath.Join(sourceRoot, "file.html")
	content := []byte("0123456789")
	require.NoError(t, os.WriteFile(filename, content, 0644))
	info, err := os.Stat(filename)
	require.NoError(t, err)
	hash := sha256.Sum256(content)
	_, err = api.lib.PublishOnline(ctx, created.Location.Id, created.Location.Revision, 1, func(_ context.Context, yield func(*library.OnlinePosition) error) error {
		return yield(&library.OnlinePosition{Path: "file.html", Size: info.Size(), Mode: uint32(info.Mode()), MtimeNS: info.ModTime().UnixNano(), Hash: hash[:]})
	})
	require.NoError(t, err)
	positions, err := api.lib.OnlineFilesPage(ctx, created.Location.Id, "", 10)
	require.NoError(t, err)
	file, err := api.lib.GetFile(ctx, positions[0].FileID)
	require.NoError(t, err)
	location, err := api.lib.GetOnlineSource(ctx, created.Location.Id)
	require.NoError(t, err)
	url := fmt.Sprintf("/originals/%d?location_id=%d&revision=%d", file.ID, location.ID, location.Revision)
	router := api.Uploader()

	// GET/HEAD/Range use one descriptor and active formats always receive attachment protections.
	for _, method := range []string{http.MethodGet, http.MethodHead} {
		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, httptest.NewRequest(method, url, nil))
		require.Equal(t, http.StatusOK, recorder.Code)
		require.Contains(t, recorder.Header().Get("Content-Disposition"), "attachment")
		require.Equal(t, "nosniff", recorder.Header().Get("X-Content-Type-Options"))
		if method == http.MethodHead {
			require.Empty(t, recorder.Body.String())
		}
	}
	request := httptest.NewRequest(http.MethodGet, url, nil)
	request.Header.Set("Range", "bytes=2-5")
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)
	require.Equal(t, http.StatusPartialContent, recorder.Code)
	require.Equal(t, "2345", recorder.Body.String())

	// An expected identity mismatch or detectable physical overwrite cannot serve different content.
	recorder = httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, fmt.Sprintf("/originals/%d?location_id=%d&revision=%d", file.ID, location.ID, location.Revision-1), nil))
	require.Equal(t, http.StatusConflict, recorder.Code)
	require.NoError(t, os.WriteFile(filename, []byte("changed"), 0644))
	recorder = httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, url, nil))
	require.Equal(t, http.StatusConflict, recorder.Code)
}
