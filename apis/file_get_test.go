package apis

import (
	"context"
	"testing"

	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/library"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestFileGetDistinguishesMissingFilesFromVirtualRoot(t *testing.T) {
	// Root zero is the only virtual node; a missing positive ID is not an empty directory.
	api, _, _ := setupOnlineAPI(t)
	ctx := context.Background()
	file := &library.File{Name: "existing"}
	require.NoError(t, api.lib.SaveFile(ctx, file))
	root, err := api.FileGet(ctx, &entity.FileGetRequest{})
	require.NoError(t, err)
	require.Len(t, root.Children, 1)
	require.Equal(t, file.ID, root.Children[0].Id)
	_, err = api.FileGet(ctx, &entity.FileGetRequest{Id: file.ID + 1})
	require.Equal(t, codes.NotFound, status.Code(err))

	// Invalid requests fail at admission instead of panicking or fabricating root data.
	for _, request := range []*entity.FileGetRequest{nil, {Id: -2}} {
		_, err := api.FileGet(ctx, request)
		require.Equal(t, codes.InvalidArgument, status.Code(err))
	}

	// Trash is a persisted reserved identity, not a second virtual-root sentinel.
	_, err = api.FileGet(ctx, &entity.FileGetRequest{Id: library.TrashFileID})
	require.Equal(t, codes.NotFound, status.Code(err))
	require.NoError(t, api.lib.Delete(ctx, []int64{file.ID}))
	trash, err := api.FileGet(ctx, &entity.FileGetRequest{Id: library.TrashFileID})
	require.NoError(t, err)
	require.EqualValues(t, library.TrashFileID, trash.File.Id)
	require.Len(t, trash.Children, 1)
	checkpoint, err := api.FileGet(ctx, &entity.FileGetRequest{Id: trash.Children[0].Id})
	require.NoError(t, err)
	require.Len(t, checkpoint.Children, 1)
	require.Equal(t, file.ID, checkpoint.Children[0].Id)
}
