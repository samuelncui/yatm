package apis

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/executor"
	"github.com/samuelncui/yatm/library"
	"github.com/samuelncui/yatm/resource"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

func TestFileGetScopeSizesCoverTheWholeTreeBeforePagination(t *testing.T) {
	// Build a nested tree whose unbacked leaf is larger than either saved leaf.
	ctx := context.Background()
	db, err := resource.OpenSQLite(filepath.Join(t.TempDir(), "library.db"))
	require.NoError(t, err)
	lib := library.New(db)
	require.NoError(t, lib.AutoMigrate())
	api := New(lib, executor.New(nil, lib, nil, executor.Paths{}, executor.Scripts{}, nil))
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
	require.NoError(t, lib.CreateOnlineSource(ctx, location))
	require.NoError(t, db.Create(&library.FileLocation{FileID: unsaved.ID, LocationID: location.ID, Path: "unsaved", Size: 11}).Error)

	// A one-child page still reports the full selected subtree, not a sum of the loaded page.
	page, err := api.FileGet(ctx, &entity.FileGetRequest{Id: root.ID, Scope: entity.FileScope_FILE_SCOPE_SAVED, Limit: 1, NeedSize: proto.Bool(true)})
	require.NoError(t, err)
	require.Len(t, page.Children, 1)
	require.Equal(t, sub.ID, page.Children[0].Id)
	require.EqualValues(t, 7, page.Children[0].Size)
	require.EqualValues(t, 16, page.File.Size)
	require.NotEmpty(t, page.NextCursor)
	next, err := api.FileGet(ctx, &entity.FileGetRequest{Id: root.ID, Scope: page.Scope, Cursor: page.NextCursor, Limit: 1, NeedSize: proto.Bool(true)})
	require.NoError(t, err)
	require.Len(t, next.Children, 1)
	require.Equal(t, saved.ID, next.Children[0].Id)
	require.EqualValues(t, 16, next.File.Size)
	require.Empty(t, next.NextCursor)

	// Explicit all scope includes the unsigned original, while direct-ID access never hides it.
	all, err := api.FileGet(ctx, &entity.FileGetRequest{Id: root.ID, Scope: entity.FileScope_FILE_SCOPE_ALL, Limit: 1, NeedSize: proto.Bool(true)})
	require.NoError(t, err)
	require.EqualValues(t, 27, all.File.Size)
	one, err := api.FileGet(ctx, &entity.FileGetRequest{Id: unsaved.ID, Scope: entity.FileScope_FILE_SCOPE_SAVED})
	require.NoError(t, err)
	require.Equal(t, unsaved.ID, one.File.Id)
	require.EqualValues(t, 11, one.File.Size)
}
