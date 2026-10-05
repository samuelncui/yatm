package restore

import (
	"context"
	"testing"

	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/internal/library"
	"github.com/stretchr/testify/require"
)

func TestFrozenRestoreSurvivesMergedAndRemovedVersion(t *testing.T) {
	// A Restore manifest is immutable even when later identical-file cleanup changes catalog history.
	ctx := context.Background()
	exe, lib := setupTestExecutor(t)
	_, source := createMediaFile(t, lib, newTapeMedia("IDENTICAL"), ".", "source.txt", []byte("saved"), nil)
	version, err := lib.LatestFileVersion(ctx, source.ID)
	require.NoError(t, err)
	first, last := version.FirstArchivedAtNS, version.LastArchivedAtNS
	job := createRestoreJob(t, exe, source.ID)
	waitIndexed(t, exe, job.ID)
	value, err := exe.GetJobRunner(ctx, job.ID)
	require.NoError(t, err)
	runner := value.(*jobRestoreRunner)
	var frozen Copy
	require.NoError(t, runner.db.First(&frozen).Error)
	candidate := loadTestCopyCandidate(t, runner, &frozen)
	require.Equal(t, source.ID, candidate.FileID)
	require.Equal(t, version.ID, candidate.FileVersionID)

	target := &library.File{Name: "merged.txt", Kind: entity.FileKind_FILE_KIND_REGULAR}
	require.NoError(t, lib.SaveFile(ctx, target))
	require.NoError(t, lib.MergeFiles(ctx, target.ID, []int64{source.ID}))
	moved, err := lib.GetFileVersion(ctx, version.ID)
	require.NoError(t, err)
	require.Equal(t, target.ID, moved.FileID)
	require.Equal(t, first, moved.FirstArchivedAtNS)
	require.Equal(t, last, moved.LastArchivedAtNS)
	_, err = lib.RemoveFileVersion(ctx, target.ID, version.ID, false)
	require.NoError(t, err)
	_, err = lib.GetFileVersion(ctx, version.ID)
	require.Error(t, err)

	// The real completion path publishes a distinct recovered File from the frozen item facts.
	require.NoError(t, finishRestoreCopy(t, runner, candidate, []byte("saved")))
	var result File
	require.NoError(t, runner.db.First(&result, frozen.ItemID).Error)
	require.Equal(t, source.ID, result.FileID)
	require.Equal(t, version.ID, result.FileVersionID)
	require.True(t, result.Completed)
	require.NotZero(t, result.ResultFileID)
	require.NotEqual(t, source.ID, result.ResultFileID)
	require.NotEqual(t, target.ID, result.ResultFileID)

	versions, _, err := lib.ListFileVersions(ctx, result.ResultFileID, 0, 10)
	require.NoError(t, err)
	require.Len(t, versions, 1)
	require.Equal(t, first, versions[0].FirstArchivedAtNS)
	require.Equal(t, last, versions[0].LastArchivedAtNS)
}
