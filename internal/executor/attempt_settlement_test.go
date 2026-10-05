package executor

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/samuelncui/yatm/entity"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestFailedPreparedJobCannotRestartAfterReopening(t *testing.T) {
	// Preparation can publish READY before its final cleanup fails; the retained manifest is not admission.
	ctx := context.Background()
	exe := setupTestExecutor(t)
	job := waitJobStatus(t, exe, createTestJob(t, exe, entity.JobKind_JOB_KIND_ARCHIVE, 0).ID,
		entity.JobStatus_JOB_STATUS_READY)
	runner, err := exe.GetJobRunner(ctx, job.ID)
	require.NoError(t, err)
	exe.recordAttemptResult(ctx, job.ID, runner, errors.New("preparation cleanup failed"), entity.JobStatus_JOB_STATUS_FAILED)
	before, err := exe.GetJob(ctx, job.ID)
	require.NoError(t, err)
	require.Equal(t, entity.JobStatus_JOB_STATUS_READY, before.Checkpoint)
	require.Equal(t, entity.JobStatus_JOB_STATUS_FAILED, before.Status)
	previous := attemptProgress(t, exe, job.ID)

	// Neither the cached nor the reopened Executor runs or clears a terminal Job's reason and timing.
	for _, current := range []*Executor{exe, New(exe.db, exe.lib, nil, exe.paths, Scripts{}, nil)} {
		called := false
		err := current.StartJob(ctx, job.ID, job.Kind, func(context.Context, Runner) error {
			called = true
			return nil
		})
		require.ErrorContains(t, err, "cannot start from status JOB_STATUS_FAILED")
		require.False(t, called)
		require.False(t, current.IsRunning(job.ID))
		after, err := current.GetJob(ctx, job.ID)
		require.NoError(t, err)
		require.Equal(t, before.Status, after.Status)
		require.Equal(t, before.Error, after.Error)
		require.Equal(t, previous.ElapsedMs, attemptProgress(t, current, job.ID).ElapsedMs)
	}
}

func TestAttemptSettlementPublishesBeforeIdleObservation(t *testing.T) {
	// Pause only the catalog publication after the final attempt timing boundary was recorded.
	ctx := context.Background()
	exe := setupTestExecutor(t)
	job := waitJobStatus(t, exe, createTestJob(t, exe, entity.JobKind_JOB_KIND_ARCHIVE, 0).ID,
		entity.JobStatus_JOB_STATUS_READY)
	entered, release := make(chan struct{}), make(chan struct{})
	var released sync.Once
	t.Cleanup(func() { released.Do(func() { close(release) }) })
	callback := exe.db.Callback().Update().Before("gorm:update")
	require.NoError(t, callback.Register("test:attempt-settlement", func(tx *gorm.DB) {
		if tx.Statement.Table != "jobs" {
			return
		}
		db, closeDB, err := exe.openStateDB(job.ID)
		if err != nil {
			tx.AddError(err)
			return
		}
		defer closeDB()
		var record JobRecord
		if err := db.First(&record, singletonJobID).Error; err != nil {
			tx.AddError(err)
			return
		}
		if record.LatestAttemptFinishedAtNS != nil {
			close(entered)
			<-release
		}
	}))
	t.Cleanup(func() { _ = exe.db.Callback().Update().Remove("test:attempt-settlement") })
	require.NoError(t, exe.StartJob(ctx, job.ID, job.Kind, func(context.Context, Runner) error { return nil }))
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("settlement did not reach its final catalog publication")
	}

	// A reader cannot see idle while the changefeed publication that clears its phase is pending.
	observing, observed := make(chan struct{}), make(chan bool, 1)
	go func() { close(observing); observed <- exe.IsRunning(job.ID) }()
	<-observing
	select {
	case <-observed:
		t.Fatal("idle ownership became observable before settlement publication")
	case <-time.After(30 * time.Millisecond):
	}
	released.Do(func() { close(release) })
	select {
	case running := <-observed:
		require.False(t, running)
	case <-time.After(time.Second):
		t.Fatal("settlement did not release attempt ownership")
	}

	// Incremental polling observes the final idle phase after ownership becomes available.
	page, err := exe.ListJob(ctx, &entity.JobFilter{ChangedAfterRevision: &job.Revision})
	require.NoError(t, err)
	require.Len(t, page.Jobs, 1)
	require.Equal(t, entity.JobPhase_JOB_PHASE_UNSPECIFIED, page.Jobs[0].Phase)
}
