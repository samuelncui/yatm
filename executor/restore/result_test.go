package restore

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/samuelncui/acp"
	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/library"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func finishRestoreCopy(t *testing.T, runner *jobRestoreRunner, copy *Copy, content []byte) error {
	t.Helper()
	target := runner.restoreTarget(copy.TargetPath)
	require.NoError(t, os.MkdirAll(filepath.Dir(target), 0755))
	require.NoError(t, os.WriteFile(target, content, 0600))
	hash := sha256.Sum256(content)
	session := &testReadSession{root: "/mounted"}
	source, err := session.SourcePath(copy.MediaPath)
	require.NoError(t, err)
	err = runner.completeCopy(context.Background(), copy.MediaID, session, &acp.StreamResult{
		ID: copy.ID, Job: &acp.Job{FullPath: source, Status: acp.JobStatusFinished, SuccessTargets: []string{target},
			Size: int64(len(content)), SHA256: hex.EncodeToString(hash[:])}})
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
				Signature: []byte("opaque-new"), Sha256: hash[:], Size: 3, Mode: 0644, MtimeNs: 2}})
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
	require.NoError(t, runner.db.First(&oldCopy, "file_version_id = ?", older.ID).Error)
	require.NoError(t, runner.db.First(&newCopy, "file_version_id = ?", latest.ID).Error)
	// Finishing the older version first must not consume the original's empty association slot.
	require.NoError(t, finishRestoreCopy(t, runner, &oldCopy, []byte("old")))
	original, err := lib.GetFileLocation(ctx, file.ID)
	require.NoError(t, err)
	require.Nil(t, original)
	oldResult, err := lib.GetRestoreResult(ctx, runner.operationID, oldCopy.ItemID)
	require.NoError(t, err)
	require.Equal(t, library.RestoreNewFile, oldResult.Outcome)
	created, err := lib.GetFile(ctx, oldResult.ResultFileID)
	require.NoError(t, err)
	require.Equal(t, library.RestoredName(file.Name, older.ID), created.Name)
	require.Equal(t, file.ParentID, created.ParentID)
	require.NoError(t, finishRestoreCopy(t, runner, &newCopy, []byte("new")))
	original, err = lib.GetFileLocation(ctx, file.ID)
	require.NoError(t, err)
	require.Equal(t, newCopy.TargetPath, original.Path)
	require.Equal(t, latest.Signature, original.Signature)
	summary, err := runner.resultSummary(ctx)
	require.NoError(t, err)
	require.EqualValues(t, 2, summary.VerifiedFiles)
	require.Zero(t, summary.PendingFiles)
}

func TestRestoreLibraryCommitSurvivesJobCheckpointFailure(t *testing.T) {
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
	require.NoError(t, runner.db.Callback().Update().Before("gorm:update").Register("test:restore-checkpoint", func(tx *gorm.DB) {
		values, ok := tx.Statement.Dest.(map[string]interface{})
		if tx.Statement.Table == "copies" && ok && values["status"] != nil {
			tx.AddError(fmt.Errorf("injected Job checkpoint failure"))
		}
	}))
	err = finishRestoreCopy(t, runner, &copy, []byte("saved"))
	require.ErrorContains(t, err, "injected Job checkpoint")
	require.NoError(t, runner.db.Callback().Update().Remove("test:restore-checkpoint"))
	result, err := lib.GetRestoreResult(ctx, runner.operationID, copy.ItemID)
	require.NoError(t, err)
	require.Equal(t, file.ID, result.ResultFileID)
	// Replay uses successful provenance, not mutable physical bytes or a second association decision.
	require.NoError(t, os.Remove(runner.restoreTarget(copy.TargetPath)))
	require.NoError(t, runner.restore(ctx, &entity.RestoreMediaRequest{Id: job.ID, Target: &entity.ReadMediaTarget{}}))
	require.Equal(t, entity.JobPhase_JOB_PHASE_COMPLETED, runner.Phase())
	var output Output
	require.NoError(t, runner.db.First(&output, copy.ItemID).Error)
	require.True(t, output.Completed)
	require.Equal(t, result.ResultFileID, output.ResultFileID)
	replayed, err := lib.GetRestoreResult(ctx, runner.operationID, copy.ItemID)
	require.NoError(t, err)
	require.Equal(t, result, replayed)
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
			err = finishRestoreCopy(t, runner, &copy, []byte("complete but damaged"))
			if allow {
				require.NoError(t, err)
				require.FileExists(t, runner.restoreTarget(copy.TargetPath))
				result, err := lib.GetRestoreResult(ctx, runner.operationID, copy.ItemID)
				require.NoError(t, err)
				require.Equal(t, library.RestoreDamaged, result.Outcome)
				require.Zero(t, result.ResultFileID)
			} else {
				require.ErrorContains(t, err, "checksum mismatch")
				require.NoFileExists(t, runner.restoreTarget(copy.TargetPath))
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
			require.Equal(t, entity.PositionHealth_DAMAGED, position.Health)
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
			Expected: &entity.ExpectedFile{FileId: file.ID, Signature: version.Signature, Sha256: version.Hash, Size: version.Size}})
	})
	require.NoError(t, err)
	positions, _, err := lib.ListContentCopies(ctx, version.Signature, 0, 10)
	require.NoError(t, err)
	require.Len(t, positions, 2)
	observe := func(position *library.Position, health entity.PositionHealth) {
		t.Helper()
		token, err := library.PositionContentToken(position)
		require.NoError(t, err)
		published, err := lib.PublishPositionHealth(ctx, &library.PositionHealthObservation{
			PositionID: position.ID, ContentToken: token, Health: health, CheckedAt: time.Now().UnixMilli()})
		require.NoError(t, err)
		require.True(t, published)
	}
	observe(positions[0], entity.PositionHealth_DAMAGED)
	observe(positions[1], entity.PositionHealth_HEALTHY)
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
	observe(positions[1], entity.PositionHealth_MISSING)
	require.ErrorContains(t, runner.checkCandidate(ctx, &copy), "known unusable")
	observe(positions[0], entity.PositionHealth_MISSING)
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
