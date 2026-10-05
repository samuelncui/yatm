package restore

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/samuelncui/yatm/entity"
	"github.com/stretchr/testify/require"
)

func TestRestoreFailedMediaAttemptSettlesPhaseAndReopens(t *testing.T) {
	// Freeze a saved copy whose physical file is unavailable when the Media attempt starts.
	ctx := context.Background()
	content := []byte("saved")
	runner, target, _ := setupVolumeRestore(t, "failure", map[string][]byte{"file.txt": content})
	t.Cleanup(func() { _ = runner.Close() })
	exe := runner.exe
	filename := filepath.Join(exe.Paths().Volumes[0], "failure", "file.txt")
	require.NoError(t, os.Remove(filename))
	api := &service{exe: exe}
	request := &entity.RestoreMediaRequest{Id: runner.job.ID, Target: target.Pack()}
	release, err := exe.AcquireJobResource(ctx, "volume:"+target.Uuid, nil)
	require.NoError(t, err)
	defer func() { release() }()
	_, err = api.RestoreMedia(ctx, request)
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		stored, err := exe.GetJob(ctx, runner.job.ID)
		return err == nil && stored.Phase == entity.JobPhase_JOB_PHASE_QUEUED
	}, 5*time.Second, time.Millisecond)
	queued, err := api.GetProgress(ctx, &entity.GetRestoreJobProgressRequest{Id: runner.job.ID})
	require.NoError(t, err)
	require.Equal(t, entity.JobPhase_JOB_PHASE_QUEUED, queued.Progress.GetStage().GetPhase())
	release()
	release = func() {}
	require.Eventually(t, func() bool { return !exe.IsRunning(runner.job.ID) }, 5*time.Second, time.Millisecond)
	failed, err := exe.GetJob(ctx, runner.job.ID)
	require.NoError(t, err)
	require.Equal(t, entity.JobStatus_JOB_STATUS_READY, failed.Status)
	require.Equal(t, entity.JobStatus_JOB_STATUS_READY, failed.Checkpoint)
	require.Contains(t, failed.Error, "inspect Media path failed")
	progress, err := api.GetProgress(ctx, &entity.GetRestoreJobProgressRequest{Id: runner.job.ID})
	require.NoError(t, err)
	require.Equal(t, entity.JobPhase_JOB_PHASE_UNSPECIFIED, progress.Progress.GetStage().GetPhase())
	require.True(t, progress.Progress.TotalKnown)

	// Reopening retains the prepared manifest; public progress still exposes no live phase.
	reopened, err := newRunner(ctx, exe, failed)
	require.NoError(t, err)
	require.Equal(t, entity.JobPhase_JOB_PHASE_UNSPECIFIED,
		reopened.(*jobRestoreRunner).progressSnapshot().GetStage().GetPhase())
	require.NoError(t, reopened.Close())

	// Restoring the same physical copy permits another explicit attempt through the cached runner.
	require.NoError(t, os.WriteFile(filename, content, 0o644))
	_, err = api.RestoreMedia(ctx, request)
	require.NoError(t, err)
	require.Eventually(t, func() bool { return !exe.IsRunning(runner.job.ID) }, 5*time.Second, time.Millisecond)
	completed, err := exe.GetJob(ctx, runner.job.ID)
	require.NoError(t, err)
	require.Equal(t, entity.JobStatus_JOB_STATUS_COMPLETED, completed.Status, completed.Error)
}
