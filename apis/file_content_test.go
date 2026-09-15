package apis_test

import (
	"context"
	"testing"

	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/library"
	"github.com/stretchr/testify/require"
)

func TestFileBrowsersIncludeContentSummaries(t *testing.T) {
	// An organized unsigned File has an explicit summary rather than a fabricated backup claim.
	fixture := newOnlineContentFixture(t)
	ctx := context.Background()
	file := &library.File{Name: "not-backed-up.txt"}
	require.NoError(t, fixture.lib.SaveFile(ctx, file))

	// Both direct-child lists and query results project the same facts without changing identity.
	root, err := fixture.api.FileGet(ctx, &entity.FileGetRequest{})
	require.NoError(t, err)
	require.Len(t, root.Children, 1)
	require.NotNil(t, root.Children[0].ContentSummary)
	require.False(t, root.Children[0].ContentSummary.HasOriginal)
	require.False(t, root.Children[0].ContentSummary.HasVersions)
	result, err := fixture.api.FileSearch(ctx, &entity.FileSearchRequest{Query: "name:not-backed-up"})
	require.NoError(t, err)
	require.Len(t, result.Results, 1)
	require.Equal(t, root.Children[0].ContentSummary, result.Results[0].File.ContentSummary)
}
