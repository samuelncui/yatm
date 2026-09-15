package library

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/samuelncui/yatm/entity"
	"github.com/stretchr/testify/require"
)

func TestSearchFilesSupportsFieldsBooleanTagsAndPaths(t *testing.T) {
	// Persist logical kinds separately from the archived content used by search projections.
	ctx := context.Background()
	db, lib := newTestLibrary(t)
	modified := time.Date(2026, time.January, 2, 3, 4, 5, 0, time.UTC)
	fixtures := []*File{
		{ID: 1, Name: "docs", Kind: entity.FileKind_FILE_KIND_DIRECTORY, ModTime: modified},
		{ID: 2, ParentID: 1, Name: "report.txt", Mode: 0o644, Size: 200, ModTime: modified, Note: "Quarterly review"},
		{ID: 3, Name: "photo.jpg", Mode: 0o644, Size: 100, ModTime: modified, Note: "Family"},
		{ID: TrashFileID, Name: ".Trash", Kind: entity.FileKind_FILE_KIND_DIRECTORY, ModTime: modified},
		{ID: 4, ParentID: TrashFileID, Name: "old.txt", Mode: 0o644, Size: 50, ModTime: modified},
		{ID: 5, Name: "100%_complete.txt", Mode: 0o644, Size: 25, ModTime: modified, Note: "literal%_value"},
	}
	require.NoError(t, db.Create(fixtures).Error)
	seedTestArchivedFacts(t, db, fixtures...)
	require.NoError(t, lib.EditFileMetadata(ctx, []int64{2}, FileMetadataEdit{
		AddTags: []string{"finance", "archive"},
	}))
	require.NoError(t, lib.EditFileMetadata(ctx, []int64{3}, FileMetadataEdit{AddTags: []string{"family"}}))

	// Search predicates must agree on directory kinds, content facts and logical paths.
	tests := []struct {
		name  string
		query string
		ids   []int64
		paths []string
	}{
		{name: "bare note", query: "quarterly", ids: []int64{2}, paths: []string{"/docs/report.txt"}},
		{name: "multiple Tags", query: "tag:finance AND tag:archive", ids: []int64{2}, paths: []string{"/docs/report.txt"}},
		{name: "wildcard", query: "name:*.jpg", ids: []int64{3}, paths: []string{"/photo.jpg"}},
		{name: "size", query: "size: >= 150", ids: []int64{2}, paths: []string{"/docs/report.txt"}},
		{name: "size exclusive lower bound", query: "size: > 100", ids: []int64{2}, paths: []string{"/docs/report.txt"}},
		{name: "size inclusive upper bound", query: "size: <= 25", ids: []int64{-1, 1, 5}, paths: []string{"/.Trash", "/docs", "/100%_complete.txt"}},
		{name: "mtime", query: "mtime: >= \"2026-01-02T03:04:05Z\"", ids: []int64{-1, 1, 2, 3, 4, 5},
			paths: []string{"/.Trash", "/docs", "/docs/report.txt", "/photo.jpg", "/.Trash/old.txt", "/100%_complete.txt"}},
		{name: "mtime exclusive upper bound", query: "mtime: < \"2026-01-02T03:04:05Z\"", ids: []int64{}, paths: []string{}},
		{name: "mtime inclusive upper bound", query: "mtime: <= \"2026-01-02T03:04:05Z\"", ids: []int64{-1, 1, 2, 3, 4, 5},
			paths: []string{"/.Trash", "/docs", "/docs/report.txt", "/photo.jpg", "/.Trash/old.txt", "/100%_complete.txt"}},
		{name: "Trash", query: "name:old", ids: []int64{4}, paths: []string{"/.Trash/old.txt"}},
		{name: "adjacent means AND", query: "tag:finance tag:archive", ids: []int64{2}, paths: []string{"/docs/report.txt"}},
		{name: "parenthesized boolean", query: "type:file AND (tag:finance OR name:old)", ids: []int64{2, 4}, paths: []string{"/docs/report.txt", "/.Trash/old.txt"}},
		{name: "NOT", query: "type:file AND NOT tag:family", ids: []int64{2, 4, 5}, paths: []string{"/docs/report.txt", "/.Trash/old.txt", "/100%_complete.txt"}},
		{name: "SQL wildcard literals", query: `name:"100%_complete"`, ids: []int64{5}, paths: []string{"/100%_complete.txt"}},
		{name: "query wildcard", query: `name:100?_*`, ids: []int64{5}, paths: []string{"/100%_complete.txt"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			page, err := lib.SearchFiles(ctx, test.query, "", 100)
			require.NoError(t, err)
			ids := make([]int64, 0, len(page.Results))
			paths := make([]string, 0, len(page.Results))
			for _, result := range page.Results {
				ids = append(ids, result.File.ID)
				paths = append(paths, result.Path)
			}
			require.ElementsMatch(t, test.ids, ids)
			require.ElementsMatch(t, test.paths, paths)
		})
	}
}

