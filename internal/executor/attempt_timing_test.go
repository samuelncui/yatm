package executor

import (
	"bytes"
	"context"
	"testing"
	"time"

	"github.com/samuelncui/yatm/entity"
	"github.com/stretchr/testify/require"
)

func attemptProgress(t *testing.T, exe *Executor, jobID int64) *entity.Progress {
	t.Helper()
	progress := &entity.Progress{}
	require.NoError(t, exe.ApplyAttemptProgress(context.Background(), jobID, progress))
	return progress
}

func TestAttemptElapsedLifecycle(t *testing.T) {
	// Indexing is a complete timed attempt whose duration does not grow while waiting for Media.
	exe := setupTestExecutor(t)
	job := waitJobStatus(t, exe, createTestJob(t, exe, entity.JobKind_JOB_KIND_ARCHIVE, 0).ID, entity.JobStatus_JOB_STATUS_READY)
	indexed := attemptProgress(t, exe, job.ID)
	require.NotNil(t, indexed.ElapsedMs)
	time.Sleep(10 * time.Millisecond)
	require.Equal(t, indexed.ElapsedMs, attemptProgress(t, exe, job.ID).ElapsedMs)

	// A real asynchronous execution receives a fresh clock and advances without progress writes.
	started := make(chan struct{})
	require.NoError(t, exe.StartJob(context.Background(), job.ID, entity.JobKind_JOB_KIND_ARCHIVE, func(ctx context.Context, _ Runner) error {
		close(started)
		<-ctx.Done()
		return ctx.Err()
	}))
	<-started
	first := attemptProgress(t, exe, job.ID)
	require.NotNil(t, first.ElapsedMs)
	revision := exe.currentJobRevision()
	time.Sleep(20 * time.Millisecond)
	live := attemptProgress(t, exe, job.ID)
	require.Greater(t, live.GetElapsedMs(), first.GetElapsedMs())
	require.Equal(t, revision, exe.currentJobRevision())

	// Cancellation freezes elapsed time after runner cleanup, and a restarted Executor reads that value.
	require.NoError(t, exe.Cancel(job.ID))
	waitJobStatus(t, exe, job.ID, entity.JobStatus_JOB_STATUS_READY)
	cancelled := attemptProgress(t, exe, job.ID)
	require.GreaterOrEqual(t, cancelled.GetElapsedMs(), live.GetElapsedMs())
	reopened := New(exe.db, exe.lib, nil, exe.paths, Scripts{}, nil)
	time.Sleep(10 * time.Millisecond)
	require.Equal(t, cancelled.ElapsedMs, attemptProgress(t, reopened, job.ID).ElapsedMs)

	// Another explicit Media attempt replaces an old long duration, then successful completion remains frozen after reopening.
	db, closeDB, err := exe.openStateDB(job.ID)
	require.NoError(t, err)
	defer closeDB()
	require.NoError(t, db.Model(&JobRecord{}).Where("id = ?", singletonJobID).Updates(map[string]any{
		"latest_attempt_started_at_ns": 1, "latest_attempt_finished_at_ns": int64(time.Hour) + 1,
	}).Error)
	require.NoError(t, exe.StartJob(context.Background(), job.ID, entity.JobKind_JOB_KIND_ARCHIVE, func(ctx context.Context, _ Runner) error {
		return exe.UpdateJobStatus(ctx, job.ID, entity.JobStatus_JOB_STATUS_READY, entity.JobStatus_JOB_STATUS_COMPLETED)
	}))
	waitJobStatus(t, exe, job.ID, entity.JobStatus_JOB_STATUS_COMPLETED)
	completed := attemptProgress(t, exe, job.ID)
	require.NotNil(t, completed.ElapsedMs)
	require.Less(t, completed.GetElapsedMs(), int64(3600000))
	time.Sleep(10 * time.Millisecond)
	require.Equal(t, completed.ElapsedMs, attemptProgress(t, reopened, job.ID).ElapsedMs)
}

