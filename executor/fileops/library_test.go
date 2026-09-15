package fileops

import (
	"context"
	"fmt"
	"io/fs"
	"path/filepath"
	"strings"
	"testing"

	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/library"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func libraryRef(id int64) *entity.FileOperationRef {
	return &entity.FileOperationRef{Target: &entity.FileOperationRef_FileId{FileId: id}}
}

func TestLibraryOperationsPreserveOrganizationBoundaries(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	f.write(t, "disk.txt", "unchanged")
	file := f.original(t, "disk.txt")
	version := &library.FileVersion{FileID: file.ID, Signature: []byte("saved")}
	require.NoError(t, f.libDB.Create(version).Error)

	// Nested logical paths and returned identities use the shared request/stream without disk changes.
	mkdir := f.execute(t, &entity.FileOperationSpec{Kind: entity.FileOperationKind_MAKE_DIRECTORY, Destination: libraryRef(0), Name: "archive/nested"}, nil)
	require.EqualValues(t, 2, mkdir.summary().Succeeded)
	dirID := mkdir.updates[2].Entry.GetFileId()
	require.NotZero(t, dirID)
	move := f.execute(t, &entity.FileOperationSpec{Kind: entity.FileOperationKind_MOVE, Sources: []*entity.FileOperationRef{libraryRef(file.ID)}, Destination: libraryRef(dirID), Name: "renamed.txt"}, nil)
	require.EqualValues(t, 1, move.summary().Succeeded)
	stored, err := f.exe.Lib().GetByPath(ctx, 0, "archive/nested/renamed.txt")
	require.NoError(t, err)
	require.Equal(t, file.ID, stored.ID)
	require.Equal(t, file.Note, stored.Note)
	require.FileExists(t, filepath.Join(f.root, "disk.txt"))

	// Library removal uses Trash and preserves the original, saved version and metadata.
	f.execute(t, &entity.FileOperationSpec{Kind: entity.FileOperationKind_DELETE, Sources: []*entity.FileOperationRef{libraryRef(file.ID), libraryRef(file.ID)}}, nil)
	parents, err := f.exe.Lib().ListParents(ctx, file.ID)
	require.NoError(t, err)
	require.EqualValues(t, library.TrashFileID, parents[0].ID)
	original, err := f.exe.Lib().GetFileLocation(ctx, file.ID)
	require.NoError(t, err)
	require.Equal(t, "disk.txt", original.Path)
	require.NoError(t, f.libDB.First(new(library.FileVersion), version.ID).Error)
}

func TestLibraryMoveNestedNameAndDescendantRollback(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	file := &library.File{Name: "old.txt", Kind: entity.FileKind_FILE_KIND_REGULAR}
	require.NoError(t, f.exe.Lib().SaveFile(ctx, file))
	stream := f.execute(t, &entity.FileOperationSpec{Kind: entity.FileOperationKind_MOVE, Sources: []*entity.FileOperationRef{libraryRef(file.ID)}, Destination: libraryRef(0), Name: "archive/new.txt"}, nil)
	require.EqualValues(t, 2, stream.summary().Succeeded)
	stored, err := f.exe.Lib().GetByPath(ctx, 0, "archive/new.txt")
	require.NoError(t, err)
	require.Equal(t, file.ID, stored.ID)

	// A failed nested self-move rolls back intermediate directories and reports the affected item.
	dir, err := f.exe.Lib().MkdirAll(ctx, 0, "parent", fs.ModePerm)
	require.NoError(t, err)
	stream = f.execute(t, &entity.FileOperationSpec{Kind: entity.FileOperationKind_MOVE, Sources: []*entity.FileOperationRef{libraryRef(dir.ID)}, Destination: libraryRef(0), Name: "parent/new/renamed"}, nil)
	require.EqualValues(t, 1, stream.summary().Failed)
	uncommitted, err := f.exe.Lib().GetByPath(ctx, 0, "parent/new")
	require.NoError(t, err)
	require.Nil(t, uncommitted)
}

func TestLibraryMkdirRetainsExistingPathRules(t *testing.T) {
	f := setup(t)
	// MkdirAll owns logical path validation, including trailing separators and current-directory lookup.
	stream := f.execute(t, &entity.FileOperationSpec{Kind: entity.FileOperationKind_MAKE_DIRECTORY,
		Destination: libraryRef(0), Name: "nested/folder/"}, nil)
	require.EqualValues(t, 2, stream.summary().Succeeded)
	id := stream.updates[2].Entry.GetFileId()
	require.NotZero(t, id)
	stream = f.execute(t, &entity.FileOperationSpec{Kind: entity.FileOperationKind_MAKE_DIRECTORY,
		Destination: libraryRef(id), Name: "."}, nil)
	require.EqualValues(t, 1, stream.summary().Succeeded)
	require.Equal(t, id, stream.updates[1].Entry.GetFileId())
}

func TestLibraryMovePreservesMetadataEditedAfterPreparation(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	file := &library.File{Name: "old.txt", Kind: entity.FileKind_FILE_KIND_REGULAR, Note: "old note"}
	require.NoError(t, f.exe.Lib().SaveFile(ctx, file))

	// Streaming can delay execution after preparation; moving must not save an old Note snapshot.
	stream := f.execute(t, &entity.FileOperationSpec{Kind: entity.FileOperationKind_MOVE,
		Sources: []*entity.FileOperationRef{libraryRef(file.ID)}, Destination: libraryRef(0), Name: "new.txt"}, func() {
		require.NoError(t, f.libDB.Model(&library.File{}).Where("id = ?", file.ID).Update("note", "edited while preparing").Error)
	})
	require.EqualValues(t, 1, stream.summary().Succeeded)
	stored, err := f.exe.Lib().GetFile(ctx, file.ID)
	require.NoError(t, err)
	require.Equal(t, "new.txt", stored.Name)
	require.Equal(t, "edited while preparing", stored.Note)
}

