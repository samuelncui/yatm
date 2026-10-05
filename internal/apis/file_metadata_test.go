package apis_test

import (
	"context"
	"testing"

	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/internal/library"
	"github.com/stretchr/testify/require"
)

func TestFileMetadataAndSearchAPIs(t *testing.T) {
	// Persist the logical directory explicitly before assigning its child File.
	ctx := context.Background()
	fixture := newContentFixture(t)
	conn := domainConnection(t, fixture.api)
	files := entity.NewFilesServiceClient(conn)
	libraryClient := entity.NewLibraryServiceClient(conn)
	directory := &library.File{Name: "docs", Kind: entity.FileKind_FILE_KIND_DIRECTORY}
	require.NoError(t, fixture.lib.SaveFile(ctx, directory))
	file := &library.File{ParentID: directory.ID, Name: "report.txt", Mode: 0o644}
	require.NoError(t, fixture.lib.SaveFile(ctx, file))

	// Apply each metadata patch through the same public atomic boundary.
	note := "reviewed"
	_, err := files.UpdateMetadata(ctx, &entity.UpdateFilesMetadataRequest{
		References: []*entity.FileOperationRef{fileRef(directory.ID)}, AddTags: []string{"folder"},
	})
	require.NoError(t, err)
	_, err = files.UpdateMetadata(ctx, &entity.UpdateFilesMetadataRequest{
		References: []*entity.FileOperationRef{fileRef(file.ID)}, AddTags: []string{" Finance ", "archive"}, Note: &note,
	})
	require.NoError(t, err)

	// Details include canonical Tags; search filters Tags without returning full metadata.
	detail, err := files.Get(ctx, &entity.GetFileRequest{Reference: fileRef(file.ID)})
	require.NoError(t, err)
	require.Equal(t, "reviewed", detail.Detail.Organization.Note)
	require.Equal(t, []string{"archive", "finance"}, detail.Detail.Organization.Tags)
	directoryView, err := files.Get(ctx, &entity.GetFileRequest{Reference: fileRef(directory.ID)})
	require.NoError(t, err)
	require.Equal(t, []string{"folder"}, directoryView.Detail.Organization.Tags)
	search, err := files.Search(ctx, &entity.SearchFilesRequest{Directory: fileRef(0), Recursive: true, Query: "tag:finance AND note:review"})
	require.NoError(t, err)
	require.Len(t, search.Entries, 1)
	require.Equal(t, "docs/report.txt", search.Entries[0].Path)
	tags, err := libraryClient.ListTags(ctx, &entity.ListTagsRequest{})
	require.NoError(t, err)
	require.Len(t, tags.Tags, 3)

	// A missing identity rejects every field in the edit before changing the existing File.
	changed := "changed"
	_, err = files.UpdateMetadata(ctx, &entity.UpdateFilesMetadataRequest{
		References: []*entity.FileOperationRef{fileRef(file.ID), fileRef(99999)}, AddTags: []string{"partial"}, Note: &changed,
	})
	require.Error(t, err)
	detail, err = files.Get(ctx, &entity.GetFileRequest{Reference: fileRef(file.ID)})
	require.NoError(t, err)
	require.Equal(t, "reviewed", detail.Detail.Organization.Note)
	require.Equal(t, []string{"archive", "finance"}, detail.Detail.Organization.Tags)
}
