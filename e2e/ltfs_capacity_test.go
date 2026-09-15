//go:build e2e

package e2e

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/samuelncui/yatm/apis"
	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/executor"
	_ "github.com/samuelncui/yatm/executor/archive"
	_ "github.com/samuelncui/yatm/executor/restore"
	"github.com/samuelncui/yatm/library"
	"github.com/samuelncui/yatm/resource"
	"github.com/stretchr/testify/require"
)

func TestLTFSFullTapeSpansMediaAndRestores(t *testing.T) {
	if os.Getenv("YATM_E2E_LTFS") != "1" {
		t.Skip("set YATM_E2E_LTFS=1 to run the LTFS file-backend E2E test")
	}
	for _, command := range []string{"mkltfs", "ltfs", "fusermount"} {
		_, err := exec.LookPath(command)
		require.NoErrorf(t, err, "%s is required", command)
	}

	// Build an isolated two-cartridge installation using the official file backend.
	root := t.TempDir()
	executorDB, err := resource.OpenSQLite(filepath.Join(root, "executor.db"))
	require.NoError(t, err)
	libraryDB, err := resource.OpenSQLite(filepath.Join(root, "library.db"))
	require.NoError(t, err)
	lib := library.New(libraryDB)
	require.NoError(t, lib.AutoMigrate())
	paths := executor.Paths{
		Work: filepath.Join(root, "work"), Source: filepath.Join(root, "source"), Target: filepath.Join(root, "target"),
	}
	firstDevice := filepath.Join(root, "tapes", "FUL001")
	secondDevice := filepath.Join(root, "tapes", "FUL002")
	scripts := executor.Scripts{
		Encrypt: testScript(t, "encrypt-noop.sh"), Mkfs: testScript(t, "mkfs-file-capacity.sh"),
		Mount: testScript(t, "mount-file.sh"), Umount: testScript(t, "umount-file.sh"),
		ReadInfo: testScript(t, "read-info-file.sh"),
	}
	exe := executor.New(executorDB, lib, []string{firstDevice, secondDevice}, paths, scripts, nil)
	require.NoError(t, exe.AutoMigrate())
	require.NoError(t, exe.ReconcileStorage(context.Background()))
	require.NoError(t, os.MkdirAll(filepath.Join(paths.Source, "dataset"), 0o755))
	files := map[string]int64{
		"a.bin": 1200 * 1024 * 1024,
		"b.bin": 1200 * 1024 * 1024,
		"c.bin": 1200 * 1024 * 1024,
	}
	expectedHashes := make(map[string][]byte, len(files))
	for name, size := range files {
		filename := filepath.Join(paths.Source, "dataset", name)
		require.NoError(t, os.WriteFile(filename, []byte(name), 0o644))
		require.NoError(t, os.Truncate(filename, size))
		expectedHashes[name], err = sha256File(filename)
		require.NoError(t, err)
	}

	// Serve the public API and create one Archive Job whose ordered files cannot fit on one cartridge.
	api := apis.New(lib, exe)
	conn := serveCLI(t, api, exe)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	jobClient := entity.NewJobServiceClient(conn)
	archiveClient := entity.NewArchiveJobServiceClient(conn)
	restoreClient := entity.NewRestoreJobServiceClient(conn)
	created, err := archiveClient.Create(ctx, &entity.CreateArchiveJobRequest{
		Spec: &entity.ArchiveJobSpec{Selections: indexedSelections(t, ctx, conn, paths.Source, "dataset")},
	})
	require.NoError(t, err)
	archiveID := created.Job.Id
	waitForPendingJob(t, ctx, jobClient, archiveID)

	// Fill the first Tape, then assert that only its continuous verified prefix was published.
	_, err = archiveClient.WriteMedia(ctx, &entity.WriteArchiveMediaRequest{
		Id: archiveID,
		Target: (&entity.ArchiveTapeTarget{
			Device: firstDevice, Barcode: "FUL001", Name: "Full Tape 1",
			Mode: entity.ArchiveTapeWriteMode_ARCHIVE_TAPE_WRITE_MODE_FORMAT,
		}).Pack(),
	})
	require.NoError(t, err)
	require.Eventually(t, func() bool { return !exe.IsRunning(archiveID) }, time.Minute, 100*time.Millisecond)
	pendingJob, err := jobClient.Get(ctx, &entity.GetJobRequest{Id: archiveID})
	require.NoError(t, err)
	require.Equal(t, entity.JobStatus_PENDING, pendingJob.Job.Status)
	require.Equal(t, entity.JobPhase_JOB_PHASE_WAITING_FOR_MEDIA, pendingJob.Job.Phase)
	firstReply, err := archiveClient.ListFiles(ctx, &entity.ListArchiveJobFilesRequest{Id: archiveID, Limit: 10})
	require.NoError(t, err)
	require.Len(t, firstReply.Items, len(files))
	submitted := 0
	var submittedBytes int64
	prefixEnded := false
	for _, item := range firstReply.Items {
		if item.Status == entity.CopyStatus_SUBMITTED {
			require.False(t, prefixEnded)
			require.NotNil(t, item.MediaId)
			submitted++
			submittedBytes += item.Size
			continue
		}
		prefixEnded = true
		require.Equal(t, entity.CopyStatus_PENDING, item.Status)
		require.Nil(t, item.MediaId)
		require.Empty(t, item.File.MediaPath)
	}
	require.Positive(t, submitted)
	require.Less(t, submitted, len(files))

	// Verify the first Tape checkpoint contains exactly the submitted prefix.
	tapes, err := lib.MGetTapeByBarcode(ctx, "FUL001")
	require.NoError(t, err)
	firstTape := tapes["FUL001"]
	require.NotNil(t, firstTape)
	require.Equal(t, library.TapeFormatLTFSV1, firstTape.Format)
	firstPositions, err := lib.ListMediaFilePositions(ctx, firstTape.ID, "", len(files))
	require.NoError(t, err)
	require.Len(t, firstPositions, submitted)
	positionPaths := make(map[string]struct{}, len(firstPositions))
	for _, position := range firstPositions {
		positionPaths[position.Path] = struct{}{}
	}
	for _, item := range firstReply.Items {
		_, published := positionPaths[item.File.MediaPath]
		require.Equal(t, item.Status == entity.CopyStatus_SUBMITTED, published, item.File.TargetPath)
		if item.Status == entity.CopyStatus_SUBMITTED {
			require.Equal(t, firstTape.ID, *item.MediaId)
		}
	}

	// Count only the durable prefix in progress and the per-Tape report.
	progressReply, err := archiveClient.GetProgress(ctx, &entity.GetArchiveJobProgressRequest{Id: archiveID})
	require.NoError(t, err)
	require.Equal(t, int64(submitted), progressReply.Progress.CopiedFiles)
	require.Equal(t, submittedBytes, progressReply.Progress.CopiedBytes)
	require.Equal(t, int64(len(files)), progressReply.Progress.TotalFiles)
	reportData, err := os.ReadFile(filepath.Join(
		paths.Work, "jobs", fmt.Sprint(archiveID), "tapes", "FUL001", "yatm-report.json",
	))
	require.NoError(t, err)
	var report struct {
		MediaID          int64  `json:"media_id"`
		FileCount        int64  `json:"file_count"`
		Bytes            int64  `json:"bytes"`
		TerminationError string `json:"termination_error"`
	}
	require.NoError(t, json.Unmarshal(reportData, &report))
	require.Equal(t, firstTape.ID, report.MediaID)
	require.Equal(t, int64(submitted), report.FileCount)
	require.Equal(t, submittedBytes, report.Bytes)
	require.NotEmpty(t, report.TerminationError)
	requireArchiveNoSpaceCheckpointLog(
		t, ctx, jobClient, archiveID, firstTape.ID, int64(submitted), submittedBytes,
	)

	// Write every remaining item to the second Tape and verify that the Job spans exactly two Media.
	_, err = archiveClient.WriteMedia(ctx, &entity.WriteArchiveMediaRequest{
		Id: archiveID,
		Target: (&entity.ArchiveTapeTarget{
			Device: secondDevice, Barcode: "FUL002", Name: "Full Tape 2",
			Mode: entity.ArchiveTapeWriteMode_ARCHIVE_TAPE_WRITE_MODE_FORMAT,
		}).Pack(),
	})
	require.NoError(t, err)
	waitForCompletedJob(t, ctx, jobClient, archiveID)
	completed, err := archiveClient.ListFiles(ctx, &entity.ListArchiveJobFilesRequest{Id: archiveID, Limit: 10})
	require.NoError(t, err)
	mediaIDs := make(map[int64]struct{})
	for _, item := range completed.Items {
		require.Equal(t, entity.CopyStatus_SUBMITTED, item.Status)
		require.NotNil(t, item.MediaId)
		mediaIDs[*item.MediaId] = struct{}{}
	}
	require.Len(t, mediaIDs, 2)
	positions := make(map[int64]map[string]*library.Position, len(mediaIDs))
	for mediaID := range mediaIDs {
		page, err := lib.ListMediaFilePositions(ctx, mediaID, "", len(files))
		require.NoError(t, err)
		positions[mediaID] = make(map[string]*library.Position, len(page))
		for _, position := range page {
			positions[mediaID][position.Path] = position
		}
	}
	fileIDs := make([]int64, 0, len(completed.Items))
	itemsByMedia := make(map[int64][]*entity.ArchiveItem, len(mediaIDs))
	positionsByTarget := make(map[int64]map[string]*library.Position, len(mediaIDs))
	for _, item := range completed.Items {
		position := positions[*item.MediaId][item.File.MediaPath]
		require.NotNil(t, position)
		require.Positive(t, item.File.Expected.FileId)
		fileIDs = append(fileIDs, item.File.Expected.FileId)
		itemsByMedia[*item.MediaId] = append(itemsByMedia[*item.MediaId], item)
		if positionsByTarget[*item.MediaId] == nil {
			positionsByTarget[*item.MediaId] = make(map[string]*library.Position)
		}
		positionsByTarget[*item.MediaId][item.File.TargetPath] = position
	}
	for mediaID, items := range itemsByMedia {
		requireTapeWriteOrder(t, items, positionsByTarget[mediaID])
	}

	// Restore the cross-Media logical set by loading each virtual Tape in turn.
	restored, err := restoreClient.Create(ctx, &entity.CreateRestoreJobRequest{
		Spec: &entity.RestoreJobSpec{Destination: restoreDestination(t, ctx, conn, paths.Target), Selections: librarySelections(fileIDs...)},
	})
	require.NoError(t, err)
	restoreID := restored.Job.Id
	waitForPendingJob(t, ctx, jobClient, restoreID)
	_, err = restoreClient.RestoreMedia(ctx, &entity.RestoreMediaRequest{
		Id: restoreID, Target: (&entity.ReadTapeTarget{Device: firstDevice}).Pack(),
	})
	require.NoError(t, err)
	require.Eventually(t, func() bool { return !exe.IsRunning(restoreID) }, time.Minute, 100*time.Millisecond)
	_, err = restoreClient.RestoreMedia(ctx, &entity.RestoreMediaRequest{
		Id: restoreID, Target: (&entity.ReadTapeTarget{Device: secondDevice}).Pack(),
	})
	require.NoError(t, err)
	waitForCompletedJob(t, ctx, jobClient, restoreID)
	for _, item := range completed.Items {
		actual, err := sha256File(filepath.Join(paths.Target, item.File.TargetPath))
		require.NoError(t, err)
		require.Equal(t, expectedHashes[filepath.Base(item.File.TargetPath)], actual, item.File.TargetPath)
	}
}

func sha256File(filename string) ([]byte, error) {
	file, err := os.Open(filename)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return nil, err
	}
	return hash.Sum(nil), nil
}
