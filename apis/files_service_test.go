package apis

import (
	"context"
	"encoding/base64"
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
	"google.golang.org/protobuf/encoding/protojson"
	"gorm.io/gorm"
)

func TestSharedFilesPureBrowseFilterAndExplicitCollection(t *testing.T) {
	// The same Files source supports uncollected entries and catalog associations without read side effects.
	api, locations, root := setupOnlineAPI(t)
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
	page, err := service.List(ctx, &entity.ListFilesRequest{Directory: directory, Query: "name:file-* AND size:4", Limit: 2})
	require.NoError(t, err)
	require.Len(t, page.Entries, 2)
	require.Nil(t, page.Entries[0].File)
	originals, err := api.lib.OnlineFilesPage(ctx, created.Location.Id, "", 10)
	require.NoError(t, err)
	require.Empty(t, originals)
	next, err := service.List(ctx, &entity.ListFilesRequest{Directory: directory, Query: "name:file-* AND size:4", Cursor: page.NextCursor, Limit: 2})
	require.NoError(t, err)
	require.Equal(t, "file-2.txt", next.Entries[0].Name)
	_, err = service.List(ctx, &entity.ListFilesRequest{Directory: directory, Query: "name:other", Cursor: page.NextCursor})
	require.Error(t, err, "a cursor cannot be reused for a different query")

	// Automatic collection obeys its switch; explicit annotation admits and edits through one service.
	collected, err := service.Collect(ctx, &entity.CollectFilesRequest{References: []*entity.FileOperationRef{page.Entries[0].Reference}, Automatic: true})
	require.NoError(t, err)
	require.Empty(t, collected.Entries)
	note := "important"
	annotated, err := service.UpdateMetadata(ctx, &entity.UpdateFilesMetadataRequest{Reference: page.Entries[0].Reference, Note: &note, AddTags: []string{"travel"}})
	require.NoError(t, err)
	require.Positive(t, annotated.File.Id)
	filtered, err := service.List(ctx, &entity.ListFilesRequest{Directory: directory, Query: "tag:travel AND note:important", Limit: 1})
	require.NoError(t, err)
	require.Len(t, filtered.Entries, 1)
	require.Equal(t, "file-0.txt", filtered.Entries[0].Name)
	_, err = service.List(ctx, &entity.ListFilesRequest{Directory: directory, Query: "bad:value"})
	require.Error(t, err)

	// Pure Get leaves catalog revisions unchanged, while guarded content serves HEAD and Range.
	before, err := api.lib.GetOnlineSource(ctx, created.Location.Id)
	require.NoError(t, err)
	entry, err := service.Get(ctx, &entity.GetFilesEntryRequest{Reference: &entity.FileOperationRef{Target: &entity.FileOperationRef_FileId{FileId: annotated.File.Id}}})
	require.NoError(t, err)
	after, err := api.lib.GetOnlineSource(ctx, created.Location.Id)
	require.NoError(t, err)
	require.Equal(t, before.Revision, after.Revision)
	data, err := protojson.Marshal(entry.ContentReference)
	require.NoError(t, err)
	url := "/files/content?ref=" + base64.RawURLEncoding.EncodeToString(data)
	mux := http.NewServeMux()
	mux.Handle("/files/", http.StripPrefix("/files", api.Uploader()))
	request := httptest.NewRequest(http.MethodGet, url, nil)
	request.Header.Set("Range", "bytes=1-2")
	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, request)
	require.Equal(t, http.StatusPartialContent, recorder.Code)
	require.Equal(t, "es", recorder.Body.String())
	require.NoError(t, os.WriteFile(filepath.Join(physical, "file-0.txt"), []byte("changed"), 0644))
	recorder = httptest.NewRecorder()
	mux.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, url, nil))
	require.Equal(t, http.StatusConflict, recorder.Code)
}