func TestAttemptElapsedInterrupted(t *testing.T) {
	// An interrupted attempt has no final duration and cannot include server downtime.
	exe := setupTestExecutor(t)
	job := waitJobStatus(t, exe, createTestJob(t, exe, entity.JobKind_JOB_KIND_ARCHIVE, 0).ID, entity.JobStatus_JOB_STATUS_READY)
	db, closeDB, err := exe.openStateDB(job.ID)
	require.NoError(t, err)
	defer closeDB()
	require.NoError(t, db.Model(&JobRecord{}).Where("id = ?", singletonJobID).Updates(map[string]any{
		"latest_attempt_started_at_ns":  time.Now().Add(-time.Hour).UnixNano(),
		"latest_attempt_finished_at_ns": nil,
	}).Error)
	reopened := New(exe.db, exe.lib, nil, exe.paths, Scripts{}, nil)
	require.Nil(t, attemptProgress(t, reopened, job.ID).ElapsedMs)
}

func TestFailedPreparationKeepsItsTiming(t *testing.T) {
	// A failed preparation is terminal: it keeps the timed attempt it ran, and creating another Job
	// is what measures a new one.
	exe := setupTestExecutor(t)
	job := createTestJob(t, exe, entity.JobKind_JOB_KIND_RESTORE, 0)
	waitJobStatus(t, exe, job.ID, entity.JobStatus_JOB_STATUS_FAILED)
	require.NotNil(t, attemptProgress(t, exe, job.ID).ElapsedMs)
	db, closeDB, err := exe.openStateDB(job.ID)
	require.NoError(t, err)
	require.NoError(t, db.Model(&JobRecord{}).Where("id = ?", singletonJobID).Updates(map[string]any{
		"latest_attempt_started_at_ns": 1, "latest_attempt_finished_at_ns": int64(time.Hour) + 1,
	}).Error)
	closeDB()
	require.EqualValues(t, 3600000, attemptProgress(t, exe, job.ID).GetElapsedMs())

	fresh := createTestJob(t, exe, entity.JobKind_JOB_KIND_RESTORE, 0)
	waitJobStatus(t, exe, fresh.ID, entity.JobStatus_JOB_STATUS_FAILED)
	progress := attemptProgress(t, exe, fresh.ID)
	require.NotNil(t, progress.ElapsedMs)
	require.Less(t, progress.GetElapsedMs(), int64(3600000))
}

func TestAttemptStartTimingFailurePreventsExecution(t *testing.T) {
	// Inject a failure specifically at the durable start boundary, after ordinary reads succeed.
	exe := setupTestExecutor(t)
	job := waitJobStatus(t, exe, createTestJob(t, exe, entity.JobKind_JOB_KIND_ARCHIVE, 0).ID, entity.JobStatus_JOB_STATUS_READY)
	previous := attemptProgress(t, exe, job.ID)
	db, closeDB, err := exe.openStateDB(job.ID)
	require.NoError(t, err)
	defer closeDB()
	require.NoError(t, db.Exec(`CREATE TRIGGER fail_attempt_start BEFORE UPDATE OF latest_attempt_started_at_ns ON job
		BEGIN SELECT RAISE(ABORT, 'injected timing failure'); END`).Error)

	// No asynchronous work or lingering admission survives a failed start record.
	run := make(chan struct{}, 1)
	err = exe.StartJob(context.Background(), job.ID, entity.JobKind_JOB_KIND_ARCHIVE, func(context.Context, Runner) error {
		run <- struct{}{}
		return nil
	})
	require.ErrorContains(t, err, "injected timing failure")
	require.False(t, exe.IsRunning(job.ID))
	require.Empty(t, run)
	require.Equal(t, previous.ElapsedMs, attemptProgress(t, exe, job.ID).ElapsedMs)
}

func TestAttemptFinishTimingFailureIsLoggedAndUnknown(t *testing.T) {
	// Fail only the final timing checkpoint and capture the shared Job logger.
	exe := setupTestExecutor(t)
	job := waitJobStatus(t, exe, createTestJob(t, exe, entity.JobKind_JOB_KIND_ARCHIVE, 0).ID, entity.JobStatus_JOB_STATUS_READY)
	db, closeDB, err := exe.openStateDB(job.ID)
	require.NoError(t, err)
	defer closeDB()
	require.NoError(t, db.Exec(`CREATE TRIGGER fail_attempt_finish BEFORE UPDATE OF latest_attempt_finished_at_ns ON job
		WHEN NEW.latest_attempt_finished_at_ns IS NOT NULL
		BEGIN SELECT RAISE(ABORT, 'injected finish failure'); END`).Error)
	runner, err := exe.GetJobRunner(context.Background(), job.ID)
	require.NoError(t, err)
	var logs bytes.Buffer
	runner.Logger().SetOutput(&logs)

	// Cleanup still releases ownership, but a missing durable endpoint remains unknown.
	require.NoError(t, exe.StartJob(context.Background(), job.ID, entity.JobKind_JOB_KIND_ARCHIVE, func(context.Context, Runner) error {
		return nil
	}))
	waitJobStatus(t, exe, job.ID, entity.JobStatus_JOB_STATUS_READY)
	require.Nil(t, attemptProgress(t, exe, job.ID).ElapsedMs)
	require.Contains(t, logs.String(), "finish Job attempt timing failed")
	require.Contains(t, logs.String(), "injected finish failure")
}