func TestSearchFilesRejectsUnsupportedQueries(t *testing.T) {
	_, lib := newTestLibrary(t)
	for _, query := range []string{"", "path:docs", "type:image", "size:-1", "name:/report.*/"} {
		t.Run(query, func(t *testing.T) {
			_, err := lib.SearchFiles(context.Background(), query, "", 100)
			require.Error(t, err)
		})
	}
	_, err := lib.SearchFiles(context.Background(), strings.Repeat("a", maxFileSearchRunes+1), "", 100)
	require.ErrorContains(t, err, "exceeds")
	_, err = lib.SearchFiles(context.Background(), "name:report", "", maxFileSearchLimit+1)
	require.ErrorContains(t, err, "page limit")
	_, err = lib.ListTags(context.Background(), "", "", -1)
	require.ErrorContains(t, err, "page limit")
}

func TestSearchFilesAndTagListUseQueryBoundOpaqueCursors(t *testing.T) {
	// Keep same-name Files under distinct logical directories for stable cursor ties.
	ctx := context.Background()
	db, lib := newTestLibrary(t)
	require.NoError(t, db.Create([]*File{
		{ID: 1, Name: "same", Mode: 0o644},
		{ID: 2, Name: "same", ParentID: 10, Mode: 0o644},
	}).Error)
	// Use distinct parents to satisfy the logical sibling-name uniqueness constraint.
	require.NoError(t, db.Create(&File{ID: 10, Name: "dir", Kind: entity.FileKind_FILE_KIND_DIRECTORY}).Error)
	require.NoError(t, lib.EditFileMetadata(ctx, []int64{1}, FileMetadataEdit{
		AddTags: []string{"alpha", "alpine"},
	}))

	// Page through same-name Files and reject cursor reuse with another query.
	first, err := lib.SearchFiles(ctx, "name:same", "", 1)
	require.NoError(t, err)
	require.Len(t, first.Results, 1)
	require.NotEmpty(t, first.NextCursor)
	second, err := lib.SearchFiles(ctx, "name:same", first.NextCursor, 1)
	require.NoError(t, err)
	require.Len(t, second.Results, 1)
	require.NotEqual(t, first.Results[0].File.ID, second.Results[0].File.ID)
	_, err = lib.SearchFiles(ctx, "name:other", first.NextCursor, 1)
	require.ErrorContains(t, err, "another query")

	// Page the canonical Tag projection and bind its cursor to the normalized prefix.
	tags, err := lib.ListTags(ctx, "al", "", 1)
	require.NoError(t, err)
	require.Len(t, tags.Tags, 1)
	require.NotEmpty(t, tags.NextCursor)
	next, err := lib.ListTags(ctx, "AL", tags.NextCursor, 1)
	require.NoError(t, err)
	require.Len(t, next.Tags, 1)
	require.NotEqual(t, tags.Tags[0].Name, next.Tags[0].Name)
	_, err = lib.ListTags(ctx, "other", tags.NextCursor, 1)
	require.ErrorContains(t, err, "another query")
	_, err = lib.SearchFiles(ctx, "name:same", tags.NextCursor, 1)
	require.ErrorContains(t, err, "another operation")
	_, err = lib.SearchFiles(ctx, "name:same", "not-a-cursor", 1)
	require.ErrorContains(t, err, "page cursor")
}
