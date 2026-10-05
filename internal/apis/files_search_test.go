package apis

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/internal/library"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm/logger"
)

func TestFilesSearchRecordedLibraryAndLiveLocation(t *testing.T) {
	// Admit a sparse 100 MB original, then change its bytes and time without publishing new facts.
	api, locations, root := setupLocationAPI(t)
	ctx := context.Background()
	physical := filepath.Join(root, "originals")
	require.NoError(t, os.Mkdir(physical, 0755))
	name := filepath.Join(physical, "recorded.bin")
	require.NoError(t, os.WriteFile(name, nil, 0644))
	const recordedSize, liveSize = int64(100_000_000), int64(2_000_000_000)
	recordedTime := time.Date(2025, 1, 2, 0, 0, 0, 0, time.UTC)
	liveTime := time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)
	require.NoError(t, os.Truncate(name, recordedSize))
	require.NoError(t, os.Chtimes(name, recordedTime, recordedTime))
	created, err := locations.Create(ctx, &entity.CreateLocationRequest{
		Location: &entity.Location{Name: "Originals", RootPath: physical},
	})
	require.NoError(t, err)
	service := &filesService{api: api}
	libraryRoot := &entity.FileOperationRef{Target: &entity.FileOperationRef_FileId{}}
	liveRef := &entity.FileOperationRef{Target: &entity.FileOperationRef_Location{
		Location: &entity.LocationEntryRef{LocationId: created.Location.Id, Path: "recorded.bin"},
	}}
	updated, err := service.UpdateMetadata(ctx, &entity.UpdateFilesMetadataRequest{
		References: []*entity.FileOperationRef{liveRef}, AddTags: []string{"example"},
	})
	require.NoError(t, err)
	fileID := updated.Entries[0].Entry.GetAssociatedFileId()
	require.Positive(t, fileID)
	require.NoError(t, os.Truncate(name, liveSize))
	require.NoError(t, os.Chtimes(name, liveTime, liveTime))

	// Search and Measure share selection semantics, independently of requested display groups.
	queries := []struct {
		query   string
		library bool
		live    bool
	}{
		{"size: > 1000000000", false, true},
		{"size:100000000", true, false},
		{`mtime: < "2026-01-01T00:00:00Z"`, true, false},
		{`mtime: >= "2026-01-01T00:00:00Z"`, false, true},
		{"has:original AND has:unknown", true, true},
		{"(tag:example AND size:100000000) OR name:absent", true, false},
		{"size:100000000 AND NOT name:absent", true, false},
		{fmt.Sprintf("location:%d AND tag:example AND size:100000000", created.Location.Id), true, false},
	}
	for _, source := range []struct {
		name      string
		directory *entity.FileOperationRef
		recursive bool
		size      int64
	}{
		{"Library", libraryRoot, true, recordedSize},
		{"Location", &entity.FileOperationRef{Target: &entity.FileOperationRef_Location{
			Location: &entity.LocationEntryRef{LocationId: created.Location.Id},
		}}, false, liveSize},
	} {
		for _, test := range queries {
			t.Run(source.name+"/"+test.query, func(t *testing.T) {
				// Display observations must not change which rows the source selects.
				want := test.library
				if source.name == "Location" {
					want = test.live
				}
				query := "type:file AND (" + test.query + ")"
				wantCount := 0
				if want {
					wantCount = 1
				}
				for _, include := range [][]entity.FilesInclude{
					nil,
					{entity.FilesInclude_FILES_INCLUDE_ATTRIBUTES},
					{entity.FilesInclude_FILES_INCLUDE_ATTRIBUTES, entity.FilesInclude_FILES_INCLUDE_STATUS},
				} {
					page, err := service.Search(ctx, &entity.SearchFilesRequest{
						Directory: source.directory, Recursive: source.recursive, Scope: entity.FileScope_FILE_SCOPE_ALL,
						Query: query, Include: include,
					})
					require.NoError(t, err)
					require.Len(t, page.Entries, wantCount)
					require.Empty(t, page.NextCursor)
					if want && len(include) == 2 {
						require.Equal(t, liveSize, page.Entries[0].GetSizeBytes(), "visible observations may be newer")
						require.Equal(t, liveTime.UnixNano(), page.Entries[0].GetMtimeNs())
					}
				}

				// Measurement selects the same root and uses that source's size facts.
				stream := &measureTestStream{ctx: ctx}
				require.NoError(t, service.Measure(&entity.MeasureFilesRequest{
					Directory: source.directory, Recursive: source.recursive,
					Scope: entity.FileScope_FILE_SCOPE_ALL, Query: query,
				}, stream))
				summary := stream.updates[len(stream.updates)-1].GetSummary()
				require.NotNil(t, summary)
				require.True(t, summary.Complete)
				wantBytes := int64(0)
				if want {
					wantBytes = source.size
				}
				require.Equal(t, wantBytes, summary.KnownBytes)
			})
		}
	}

	// Missing paths and unavailable registrations retain recorded Library membership and attributes.
	for _, availability := range []entity.OriginalAvailability{
		entity.OriginalAvailability_ORIGINAL_AVAILABILITY_MISSING,
		entity.OriginalAvailability_ORIGINAL_AVAILABILITY_UNAVAILABLE,
	} {
		if availability == entity.OriginalAvailability_ORIGINAL_AVAILABILITY_MISSING {
			require.NoError(t, os.Remove(name))
		} else {
			require.NoError(t, os.Rename(physical, physical+"-offline"))
		}
		page, err := service.Search(ctx, &entity.SearchFilesRequest{
			Directory: libraryRoot, Recursive: true, Scope: entity.FileScope_FILE_SCOPE_ALL,
			Query:   "has:original AND size:100000000",
			Include: []entity.FilesInclude{entity.FilesInclude_FILES_INCLUDE_ATTRIBUTES, entity.FilesInclude_FILES_INCLUDE_STATUS},
		})
		require.NoError(t, err)
		require.Len(t, page.Entries, 1)
		require.Equal(t, fileID, page.Entries[0].Reference.GetFileId())
		require.Equal(t, recordedSize, page.Entries[0].GetSizeBytes())
		require.Equal(t, availability, page.Entries[0].Status.Original)
	}
	original, err := api.lib.GetFileLocation(ctx, fileID)
	require.NoError(t, err)
	require.Equal(t, recordedSize, original.Size)
	require.Equal(t, recordedTime.UnixNano(), original.MtimeNS)
}

