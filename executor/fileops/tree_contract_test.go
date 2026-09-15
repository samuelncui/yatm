package fileops

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"testing"

	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/library"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

type organizationFixture struct {
	f       *fixture
	logical bool
}

func (f organizationFixture) file(t *testing.T, name string) *entity.FileOperationRef {
	t.Helper()
	if !f.logical {
		f.f.write(t, name, "payload")
		return f.f.ref(t, name)
	}
	parent, err := f.f.exe.Lib().MkdirAll(context.Background(), 0, path.Dir(name), fs.ModePerm)
	require.NoError(t, err)
	file := &library.File{ParentID: parent.ID, Name: path.Base(name), Kind: entity.FileKind_FILE_KIND_REGULAR}
	require.NoError(t, f.f.exe.Lib().SaveFile(context.Background(), file))
	return libraryRef(file.ID)
}

func (f organizationFixture) dir(t *testing.T, name string) *entity.FileOperationRef {
	t.Helper()
	if !f.logical {
		require.NoError(t, os.MkdirAll(filepath.Join(f.f.root, name), 0755))
		return f.f.ref(t, name)
	}
	if name == "" {
		return libraryRef(0)
	}
	dir, err := f.f.exe.Lib().MkdirAll(context.Background(), 0, name, fs.ModePerm)
	require.NoError(t, err)
	return libraryRef(dir.ID)
}

func (f organizationFixture) ref(t *testing.T, name string) *entity.FileOperationRef {
	t.Helper()
	if !f.logical {
		return f.f.ref(t, name)
	}
	file, err := f.f.exe.Lib().GetByPath(context.Background(), 0, name)
	require.NoError(t, err)
	require.NotNil(t, file)
	return libraryRef(file.ID)
}

func (f organizationFixture) exists(t *testing.T, name string, want bool) {
	t.Helper()
	if !f.logical {
		_, err := os.Lstat(filepath.Join(f.f.root, name))
		require.Equal(t, want, err == nil, name)
		return
	}
	file, err := f.f.exe.Lib().GetByPath(context.Background(), 0, name)
	require.NoError(t, err)
	require.Equal(t, want, file != nil, name)
}

func TestOrganizationStoreContract(t *testing.T) {
	for _, logical := range []bool{false, true} {
		for _, scenario := range []string{"merge", "conflict", "same location", "nested name", "overlap", "pagination"} {
			t.Run(fmt.Sprintf("library=%t/%s", logical, scenario), func(t *testing.T) {
				// Both real adapters receive the same public operation and assert the same tree rules.
				f := organizationFixture{f: setup(t), logical: logical}
				f.file(t, "from/folder/a")
				f.file(t, "to/folder/b")
				source, target := f.ref(t, "from/folder"), f.ref(t, "to")
				spec := &entity.FileOperationSpec{Kind: entity.FileOperationKind_MOVE, Sources: []*entity.FileOperationRef{source}, Destination: target}
				switch scenario {
				case "conflict":
					f.file(t, "to/folder/a")
				case "same location":
					spec.Destination = f.ref(t, "from")
				case "nested name":
					spec.Name = "new/deep/folder"
				case "overlap":
					spec.Sources = []*entity.FileOperationRef{f.ref(t, "from/folder/a"), source, source}
				case "pagination":
					for i := 0; i < 135; i++ {
						f.file(t, fmt.Sprintf("from/folder/n%03d", i))
					}
					spec.Sources = []*entity.FileOperationRef{f.ref(t, "from/folder")}
				}
				if !logical {
					spec.Destination = f.ref(t, spec.Destination.GetLocation().Path)
				}
				stream := f.f.execute(t, spec, nil)

				// Preflight conflicts leave the complete selected root untouched.
				if scenario == "conflict" {
					require.EqualValues(t, 1, stream.summary().Failed)
					f.exists(t, "from/folder/a", true)
					f.exists(t, "to/folder/b", true)
					return
				}
				require.Zero(t, stream.summary().Failed)
				require.Zero(t, stream.summary().Unprocessed)
				if scenario == "same location" {
					f.exists(t, "from/folder/a", true)
					return
				}
				if scenario == "nested name" {
					f.exists(t, "to/new/deep/folder/a", true)
					return
				}
				f.exists(t, "from/folder", false)
				f.exists(t, "to/folder/a", true)
				f.exists(t, "to/folder/b", true)
				if scenario == "pagination" {
					f.exists(t, "to/folder/n134", true)
				}
			})
		}
	}
}

