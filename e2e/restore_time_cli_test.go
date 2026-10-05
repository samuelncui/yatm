//go:build e2e

package e2e

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/samuelncui/yatm/entity"
	"github.com/stretchr/testify/require"
)

func TestCLIRestoreRecordedTimePolicy(t *testing.T) {
	// All business steps use production binaries; only source-file edits are fixture operations.
	connection, root := startBinaryInstallation(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	source := new(entity.CreateLocationResponse)
	cliResult(t, ctx, connection, source, "location", "create", "--name", "History", "--root", filepath.Join(root, "originals"))
	target := new(entity.CreateLocationResponse)
	cliResult(t, ctx, connection, target, "location", "create", "--name", "Restored", "--root", filepath.Join(root, "restore"), "--restore-target")
	volume := new(entity.InitializeVolumeResponse)
	cliResult(t, ctx, connection, volume, "volume", "initialize", filepath.Join(root, "volumes", "disk"), "--name", "History disk", "--type", "hdd")
	entry := new(entity.FilesDetail)
	var cutoff int64
	var versionA, versionC, fileID, parentID, laterID int64

	// A/B/A/C/A must retain the middle A save even after its last-save time advances.
	for index, content := range []string{"alpha", "bravo revision", "alpha", "charlie revision", "alpha"} {
		require.NoError(t, os.WriteFile(filepath.Join(root, "originals", "history.txt"), []byte(content), 0644))
		job := new(entity.CreateArchiveJobResponse)
		cliResult(t, ctx, connection, job, "archive", "create", "--location", decimal(source.Location.Id)+":history.txt")
		waitCLIJob(t, ctx, connection, job.Job.Id, true)
		cliResult(t, ctx, connection, new(entity.WriteArchiveMediaResponse), "archive", "write", "volume", decimal(job.Job.Id), "--uuid", volume.Media.Identity)
		waitCLIJob(t, ctx, connection, job.Job.Id, false)
		cliResult(t, ctx, connection, entry, "files", "get", "--location-id", decimal(source.Location.Id), "--path", "history.txt")
		fileID, parentID = *entry.Entry.AssociatedFileId, entry.Organization.Parent.GetFileId()
		versions := new(entity.ListFileVersionsResponse)
		cliResult(t, ctx, connection, versions, "files", "versions", decimal(fileID))
		switch index {
		case 0:
			versionA = versions.Versions[0].Id
		case 2:
			require.Equal(t, versionA, versions.Versions[0].Id)
			cutoff = versions.Versions[0].GetLastArchivedAtNs()
		case 3:
			versionC = versions.Versions[0].Id
		}
	}
	versions := new(entity.ListFileVersionsResponse)
	cliResult(t, ctx, connection, versions, "files", "versions", decimal(fileID))
	require.Len(t, versions.Versions, 3, "content versions stay deduplicated")

	// A later-only File stays selected but has no eligible version at the requested cutoff.
	require.NoError(t, os.WriteFile(filepath.Join(root, "originals", "later.txt"), []byte("later-only content"), 0644))
	job := new(entity.CreateArchiveJobResponse)
	cliResult(t, ctx, connection, job, "archive", "create", "--location", decimal(source.Location.Id)+":later.txt")
	waitCLIJob(t, ctx, connection, job.Job.Id, true)
	cliResult(t, ctx, connection, new(entity.WriteArchiveMediaResponse), "archive", "write", "volume", decimal(job.Job.Id), "--uuid", volume.Media.Identity)
	waitCLIJob(t, ctx, connection, job.Job.Id, false)
	cliResult(t, ctx, connection, entry, "files", "get", "--location-id", decimal(source.Location.Id), "--path", "later.txt")
	laterID = *entry.Entry.AssociatedFileId
	before := time.Unix(0, cutoff).UTC().Format(time.RFC3339Nano)

	// Overlapping roots retain one automatic match and report the later-only File once.
	review := new(entity.SelectionInspectionResult)
	cliResult(t, ctx, connection, review, "restore", "estimate", "--file-id", decimal(parentID),
		"--file-id", decimal(fileID), "--file-id", decimal(laterID), "--before", before)
	require.EqualValues(t, 1, review.UnmatchedVersionCount)
	require.Zero(t, review.SkippedVersionCount)
	require.Zero(t, review.MissingCopyCount, "date mismatch is not a missing archive copy")
	require.EqualValues(t, 1, review.FileCount)
	for _, result := range review.ResolvedVersions {
		if result.FileId == fileID {
			require.Equal(t, versionA, result.Version.Id)
			require.Equal(t, cutoff, result.GetArchivedAtNs())
		}
	}

	// Explicit custom content overrides the automatic File even when its date is later.
	cliResult(t, ctx, connection, review, "restore", "estimate", "--file-id", decimal(fileID),
		"--version-id", decimal(versionC), "--before", before)
	require.EqualValues(t, 1, review.FileCount)
	require.Zero(t, review.UnmatchedVersionCount)
	require.Len(t, review.ResolvedVersions, 1)
	require.Equal(t, versionC, review.ResolvedVersions[0].Version.Id)

	// A nested override stays effective when the selected input is its containing directory.
	cliResult(t, ctx, connection, review, "restore", "estimate", "--file-id", decimal(parentID),
		"--version-id", decimal(versionC), "--before", before, "--skip-unmatched-versions")
	require.EqualValues(t, 1, review.FileCount)
	require.EqualValues(t, 1, review.SkippedVersionCount)
	require.Len(t, review.ResolvedVersions, 1)
	require.Equal(t, versionC, review.ResolvedVersions[0].Version.Id)

	// Explicit skipping permits only the matching content, retaining the chosen version in the Job.
	cliResult(t, ctx, connection, review, "restore", "estimate", "--file-id", decimal(parentID),
		"--before", before, "--skip-unmatched-versions")
	require.EqualValues(t, 1, review.SkippedVersionCount)
	restored := new(entity.CreateRestoreJobResponse)
	cliResult(t, ctx, connection, restored, "restore", "create", "--file-id", decimal(parentID), "--before", before,
		"--skip-unmatched-versions", "--target-location", decimal(target.Location.Id), "--directory", "dated")
	waitCLIJob(t, ctx, connection, restored.Job.Id, true)
	manifest := new(entity.ListRestoreJobFilesResponse)
	cliResult(t, ctx, connection, manifest, "restore", "files", decimal(restored.Job.Id), "--media-id", decimal(volume.Media.Id))
	require.Len(t, manifest.Items, 1)
	require.Equal(t, versionA, manifest.Items[0].File.FileVersionId)

	// Confirm the frozen choice through actual CLI restore and output bytes.
	cliResult(t, ctx, connection, new(entity.RestoreMediaResponse), "restore", "run", "volume", decimal(restored.Job.Id), "--uuid", volume.Media.Identity)
	waitCLIJob(t, ctx, connection, restored.Job.Id, false)
	data, err := os.ReadFile(filepath.Join(root, "restore", "dated", "Unforged", "History", "history.txt"))
	require.NoError(t, err)
	require.Equal(t, "alpha", string(data))
	require.NoFileExists(t, filepath.Join(root, "restore", "dated", "Unforged", "History", "later.txt"))
}
