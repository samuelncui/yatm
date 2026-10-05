//go:build e2e

package e2e

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/samuelncui/yatm/entity"
	"github.com/stretchr/testify/require"
)

func TestCLIQueuedMediaCancellationAndExplicitMediaContinuation(t *testing.T) {
	// Use the real CLI and transport; only the exclusive lease holder is a local fixture.
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	f := newVolumeE2EFixture(t, ctx)
	require.NoError(t, os.MkdirAll(f.paths.Source, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(f.paths.Source, "file.txt"), []byte("saved"), 0o644))
	location := new(entity.CreateLocationResponse)
	cliResult(t, ctx, f.cli, location, "location", "create", "--name", "Originals", "--root", f.paths.Source)
	volume := new(entity.InitializeVolumeResponse)
	cliResult(t, ctx, f.cli, volume, "volume", "initialize", f.volumeRoot, "--name", "Archive", "--type", "hdd")
	archive := new(entity.CreateArchiveJobResponse)
	cliResult(t, ctx, f.cli, archive, "archive", "create", "--location", decimal(location.Location.Id)+":file.txt")
	jobID := archive.Job.Id
	waitCLIJob(t, ctx, f.cli, jobID, true)
	t.Cleanup(func() {
		if f.exe.IsRunning(jobID) {
			_ = f.exe.Cancel(jobID)
			require.Eventually(t, func() bool { return !f.exe.IsRunning(jobID) }, 5*time.Second, time.Millisecond)
		}
	})
	release, err := f.exe.AcquireJobResource(ctx, "volume:"+volume.Media.Identity, nil)
	require.NoError(t, err)
	defer func() { release() }()
	write := []string{"archive", "write", "volume", decimal(jobID), "--uuid", volume.Media.Identity}

	// A queued READY Job keeps waiting, rejects another Media operation/deletion, and reports no percentage.
	cliResult(t, ctx, f.cli, new(entity.WriteArchiveMediaResponse), write...)
	require.Eventually(t, func() bool {
		stored, err := f.exe.GetJob(ctx, jobID)
		return err == nil && stored.Phase == entity.JobPhase_JOB_PHASE_QUEUED
	}, 5*time.Second, time.Millisecond)
	output, err := f.cli.run(ctx, "job", "wait", decimal(jobID), "--wait-timeout", "300ms", "--poll-interval", "100ms")
	require.ErrorContains(t, err, "deadline_exceeded")
	queued := new(entity.GetJobResponse)
	require.NoError(t, decodeCLIOutput(output, queued))
	require.Equal(t, entity.JobStatus_JOB_STATUS_READY, queued.Job.Status)
	require.Equal(t, entity.JobPhase_JOB_PHASE_QUEUED, queued.Job.Phase)
	_, err = f.cli.run(ctx, write...)
	require.Error(t, err)
	_, err = f.cli.run(ctx, "job", "delete", decimal(jobID))
	require.Error(t, err)
	progress := new(entity.GetArchiveJobProgressResponse)
	cliResult(t, ctx, f.cli, progress, "job", "progress", decimal(jobID))
	require.Equal(t, entity.JobPhase_JOB_PHASE_QUEUED, progress.Progress.GetStage().GetPhase())
	require.Nil(t, progress.Progress.GetStage().Total)

	// Media cancellation returns to idle READY with its reason and keeps the prepared manifest.
	cliResult(t, ctx, f.cli, new(entity.CancelJobResponse), "job", "cancel", decimal(jobID))
	require.Eventually(t, func() bool { return !f.exe.IsRunning(jobID) }, 5*time.Second, time.Millisecond)
	output, err = f.cli.run(ctx, "job", "wait", decimal(jobID), "--wait-timeout", "5s", "--poll-interval", "100ms")
	require.ErrorContains(t, err, "action_required")
	require.ErrorContains(t, err, "Cancelled by the operator")
	ready := new(entity.GetJobResponse)
	require.NoError(t, decodeCLIOutput(output, ready))
	require.Equal(t, entity.JobStatus_JOB_STATUS_READY, ready.Job.Status)
	require.Equal(t, entity.JobPhase_JOB_PHASE_UNSPECIFIED, ready.Job.Phase)
	require.Equal(t, "Cancelled by the operator", ready.Job.Error)
	items := new(entity.ListArchiveJobFilesResponse)
	cliResult(t, ctx, f.cli, items, "archive", "files", decimal(jobID))
	require.Len(t, items.Items, 1)
	require.Equal(t, entity.CopyStatus_COPY_STATUS_PENDING, items.Items[0].Status)
	require.Empty(t, items.Items[0].File.MediaPath)

	// An explicit Media continuation uses the retained manifest after the holder releases.
	cliResult(t, ctx, f.cli, new(entity.WriteArchiveMediaResponse), write...)
	release()
	release = func() {}
	waitCLIJob(t, ctx, f.cli, jobID, false)
	cliResult(t, ctx, f.cli, items, "archive", "files", decimal(jobID))
	require.Equal(t, entity.CopyStatus_COPY_STATUS_SUBMITTED, items.Items[0].Status)
	data, err := os.ReadFile(filepath.Join(f.volumeRoot, items.Items[0].File.MediaPath))
	require.NoError(t, err)
	require.Equal(t, "saved", string(data))
}