func TestLibrarySearchCatalogContentAndPagination(t *testing.T) {
	// Originals, unsigned originals and saved-only Files use distinct published content facts.
	api, db := setupMeasureAPI(t)
	ctx := context.Background()
	files := []*library.File{{Name: "a"}, {Name: "b"}, {Name: "c"}, {Name: "d"}, {Name: "e"}, {Name: "f"}}
	for _, file := range files {
		require.NoError(t, api.lib.SaveFile(ctx, file))
	}
	for index, signature := range [][]byte{[]byte("shared"), []byte("shared"), nil, []byte("changed")} {
		require.NoError(t, db.Create(&library.FileLocation{
			FileID: files[index].ID, LocationID: 100, Path: files[index].Name, Signature: signature, Size: 100,
		}).Error)
	}
	for _, index := range []int{2, 3, 4} {
		require.NoError(t, db.Create(&library.FileVersion{
			FileID: files[index].ID, Signature: []byte("shared"), Size: 200,
		}).Error)
	}
	require.NoError(t, db.Create(&library.Position{
		MediaID: 1, Path: "saved", Signature: []byte("shared"), Health: entity.PositionHealth_POSITION_HEALTH_DAMAGED,
	}).Error)
	service := &filesService{api: api}
	directory := &entity.FileOperationRef{Target: &entity.FileOperationRef_FileId{}}

	// Each page contains matches, including across nonmatches; content selection never uses row colors.
	for _, test := range []struct {
		query string
		want  []int64
	}{
		{"has:original", []int64{files[0].ID, files[1].ID, files[2].ID, files[3].ID}},
		{"has:archive", []int64{files[0].ID, files[1].ID, files[4].ID}},
		{"has:unknown", []int64{files[2].ID}},
		{"has:duplicates", []int64{files[0].ID, files[1].ID}},
		{"NOT has:original", []int64{files[4].ID, files[5].ID}},
		{"(has:unknown OR size:200) AND NOT name:d", []int64{files[2].ID, files[4].ID}},
		{"has:original AND NOT has:archive", []int64{files[2].ID, files[3].ID}},
		{"location:100 OR has:archive", []int64{files[0].ID, files[1].ID, files[2].ID, files[3].ID, files[4].ID}},
	} {
		t.Run(test.query, func(t *testing.T) {
			// Compare the public source with the existing Catalog search for the same explicit expectation.
			catalog, err := api.lib.SearchFiles(ctx, test.query, "", 100)
			require.NoError(t, err)
			var catalogIDs []int64
			idsToMatch := make([]int64, 0, len(files))
			for _, file := range files {
				idsToMatch = append(idsToMatch, file.ID)
			}
			for _, result := range catalog.Results {
				catalogIDs = append(catalogIDs, result.File.ID)
			}
			require.Equal(t, test.want, catalogIDs)

			// Unpaged Library reads and measurement batches use the same complete predicate.
			all, err := api.lib.ListAllFileRows(ctx, 0, entity.FileScope_FILE_SCOPE_ALL, false, test.query)
			require.NoError(t, err)
			var allIDs []int64
			for _, file := range all.Files {
				allIDs = append(allIDs, file.ID)
			}
			require.Equal(t, test.want, allIDs)
			matches, err := api.lib.MatchFileRows(ctx, idsToMatch, entity.FileScope_FILE_SCOPE_ALL, test.query)
			require.NoError(t, err)
			require.Len(t, matches, len(test.want))
			for _, id := range test.want {
				require.True(t, matches[id])
			}

			// Changing display includes cannot change membership or the query-bound continuation.
			var ids []int64
			cursor := ""
			for index, id := range test.want {
				page, err := service.Search(ctx, &entity.SearchFilesRequest{
					Directory: directory, Scope: entity.FileScope_FILE_SCOPE_ALL,
					Query: test.query, Cursor: cursor, Limit: 1,
				})
				require.NoError(t, err)
				require.Len(t, page.Entries, 1)
				require.Equal(t, id, page.Entries[0].Reference.GetFileId())
				require.Nil(t, page.Entries[0].Status)
				require.Nil(t, page.Entries[0].SizeBytes)
				projected, err := service.Search(ctx, &entity.SearchFilesRequest{
					Directory: directory, Scope: entity.FileScope_FILE_SCOPE_ALL,
					Query: test.query, Cursor: cursor, Limit: 1,
					Include: []entity.FilesInclude{entity.FilesInclude_FILES_INCLUDE_STATUS},
				})
				require.NoError(t, err)
				require.Len(t, projected.Entries, 1)
				require.Equal(t, id, projected.Entries[0].Reference.GetFileId())
				require.Equal(t, page.NextCursor, projected.NextCursor)
				ids = append(ids, id)
				cursor = page.NextCursor
				require.Equal(t, index < len(test.want)-1, cursor != "")
			}
			require.Equal(t, test.want, ids)
		})
	}
}

