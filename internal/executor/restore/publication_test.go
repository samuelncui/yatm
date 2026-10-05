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
	"github.com/samuelncui/yatm/internal/executor"
	"github.com/samuelncui/yatm/internal/library"
	mediapkg "github.com/samuelncui/yatm/internal/media"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestRestoreResultCheckpointFailureRetainsPendingOutputForRetry(t *testing.T) {
	ctx := context.Background()
	exe, lib := setupTestExecutor(t)
	root := filepath.Join(exe.Paths().Volumes[0], "finalize")
	require.NoError(t, os.MkdirAll(root, 0755))
	volume, err := mediapkg.InitializeVolume(root, &entity.VolumeMediaProfile{SerialNumber: "finalize", Type: entity.VolumeType_VOLUME_TYPE_HDD})
	require.NoError(t, err)
	content := []byte("saved")
	require.NoError(t, os.WriteFile(filepath.Join(root, "file.txt"), content, 0644))
	_, file := createMediaFile(t, lib, &library.Media{Kind: entity.MediaKind_MEDIA_KIND_VOLUME, Identity: volume.Marker.UUID,
		Name: "Finalize", Profile: volume.Marker.Profile.Pack(), CreatedAtNS: volume.Marker.CreatedAtNS}, ".", "file.txt", content, nil)
	job := createRestoreJob(t, exe, file.ID)
	waitIndexed(t, exe, job.ID)
	value, err := exe.GetJobRunner(ctx, job.ID)
	require.NoError(t, err)
	runner := value.(*jobRestoreRunner)
	attempt := func() error {
		result := make(chan error, 1)
		require.NoError(t, exe.StartJob(ctx, job.ID, entity.JobKind_JOB_KIND_RESTORE, func(runCtx context.Context, value executor.Runner) error {
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
	// Fail the durable result checkpoint after ACP has produced complete output bytes.
	require.NoError(t, runner.db.Callback().Create().Before("gorm:create").Register("test:fail-result-checkpoint", func(tx *gorm.DB) {
		output, ok := tx.Statement.Dest.(*File)
		if tx.Statement.Table == "files" && ok && output.Ready {
			tx.AddError(errors.New("injected result checkpoint failure"))
		}
	}))
	err = attempt()
	require.Error(t, err)
	require.NoError(t, runner.db.Callback().Create().Remove("test:fail-result-checkpoint"))
	var copy Copy
	require.NoError(t, runner.db.First(&copy).Error)
	candidate := loadTestCopyCandidate(t, runner, &copy)
	require.Equal(t, entity.CopyStatus_COPY_STATUS_PENDING, copy.Status)
	var output File
	require.NoError(t, runner.db.First(&output, copy.ItemID).Error)
	require.False(t, output.Ready)
	require.False(t, output.Finalized)
	require.False(t, output.Completed)
	original, err := lib.GetFileLocation(ctx, file.ID)
	require.NoError(t, err)
	require.Nil(t, original)
	require.FileExists(t, runner.restoreTarget(candidate.TargetPath))

	// A later attempt verifies and checkpoints these bytes through the selected Media attempt;
	// it does not treat this Job's output as an unrelated preexisting file.
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
			Expected: &entity.ExpectedFile{FileId: file.ID, Signature: []byte("new-content"), Sha256: hash[:], SizeBytes: 3}})
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
	require.NoError(t, runner.db.Model(&File{}).Where("item_id = ?", oldVersion.ID).Update("path", "").Error)
	_, err = lib.CommitMedia(ctx, media, func(_ context.Context, yield func(*library.MediaFile) error) error {
		return yield(&library.MediaFile{Path: "old-again.txt", Size: oldVersion.Size, Hash: oldVersion.Hash, Mode: 0644, WriteTime: time.Now().Add(time.Hour),
			Expected: &entity.ExpectedFile{FileId: file.ID, Signature: oldVersion.Signature, Sha256: oldVersion.Hash, SizeBytes: oldVersion.Size}})
	})
	require.NoError(t, err)
	var config Config
	require.NoError(t, runner.db.First(&config, 1).Error)
	require.NoError(t, runner.applySpec(ctx, config.Spec))
	var selected []File
	require.NoError(t, runner.db.Where("reconnect = ?", true).Find(&selected).Error)
	require.Len(t, selected, 1)
	require.Equal(t, latest.ID, selected[0].FileVersionID)
}