func TestLibraryOverlappingSelectionAndDirectoryMerge(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	source, err := f.exe.Lib().MkdirAll(ctx, 0, "from/folder", fs.ModePerm)
	require.NoError(t, err)
	child, err := f.exe.Lib().MkdirAll(ctx, source.ID, "child", fs.ModePerm)
	require.NoError(t, err)
	target, err := f.exe.Lib().MkdirAll(ctx, 0, "to", fs.ModePerm)
	require.NoError(t, err)
	existing, err := f.exe.Lib().MkdirAll(ctx, target.ID, "folder", fs.ModePerm)
	require.NoError(t, err)

	// The selected parent owns its descendants; Library's existing directory merge remains intact.
	stream := f.execute(t, &entity.FileOperationSpec{Kind: entity.FileOperationKind_MOVE,
		Sources: []*entity.FileOperationRef{libraryRef(child.ID), libraryRef(source.ID)}, Destination: libraryRef(target.ID)}, nil)
	require.EqualValues(t, 2, stream.summary().Succeeded)
	require.Equal(t, existing.ID, stream.updates[2].Entry.GetFileId())
	stored, err := f.exe.Lib().GetFile(ctx, child.ID)
	require.NoError(t, err)
	require.Equal(t, existing.ID, stored.ParentID)
}

func TestSharedOperationRejectsInvalidSelectionsBeforeMutation(t *testing.T) {
	f := setup(t)
	f.write(t, "physical.txt", "must remain")
	file := f.original(t, "physical.txt")
	for _, spec := range []*entity.FileOperationSpec{
		nil,
		{Kind: entity.FileOperationKind_DELETE, Sources: []*entity.FileOperationRef{nil}},
		{Kind: entity.FileOperationKind_DELETE, Sources: []*entity.FileOperationRef{{}}},
		{Kind: entity.FileOperationKind_DELETE, Sources: []*entity.FileOperationRef{libraryRef(file.ID), f.ref(t, "physical.txt")}},
		{Kind: entity.FileOperationKind_DELETE, Sources: []*entity.FileOperationRef{libraryRef(file.ID), libraryRef(999999)}},
		{Kind: entity.FileOperationKind_DELETE, Sources: []*entity.FileOperationRef{libraryRef(0)}},
		{Kind: entity.FileOperationKind_COPY, Sources: []*entity.FileOperationRef{libraryRef(file.ID)}, Destination: libraryRef(0)},
		{Kind: entity.FileOperationKind_MOVE, Sources: []*entity.FileOperationRef{libraryRef(file.ID)}, Destination: libraryRef(file.ID)},
	} {
		stream := &operationStream{ctx: context.Background()}
		err := (&service{exe: f.exe}).Execute(&entity.ExecuteFileOperationRequest{Spec: spec, ConfirmDelete: true}, stream)
		require.Error(t, err)
		require.Empty(t, stream.updates)
		stored, err := f.exe.Lib().GetFile(context.Background(), file.ID)
		require.NoError(t, err)
		require.Equal(t, file.ParentID, stored.ParentID)
		require.FileExists(t, filepath.Join(f.root, "physical.txt"))
	}
}

func TestLibraryOperationExcludesCatalogReplacement(t *testing.T) {
	for _, kind := range []entity.FileOperationKind{entity.FileOperationKind_MOVE, entity.FileOperationKind_MAKE_DIRECTORY, entity.FileOperationKind_DELETE} {
		t.Run(kind.String(), func(t *testing.T) {
			f := setup(t)
			ctx := context.Background()
			parent, err := f.exe.Lib().MkdirAll(ctx, 0, "folder", fs.ModePerm)
			require.NoError(t, err)
			file := &library.File{Name: "file.txt", Kind: entity.FileKind_FILE_KIND_REGULAR}
			require.NoError(t, f.exe.Lib().SaveFile(ctx, file))
			spec := &entity.FileOperationSpec{Kind: kind, Sources: []*entity.FileOperationRef{libraryRef(file.ID)}, Destination: libraryRef(parent.ID), Name: "new.txt"}
			if kind == entity.FileOperationKind_MAKE_DIRECTORY {
				spec.Sources = nil
			}
			if kind == entity.FileOperationKind_DELETE {
				spec.Destination, spec.Name = nil, ""
			}

			// Attempt imported ID replacement during lookup; the request must hold admission until return.
			attempted := false
			require.NoError(t, f.libDB.Callback().Query().After("gorm:after_query").Register("test:catalog_replacement", func(tx *gorm.DB) {
				if attempted || tx.Statement.Table != "files" {
					return
				}
				attempted = true
				backup := fmt.Sprintf(`{"files":[{"id":%d,"name":"replacement","mode":420}]}`, file.ID)
				require.ErrorIs(t, f.exe.Lib().Import(ctx, strings.NewReader(backup)), library.ErrOnlineBusy)
			}))
			f.execute(t, spec, nil)
			require.True(t, attempted)
			require.NoError(t, f.exe.Lib().Import(ctx, strings.NewReader(`{"files":[]}`)))
		})
	}
}