func TestMergedPhysicalDirectoryPreservesTargetOriginals(t *testing.T) {
	// The destination original is independent and must survive another directory's merge.
	f := setup(t)
	f.write(t, "from/folder/a", "a")
	f.write(t, "to/folder/b", "b")
	from, to := f.original(t, "from/folder/a"), f.original(t, "to/folder/b")
	stream := f.execute(t, &entity.FileOperationSpec{Kind: entity.FileOperationKind_MOVE,
		Sources: []*entity.FileOperationRef{f.ref(t, "from/folder")}, Destination: f.ref(t, "to")}, nil)
	require.Zero(t, stream.summary().Failed)
	for _, test := range []struct {
		id   int64
		path string
	}{{from.ID, "to/folder/a"}, {to.ID, "to/folder/b"}} {
		original, err := f.exe.Lib().GetFileLocation(context.Background(), test.id)
		require.NoError(t, err)
		require.Equal(t, test.path, original.Path)
	}
}

func TestOrganizationCopyIsRejected(t *testing.T) {
	f := setup(t)
	f.write(t, "source", "untouched")
	stream := &operationStream{ctx: context.Background()}
	err := (&service{exe: f.exe}).Execute(&entity.ExecuteFileOperationRequest{Spec: &entity.FileOperationSpec{
		Kind: entity.FileOperationKind_COPY, Sources: []*entity.FileOperationRef{f.ref(t, "source")}, Destination: f.ref(t, ""), Name: "copy"}}, stream)
	require.ErrorContains(t, err, "unsupported")
	require.Empty(t, stream.updates)
	require.NoFileExists(t, filepath.Join(f.root, "copy"))
}

func TestLibraryRollbackNeverReturnsCreatedFileIdentities(t *testing.T) {
	// A later primitive failure rolls back the entire logical root, including created ancestors.
	f := organizationFixture{f: setup(t), logical: true}
	source := f.file(t, "source.txt")
	require.NoError(t, f.f.libDB.Callback().Create().Before("gorm:create").Register("reject-deep-directory", func(tx *gorm.DB) {
		file, ok := tx.Statement.Dest.(*library.File)
		if ok && file.Name == "deep" {
			tx.AddError(errors.New("injected directory failure"))
		}
	}))
	stream := f.f.execute(t, &entity.FileOperationSpec{Kind: entity.FileOperationKind_MOVE,
		Sources: []*entity.FileOperationRef{source}, Destination: libraryRef(0), Name: "new/deep/renamed.txt"}, nil)
	require.Positive(t, stream.summary().Failed)
	f.exists(t, "source.txt", true)
	f.exists(t, "new", false)
	for _, update := range stream.updates {
		if update.Entry != nil {
			require.Nil(t, update.Entry.FileId)
		}
	}
}

func TestPhysicalMergeStreamsEachPrimitiveBeforeContinuing(t *testing.T) {
	// A disconnect after the first result stops the rest of the same selected tree.
	f := setup(t)
	f.write(t, "from/folder/a", "a")
	f.write(t, "from/folder/b", "b")
	f.write(t, "to/folder/c", "c")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var moved string
	stream := &operationStream{ctx: ctx, onSend: func(update *entity.FileOperationUpdate) error {
		if update.Entry != nil {
			moved = filepath.Base(update.Entry.SourcePath)
			cancel()
		}
		return nil
	}}
	err := (&service{exe: f.exe}).Execute(&entity.ExecuteFileOperationRequest{Spec: &entity.FileOperationSpec{Kind: entity.FileOperationKind_MOVE, Sources: []*entity.FileOperationRef{f.ref(t, "from/folder")}, Destination: f.ref(t, "to")}}, stream)
	require.ErrorIs(t, err, context.Canceled)
	require.Contains(t, []string{"a", "b"}, moved)
	other := "a"
	if moved == "a" {
		other = "b"
	}
	require.FileExists(t, filepath.Join(f.root, "to/folder", moved))
	require.FileExists(t, filepath.Join(f.root, "from/folder", other))
	require.NoFileExists(t, filepath.Join(f.root, "to/folder", other))
}
