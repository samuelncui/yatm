package apis_test

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/samuelncui/yatm/apis"
	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/executor"
	"github.com/samuelncui/yatm/library"
	"github.com/samuelncui/yatm/resource"
	"github.com/stretchr/testify/require"
)

type previewFixture struct {
	manifest  *entity.PreviewManifest
	data      string
	signature []byte
}

func (*previewFixture) Supports(string) bool {
	return false
}

func (*previewFixture) Generate(context.Context, string, []byte, int64, int64, bool) ([]byte, error) {
	return nil, nil
}

func (p *previewFixture) Manifest(signature []byte) (*entity.PreviewManifest, error) {
	if p.signature != nil && !bytes.Equal(p.signature, signature) {
		return nil, os.ErrNotExist
	}
	return p.manifest, nil
}

func (p *previewFixture) Open(_ []byte, _ string) (io.ReadCloser, error) {
	return io.NopCloser(strings.NewReader(p.data)), nil
}

func TestFilePreviewMetadataAndAsset(t *testing.T) {
	// Store organization and one archived version independently of its preview cache key.
	root := t.TempDir()
	executorDB, err := resource.OpenSQLite(filepath.Join(root, "executor.db"))
	require.NoError(t, err)
	libraryDB, err := resource.OpenSQLite(filepath.Join(root, "library.db"))
	require.NoError(t, err)
	lib := library.New(libraryDB)
	require.NoError(t, lib.AutoMigrate())
	file := &library.File{Name: "photo.jpg", Mode: 0o644}
	require.NoError(t, lib.SaveFile(context.Background(), file))
	version := &library.FileVersion{FileID: file.ID, Signature: []byte("opaque"), Hash: make([]byte, 32), Mode: 0644}
	require.NoError(t, libraryDB.Create(version).Error)

	// Expose only this content's cache entry through the ordinary metadata and asset routes.
	previews := &previewFixture{
		manifest: &entity.PreviewManifest{Assets: []*entity.PreviewAsset{{
			Name: "thumbnail.webp", Role: "thumbnail", MediaType: "image/webp",
		}}},
		data: "preview data",
	}
	exe := executor.New(executorDB, lib, nil, executor.Paths{Work: root}, executor.Scripts{}, previews)
	api := apis.New(lib, exe)
	previews.signature, err = library.NewFileSignature(version.Hash, version.Size)
	require.NoError(t, err)
	reply, err := api.FileGet(context.Background(), &entity.FileGetRequest{Id: file.ID})
	require.NoError(t, err)
	require.Equal(t, previews.manifest, reply.Preview)
	request := httptest.NewRequest(http.MethodGet, "/previews/"+strconv.FormatInt(file.ID, 10)+"/thumbnail", nil)
	response := httptest.NewRecorder()
	api.Uploader().ServeHTTP(response, request)
	require.Equal(t, http.StatusOK, response.Code)
	require.Equal(t, "image/webp", response.Header().Get("Content-Type"))
	require.Equal(t, "no-cache", response.Header().Get("Cache-Control"))
	require.Equal(t, previews.data, response.Body.String())

	// Content-free organization has no image merely because a preview backend is configured.
	unsigned := &library.File{Name: "missing.txt", Mode: 0o644}
	require.NoError(t, lib.SaveFile(context.Background(), unsigned))
	request = httptest.NewRequest(http.MethodGet, "/previews/"+strconv.FormatInt(unsigned.ID, 10)+"/thumbnail", nil)
	response = httptest.NewRecorder()
	api.Uploader().ServeHTTP(response, request)
	require.Equal(t, http.StatusNotFound, response.Code)

	// An unsigned current original must not borrow an older archived version's preview.
	location := &library.Location{Name: "Originals", ExecutorID: "local", RootPath: root,
		Binding: entity.OnlineBinding_CONFIRMED}
	require.NoError(t, libraryDB.Create(location).Error)
	original := &library.FileLocation{FileID: file.ID, LocationID: location.ID, Path: "photo.jpg", Mode: 0644}
	require.NoError(t, libraryDB.Create(original).Error)
	reply, err = api.FileGet(context.Background(), &entity.FileGetRequest{Id: file.ID})
	require.NoError(t, err)
	require.Nil(t, reply.Preview)
	assetPath := "/previews/" + strconv.FormatInt(file.ID, 10) + "/thumbnail"
	for _, test := range []struct {
		query  string
		status int
	}{
		{"", http.StatusNotFound},
		{"?version_id=" + strconv.FormatInt(version.ID, 10), http.StatusOK},
	} {
		response = httptest.NewRecorder()
		api.Uploader().ServeHTTP(response, httptest.NewRequest(http.MethodGet, assetPath+test.query, nil))
		require.Equal(t, test.status, response.Code)
	}

	// A changed known original also cannot fall back to the saved asset.
	original.Hash, original.Signature = bytes.Repeat([]byte{1}, 32), []byte("changed")
	require.NoError(t, libraryDB.Save(original).Error)
	reply, err = api.FileGet(context.Background(), &entity.FileGetRequest{Id: file.ID})
	require.NoError(t, err)
	require.Nil(t, reply.Preview)
	response = httptest.NewRecorder()
	api.Uploader().ServeHTTP(response, httptest.NewRequest(http.MethodGet, assetPath, nil))
	require.Equal(t, http.StatusNotFound, response.Code)
}
