package library

import (
	"bytes"
	"context"
	"testing"

	"github.com/samuelncui/yatm/entity"
	"github.com/stretchr/testify/require"
)

func TestLibraryPathsPreserveLiteralUTF8(t *testing.T) {
	// Distinct authored names must retain their identities through creation, lookup and rename.
	ctx := context.Background()
	lib := newJSONLTestLibrary(t)
	names := []string{`back\slash`, `literal\n`, "line\nbreak", " leading", "trailing ", " \t\n", `quotes'"`, "100%?#", "照片 😀"}
	for _, name := range names {
		t.Run(name, func(t *testing.T) {
			// Lookup must resolve the exact components that MkdirAll persisted.
			directory, err := lib.MkdirAll(ctx, 0, name, 0o755)
			require.NoError(t, err)
			require.Equal(t, name, directory.Name)
			child, err := lib.MkdirAll(ctx, directory.ID, " \t ", 0o755)
			require.NoError(t, err)
			found, err := lib.GetByPath(ctx, 0, name+"/ \t ")
			require.NoError(t, err)
			require.NotNil(t, found)
			require.Equal(t, child.ID, found.ID)

			// A replacement name is literal too; it never denotes a backslash-separated path.
			file := &File{ParentID: directory.ID, Name: "before", Kind: entity.FileKind_FILE_KIND_REGULAR}
			require.NoError(t, lib.SaveFile(ctx, file))
			require.NoError(t, lib.MoveFileToPath(ctx, file, name))
			require.Equal(t, name, file.Name)
			require.Equal(t, directory.ID, file.ParentID)
			found, err = lib.GetByPath(ctx, directory.ID, name)
			require.NoError(t, err)
			require.NotNil(t, found)
			require.Equal(t, file.ID, found.ID)
		})
	}

	// A listing retains all literal names exactly once, including whitespace-only entries.
	rows, err := lib.List(ctx, 0)
	require.NoError(t, err)
	actual := make([]string, 0, len(rows))
	for _, row := range rows {
		actual = append(actual, row.Name)
	}
	require.ElementsMatch(t, names, actual)

	// Metadata backups round-trip these same identities without display escaping becoming stored text.
	var backup bytes.Buffer
	require.NoError(t, lib.Export(ctx, &backup, []entity.LibraryEntityType{entity.LibraryEntityType_LIBRARY_ENTITY_TYPE_FILE}))
	restored := newJSONLTestLibrary(t)
	require.NoError(t, restored.Import(ctx, bytes.NewReader(backup.Bytes()), false))
	for _, name := range names {
		before, err := lib.GetByPath(ctx, 0, name+"/"+name)
		require.NoError(t, err)
		after, err := restored.GetByPath(ctx, 0, name+"/"+name)
		require.NoError(t, err)
		require.NotNil(t, before)
		require.NotNil(t, after)
		require.Equal(t, before.ID, after.ID)
		require.Equal(t, name, after.Name)
	}
}

func TestLibraryPathLookupKeepsRootContainment(t *testing.T) {
	// Slash shortcuts remain supported without allowing a path above the selected root.
	ctx := context.Background()
	lib := newJSONLTestLibrary(t)
	dir, err := lib.MkdirAll(ctx, 0, " literal\\dir ", 0o755)
	require.NoError(t, err)
	for _, value := range []string{"/" + dir.Name + "/", "./unused/../" + dir.Name, dir.Name} {
		found, err := lib.GetByPath(ctx, 0, value)
		require.NoError(t, err)
		require.NotNil(t, found)
		require.Equal(t, dir.ID, found.ID)
	}
	for _, value := range []string{"", ".", "./"} {
		found, err := lib.GetByPath(ctx, dir.ID, value)
		require.NoError(t, err)
		require.Equal(t, dir.ID, found.ID)
	}
	for _, value := range []string{"../escape", "a/../../escape", "invalid\xff", "nul\x00"} {
		_, err := lib.GetByPath(ctx, dir.ID, value)
		require.Error(t, err)
	}
}

func TestOriginalPathPersistenceRejectsInvalidUTF8(t *testing.T) {
	// Reject unrepresentable original paths before persistence can expose them through JSON or protobuf.
	lib := newJSONLTestLibrary(t)
	for _, value := range []string{"bad\xff", "nul\x00", "a/../b"} {
		err := lib.db.Create(&FileLocation{FileID: 1, LocationID: 1, Path: value}).Error
		require.Error(t, err)
	}
	var count int64
	require.NoError(t, lib.db.Model(&FileLocation{}).Count(&count).Error)
	require.Zero(t, count)
}

func TestSaveFileRejectsInvalidIdentity(t *testing.T) {
	// Direct logical writes cannot introduce names that the protocol would reject or rewrite.
	ctx := context.Background()
	lib := newJSONLTestLibrary(t)
	for _, name := range []string{"", ".", "..", "a/b", "invalid\xff", "nul\x00"} {
		require.Error(t, lib.SaveFile(ctx, &File{Name: name}))
	}
	rows, err := lib.List(ctx, 0)
	require.NoError(t, err)
	require.Empty(t, rows)
}

func TestNewRestoredFilePreservesLiteralUTF8(t *testing.T) {
	// A separately restored File uses the source spelling with only the established version suffix.
	lib := newJSONLTestLibrary(t)
	for _, name := range []string{`back\slash.txt`, " leading", "trailing ", " \t\n", `quotes'"`, "照片"} {
		file, err := newRestoredFile(lib.db, 0, name, 17)
		require.NoError(t, err)
		require.Equal(t, RestoredName(name, 17), file.Name)
	}
	for _, name := range []string{"invalid\xff", "nul\x00", "a/b", ".."} {
		_, err := newRestoredFile(lib.db, 0, name, 17)
		require.Error(t, err)
	}
}
