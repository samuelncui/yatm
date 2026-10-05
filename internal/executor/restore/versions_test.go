package restore

import (
	"context"
	"crypto/sha256"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/internal/library"
	"github.com/stretchr/testify/require"
)

func TestRestoreReservesIndependentPathsForMultipleVersions(t *testing.T) {
	// Give an independently organized File two confirmed archived contents.
	ctx := context.Background()
	exe, lib := setupTestExecutor(t)
	media, file := createMediaFile(t, lib, newTapeMedia("VERSIONS"), ".", "draft.txt", []byte("first"), nil)
	hash := sha256.Sum256([]byte("second"))
	_, err := lib.CommitMedia(ctx, media, func(_ context.Context, yield func(*library.MediaFile) error) error {
		return yield(&library.MediaFile{
			Path: "renamed-second-copy.txt", Size: 6, Hash: hash[:], Mode: 0644,
			ModTime: time.Unix(2, 0), WriteTime: time.Unix(3, 0),
			Expected: &entity.ExpectedFile{FileId: file.ID, Signature: []byte("opaque-second"),
				Sha256: hash[:], SizeBytes: 6, Mode: 0600, MtimeNs: 4},
		})
	})
	require.NoError(t, err)
	versions, _, err := lib.ListFileVersions(ctx, file.ID, 0, 10)
	require.NoError(t, err)
	require.Len(t, versions, 2)
	spec := &entity.RestoreJobSpec{FileVersionIds: []int64{versions[0].ID, versions[0].ID, versions[1].ID}, Destination: &entity.RestoreDestination{LocationId: 1}}
	job, err := Create(ctx, exe, 0, spec)
	require.NoError(t, err)
	waitIndexed(t, exe, job.ID)
	value, err := exe.GetJobRunner(ctx, job.ID)
	require.NoError(t, err)
	runner := value.(*jobRestoreRunner)

	// Repeated selection is deduplicated; distinct contents must not silently share an output.
	var count int64
	require.NoError(t, runner.db.Model(&Copy{}).Count(&count).Error)
	require.EqualValues(t, 2, count)
	var outputs []File
	require.NoError(t, runner.db.Order("item_id").Find(&outputs).Error)
	require.Len(t, outputs, 2)
	require.Equal(t, "Unforged/VERSIONS/draft.txt", outputs[0].Path)
	require.Equal(t, "Unforged/VERSIONS/"+library.RestoredName("draft.txt", outputs[1].ItemID), outputs[1].Path)
	var selected File
	require.NoError(t, runner.db.Where("file_id = ?", file.ID).First(&selected).Error)
	require.Equal(t, file.ParentID, selected.ParentID)
	require.Equal(t, file.Name, selected.Name)

	// Reindexing uses the durable reservations rather than allocating additional suffixes.
	var config Config
	require.NoError(t, runner.db.First(&config, 1).Error)
	require.True(t, config.ManifestFrozen)
	require.NoError(t, runner.applySpec(ctx, config.Spec))
	var retried []File
	require.NoError(t, runner.db.Order("item_id").Find(&retried).Error)
	require.Equal(t, outputs, retried)
}

