package archive

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/internal/executor"
	"github.com/samuelncui/yatm/internal/library"
	mediapkg "github.com/samuelncui/yatm/internal/media"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestArchiveFailedMediaAttemptSettlesPhaseAndReopens(t *testing.T) {
	for _, failStateRead := range []bool{false, true} {
		name := "copy_failure"
		if failStateRead {
			name = "copy_and_final_state_read_failure"
		}
		t.Run(name, func(t *testing.T) {
			// Freeze one real original that is unavailable when the Media attempt starts.
			ctx := context.Background()
			exe := setupTestExecutor(t, executor.Scripts{})
			_, file, filename := publishArchiveOriginal(t, exe, "failure", []byte("saved"))
			api := &service{exe: exe}
			created, err := api.Create(ctx, &entity.CreateArchiveJobRequest{Spec: &entity.ArchiveJobSpec{
				Selections: archiveSelections(file.ID),
			}})
			require.NoError(t, err)
			job := waitIndexed(t, exe, created.Job.Id)
			value, err := exe.GetJobRunner(ctx, job.ID)
			require.NoError(t, err)
			runner := value.(*jobArchiveRunner)
			t.Cleanup(func() { _ = runner.Close() })
			root := filepath.Join(exe.Paths().Volumes[0], "failure")
			require.NoError(t, os.MkdirAll(root, 0o755))
			volume, err := mediapkg.InitializeVolume(root, &entity.VolumeMediaProfile{
				SerialNumber: "failure", Type: entity.VolumeType_VOLUME_TYPE_HDD,
			})
			require.NoError(t, err)
			_, err = exe.Lib().CreateMedia(ctx, &library.Media{
				Kind: entity.MediaKind_MEDIA_KIND_VOLUME, Identity: volume.Marker.UUID, Name: "Failure",
				Profile: volume.Marker.Profile.Pack(), CreatedAtNS: volume.Marker.CreatedAtNS,
			})
			require.NoError(t, err)
			require.NoError(t, os.Rename(filename, filename+"-unavailable"))

			// Fail only the final Job read, after the ordinary source failure has finalized.
			var injected atomic.Bool
			if failStateRead {
				require.NoError(t, runner.db.Callback().Query().Before("gorm:query").Register("test:final-state-read", func(tx *gorm.DB) {
					if tx.Statement.Schema != nil && tx.Statement.Schema.Name == "JobRecord" &&
						runner.Phase() == entity.JobPhase_JOB_PHASE_FINALIZING_MEDIA && injected.CompareAndSwap(false, true) {
						tx.AddError(errors.New("injected final state read failure"))
					}
				}))
				t.Cleanup(func() { _ = runner.db.Callback().Query().Remove("test:final-state-read") })
			}
			release, err := exe.AcquireJobResource(ctx, "volume:"+volume.Marker.UUID, nil)
			require.NoError(t, err)
			defer func() { release() }()
			request := &entity.WriteArchiveMediaRequest{Id: job.ID,
				Target: (&entity.ArchiveVolumeTarget{Uuid: volume.Marker.UUID}).Pack()}
			_, err = api.WriteMedia(ctx, request)
			require.NoError(t, err)
			require.Eventually(t, func() bool {
				stored, err := exe.GetJob(ctx, job.ID)
				return err == nil && stored.Phase == entity.JobPhase_JOB_PHASE_QUEUED
			}, 5*time.Second, time.Millisecond)
			queued, err := api.GetProgress(ctx, &entity.GetArchiveJobProgressRequest{Id: job.ID})
			require.NoError(t, err)
			require.Equal(t, entity.JobPhase_JOB_PHASE_QUEUED, queued.Progress.GetStage().GetPhase())
			release()
			release = func() {}
			require.Eventually(t, func() bool { return !exe.IsRunning(job.ID) }, 5*time.Second, time.Millisecond)
			failed, err := exe.GetJob(ctx, job.ID)
			require.NoError(t, err)
			require.Equal(t, entity.JobStatus_JOB_STATUS_READY, failed.Status)
			require.Equal(t, entity.JobStatus_JOB_STATUS_READY, failed.Checkpoint)
			require.Contains(t, failed.Error, "original is unavailable")
			require.Equal(t, failStateRead, injected.Load())
			if failStateRead {
				require.Contains(t, failed.Error, "injected final state read failure")
			}
			progress, err := api.GetProgress(ctx, &entity.GetArchiveJobProgressRequest{Id: job.ID})
			require.NoError(t, err)
			require.Equal(t, entity.JobPhase_JOB_PHASE_UNSPECIFIED, progress.Progress.GetStage().GetPhase())
			require.True(t, progress.Progress.TotalKnown)

			// Reopening reconstructs the retained manifest's stable progress phase.
			reopened, err := newRunner(ctx, exe, failed)
			require.NoError(t, err)
			require.Equal(t, entity.JobPhase_JOB_PHASE_UNSPECIFIED,
				reopened.(*jobArchiveRunner).progressSnapshot().GetStage().GetPhase())
			require.NoError(t, reopened.Close())

			// The same cached runner must accept another explicit attempt after the original is available.
			require.NoError(t, os.Rename(filename+"-unavailable", filename))
			_, err = api.WriteMedia(ctx, request)
			require.NoError(t, err)
			require.Eventually(t, func() bool { return !exe.IsRunning(job.ID) }, 5*time.Second, time.Millisecond)
			completed, err := exe.GetJob(ctx, job.ID)
			require.NoError(t, err)
			require.Equal(t, entity.JobStatus_JOB_STATUS_COMPLETED, completed.Status, completed.Error)
			require.Equal(t, entity.JobPhase_JOB_PHASE_UNSPECIFIED, progress.Progress.GetStage().GetPhase())
		})
	}
}
