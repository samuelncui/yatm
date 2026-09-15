package executor

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/samuelncui/yatm/entity"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestRecoveryPreservesDamagedCommittedBundle(t *testing.T) {
	// A committed common record proves initialization once completed in the same database transaction.
	for _, test := range []struct {
		name      string
		status    entity.JobStatus
		kind      entity.JobKind
		damageSQL string
		remove    []string
		linkState bool
	}{
		{name: "completed missing manifest", status: entity.JobStatus_COMPLETED, damageSQL: "DROP TABLE items"},
		{name: "pending missing manifest", status: entity.JobStatus_PENDING, damageSQL: "DROP TABLE items"},
		{name: "indexing missing manifest", status: entity.JobStatus_INDEXING, damageSQL: "DROP TABLE items"},
		{name: "indexing missing config", status: entity.JobStatus_INDEXING, damageSQL: "DROP TABLE config"},
		{name: "indexing empty config", status: entity.JobStatus_INDEXING, damageSQL: "DELETE FROM config"},
		{name: "pending empty manifest", status: entity.JobStatus_PENDING, damageSQL: "DELETE FROM items"},
		{name: "sync completed missing config", status: entity.JobStatus_COMPLETED, kind: entity.JobKind_SCAN, damageSQL: "DROP TABLE config"},
		{name: "completed missing metadata", status: entity.JobStatus_COMPLETED, remove: []string{"job.json"}},
		{name: "completed missing state", status: entity.JobStatus_COMPLETED, remove: []string{"state.db"}},
		{name: "completed only evidence", status: entity.JobStatus_COMPLETED, remove: []string{"job.json", "state.db"}},
		{name: "completed state symlink", status: entity.JobStatus_COMPLETED, remove: []string{"state.db"}, linkState: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx := context.Background()
			exe := setupTestExecutor(t)
			job := &Job{ExecutorID: localExecutorID}
			require.NoError(t, exe.db.Create(job).Error)
			kind := test.kind
			if kind == entity.JobKind_JOB_KIND_UNSPECIFIED {
				kind = entity.JobKind_ARCHIVE
			}
			require.NoError(t, exe.createBundle(ctx, job, &JobRecord{ID: singletonJobID, Kind: kind, Status: test.status}, func(db *gorm.DB) error {
				for _, statement := range []string{
					"CREATE TABLE items (id INTEGER PRIMARY KEY)", "INSERT INTO items (id) VALUES (1)",
					"CREATE TABLE config (id INTEGER PRIMARY KEY)", "INSERT INTO config (id) VALUES (1)",
				} {
					if err := db.Exec(statement).Error; err != nil {
						return err
					}
				}
				return nil
			}))
			evidence := filepath.Join(exe.jobWorkPath(job.ID), "archive-report.txt")
			require.NoError(t, os.WriteFile(evidence, []byte("retained recovery evidence"), 0644))
			if test.damageSQL != "" {
				db, closeDB, err := exe.openStateDB(job.ID)
				require.NoError(t, err)
				require.NoError(t, db.Exec(test.damageSQL).Error)
				closeDB()
			}
			for _, name := range test.remove {
				require.NoError(t, os.Remove(filepath.Join(exe.jobWorkPath(job.ID), name)))
			}
			if test.linkState {
				require.NoError(t, os.Symlink(evidence, exe.stateDBPath(job.ID)))
			}

			// Damage must report an error and preserve every surviving file and catalog entry.
			require.Error(t, exe.ReconcileStorage(ctx))
			require.FileExists(t, evidence)
			var stored Job
			require.NoError(t, exe.db.First(&stored, job.ID).Error)
		})
	}
}

func TestRecoveryPreservesOrphanEvidenceWithoutBundleFiles(t *testing.T) {
	exe := setupTestExecutor(t)
	dir := exe.jobWorkPath(918)
	require.NoError(t, os.MkdirAll(dir, 0755))
	evidence := filepath.Join(dir, "restore-report.txt")
	require.NoError(t, os.WriteFile(evidence, []byte("retained recovery evidence"), 0644))

	require.ErrorIs(t, exe.ReconcileStorage(context.Background()), ErrOrphanBundle)
	require.FileExists(t, evidence)
}

func TestRecoveryRejectsBundleDirectorySymlinks(t *testing.T) {
	exe := setupTestExecutor(t)
	job := &Job{ExecutorID: localExecutorID}
	require.NoError(t, exe.db.Create(job).Error)
	require.NoError(t, os.MkdirAll(filepath.Dir(exe.jobWorkPath(job.ID)), 0755))
	require.NoError(t, os.Symlink(t.TempDir(), exe.jobWorkPath(job.ID)))

	require.ErrorContains(t, exe.ReconcileStorage(context.Background()), "not a directory")
	info, err := os.Lstat(exe.jobWorkPath(job.ID))
	require.NoError(t, err)
	require.NotZero(t, info.Mode()&os.ModeSymlink)
}

func TestRecoveryPreservesCatalogWhenWholeBundleIsMissing(t *testing.T) {
	exe := setupTestExecutor(t)
	job := &Job{ExecutorID: localExecutorID}
	require.NoError(t, exe.db.Create(job).Error)

	// Without the bundle, startup cannot tell whether creation failed or committed evidence was lost.
	require.ErrorIs(t, exe.ReconcileStorage(context.Background()), ErrBundleMissing)
	var stored Job
	require.NoError(t, exe.db.First(&stored, job.ID).Error)
}
