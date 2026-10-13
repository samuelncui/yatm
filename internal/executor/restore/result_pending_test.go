package restore

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/internal/executor"
	"github.com/samuelncui/yatm/internal/library"
	"github.com/samuelncui/yatm/internal/resource"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestRestoreCheckpointUsesBoundedPendingExistence(t *testing.T) {
	for _, status := range []entity.JobStatus{entity.JobStatus_JOB_STATUS_READY, entity.JobStatus_JOB_STATUS_PREPARING} {
		t.Run(status.String(), func(t *testing.T) {
			// Use the normal Go SQLite driver and Job schema with equivalent copies and another pending item.
			ctx := context.Background()
			db, err := resource.OpenSQLite(filepath.Join(t.TempDir(), "state.db"))
			require.NoError(t, err)
			connection, err := db.DB()
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, connection.Close()) })
			require.NoError(t, db.AutoMigrate(&executor.JobRecord{}, &File{}, &Copy{}))
			require.NoError(t, db.Create(&executor.JobRecord{ID: 1, Status: status, Checkpoint: status}).Error)
			require.NoError(t, db.Create(&[]Copy{
				{ItemID: 1, MediaID: 1, Status: entity.CopyStatus_COPY_STATUS_PENDING},
				{ItemID: 1, MediaID: 2, Status: entity.CopyStatus_COPY_STATUS_PENDING},
				{ItemID: 2, MediaID: 1, Status: entity.CopyStatus_COPY_STATUS_PENDING},
			}).Error)
			runner := &jobRestoreRunner{db: db}

			// Retain executed queries to distinguish a one-entry lookup from a full aggregate count.
			var queries []struct {
				sql  string
				args []any
			}
			const callback = "test:restore-pending-plan"
			require.NoError(t, db.Callback().Query().After("gorm:query").Register(callback, func(tx *gorm.DB) {
				if tx.Statement.Table == "copies" {
					queries = append(queries, struct {
						sql  string
						args []any
					}{
						tx.Statement.SQL.String(), append([]any(nil), tx.Statement.Vars...),
					})
				}
			}))

			// Completing an item satisfies every replica, but another pending item prevents completion.
			require.NoError(t, runner.checkpointResult(ctx, &copyCandidate{ItemID: 1, TargetPath: "one"}, &library.RestoreResult{}))
			require.NoError(t, db.Callback().Query().Remove(callback))
			var copies []Copy
			require.NoError(t, db.Order("id").Find(&copies).Error)
			require.Equal(t, entity.CopyStatus_COPY_STATUS_COMPLETED, copies[0].Status)
			require.Equal(t, entity.CopyStatus_COPY_STATUS_COMPLETED, copies[1].Status)
			require.Equal(t, entity.CopyStatus_COPY_STATUS_PENDING, copies[2].Status)
			var job executor.JobRecord
			require.NoError(t, db.First(&job, 1).Error)
			require.Equal(t, status, job.Status)
			require.Equal(t, status, job.Checkpoint)

			// Only READY can advance when the last pending item becomes satisfied; indexing retains its state.
			require.NoError(t, runner.checkpointResult(ctx, &copyCandidate{ItemID: 2, TargetPath: "two"}, &library.RestoreResult{}))
			require.NoError(t, db.First(&job, 1).Error)
			want := status
			if status == entity.JobStatus_JOB_STATUS_READY {
				want = entity.JobStatus_JOB_STATUS_COMPLETED
			}
			require.Equal(t, want, job.Status)
			require.Equal(t, want, job.Checkpoint)

			// The driver must execute a bounded covering-index lookup, independently of wall-clock noise.
			require.Len(t, queries, 1)
			query := queries[0]
			require.NotContains(t, strings.ToLower(query.sql), "count(")
			require.Contains(t, strings.ToLower(query.sql), "limit 1")
			var plan []struct{ Detail string }
			require.NoError(t, db.Raw("EXPLAIN QUERY PLAN "+query.sql, query.args...).Scan(&plan).Error)
			require.Contains(t, fmt.Sprint(plan), "COVERING INDEX idx_copies_status")
		})
	}
}

func TestRestorePendingObservationFailureRollsBackCheckpoint(t *testing.T) {
	// Begin with a staged output and two pending replicas in an isolated normal Job database.
	ctx := context.Background()
	db, err := resource.OpenSQLite(filepath.Join(t.TempDir(), "state.db"))
	require.NoError(t, err)
	connection, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, connection.Close()) })
	require.NoError(t, db.AutoMigrate(&executor.JobRecord{}, &File{}, &Copy{}))
	require.NoError(t, db.Create(&executor.JobRecord{
		ID: 1, Status: entity.JobStatus_JOB_STATUS_READY, Checkpoint: entity.JobStatus_JOB_STATUS_READY,
	}).Error)
	require.NoError(t, db.Create(&File{ItemID: 1, Path: "one", Ready: true, Finalized: true}).Error)
	require.NoError(t, db.Create(&[]Copy{
		{ItemID: 1, MediaID: 1, Status: entity.CopyStatus_COPY_STATUS_PENDING},
		{ItemID: 1, MediaID: 2, Status: entity.CopyStatus_COPY_STATUS_PENDING},
	}).Error)
	runner := &jobRestoreRunner{db: db}

	// An observation failure after result writes must abort the whole per-item transaction.
	failure := errors.New("injected pending observation failure")
	const callback = "test:restore-pending-failure"
	require.NoError(t, db.Callback().Query().Before("gorm:query").Register(callback, func(tx *gorm.DB) {
		if tx.Statement.Table == "copies" {
			tx.AddError(failure)
		}
	}))
	err = runner.checkpointResult(ctx, &copyCandidate{ItemID: 1, TargetPath: "one"}, &library.RestoreResult{
		ResultFileID: 99, ActualHash: []byte("observed"), ActualSize: 8,
	})
	require.ErrorIs(t, err, failure)
	require.NoError(t, db.Callback().Query().Remove(callback))

	// Neither completed output facts, replica statuses, nor the Job checkpoint may leak from rollback.
	var output File
	require.NoError(t, db.First(&output, 1).Error)
	require.True(t, output.Ready)
	require.True(t, output.Finalized)
	require.False(t, output.Completed)
	require.Zero(t, output.ResultFileID)
	require.Empty(t, output.ActualHash)
	require.Zero(t, output.ActualSize)
	var copies []Copy
	require.NoError(t, db.Find(&copies).Error)
	require.Len(t, copies, 2)
	for _, copy := range copies {
		require.Equal(t, entity.CopyStatus_COPY_STATUS_PENDING, copy.Status)
	}
	var job executor.JobRecord
	require.NoError(t, db.First(&job, 1).Error)
	require.Equal(t, entity.JobStatus_JOB_STATUS_READY, job.Status)
	require.Equal(t, entity.JobStatus_JOB_STATUS_READY, job.Checkpoint)
}
