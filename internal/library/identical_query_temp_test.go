package library

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/internal/resource"
	"github.com/stretchr/testify/require"
)

func TestPrepareTemporaryRetainsLiveIdenticalResults(t *testing.T) {
	// Re-preparation in this process cannot remove a result still held by a reader.
	db, lib := newTestLibrary(t)
	file := &File{Name: "one", Kind: entity.FileKind_FILE_KIND_REGULAR}
	createFileRows(t, db, file)
	require.NoError(t, db.Create(&FileVersion{FileID: file.ID, Signature: []byte("signature")}).Error)
	snapshot, err := lib.OpenIdenticalSnapshot(context.Background(), IdenticalScope{Source: IdenticalLibrary})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, snapshot.Close()) })

	// Even a sweep beyond the retention period must preserve the process-local hold.
	require.NoError(t, resource.CleanupTemporary(lib.work, time.Now().Add(8*24*time.Hour)))
	require.DirExists(t, snapshot.directory)
	require.NoError(t, lib.PrepareTemporary(lib.work))
	require.DirExists(t, snapshot.directory)
}

func TestPrepareTemporaryRejectsLinkedRoot(t *testing.T) {
	// A linked temporary root cannot redirect cleanup outside Work.
	work := t.TempDir()
	outside := t.TempDir()
	require.NoError(t, os.Symlink(outside, resource.TemporaryRoot(work)))
	require.Error(t, (&Library{}).PrepareTemporary(work))
}

func TestIdenticalSnapshotsUsePreparedTemp(t *testing.T) {
	// Both complete Find and targeted validation use the same Work temporary root.
	db, lib := newTestLibrary(t)
	file := &File{Name: "one", Kind: entity.FileKind_FILE_KIND_REGULAR}
	createFileRows(t, db, file)
	require.NoError(t, db.Create(&FileVersion{FileID: file.ID, Signature: []byte("signature")}).Error)
	scope := IdenticalScope{Source: IdenticalLibrary}
	for _, open := range []func() (*IdenticalSnapshot, error){
		func() (*IdenticalSnapshot, error) { return lib.OpenIdenticalSnapshot(context.Background(), scope) },
		func() (*IdenticalSnapshot, error) {
			return lib.OpenIdenticalComponent(context.Background(), scope, file.ID)
		},
	} {
		snapshot, err := open()
		require.NoError(t, err)
		require.Equal(t, resource.TemporaryRoot(lib.work), filepath.Dir(snapshot.directory))
		require.DirExists(t, snapshot.directory)
		require.NoError(t, snapshot.Close())
		require.NoDirExists(t, snapshot.directory)
	}
}
