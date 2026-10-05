package apis

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/internal/executor"
	"github.com/samuelncui/yatm/internal/library"
	"github.com/samuelncui/yatm/internal/resource"
	"github.com/stretchr/testify/require"
)

func TestFileListScopeBeforePaginationWithoutRecursiveSize(t *testing.T) {
	// Build a nested tree whose unbacked leaf is larger than either saved leaf.
	ctx := context.Background()
	db, err := resource.OpenSQLite(filepath.Join(t.TempDir(), "library.db"))
	require.NoError(t, err)
	lib := library.New(db)
	require.NoError(t, lib.AutoMigrate())
	api := New(lib, executor.New(nil, lib, nil, executor.Paths{}, executor.Scripts{}, nil))
	service := &filesService{api: api}
	ref := func(id int64) *entity.FileOperationRef {
		return &entity.FileOperationRef{Target: &entity.FileOperationRef_FileId{FileId: id}}
	}
	root := &library.File{Name: "Directory", Kind: entity.FileKind_FILE_KIND_DIRECTORY}
	require.NoError(t, lib.SaveFile(ctx, root))
	sub := &library.File{Name: "a-directory", ParentID: root.ID, Kind: entity.FileKind_FILE_KIND_DIRECTORY}
	require.NoError(t, lib.SaveFile(ctx, sub))
	nested := &library.File{Name: "nested", ParentID: sub.ID, Kind: entity.FileKind_FILE_KIND_REGULAR}
	unsaved := &library.File{Name: "b-unbacked", ParentID: root.ID, Kind: entity.FileKind_FILE_KIND_REGULAR}
	saved := &library.File{Name: "z-saved", ParentID: root.ID, Kind: entity.FileKind_FILE_KIND_REGULAR}
	for _, file := range []*library.File{nested, unsaved, saved} {
		require.NoError(t, lib.SaveFile(ctx, file))
	}
	require.NoError(t, db.Create(&library.FileVersion{FileID: nested.ID, Signature: []byte("nested"), Size: 7}).Error)
	require.NoError(t, db.Create(&library.FileVersion{FileID: saved.ID, Signature: []byte("saved"), Size: 9}).Error)
	location := &library.Location{Name: "Originals", ExecutorID: "local", RootPath: t.TempDir()}
	require.NoError(t, lib.CreateLocation(ctx, location))
	require.NoError(t, db.Create(&library.FileLocation{FileID: unsaved.ID, LocationID: location.ID, Path: "unsaved", Size: 11}).Error)

	// Scope filters before paging; directory rows never report recursive sizes.
	page, err := service.Search(ctx, &entity.SearchFilesRequest{Directory: ref(root.ID), Scope: entity.FileScope_FILE_SCOPE_SAVED, Limit: 1, Include: []entity.FilesInclude{entity.FilesInclude_FILES_INCLUDE_ATTRIBUTES}})
	require.NoError(t, err)
	require.Len(t, page.Entries, 1)
	require.Equal(t, sub.ID, page.Entries[0].Reference.GetFileId())
	require.Nil(t, page.Entries[0].SizeBytes)
	require.NotEmpty(t, page.NextCursor)
	next, err := service.Search(ctx, &entity.SearchFilesRequest{Directory: ref(root.ID), Scope: page.Scope, Cursor: page.NextCursor, Limit: 1, Include: []entity.FilesInclude{entity.FilesInclude_FILES_INCLUDE_ATTRIBUTES}})
	require.NoError(t, err)
	require.Len(t, next.Entries, 1)
	require.Equal(t, saved.ID, next.Entries[0].Reference.GetFileId())
	require.Empty(t, next.NextCursor)

	// Explicit all scope includes the unsigned original, while direct-ID access never hides it.
	all, err := service.Search(ctx, &entity.SearchFilesRequest{Directory: ref(root.ID), Scope: entity.FileScope_FILE_SCOPE_ALL, Limit: 1, Include: []entity.FilesInclude{entity.FilesInclude_FILES_INCLUDE_ATTRIBUTES}})
	require.NoError(t, err)
	require.Len(t, all.Entries, 1)
	require.NotEmpty(t, all.NextCursor)
	one, err := service.Get(ctx, &entity.GetFileRequest{Reference: ref(unsaved.ID)})
	require.NoError(t, err)
	require.Equal(t, unsaved.ID, one.Detail.Entry.Reference.GetFileId())
	require.EqualValues(t, 11, one.Detail.Entry.GetSizeBytes())
}
