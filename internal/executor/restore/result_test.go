package restore

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/internal/library"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func finishRestoreCopy(t *testing.T, runner *jobRestoreRunner, copy *copyCandidate, content []byte) error {
	t.Helper()
	target := runner.restoreTarget(copy.TargetPath)
	require.NoError(t, os.MkdirAll(filepath.Dir(target), 0755))
	require.NoError(t, os.WriteFile(target, content, 0600))
	hash := sha256.Sum256(content)
	session := &testReadSession{root: "/mounted"}
	source, err := session.SourcePath(copy.MediaPath)
	require.NoError(t, err)
	err = completeTestCopy(t, runner, copy.MediaID, session, copy, source, target, hash[:], content)
	if err != nil {
		return err
	}
	if err := session.Finalize(context.Background()); err != nil {
		return err
	}
	return runner.finalizeOutputs(context.Background(), copy.MediaID)
}

func TestRestoreLatestSelectedVersionReconnectsRegardlessOfCompletionOrder(t *testing.T) {
	ctx := context.Background()
	exe, lib := setupTestExecutor(t)
	media, file := createMediaFile(t, lib, newTapeMedia("ORDER"), ".", "photo.jpg", []byte("old"), nil)
	older, err := lib.LatestFileVersion(ctx, file.ID)
	require.NoError(t, err)
	hash := sha256.Sum256([]byte("new"))
	_, err = lib.CommitMedia(ctx, media, func(_ context.Context, yield func(*library.MediaFile) error) error {
		return yield(&library.MediaFile{Path: "new-photo.jpg", Size: 3, Hash: hash[:], Mode: 0644,
			ModTime: time.Unix(2, 0), WriteTime: time.Now(), Expected: &entity.ExpectedFile{FileId: file.ID,
				Signature: []byte("opaque-new"), Sha256: hash[:], SizeBytes: 3, Mode: 0644, MtimeNs: 2}})
	})
	require.NoError(t, err)
	latest, err := lib.LatestFileVersion(ctx, file.ID)
	require.NoError(t, err)
	require.NotEqual(t, older.ID, latest.ID)
	job, err := Create(ctx, exe, 0, &entity.RestoreJobSpec{FileVersionIds: []int64{latest.ID, older.ID}, Destination: &entity.RestoreDestination{LocationId: 1}})
	require.NoError(t, err)
	waitIndexed(t, exe, job.ID)
	value, err := exe.GetJobRunner(ctx, job.ID)
	require.NoError(t, err)
	runner := value.(*jobRestoreRunner)
	var oldCopy, newCopy Copy
	require.NoError(t, runner.db.First(&oldCopy, "item_id = ?", older.ID).Error)
	oldCandidate := loadTestCopyCandidate(t, runner, &oldCopy)
	require.NoError(t, runner.db.First(&newCopy, "item_id = ?", latest.ID).Error)
	newCandidate := loadTestCopyCandidate(t, runner, &newCopy)
	// Finishing the older version first must not consume the original's empty association slot.
	require.NoError(t, finishRestoreCopy(t, runner, oldCandidate, []byte("old")))
	original, err := lib.GetFileLocation(ctx, file.ID)
	require.NoError(t, err)
	require.Nil(t, original)
	var oldResult File
	require.NoError(t, runner.db.First(&oldResult, oldCopy.ItemID).Error)
	require.NotEqual(t, file.ID, oldResult.ResultFileID)
	created, err := lib.GetFile(ctx, oldResult.ResultFileID)
	require.NoError(t, err)
	require.Equal(t, library.RestoredName(file.Name, older.ID), created.Name)
	require.Equal(t, file.ParentID, created.ParentID)
	require.NoError(t, finishRestoreCopy(t, runner, newCandidate, []byte("new")))
	original, err = lib.GetFileLocation(ctx, file.ID)
	require.NoError(t, err)
	require.Equal(t, newCandidate.TargetPath, original.Path)
	require.Equal(t, latest.Signature, original.Signature)
	summary, err := runner.resultSummary(ctx)
	require.NoError(t, err)
	require.EqualValues(t, 2, summary.VerifiedFiles)
	require.Zero(t, summary.PendingFiles)
}

