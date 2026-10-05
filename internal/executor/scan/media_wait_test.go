package scan

import (
	"context"
	"testing"
	"time"

	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/internal/executor"
	"github.com/samuelncui/yatm/internal/library"
	"github.com/stretchr/testify/require"
)

func TestPreparedTapeScanMediaFailureIsTerminal(t *testing.T) {
	// Tape indexing prepares the manifest without touching a device.
	ctx := context.Background()
	exe, volume, _ := setupScanExecutor(t)
	media, err := exe.Lib().CreateMedia(ctx, &library.Media{
		Kind: entity.MediaKind_MEDIA_KIND_TAPE, Identity: "SCAN001", Name: "Scan fixture",
		Profile: (&entity.TapeMediaProfile{Format: "ltfs_v1"}).Pack(),
	})
	require.NoError(t, err)
	job := createScanJob(t, exe, media.ID, false)
	waitScanStatus(t, exe, job.ID, entity.JobStatus_JOB_STATUS_READY)

	// An admitted mismatching Media operation fails before any physical I/O and ends the Scan.
	request := &entity.ReadScanMediaRequest{Id: job.ID, Target: (&entity.ReadVolumeTarget{Uuid: volume.Marker.UUID}).Pack()}
	_, err = (&service{exe: exe}).ReadMedia(ctx, request)
	require.NoError(t, err)
	failed := waitScanStatus(t, exe, job.ID, entity.JobStatus_JOB_STATUS_FAILED)
	require.Equal(t, entity.JobStatus_JOB_STATUS_READY, failed.Checkpoint)
	require.Contains(t, failed.Error, "Scan requires Tape Media")

	// The complete frozen manifest never authorizes retrying this failed Scan.
	_, err = (&service{exe: exe}).ReadMedia(ctx, request)
	require.ErrorContains(t, err, "cannot start from status JOB_STATUS_FAILED")
	require.False(t, exe.IsRunning(job.ID))
}

func TestQueuedVolumeScanCancellationEndsJob(t *testing.T) {
	// Hold the same exclusive Volume lease that an independent Media attempt uses.
	ctx := context.Background()
	exe, volume, media := setupScanExecutor(t)
	writeScanFile(t, volume.Root, "queued.txt", []byte("content"), 0o644, time.Unix(20, 0))
	key := "volume:" + media.Identity
	release, err := exe.AcquireJobResource(ctx, key, nil)
	require.NoError(t, err)
	defer func() {
		if release != nil {
			release()
		}
	}()

	// Automatic Scan execution is admitted but queues before touching the held Volume.
	job := createScanJob(t, exe, media.ID, false)
	t.Cleanup(func() {
		if exe.IsRunning(job.ID) {
			require.NoError(t, exe.Cancel(job.ID))
			require.Eventually(t, func() bool { return !exe.IsRunning(job.ID) }, 5*time.Second, time.Millisecond)
		}
	})
	waitQueued := func() {
		t.Helper()
		require.Eventually(t, func() bool {
			stored, err := exe.GetJob(ctx, job.ID)
			return err == nil && stored.Status == entity.JobStatus_JOB_STATUS_PREPARING &&
				stored.Phase == entity.JobPhase_JOB_PHASE_QUEUED && exe.IsRunning(job.ID)
		}, 5*time.Second, time.Millisecond)
	}
	waitQueued()
	request := &entity.ReadScanMediaRequest{Id: job.ID, Target: (&entity.ReadVolumeTarget{Uuid: media.Identity}).Pack()}
	_, err = (&service{exe: exe}).ReadMedia(ctx, request)
	require.ErrorContains(t, err, "cannot start from status JOB_STATUS_PREPARING")
	_, err = exe.DeleteJobs(ctx, false, job.ID)
	require.ErrorIs(t, err, executor.ErrJobBusy)

	// Cancel drains the queued attempt without publishing inventory and ends this Scan.
	require.NoError(t, exe.Cancel(job.ID))
	failed := waitScanStatus(t, exe, job.ID, entity.JobStatus_JOB_STATUS_FAILED)
	require.Equal(t, "Cancelled by the operator", failed.Error)
	require.Equal(t, entity.JobStatus_JOB_STATUS_PREPARING, failed.Checkpoint)
	require.Equal(t, entity.JobPhase_JOB_PHASE_UNSPECIFIED, failed.Phase)
	positions, err := exe.Lib().ListMediaFilePositions(ctx, media.ID, "", 10)
	require.NoError(t, err)
	require.Empty(t, positions)

	// Media selection cannot restart the failed Job; a new Scan completes after the holder releases.
	_, err = (&service{exe: exe}).ReadMedia(ctx, request)
	require.ErrorContains(t, err, "cannot start from status JOB_STATUS_FAILED")
	release()
	release = nil
	fresh := createScanJob(t, exe, media.ID, false)
	completed := waitScanStatus(t, exe, fresh.ID, entity.JobStatus_JOB_STATUS_COMPLETED)
	require.Empty(t, completed.Error)
	positions, err = exe.Lib().ListMediaFilePositions(ctx, media.ID, "", 10)
	require.NoError(t, err)
	require.Len(t, positions, 1)
	require.Equal(t, "queued.txt", positions[0].Path)

	// The completed attempt leaves no Volume lease behind.
	leaseCtx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	releaseAgain, err := exe.AcquireJobResource(leaseCtx, key, nil)
	require.NoError(t, err)
	releaseAgain()
}
