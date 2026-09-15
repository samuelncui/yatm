package apis

import (
	"context"
	"crypto/sha256"
	"testing"

	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/library"
	"github.com/stretchr/testify/require"
)

func TestDuplicateGroupCatalogReplies(t *testing.T) {
	// Publish equal-content originals through the ordinary Location path; no search hashes are needed.
	api, locations, root := setupOnlineAPI(t)
	ctx := context.Background()
	created, err := locations.Create(ctx, &entity.CreateLocationRequest{Location: &entity.Location{Name: "Photos", RootPath: root}})
	require.NoError(t, err)
	hash := sha256.Sum256([]byte("same"))
	_, err = api.lib.PublishOnline(ctx, created.Location.Id, created.Location.Revision, 1,
		func(_ context.Context, yield func(*library.OnlinePosition) error) error {
			for _, path := range []string{"copy.png", "first.png", "third.png"} {
				if err := yield(&library.OnlinePosition{Path: path, Hash: hash[:], Size: 4, Mode: 0644}); err != nil {
					return err
				}
			}
			return nil
		})
	require.NoError(t, err)
	service := &fileCatalogService{api: api}

	// A name filter includes the whole group and exposes match flags and both physical/logical paths.
	page, err := service.ListDuplicateGroups(ctx, &entity.ListDuplicateGroupsRequest{Query: "name:first", Limit: 1})
	require.NoError(t, err)
	require.Len(t, page.Groups, 1)
	require.Equal(t, int64(3), page.Groups[0].OriginalCount)
	require.Equal(t, int64(1), page.Groups[0].MatchingCount)
	members, err := service.ListDuplicateMembers(ctx, &entity.ListDuplicateMembersRequest{
		Signature: page.Groups[0].Signature, Query: "name:first", Limit: 2})
	require.NoError(t, err)
	require.Len(t, members.Members, 2)
	require.NotEmpty(t, members.NextCursor)
	require.Equal(t, page.IndexRevision, members.IndexRevision)
	for _, member := range members.Members {
		require.NotNil(t, member.File.ContentSummary)
		require.Equal(t, member.File.Id, member.Original.FileId)
		require.Equal(t, "Photos", member.LocationName)
		require.Contains(t, member.LibraryPath, "/Unforged/Photos/")
		require.Equal(t, member.File.Name == "first.png", member.MatchesFilter)
	}

	// Malformed paging requests cannot fall through to a full-catalog response.
	_, err = service.ListDuplicateGroups(ctx, nil)
	require.Error(t, err)
	_, err = service.ListDuplicateMembers(ctx, nil)
	require.Error(t, err)
	_, err = service.ListDuplicateGroups(ctx, &entity.ListDuplicateGroupsRequest{Limit: -1})
	require.Error(t, err)
}
