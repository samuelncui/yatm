package apis

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/internal/library"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

func TestLocationSearchCombinesPreferenceAndMetadataFilters(t *testing.T) {
	// Register an ordinary and preferred directory with the same visible name.
	ctx := context.Background()
	api, service, root := setupLocationAPI(t)
	ordinary := &library.Location{Name: "Travel photos", RootPath: filepath.Join(root, "ordinary"), ExecutorID: "local"}
	require.NoError(t, api.lib.CreateLocation(ctx, ordinary))
	preferred := &library.Location{Name: "Travel photos", RootPath: filepath.Join(root, "preferred"), ExecutorID: "local", RestoreTarget: true}
	require.NoError(t, api.lib.CreateLocation(ctx, preferred))

	// The server combines search and preference before taking the bounded page.
	request := &entity.ListLocationsRequest{Query: "TRAVEL", Limit: 1, RestoreTarget: proto.Bool(true)}
	reply, err := service.List(ctx, request)
	require.NoError(t, err)
	require.False(t, reply.HasMore)
	require.Len(t, reply.Locations, 1)
	require.Equal(t, preferred.ID, reply.Locations[0].Id)

	// Deleted metadata cannot be selected from stale search results or a saved ID.
	_, err = api.lib.DeleteLocation(ctx, preferred.ID, false)
	require.NoError(t, err)
	reply, err = service.List(ctx, request)
	require.NoError(t, err)
	require.Empty(t, reply.Locations)
	_, err = service.Get(ctx, &entity.GetLocationRequest{Id: preferred.ID})
	require.Error(t, err)
}