func TestIdleProgressHasNoPhaseAndKeepsManifestReadiness(t *testing.T) {
	// Cached and reopened prepared Jobs expose the complete manifest without inventing a live queue.
	exe := setupTestExecutor(t)
	job := waitJobStatus(t, exe, createTestJob(t, exe, entity.JobKind_JOB_KIND_ARCHIVE, 0).ID, entity.JobStatus_JOB_STATUS_READY)
	for _, current := range []*Executor{exe, New(exe.db, exe.lib, nil, exe.paths, Scripts{}, nil)} {
		progress := &entity.Progress{Stage: &entity.StageProgress{Phase: entity.JobPhase_JOB_PHASE_QUEUED,
			Unit: entity.ProgressUnit_PROGRESS_UNIT_ITEMS, Completed: 2, Total: new(int64),
			RatePerSecond: new(float64), RemainingSeconds: new(int64)}}
		require.NoError(t, current.ApplyAttemptProgress(context.Background(), job.ID, progress))
		require.True(t, progress.TotalKnown)
		require.Equal(t, entity.JobPhase_JOB_PHASE_UNSPECIFIED, progress.Stage.Phase)
		require.EqualValues(t, 2, progress.Stage.Completed)
		require.Nil(t, progress.Stage.Total)
		require.Nil(t, progress.Stage.RatePerSecond)
		require.Nil(t, progress.Stage.RemainingSeconds)
		require.Equal(t, entity.EstimateState_ESTIMATE_STATE_NOT_APPLICABLE, progress.Stage.EstimateState)
		stored, err := current.GetJob(context.Background(), job.ID)
		require.NoError(t, err)
		require.Equal(t, entity.JobPhase_JOB_PHASE_UNSPECIFIED, stored.Phase)
	}

	// A failed preparation may retain partial rows; those are never a complete executable manifest.
	failed := waitJobStatus(t, exe, createTestJob(t, exe, entity.JobKind_JOB_KIND_RESTORE, 0).ID, entity.JobStatus_JOB_STATUS_FAILED)
	progress := &entity.Progress{TotalKnown: true}
	require.NoError(t, exe.ApplyAttemptProgress(context.Background(), failed.ID, progress))
	require.False(t, progress.TotalKnown)
}

func TestQueuedAttemptPublishesItsFinalPhase(t *testing.T) {
	exe := setupTestExecutor(t)
	job := waitJobStatus(t, exe, createTestJob(t, exe, entity.JobKind_JOB_KIND_ARCHIVE, 0).ID, entity.JobStatus_JOB_STATUS_READY)
	started := make(chan struct{})
	require.NoError(t, exe.StartJob(context.Background(), job.ID, job.Kind, func(ctx context.Context, _ Runner) error {
		close(started)
		<-ctx.Done()
		return ctx.Err()
	}))
	<-started
	queued, err := exe.GetJob(context.Background(), job.ID)
	require.NoError(t, err)
	require.Equal(t, entity.JobPhase_JOB_PHASE_QUEUED, queued.Phase)
	progress := &entity.Progress{Stage: &entity.StageProgress{Phase: queued.Phase}}
	require.NoError(t, exe.ApplyAttemptProgress(context.Background(), job.ID, progress))
	require.Equal(t, entity.JobPhase_JOB_PHASE_QUEUED, progress.Stage.Phase)
	require.NoError(t, exe.Cancel(job.ID))
	waitJobStatus(t, exe, job.ID, entity.JobStatus_JOB_STATUS_READY)
	// List changefeed must deliver the disappeared phase rather than leaving a queued card behind.
	require.Eventually(t, func() bool {
		page, err := exe.ListJob(context.Background(), &entity.JobFilter{ChangedAfterRevision: &queued.Revision})
		return err == nil && len(page.Jobs) == 1 && page.Jobs[0].Phase == entity.JobPhase_JOB_PHASE_UNSPECIFIED
	}, time.Second, time.Millisecond)
}
