package apis_test

import (
	"context"
	"crypto/sha256"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/internal/apis"
	"github.com/samuelncui/yatm/internal/dataformat"
	"github.com/samuelncui/yatm/internal/executor"
	"github.com/samuelncui/yatm/internal/library"
	"github.com/samuelncui/yatm/internal/resource"
	"github.com/stretchr/testify/require"
)

type contentFixture struct {
	api        *apis.API
	lib        *library.Library
	media      *entity.Media
	volumeRoot string
}

func newContentFixture(t *testing.T) *contentFixture {
	// Keep the shared Media/API fixture for inventory and metadata tests.
	t.Helper()
	root := t.TempDir()
	volumesRoot := filepath.Join(root, "volumes")
	volumeRoot := filepath.Join(volumesRoot, "fixture")
	require.NoError(t, os.MkdirAll(volumeRoot, 0o755))
	executorDB, err := resource.OpenSQLite(filepath.Join(root, "executor.db"))
	require.NoError(t, err)
	libraryDB, err := resource.OpenSQLite(filepath.Join(root, "library.db"))
	require.NoError(t, err)
	lib := library.New(libraryDB)
	require.NoError(t, lib.AutoMigrate())
	exe := executor.New(executorDB, lib, nil, executor.Paths{
		Work: filepath.Join(root, "work"), Volumes: []string{volumesRoot},
	}, executor.Scripts{}, nil)
	require.NoError(t, exe.AutoMigrate())

	// Register the fixture Volume through the public API.
	api := apis.New(lib, exe)
	initialized, err := entity.NewMediaServiceClient(domainConnection(t, api)).InitializeVolume(context.Background(), &entity.InitializeVolumeRequest{
		MountPoint: volumeRoot, Name: "fixture",
		Profile: &entity.VolumeMediaProfile{Type: entity.VolumeType_VOLUME_TYPE_HDD},
	})
	require.NoError(t, err)
	return &contentFixture{api: api, lib: lib, media: initialized.Media, volumeRoot: volumeRoot}
}

func (fixture *contentFixture) addPosition(t *testing.T, relative string, data []byte) *library.Position {
	// Save a real file and its matching catalog Position for inventory tests.
	t.Helper()
	filename := filepath.Join(fixture.volumeRoot, filepath.FromSlash(relative))
	require.NoError(t, os.MkdirAll(filepath.Dir(filename), 0o755))
	require.NoError(t, os.WriteFile(filename, data, 0o644))
	info, err := os.Stat(filename)
	require.NoError(t, err)
	hash := sha256.Sum256(data)

	// Register the logical File before recording its physical Position.
	file := &library.File{
		Name: filepath.Base(relative), Mode: uint32(info.Mode()), ModTime: info.ModTime(),
		Size: info.Size(), Hash: hash[:],
	}
	require.NoError(t, fixture.lib.SaveFile(context.Background(), file))

	// Store the actual filesystem precision in the Position's ns field.
	mtime, err := dataformat.Nanoseconds(info.ModTime())
	require.NoError(t, err)
	position := &library.Position{
		MediaID: fixture.media.Id, Path: filepath.ToSlash(relative),
		Mode: uint32(info.Mode()), MtimeNS: mtime, Size: info.Size(), Hash: hash[:],
	}
	require.NoError(t, fixture.lib.SavePosition(context.Background(), position))
	return position
}

func TestFileByteHTTPRoutesAreRemoved(t *testing.T) {
	// No former original, observed-file or Position route may expose file bytes.
	router := apis.New(nil, nil).Uploader()
	for _, path := range []string{"/content", "/content/1", "/originals/1", "/locations/1/content"} {
		for _, method := range []string{http.MethodGet, http.MethodHead} {
			t.Run(method+" "+path, func(t *testing.T) {
				response := httptest.NewRecorder()
				router.ServeHTTP(response, httptest.NewRequest(method, path, nil))
				require.Equal(t, http.StatusNotFound, response.Code)
			})
		}
	}
}

func TestLibraryJSONLHTTPExportRemainsAvailable(t *testing.T) {
	// Metadata transfer remains available independently of file-byte transfer.
	db, err := resource.OpenSQLite(filepath.Join(t.TempDir(), "library.db"))
	require.NoError(t, err)
	lib := library.New(db)
	require.NoError(t, lib.AutoMigrate())

	// The retained HTTP endpoint streams the Library backup format.
	response := httptest.NewRecorder()
	apis.New(lib, nil).Uploader().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/library/_export", nil))
	require.Equal(t, http.StatusOK, response.Code)
	require.Equal(t, library.JSONLContentType, response.Header().Get("Content-Type"))
	require.True(t, strings.HasPrefix(response.Body.String(), `{"type":"header","format":"yatm-library-backup"`))
}