func TestRestoreJobCheckpointFailureRequiresExplicitHandling(t *testing.T) {
	ctx := context.Background()
	exe, lib := setupTestExecutor(t)
	_, file := createMediaFile(t, lib, newTapeMedia("CHECKPOINT"), ".", "file.txt", []byte("saved"), nil)
	job := createRestoreJob(t, exe, file.ID)
	waitIndexed(t, exe, job.ID)
	value, err := exe.GetJobRunner(ctx, job.ID)
	require.NoError(t, err)
	runner := value.(*jobRestoreRunner)
	var copy Copy
	require.NoError(t, runner.db.First(&copy).Error)
	candidate := loadTestCopyCandidate(t, runner, &copy)
	require.NoError(t, runner.db.Callback().Update().Before("gorm:update").Register("test:restore-checkpoint", func(tx *gorm.DB) {
		values, ok := tx.Statement.Dest.(map[string]interface{})
		if tx.Statement.Table == "copies" && ok && values["status"] != nil {
			tx.AddError(fmt.Errorf("injected Job checkpoint failure"))
		}
	}))
	err = finishRestoreCopy(t, runner, candidate, []byte("saved"))
	require.ErrorContains(t, err, "injected Job checkpoint")
	require.NoError(t, runner.db.Callback().Update().Remove("test:restore-checkpoint"))
	original, err := lib.GetFileLocation(ctx, file.ID)
	require.NoError(t, err)
	require.NotNil(t, original)
	// A failed Job checkpoint is reported, never repaired from a permanent receipt.
	require.ErrorContains(t, runner.completeOutput(ctx, candidate, candidate.Hash, candidate.Size, false), "already linked")
	var output File
	require.NoError(t, runner.db.First(&output, copy.ItemID).Error)
	require.False(t, output.Completed)
	require.FileExists(t, runner.restoreTarget(candidate.TargetPath))

}

func TestRestoreCompleteDamagedOutputRequiresExplicitSalvage(t *testing.T) {
	for _, allow := range []bool{false, true} {
		t.Run(fmt.Sprint(allow), func(t *testing.T) {
			ctx := context.Background()
			exe, lib := setupTestExecutor(t)
			_, file := createMediaFile(t, lib, newTapeMedia("SALVAGE"), ".", "file.txt", []byte("saved"), nil)
			version, err := lib.LatestFileVersion(ctx, file.ID)
			require.NoError(t, err)
			job, err := Create(ctx, exe, 0, &entity.RestoreJobSpec{FileVersionIds: []int64{version.ID}, AllowDamagedCopies: allow,
				Destination: &entity.RestoreDestination{LocationId: 1}})
			require.NoError(t, err)
			waitIndexed(t, exe, job.ID)
			value, err := exe.GetJobRunner(ctx, job.ID)
			require.NoError(t, err)
			runner := value.(*jobRestoreRunner)
			var copy Copy
			require.NoError(t, runner.db.First(&copy).Error)
			candidate := loadTestCopyCandidate(t, runner, &copy)
			err = finishRestoreCopy(t, runner, candidate, []byte("complete but damaged"))
			if allow {
				require.NoError(t, err)
				require.FileExists(t, runner.restoreTarget(candidate.TargetPath))
				var result File
				require.NoError(t, runner.db.First(&result, copy.ItemID).Error)
				require.True(t, result.Damaged)
				require.Zero(t, result.ResultFileID)
			} else {
				require.ErrorContains(t, err, "checksum mismatch")
				require.NoFileExists(t, runner.restoreTarget(candidate.TargetPath))
			}
			original, err := lib.GetFileLocation(ctx, file.ID)
			require.NoError(t, err)
			require.Nil(t, original)
			position, err := lib.GetPosition(ctx, copy.PositionID)
			require.NoError(t, err)
			require.Equal(t, entity.PositionHealth_POSITION_HEALTH_UNKNOWN, position.Health)
			require.NoError(t, runner.publishHealth(ctx, copy.MediaID))
			position, err = lib.GetPosition(ctx, copy.PositionID)
			require.NoError(t, err)
			require.Equal(t, entity.PositionHealth_POSITION_HEALTH_DAMAGED, position.Health)
			summary, err := runner.resultSummary(ctx)
			require.NoError(t, err)
			require.Zero(t, summary.VerifiedFiles)
			if allow {
				require.EqualValues(t, 1, summary.DamagedFiles)
				require.EqualValues(t, 1, summary.UnlinkedFiles)
				require.Zero(t, summary.PendingFiles)
			} else {
				require.EqualValues(t, 1, summary.PendingFiles)
			}
		})
	}
}

