package library

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/samuelncui/yatm/entity"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestIdenticalConstructionFailureCleansStaging(t *testing.T) {
	for _, component := range []bool{false, true} {
		name := "/full"
		if component {
			name = "/component"
		}
		for _, stage := range []string{"canceled", "catalog failure", "cleanup failure"} {
			t.Run(stage+name, func(t *testing.T) {
				// Both builders own their private directory until construction succeeds.
				db, lib := newTestLibrary(t)
				sqlDB, err := db.DB()
				require.NoError(t, err)
				t.Cleanup(func() { require.NoError(t, sqlDB.Close()) })
				lib.identicalTempRoot = t.TempDir()
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				failure := errors.New("catalog unavailable")
				if stage == "canceled" {
					cancel()
				} else {
					require.NoError(t, db.Callback().Query().Before("gorm:query").Register("audit:query", func(tx *gorm.DB) {
						// A filesystem denial during cleanup must not hide the catalog failure.
						if stage == "cleanup failure" {
							if os.Geteuid() == 0 {
								t.Skip("permission fixture requires an unprivileged user")
							}
							entries, err := os.ReadDir(lib.identicalTempRoot)
							require.NoError(t, err)
							require.Len(t, entries, 1)
							directory := filepath.Join(lib.identicalTempRoot, entries[0].Name())
							t.Cleanup(func() {
								require.NoError(t, os.Chmod(directory, 0700))
								require.NoError(t, os.RemoveAll(directory))
							})
							require.NoError(t, os.Chmod(directory, 0500))
						}
						tx.AddError(failure)
					}))
				}

				// Failed migration or catalog collection closes SQLite before attempting removal.
				scope := IdenticalScope{Source: IdenticalLibrary}
				var snapshot *IdenticalSnapshot
				if component {
					snapshot, err = lib.OpenIdenticalComponent(ctx, scope, 1)
				} else {
					snapshot, err = lib.OpenIdenticalSnapshot(ctx, scope)
				}
				require.Nil(t, snapshot)
				if stage == "canceled" {
					require.ErrorIs(t, err, context.Canceled)
				} else {
					require.ErrorIs(t, err, failure)
				}
				if stage == "cleanup failure" {
					require.ErrorIs(t, err, os.ErrPermission)
					return
				}
				entries, err := os.ReadDir(lib.identicalTempRoot)
				require.NoError(t, err)
				require.Empty(t, entries)
			})
		}
	}
}

func TestIdenticalResultsOutliveConstructionContext(t *testing.T) {
	// Use real shared signatures so lifetime checks exercise retained component reads.
	db, lib := newTestLibrary(t)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, sqlDB.Close()) })
	lib.identicalTempRoot = t.TempDir()
	for _, name := range []string{"one", "two"} {
		file := &File{Name: name, Kind: entity.FileKind_FILE_KIND_REGULAR}
		createFileRows(t, db, file)
		require.NoError(t, db.Create(&FileVersion{FileID: file.ID, Signature: []byte("shared")}).Error)
	}
	scope := IdenticalScope{Source: IdenticalLibrary}
	for _, open := range []func(context.Context) (*IdenticalSnapshot, error){
		func(ctx context.Context) (*IdenticalSnapshot, error) { return lib.OpenIdenticalSnapshot(ctx, scope) },
		func(ctx context.Context) (*IdenticalSnapshot, error) {
			return lib.OpenIdenticalComponent(ctx, scope, 1)
		},
	} {
		// Returning transfers ownership to the caller, independently of the original request.
		ctx, cancel := context.WithCancel(context.Background())
		snapshot, err := open(ctx)
		cancel()
		require.NoError(t, err)
		t.Cleanup(func() { require.NoError(t, snapshot.Close()) })
		groups, err := snapshot.Groups("", 10)
		require.NoError(t, err)
		require.Len(t, groups.Groups, 1)
		require.EqualValues(t, 2, groups.Groups[0].Count)

		// Explicit release closes the pool and removes only this retained result's directory.
		pool, err := snapshot.db.DB()
		require.NoError(t, err)
		require.NoError(t, snapshot.Close())
		require.Zero(t, pool.Stats().OpenConnections)
		require.NoDirExists(t, snapshot.directory)
	}
}
