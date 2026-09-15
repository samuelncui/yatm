package apis_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/samuelncui/yatm/apis"
	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/executor"
	"github.com/samuelncui/yatm/library"
	mediapkg "github.com/samuelncui/yatm/media"
	"github.com/samuelncui/yatm/resource"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

type onlineContentFixture struct {
	api        *apis.API
	lib        *library.Library
	media      *entity.Media
	volumeRoot string
	db         *gorm.DB
}

func newOnlineContentFixture(t *testing.T) *onlineContentFixture {
	t.Helper()
	// Isolate the catalog, Executor and mounted Volume namespace.
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

	// Register the writable Volume through its public API before returning the fixture.
	api := apis.New(lib, exe)
	initialized, err := api.VolumeInitialize(context.Background(), &entity.VolumeInitializeRequest{
		MountPoint: volumeRoot, Name: "fixture",
		Profile: &entity.VolumeMediaProfile{Type: entity.VolumeType_VOLUME_TYPE_HDD},
	})
	require.NoError(t, err)
	return &onlineContentFixture{api: api, lib: lib, media: initialized.Media, volumeRoot: volumeRoot, db: libraryDB}
}

func TestPositionContentExcludesCatalogReplacement(t *testing.T) {
	fixture := newOnlineContentFixture(t)
	position := fixture.addPosition(t, "file.txt", []byte("saved content"))
	var backup bytes.Buffer
	require.NoError(t, fixture.lib.Export(context.Background(), &backup, []entity.LibraryEntityType{
		entity.LibraryEntityType_FILE, entity.LibraryEntityType_MEDIA, entity.LibraryEntityType_POSITION,
	}))
	attempted := false
	var importErr error
	// Import cannot replace the Position/Media relationship between the two lookups.
	require.NoError(t, fixture.db.Callback().Query().After("gorm:after_query").Register("test:catalog_replacement", func(tx *gorm.DB) {
		if attempted || tx.Statement.Table != "positions" {
			return
		}
		attempted = true
		importErr = fixture.lib.Import(context.Background(), bytes.NewReader(backup.Bytes()))
	}))
	response := httptest.NewRecorder()
	fixture.api.Uploader().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/content/"+strconv.FormatInt(position.ID, 10), nil))
	require.True(t, attempted)
	require.ErrorIs(t, importErr, library.ErrOnlineBusy)
	require.Equal(t, http.StatusOK, response.Code)
	require.Equal(t, "saved content", response.Body.String())
}

func (fixture *onlineContentFixture) addPosition(t *testing.T, relative string, data []byte) *library.Position {
	t.Helper()
	filename := filepath.Join(fixture.volumeRoot, filepath.FromSlash(relative))
	require.NoError(t, os.MkdirAll(filepath.Dir(filename), 0o755))
	require.NoError(t, os.WriteFile(filename, data, 0o644))
	info, err := os.Stat(filename)
	require.NoError(t, err)
	hash := sha256.Sum256(data)
	file := &library.File{
		Name: filepath.Base(relative), Mode: uint32(info.Mode()), ModTime: info.ModTime(),
		Size: info.Size(), Hash: hash[:],
	}
	require.NoError(t, fixture.lib.SaveFile(context.Background(), file))
	position := &library.Position{
		MediaID: fixture.media.Id, Path: filepath.ToSlash(relative),
		Mode: uint32(info.Mode()), ModTime: info.ModTime(), Size: info.Size(), Hash: hash[:],
	}
	require.NoError(t, fixture.lib.SavePosition(context.Background(), position))
	return position
}

func TestOnlineVolumeContentSupportsGetHeadAndRange(t *testing.T) {
	// Serve a published plain-text copy through its mounted Volume identity.
	fixture := newOnlineContentFixture(t)
	position := fixture.addPosition(t, "folder/fixture.txt", []byte("0123456789"))
	router := fixture.api.Uploader()
	url := "/content/" + strconv.FormatInt(position.ID, 10)

	// Open renders passive content inline.
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, url, nil)
	router.ServeHTTP(recorder, request)
	require.Equal(t, http.StatusOK, recorder.Code)
	require.Equal(t, []byte("0123456789"), recorder.Body.Bytes())
	require.Contains(t, recorder.Header().Get("Content-Disposition"), "inline")

	// HEAD preserves metadata without returning content bytes.
	recorder = httptest.NewRecorder()
	request = httptest.NewRequest(http.MethodHead, url, nil)
	router.ServeHTTP(recorder, request)
	require.Equal(t, http.StatusOK, recorder.Code)
	require.Equal(t, "10", recorder.Header().Get("Content-Length"))
	require.Empty(t, recorder.Body.Bytes())

	// Range supports partial reads from the same guarded content endpoint.
	recorder = httptest.NewRecorder()
	request = httptest.NewRequest(http.MethodGet, url, nil)
	request.Header.Set("Range", "bytes=2-5")
	router.ServeHTTP(recorder, request)
	require.Equal(t, http.StatusPartialContent, recorder.Code)
	require.Equal(t, []byte("2345"), recorder.Body.Bytes())
}

func TestOnlineVolumeContentRejectsUnavailableAndUnsafeMedia(t *testing.T) {
	fixture := newOnlineContentFixture(t)
	position := fixture.addPosition(t, "fixture.txt", []byte("fixture"))
	router := fixture.api.Uploader()
	url := "/content/" + strconv.FormatInt(position.ID, 10)

	marker := filepath.Join(fixture.volumeRoot, mediapkg.VolumeMarkerName)
	offlineMarker := filepath.Join(fixture.volumeRoot, ".yatm.offline")
	require.NoError(t, os.Rename(marker, offlineMarker))
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, url, nil))
	require.Equal(t, http.StatusConflict, recorder.Code)
	require.NoError(t, os.Rename(offlineMarker, marker))

	outside := filepath.Join(t.TempDir(), "outside.txt")
	require.NoError(t, os.WriteFile(outside, []byte("outside"), 0o644))
	require.NoError(t, os.Symlink(outside, filepath.Join(fixture.volumeRoot, "linked.txt")))
	info, err := os.Stat(outside)
	require.NoError(t, err)
	file := &library.File{Name: "linked.txt", Mode: uint32(info.Mode()), ModTime: info.ModTime(), Size: info.Size()}
	require.NoError(t, fixture.lib.SaveFile(context.Background(), file))
	linked := &library.Position{
		MediaID: fixture.media.Id, Path: "linked.txt",
		Mode: uint32(info.Mode()), ModTime: info.ModTime(), Size: info.Size(),
	}
	require.NoError(t, fixture.lib.SavePosition(context.Background(), linked))
	recorder = httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(
		http.MethodGet, "/content/"+strconv.FormatInt(linked.ID, 10), nil,
	))
	require.Equal(t, http.StatusConflict, recorder.Code)

	tape, err := fixture.lib.CreateMedia(context.Background(), &library.Media{
		Kind: entity.MediaKind_MEDIA_KIND_TAPE, Identity: "ABC001", Name: "Tape",
		Profile: (&entity.TapeMediaProfile{Format: library.TapeFormatLTFSV0}).Pack(),
	})
	require.NoError(t, err)
	tapePosition := &library.Position{MediaID: tape.ID, Path: "linked.txt"}
	require.NoError(t, fixture.lib.SavePosition(context.Background(), tapePosition))
	recorder = httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(
		http.MethodGet, "/content/"+strconv.FormatInt(tapePosition.ID, 10), nil,
	))
	require.Equal(t, http.StatusConflict, recorder.Code)
}