func TestRestorePrefersUsableCopiesAndNeverSelectsKnownMissingBytes(t *testing.T) {
	ctx := context.Background()
	exe, lib := setupTestExecutor(t)
	media, file := createMediaFile(t, lib, newTapeMedia("HEALTH"), ".", "first.txt", []byte("saved"), nil)
	version, err := lib.LatestFileVersion(ctx, file.ID)
	require.NoError(t, err)
	_, err = lib.CommitMedia(ctx, media, func(_ context.Context, yield func(*library.MediaFile) error) error {
		return yield(&library.MediaFile{Path: "second.txt", Size: version.Size, Hash: version.Hash, Mode: 0644,
			Expected: &entity.ExpectedFile{FileId: file.ID, Signature: version.Signature, Sha256: version.Hash, SizeBytes: version.Size}})
	})
	require.NoError(t, err)
	positions, _, err := lib.ListContentCopies(ctx, version.Signature, 0, 10)
	require.NoError(t, err)
	require.Len(t, positions, 2)
	observe := func(position *library.Position, health entity.PositionHealth) {
		t.Helper()
		require.NoError(t, lib.PublishPositionHealth(ctx, &library.PositionHealthObservation{
			PositionID: position.ID, Health: health, CheckedAtNS: time.Now().UnixNano()}))
	}
	observe(positions[0], entity.PositionHealth_POSITION_HEALTH_DAMAGED)
	observe(positions[1], entity.PositionHealth_POSITION_HEALTH_HEALTHY)
	job, err := Create(ctx, exe, 0, &entity.RestoreJobSpec{FileVersionIds: []int64{version.ID}, AllowDamagedCopies: true,
		Destination: &entity.RestoreDestination{LocationId: 1}})
	require.NoError(t, err)
	waitIndexed(t, exe, job.ID)
	value, err := exe.GetJobRunner(ctx, job.ID)
	require.NoError(t, err)
	runner := value.(*jobRestoreRunner)
	var copy Copy
	require.NoError(t, runner.db.First(&copy).Error)
	require.Equal(t, positions[1].ID, copy.PositionID, "usable bytes on one Media win even with salvage enabled")
	observe(positions[1], entity.PositionHealth_POSITION_HEALTH_MISSING)
	observe(positions[0], entity.PositionHealth_POSITION_HEALTH_MISSING)
	newJob, err := Create(ctx, exe, 0, &entity.RestoreJobSpec{FileVersionIds: []int64{version.ID}, AllowDamagedCopies: true,
		Destination: &entity.RestoreDestination{LocationId: 1}})
	require.NoError(t, err)
	require.Eventually(t, func() bool { return !exe.IsRunning(newJob.ID) }, 5*time.Second, time.Millisecond)
	newValue, err := exe.GetJobRunner(ctx, newJob.ID)
	require.NoError(t, err)
	newRunner := newValue.(*jobRestoreRunner)
	var config Config
	require.NoError(t, newRunner.db.First(&config, 1).Error)
	require.False(t, config.ManifestFrozen)
	require.ErrorContains(t, newRunner.applySpec(ctx, config.Spec), "no archived copy")
}
