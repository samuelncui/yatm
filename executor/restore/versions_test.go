package restore

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/samuelncui/acp"
	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/library"
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
				Sha256: hash[:], Size: 6, Mode: 0600, MtimeNs: 4},
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
	var outputs []Output
	require.NoError(t, runner.db.Order("item_id").Find(&outputs).Error)
	require.Len(t, outputs, 2)
	require.Equal(t, "Unforged/VERSIONS/draft.txt", outputs[0].Path)
	require.Equal(t, "Unforged/VERSIONS/"+library.RestoredName("draft.txt", outputs[1].ItemID), outputs[1].Path)
	var selected FileSelection
	require.NoError(t, runner.db.First(&selected, file.ID).Error)
	require.Equal(t, file.ParentID, selected.ParentID)
	require.Equal(t, file.Name, selected.Name)

	// Reindexing uses the durable reservations rather than allocating additional suffixes.
	var config Config
	require.NoError(t, runner.db.First(&config, 1).Error)
	require.True(t, config.ManifestFrozen)
	require.NoError(t, runner.applySpec(ctx, config.Spec))
	var retried []Output
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
	var first Copy
	require.NoError(t, runner.db.First(&first).Error)
	second := first
	second.ID, second.ItemID, second.FileVersionID = 0, first.ItemID+1, first.FileVersionID+1
	second.TargetPath = "another-version.txt"
	require.NoError(t, runner.db.Create(&second).Error)
	session := &testReadSession{root: "/mounted"}
	source, err := session.SourcePath(first.MediaPath)
	require.NoError(t, err)
	target := runner.restoreTarget(first.TargetPath)
	require.NoError(t, os.MkdirAll(filepath.Dir(target), 0755))
	require.NoError(t, os.WriteFile(target, content, 0644))

	// A valid ACP completion cannot complete another content item merely sharing its File ID.
	require.NoError(t, runner.completeCopy(ctx, media.ID, session, &acp.StreamResult{
		ID: first.ID, Job: &acp.Job{FullPath: source, Status: acp.JobStatusFinished,
			SuccessTargets: []string{target}, Size: int64(len(content)), SHA256: hex.EncodeToString(first.Hash)},
	}))
	require.NoError(t, runner.finalizeOutputs(ctx, media.ID))
	require.NoError(t, runner.db.First(&first, first.ID).Error)
	require.NoError(t, runner.db.First(&second, second.ID).Error)
	require.Equal(t, entity.CopyStatus_COMPLETED, first.Status)
	require.Equal(t, entity.CopyStatus_PENDING, second.Status)
	stored, err := exe.GetJob(ctx, job.ID)
	require.NoError(t, err)
	require.Equal(t, entity.JobStatus_PENDING, stored.Status)
	runner.dropProgress()
	progress := runner.getProgress().ToEntity()
	require.EqualValues(t, 2, progress.TotalFiles)
	require.EqualValues(t, 1, progress.CopiedFiles)
}
