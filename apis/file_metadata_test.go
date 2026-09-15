package apis_test

import (
	"context"
	"testing"

	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/library"
	"github.com/stretchr/testify/require"
)

func TestFileMetadataAndSearchAPIs(t *testing.T) {
	// Persist the logical directory explicitly before assigning its child File.
	ctx := context.Background()
	fixture := newOnlineContentFixture(t)
	directory := &library.File{Name: "docs", Kind: entity.FileKind_FILE_KIND_DIRECTORY}
	require.NoError(t, fixture.lib.SaveFile(ctx, directory))
	file := &library.File{ParentID: directory.ID, Name: "report.txt", Mode: 0o644}
	require.NoError(t, fixture.lib.SaveFile(ctx, file))

	// Apply each metadata patch through the same public atomic boundary.
	note := "reviewed"
	_, err := fixture.api.FileMetadataEdit(ctx, &entity.FileMetadataEditRequest{
		Ids: []int64{directory.ID}, AddTags: []string{"folder"},
	})
	require.NoError(t, err)
	_, err = fixture.api.FileMetadataEdit(ctx, &entity.FileMetadataEditRequest{
		Ids: []int64{file.ID}, AddTags: []string{" Finance ", "archive"}, Note: &note,
	})
	require.NoError(t, err)

	// Every public File response includes its complete canonical Tag projection.
	detail, err := fixture.api.FileGet(ctx, &entity.FileGetRequest{Id: file.ID})
	require.NoError(t, err)
	require.Equal(t, "reviewed", detail.File.Note)
	require.Equal(t, []string{"archive", "finance"}, detail.File.Tags)
	directoryView, err := fixture.api.FileGet(ctx, &entity.FileGetRequest{Id: directory.ID})
	require.NoError(t, err)
	require.Equal(t, []string{"folder"}, directoryView.File.Tags)
	require.Len(t, directoryView.Children, 1)
	require.Equal(t, []string{"archive", "finance"}, directoryView.Children[0].Tags)
	search, err := fixture.api.FileSearch(ctx, &entity.FileSearchRequest{Query: "tag:finance AND note:review"})
	require.NoError(t, err)
	require.Len(t, search.Results, 1)
	require.Equal(t, "/docs/report.txt", search.Results[0].Path)
	require.Equal(t, []string{"archive", "finance"}, search.Results[0].File.Tags)
	tags, err := fixture.api.TagList(ctx, &entity.TagListRequest{})
	require.NoError(t, err)
	require.Len(t, tags.Tags, 3)

	// A missing identity rejects every field in the edit before changing the existing File.
	changed := "changed"
	_, err = fixture.api.FileMetadataEdit(ctx, &entity.FileMetadataEditRequest{
		Ids: []int64{file.ID, 99999}, AddTags: []string{"partial"}, Note: &changed,
	})
	require.Error(t, err)
	detail, err = fixture.api.FileGet(ctx, &entity.FileGetRequest{Id: file.ID})
	require.NoError(t, err)
	require.Equal(t, "reviewed", detail.File.Note)
	require.Equal(t, []string{"archive", "finance"}, detail.File.Tags)
}
