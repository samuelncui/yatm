package apis

import (
	"context"
	"testing"

	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/internal/library"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestFileGetDistinguishesMissingFilesFromVirtualRoot(t *testing.T) {
	// Root zero is the only virtual node; a missing positive ID is not an empty directory.
	api, _, _ := setupLocationAPI(t)
	ctx := context.Background()
	service := &filesService{api: api}
	ref := func(id int64) *entity.FileOperationRef {
		return &entity.FileOperationRef{Target: &entity.FileOperationRef_FileId{FileId: id}}
	}
	file := &library.File{Name: "existing"}
	require.NoError(t, api.lib.SaveFile(ctx, file))
	root, err := listFiles(t, ctx, service, &entity.ListFilesRequest{Directory: ref(0)})
	require.NoError(t, err)
	require.Len(t, root.Entries, 1)
	require.Equal(t, file.ID, root.Entries[0].Reference.GetFileId())
	_, err = service.Get(ctx, &entity.GetFileRequest{Reference: ref(file.ID + 1)})
	require.Equal(t, codes.NotFound, status.Code(err))

	// Invalid requests fail at admission instead of panicking or fabricating root data.
	for _, request := range []*entity.GetFileRequest{nil, {Reference: ref(-2)}} {
		_, err := service.Get(ctx, request)
		require.Equal(t, codes.InvalidArgument, status.Code(err))
	}

	// Trash is a persisted reserved identity, not a second virtual-root sentinel.
	_, err = service.Get(ctx, &entity.GetFileRequest{Reference: ref(library.TrashFileID)})
	require.Equal(t, codes.NotFound, status.Code(err))
	require.NoError(t, api.lib.Delete(ctx, []int64{file.ID}))
	trash, err := listFiles(t, ctx, service, &entity.ListFilesRequest{Directory: ref(library.TrashFileID), Include: []entity.FilesInclude{entity.FilesInclude_FILES_INCLUDE_NAVIGATION}})
	require.NoError(t, err)
	require.EqualValues(t, library.TrashFileID, trash.Directory.Reference.GetFileId())
	require.Len(t, trash.Entries, 1)
	checkpoint, err := listFiles(t, ctx, service, &entity.ListFilesRequest{Directory: trash.Entries[0].Reference})
	require.NoError(t, err)
	require.Len(t, checkpoint.Entries, 1)
	require.Equal(t, entity.EntryKind_ENTRY_KIND_DIRECTORY, checkpoint.Entries[0].Kind)

	// Delete and Merge retain each File inside its own container beneath the checkpoint.
	container, err := listFiles(t, ctx, service, &entity.ListFilesRequest{Directory: checkpoint.Entries[0].Reference})
	require.NoError(t, err)
	require.Len(t, container.Entries, 1)
	require.Equal(t, file.ID, container.Entries[0].Reference.GetFileId())
	require.Equal(t, file.Name, container.Entries[0].Name)
}
