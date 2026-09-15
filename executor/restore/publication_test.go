package restore

import (
	"context"
	"crypto/sha256"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/executor"
	"github.com/samuelncui/yatm/library"
	mediapkg "github.com/samuelncui/yatm/media"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestRestoreFailedFinalizeNeverPublishesOrAdoptsPendingOutput(t *testing.T) {
	t.Run("ready checkpoint", func(t *testing.T) { testRestoreFailedFinalize(t, false) })
	t.Run("ready checkpoint failure", func(t *testing.T) { testRestoreFailedFinalize(t, true) })
}

func testRestoreFailedFinalize(t *testing.T, failReady bool) {
	ctx := context.Background()
	exe, lib := setupTestExecutor(t)
	root := filepath.Join(exe.Paths().Volumes[0], "finalize")
	require.NoError(t, os.MkdirAll(root, 0755))
	volume, err := mediapkg.InitializeVolume(root, &entity.VolumeMediaProfile{SerialNumber: "finalize", Type: entity.VolumeType_VOLUME_TYPE_HDD})
	require.NoError(t, err)
	markerPath := filepath.Join(root, mediapkg.VolumeMarkerName)
	marker, err := os.ReadFile(markerPath)
	require.NoError(t, err)
	content := []byte("saved")
	require.NoError(t, os.WriteFile(filepath.Join(root, "file.txt"), content, 0644))
	_, file := createMediaFile(t, lib, &library.Media{Kind: entity.MediaKind_MEDIA_KIND_VOLUME, Identity: volume.Marker.UUID,
		Name: "Finalize", Profile: volume.Marker.Profile.Pack(), CreateTime: volume.Marker.CreatedAt}, ".", "file.txt", content, nil)
	job := createRestoreJob(t, exe, file.ID)
	waitIndexed(t, exe, job.ID)
	value, err := exe.GetJobRunner(ctx, job.ID)
	require.NoError(t, err)
	runner := value.(*jobRestoreRunner)
	attempt := func() error {
		result := make(chan error, 1)
		require.NoError(t, exe.StartJob(ctx, job.ID, entity.JobKind_RESTORE, func(runCtx context.Context, value executor.Runner) error {
			err := value.(*jobRestoreRunner).restore(runCtx, &entity.RestoreMediaRequest{Id: job.ID, Target: (&entity.ReadVolumeTarget{Uuid: volume.Marker.UUID}).Pack()})
			result <- err
			return err
		}))
		select {
		case err := <-result:
			require.Eventually(t, func() bool { return !exe.IsRunning(job.ID) }, 5*time.Second, time.Millisecond)
			return err
		case <-time.After(5 * time.Second):
			t.Fatal("Restore attempt did not return")
			return nil
		}
	}
	// Damage only the isolated marker after actual bytes are ready, before Session.Finalize validates identity.
	require.NoError(t, runner.db.Callback().Create().Before("gorm:create").Register("test:break-finalize", func(tx *gorm.DB) {
		output, ok := tx.Statement.Dest.(*Output)
		if tx.Statement.Table == "outputs" && ok && output.Ready {
			tx.AddError(os.WriteFile(markerPath, []byte("invalid marker"), 0644))
			if failReady {
				tx.AddError(errors.New("injected ready checkpoint failure"))
			}
		}
	}))
	err = attempt()
	require.Error(t, err)
	require.NoError(t, runner.db.Callback().Create().Remove("test:break-finalize"))
	var copy Copy
	require.NoError(t, runner.db.First(&copy).Error)
	require.Equal(t, entity.CopyStatus_PENDING, copy.Status)
	var output Output
	require.NoError(t, runner.db.First(&output, copy.ItemID).Error)
	require.Equal(t, !failReady, output.Ready)
	require.False(t, output.Finalized)
	require.False(t, output.Completed)
	original, err := lib.GetFileLocation(ctx, file.ID)
	require.NoError(t, err)
	require.Nil(t, original)
	result, err := lib.GetRestoreResult(ctx, runner.operationID, copy.ItemID)
	require.NoError(t, err)
	require.Nil(t, result)
	require.FileExists(t, runner.restoreTarget(copy.TargetPath))

	// RetryIndex cannot turn this Job's unfinalized bytes into an ordinary existing-file adoption.
	require.NoError(t, os.WriteFile(markerPath, marker, 0644))
	var config Config
	require.NoError(t, runner.db.First(&config, 1).Error)
	require.NoError(t, runner.applySpec(ctx, config.Spec))
	original, err = lib.GetFileLocation(ctx, file.ID)
	require.NoError(t, err)
	require.Nil(t, original)
	require.NoError(t, attempt())
	original, err = lib.GetFileLocation(ctx, file.ID)
	require.NoError(t, err)
	require.NotNil(t, original)
	require.Equal(t, entity.JobPhase_JOB_PHASE_COMPLETED, runner.Phase())
}

func TestRestoreCandidateRemainsUniqueAfterPartialReservationsAndArchiveDateChange(t *testing.T) {
	ctx := context.Background()
	exe, lib := setupTestExecutor(t)
	media, file := createMediaFile(t, lib, newTapeMedia("CHOICE"), ".", "file.txt", []byte("old"), nil)
	oldVersion, err := lib.LatestFileVersion(ctx, file.ID)
	require.NoError(t, err)
	hash := sha256.Sum256([]byte("new"))
	_, err = lib.CommitMedia(ctx, media, func(_ context.Context, yield func(*library.MediaFile) error) error {
		return yield(&library.MediaFile{Path: "new.txt", Size: 3, Hash: hash[:], Mode: 0644, WriteTime: time.Now(),
			Expected: &entity.ExpectedFile{FileId: file.ID, Signature: []byte("new-content"), Sha256: hash[:], Size: 3}})
	})
	require.NoError(t, err)
	latest, err := lib.LatestFileVersion(ctx, file.ID)
	require.NoError(t, err)
	job, err := Create(ctx, exe, 0, &entity.RestoreJobSpec{FileVersionIds: []int64{oldVersion.ID, latest.ID}, Destination: &entity.RestoreDestination{LocationId: 1}})
	require.NoError(t, err)
	waitIndexed(t, exe, job.ID)
	value, err := exe.GetJobRunner(ctx, job.ID)
	require.NoError(t, err)
	runner := value.(*jobRestoreRunner)
	// Reproduce a durable prefix of reservations, followed by newer archive metadata on a missing item.
	require.NoError(t, runner.db.Delete(&Output{}, oldVersion.ID).Error)
	_, err = lib.CommitMedia(ctx, media, func(_ context.Context, yield func(*library.MediaFile) error) error {
		return yield(&library.MediaFile{Path: "old-again.txt", Size: oldVersion.Size, Hash: oldVersion.Hash, Mode: 0644, WriteTime: time.Now().Add(time.Hour),
			Expected: &entity.ExpectedFile{FileId: file.ID, Signature: oldVersion.Signature, Sha256: oldVersion.Hash, Size: oldVersion.Size}})
	})
	require.NoError(t, err)
	var config Config
	require.NoError(t, runner.db.First(&config, 1).Error)
	require.NoError(t, runner.applySpec(ctx, config.Spec))
	var selected []FileSelection
	require.NoError(t, runner.db.Find(&selected).Error)
	require.Len(t, selected, 1)
	require.Equal(t, latest.ID, selected[0].ReconnectVersionID)
}