func TestLibrarySearchPredicatesDoNotAddProjectionReads(t *testing.T) {
	// Published originals need no current access or row hydration for predicate evaluation.
	api, db := setupMeasureAPI(t)
	ctx := context.Background()
	for index := 0; index < 100; index++ {
		file := &library.File{Name: fmt.Sprintf("file-%03d", index)}
		require.NoError(t, api.lib.SaveFile(ctx, file))
		require.NoError(t, db.Create(&library.FileLocation{
			FileID: file.ID, LocationID: 100, Path: file.Name, Size: 100,
		}).Error)
	}
	trace := &filesQueryLog{Interface: logger.Discard}
	db.Logger = trace
	service := &filesService{api: api}
	directory := &entity.FileOperationRef{Target: &entity.FileOperationRef_FileId{}}

	// The SQL statement count stays constant for full pages of different sizes.
	for _, limit := range []int32{1, 50, 100} {
		trace.queries = nil
		page, err := service.Search(ctx, &entity.SearchFilesRequest{
			Directory: directory, Scope: entity.FileScope_FILE_SCOPE_ALL,
			Query: "has:original AND size:100", Limit: limit,
		})
		require.NoError(t, err)
		require.Len(t, page.Entries, int(limit))
		require.Len(t, trace.queries, 1, "filter before pagination in one Catalog read")
		t.Logf("Library Search page limit %d: %d SQL statement", limit, len(trace.queries))
	}
}

func TestLibrarySearchRecordedCursorScopeAndTies(t *testing.T) {
	// Equal matching names under different parents must retain their File ID ordering.
	api, db := setupMeasureAPI(t)
	ctx := context.Background()
	var want []int64
	var parents []int64
	for _, name := range []string{"a", "b"} {
		parent, err := api.lib.MkdirAll(ctx, 0, name, 0755)
		require.NoError(t, err)
		file := &library.File{Name: "same", ParentID: parent.ID}
		require.NoError(t, api.lib.SaveFile(ctx, file))
		require.NoError(t, db.Create(&library.FileLocation{
			FileID: file.ID, LocationID: 100, Path: name + "/same", Size: 100,
		}).Error)
		want = append(want, file.ID)
		parents = append(parents, parent.ID)
	}
	service := &filesService{api: api}
	request := &entity.SearchFilesRequest{
		Directory: &entity.FileOperationRef{Target: &entity.FileOperationRef_FileId{}},
		Scope:     entity.FileScope_FILE_SCOPE_ALL, Recursive: true, Query: "has:original AND size:100", Limit: 1,
	}

	// Pagination counts matching rows and ends exactly after the second equal-name result.
	first, err := service.Search(ctx, request)
	require.NoError(t, err)
	require.Len(t, first.Entries, 1)
	require.Equal(t, want[0], first.Entries[0].Reference.GetFileId())
	require.Equal(t, "a/same", first.Entries[0].Path)
	require.NotEmpty(t, first.NextCursor)
	request.Cursor = first.NextCursor
	second, err := service.Search(ctx, request)
	require.NoError(t, err)
	require.Len(t, second.Entries, 1)
	require.Equal(t, want[1], second.Entries[0].Reference.GetFileId())
	require.Equal(t, "b/same", second.Entries[0].Path)
	require.Empty(t, second.NextCursor)

	// Continuations remain bound to the query, visibility, parent and recursive mode.
	request.Query = "has:original"
	_, err = service.Search(ctx, request)
	require.Error(t, err)
	request.Query = "has:original AND size:100"
	request.Scope = entity.FileScope_FILE_SCOPE_SAVED
	_, err = service.Search(ctx, request)
	require.Error(t, err)
	request.Scope = entity.FileScope_FILE_SCOPE_ALL
	request.Recursive = false
	_, err = service.Search(ctx, request)
	require.Error(t, err)
	request.Recursive = true
	request.Directory = &entity.FileOperationRef{Target: &entity.FileOperationRef_FileId{FileId: parents[0]}}
	_, err = service.Search(ctx, request)
	require.Error(t, err)
}