func TestOriginalObservationDistinguishesMissingAndUnavailable(t *testing.T) {
	api, locations, root := setupOnlineAPI(t)
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
	require.Equal(t, entity.OriginalAvailability_ORIGINAL_PRESENT, present.OriginalAvailability)
	require.True(t, present.CurrentObservationValid)
	require.False(t, present.SignatureKnown, "a metadata read does not hash content")
	require.NoError(t, os.WriteFile(name, []byte("replacement content"), 0644))
	changed := observe()
	require.Equal(t, entity.OriginalAvailability_ORIGINAL_PRESENT, changed.OriginalAvailability)
	require.False(t, changed.CurrentObservationValid)
	require.NoError(t, os.Remove(name))
	require.Equal(t, entity.OriginalAvailability_ORIGINAL_MISSING, observe().OriginalAvailability)
	require.NoError(t, os.Rename(physical, physical+"-offline"))
	require.Equal(t, entity.OriginalAvailability_ORIGINAL_UNAVAILABLE, observe().OriginalAvailability)
	original, err = api.lib.GetFileLocation(ctx, file.ID)
	require.NoError(t, err)
	require.NotNil(t, original, "observation never deletes persistent organization")
}

func TestSparseLocationQueryObservesEachEntryOnce(t *testing.T) {
	api, locations, root := setupOnlineAPI(t)
	ctx := context.Background()
	physical := filepath.Join(root, "sparse")
	require.NoError(t, os.Mkdir(physical, 0755))
	for index := 0; index < 600; index++ {
		require.NoError(t, os.WriteFile(filepath.Join(physical, fmt.Sprintf("file-%03d", index)), nil, 0644))
	}
	created, err := locations.Create(ctx, &entity.CreateLocationRequest{Location: &entity.Location{Name: "Sparse", RootPath: physical}})
	require.NoError(t, err)
	seen := make(map[string]bool)
	page, err := api.exe.ListLocationEntriesMatching(ctx, &entity.ListLocationEntriesRequest{LocationId: created.Location.Id, Limit: 1}, func(entries []*entity.LocationEntry) ([]int, error) {
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

func TestSharedFilesContentNeverCrossesRebinding(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	db, err := resource.OpenSQLite(filepath.Join(root, "library.db"))
	require.NoError(t, err)
	lib := library.New(db)
	require.NoError(t, lib.AutoMigrate())
	ctx := context.Background()
	for _, dir := range []string{"before", "after"} {
		require.NoError(t, os.Mkdir(filepath.Join(root, dir), 0755))
		require.NoError(t, os.WriteFile(filepath.Join(root, dir, "file.txt"), []byte(dir), 0644))
	}
	location := &library.Location{Name: "Bound", ExecutorID: "local", RootPath: filepath.Join(root, "before")}
	require.NoError(t, lib.CreateOnlineSource(ctx, location))
	exe := executor.New(db, lib, nil, executor.Paths{Source: root, Work: filepath.Join(root, "work")}, executor.Scripts{}, nil)
	live, err := exe.ObserveLocationEntry(ctx, location.ID, "file.txt")
	require.NoError(t, err)
	original, err := exe.AdmitLocationEntry(ctx, live.Reference)
	require.NoError(t, err)
	api := New(lib, exe)
	switched := false
	require.NoError(t, db.Callback().Query().After("gorm:after_query").Register("test:rebind_after_location_read", func(tx *gorm.DB) {
		if _, ok := tx.Statement.Dest.(*library.Location); !ok || switched {
			return
		}
		switched = true
		// Change the binding after a reader has obtained the old Location value.
		require.NoError(t, db.Model(&library.Location{}).Where("id = ?", location.ID).Updates(map[string]any{
			"root_path": filepath.Join(root, "after"), "binding_token": "replacement-binding",
		}).Error)
	}))
	t.Cleanup(func() { _ = db.Callback().Query().Remove("test:rebind_after_location_read") })
	entry, err := (&filesService{api: api}).Get(ctx, &entity.GetFilesEntryRequest{Reference: &entity.FileOperationRef{Target: &entity.FileOperationRef_FileId{FileId: original.FileID}}})
	require.NoError(t, err)
	require.True(t, switched)
	if entry.ContentReference == nil {
		require.False(t, entry.CanRead)
		return
	}
	require.Equal(t, original.ObservedBindingToken, entry.ContentReference.GetLocation().BindingToken)
	encoded, err := protojson.Marshal(entry.ContentReference)
	require.NoError(t, err)
	recorder := httptest.NewRecorder()
	api.Uploader().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/content?ref="+base64.RawURLEncoding.EncodeToString(encoded), nil))
	require.Equal(t, http.StatusConflict, recorder.Code, "a pre-rebind content link cannot read a new root")
}
