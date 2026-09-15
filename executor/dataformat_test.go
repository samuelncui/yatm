package executor

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/internal/dataformat"
	"github.com/samuelncui/yatm/library"
	"github.com/samuelncui/yatm/resource"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestExecutorRejectsUnsupportedBundleBeforeWrites(t *testing.T) {
	for _, kind := range []entity.JobKind{entity.JobKind_ARCHIVE, entity.JobKind_RESTORE, entity.JobKind_SCAN} {
		t.Run(kind.String(), func(t *testing.T) {
			// Start with an ordinary completed bundle, then replace only its family marker with Draft metadata.
			ctx := context.Background()
			catalogPath := filepath.Join(t.TempDir(), "catalog.db")
			db, err := resource.OpenSQLite(catalogPath)
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, closeGORMDB(db)) })
			exe := New(db, &library.Library{}, nil, Paths{Work: t.TempDir()}, Scripts{}, nil)
			require.NoError(t, exe.AutoMigrate())
			job := &Job{ExecutorID: localExecutorID}
			require.NoError(t, db.Create(job).Error)
			require.NoError(t, exe.createBundle(ctx, job, &JobRecord{ID: 1, Kind: kind, Status: entity.JobStatus_COMPLETED}, func(db *gorm.DB) error {
				return db.Exec("CREATE TABLE retained_evidence (id INTEGER PRIMARY KEY)").Error
			}))
			metadataPath := filepath.Join(exe.jobWorkPath(job.ID), "job.json")
			published, err := os.ReadFile(metadataPath)
			require.NoError(t, err)
			var metadata dataformat.Bundle
			require.NoError(t, json.Unmarshal(published, &metadata))
			require.Equal(t, dataformat.BundleFormat, metadata.Format)
			require.Equal(t, 1, metadata.FormatVersion)
			draft := []byte(`{"format_version":2,"id":1}`)
			require.NoError(t, os.WriteFile(metadataPath, draft, 0o644))
			catalogBefore, err := os.ReadFile(catalogPath)
			require.NoError(t, err)
			stateBefore, err := os.ReadFile(exe.stateDBPath(job.ID))
			require.NoError(t, err)

			// All runtime entry points reject before typed migrations, projection repair, logs or cleanup.
			_, err = exe.NewStateDB(ctx, job.ID)
			require.ErrorContains(t, err, "unsupported Job bundle format")
			require.ErrorContains(t, exe.AutoMigrate(), "unsupported Job bundle format")
			require.ErrorContains(t, exe.ReconcileStorage(ctx), "unsupported Job bundle format")
			_, err = exe.GetJobRunner(ctx, job.ID)
			require.ErrorContains(t, err, "unsupported Job bundle format")
			catalogAfter, err := os.ReadFile(catalogPath)
			require.NoError(t, err)
			require.Equal(t, catalogBefore, catalogAfter)
			stateAfter, err := os.ReadFile(exe.stateDBPath(job.ID))
			require.NoError(t, err)
			require.Equal(t, stateBefore, stateAfter)
			metadataAfter, err := os.ReadFile(metadataPath)
			require.NoError(t, err)
			require.Equal(t, draft, metadataAfter)
			require.NoFileExists(t, filepath.Join(exe.jobWorkPath(job.ID), "job.log"))
		})
	}
}

func TestStartupRepairsCheckpointWithoutDraftRevisionBackfill(t *testing.T) {
	// Current projections need no guessed revision migration, even when a fixture has revision zero.
	ctx := context.Background()
	exe := setupTestExecutor(t)
	job := &Job{ExecutorID: localExecutorID, CatalogKind: entity.JobKind_ARCHIVE, CatalogStatus: entity.JobStatus_COMPLETED}
	require.NoError(t, exe.db.Create(job).Error)
	require.NoError(t, exe.createBundle(ctx, job, &JobRecord{ID: 1, Kind: entity.JobKind_ARCHIVE, Status: entity.JobStatus_COMPLETED}, func(*gorm.DB) error { return nil }))
	require.NoError(t, exe.AutoMigrate())
	var stored Job
	require.NoError(t, exe.db.First(&stored, job.ID).Error)
	require.Zero(t, stored.Revision)

	// A real unprojected checkpoint must still publish normally after the Draft-only loop is removed.
	state, closeState, err := exe.openStateDB(job.ID)
	require.NoError(t, err)
	require.NoError(t, state.Model(&JobRecord{}).Where("id = 1").Update("status", entity.JobStatus_PENDING).Error)
	closeState()
	require.NoError(t, exe.AutoMigrate())
	require.NoError(t, exe.db.First(&stored, job.ID).Error)
	require.Equal(t, entity.JobStatus_PENDING, stored.CatalogStatus)
	require.Positive(t, stored.Revision)
}
