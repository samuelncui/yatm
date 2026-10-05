package apis_test

import (
	"context"
	"testing"

	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/internal/library"
	"github.com/stretchr/testify/require"
)

func TestFileBrowsersIncludeContentSummaries(t *testing.T) {
	// An organized unsigned File has an explicit summary rather than a fabricated archive claim.
	fixture := newContentFixture(t)
	ctx := context.Background()
	client := entity.NewFilesServiceClient(domainConnection(t, fixture.api))
	file := &library.File{Name: "not-backed-up.txt"}
	require.NoError(t, fixture.lib.SaveFile(ctx, file))

	// Both direct-child lists and query results project the same facts without changing identity.
	root, err := client.List(ctx, &entity.ListFilesRequest{Directory: fileRef(0), Include: []entity.FilesInclude{entity.FilesInclude_FILES_INCLUDE_STATUS}})
	require.NoError(t, err)
	listed, err := collectListed(root)
	require.NoError(t, err)
	require.Len(t, listed, 1)
	require.NotNil(t, listed[0].Status)
	require.Equal(t, entity.OriginalAvailability_ORIGINAL_AVAILABILITY_UNLINKED, listed[0].Status.Original)
	require.Equal(t, entity.FilesArchive_FILES_ARCHIVE_NONE, listed[0].Status.Archive)
	search, err := client.Search(ctx, &entity.SearchFilesRequest{Directory: fileRef(0), Recursive: true, Query: "name:not-backed-up", Include: []entity.FilesInclude{entity.FilesInclude_FILES_INCLUDE_STATUS}})
	require.NoError(t, err)
	result, err := collectSearched(search, nil)
	require.NoError(t, err)
	require.Len(t, result, 1)
	require.Equal(t, listed[0].Status, result[0].Status)
}