func TestRestoreCompletionIsPerItemNotFile(t *testing.T) {
	// Seed separate restore items of the same File, as supported by frozen migrated manifests.
	ctx := context.Background()
	exe, lib := setupTestExecutor(t)
	content := []byte("first")
	media, file := createMediaFile(t, lib, newTapeMedia("VERSIONS"), ".", "draft.txt", content, nil)
	job := createRestoreJob(t, exe, file.ID)
	waitIndexed(t, exe, job.ID)
	value, err := exe.GetJobRunner(ctx, job.ID)
	require.NoError(t, err)
	runner := value.(*jobRestoreRunner)
	var firstRow Copy
	require.NoError(t, runner.db.First(&firstRow).Error)
	first := loadTestCopyCandidate(t, runner, &firstRow)
	secondRow := firstRow
	secondRow.ID, secondRow.ItemID = 0, first.ItemID+1
	require.NoError(t, runner.db.Create(&secondRow).Error)
	var secondFile File
	require.NoError(t, runner.db.First(&secondFile, first.ItemID).Error)
	secondFile.ItemID, secondFile.FileVersionID, secondFile.Path = secondRow.ItemID, first.FileVersionID+1, "another-version.txt"
	require.NoError(t, runner.db.Create(&secondFile).Error)
	session := &testReadSession{root: "/mounted"}
	source, err := session.SourcePath(first.MediaPath)
	require.NoError(t, err)
	target := runner.restoreTarget(first.TargetPath)
	require.NoError(t, os.MkdirAll(filepath.Dir(target), 0755))
	require.NoError(t, os.WriteFile(target, content, 0644))

	// A valid ACP completion cannot complete another content item merely sharing its File ID.
	require.NoError(t, completeTestCopy(t, runner, media.ID, session, first, source, target, first.Hash, content))
	require.NoError(t, runner.finalizeOutputs(ctx, media.ID))
	require.NoError(t, runner.db.First(&firstRow, first.ID).Error)
	require.NoError(t, runner.db.First(&secondRow, secondRow.ID).Error)
	require.Equal(t, entity.CopyStatus_COPY_STATUS_COMPLETED, firstRow.Status)
	require.Equal(t, entity.CopyStatus_COPY_STATUS_PENDING, secondRow.Status)
	stored, err := exe.GetJob(ctx, job.ID)
	require.NoError(t, err)
	require.Equal(t, entity.JobStatus_JOB_STATUS_READY, stored.Status)
	runner.dropProgress()
	progress := runner.getProgress().ToEntity()
	require.EqualValues(t, 2, progress.TotalFileCount)
	require.EqualValues(t, 1, progress.CopiedFileCount)
}

func TestRestoreFileOwnsOneOutcomeAcrossMediaCandidates(t *testing.T) {
	// A second archived copy supplies another read choice, not another output.
	ctx := context.Background()
	exe, lib := setupTestExecutor(t)
	content := []byte("shared")
	_, file := createMediaFile(t, lib, newTapeMedia("PRIMARY"), ".", "shared.txt", content, nil)
	version, err := lib.LatestFileVersion(ctx, file.ID)
	require.NoError(t, err)
	_, err = lib.CommitMedia(ctx, newTapeMedia("SECONDARY"), func(_ context.Context, yield func(*library.MediaFile) error) error {
		return yield(&library.MediaFile{Path: "copy.txt", Hash: version.Hash, Size: version.Size, Mode: 0644,
			Expected: &entity.ExpectedFile{FileId: file.ID, Signature: version.Signature, Sha256: version.Hash, SizeBytes: version.Size}})
	})
	require.NoError(t, err)
	job := createRestoreJob(t, exe, file.ID)
	waitIndexed(t, exe, job.ID)
	value, err := exe.GetJobRunner(ctx, job.ID)
	require.NoError(t, err)
	runner := value.(*jobRestoreRunner)
	var files []File
	require.NoError(t, runner.db.Find(&files).Error)
	require.Len(t, files, 1)
	var copies []Copy
	require.NoError(t, runner.db.Order("id").Find(&copies).Error)
	require.Len(t, copies, 2)
	candidates := loadTestCopyCandidates(t, runner, copies)
	require.Equal(t, candidates[0].TargetPath, candidates[1].TargetPath)

	// Reading either candidate completes the single item and both alternatives.
	require.NoError(t, finishRestoreCopy(t, runner, candidates[1], content))
	var pending int64
	require.NoError(t, runner.db.Model(&Copy{}).Where("status = ?", entity.CopyStatus_COPY_STATUS_PENDING).Count(&pending).Error)
	require.Zero(t, pending)
	summary, err := runner.resultSummary(ctx)
	require.NoError(t, err)
	require.EqualValues(t, 1, summary.VerifiedFiles)
	require.NoError(t, runner.db.Find(&files).Error)
	require.Len(t, files, 1)
	require.True(t, files[0].Completed)
}
