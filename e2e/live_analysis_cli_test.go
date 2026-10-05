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

func TestCLILiveAdmissionAnalyzeAndArchive(t *testing.T) {
	// Live reads remain pure; a selected Archive establishes the File association.
	connection, root := startBinaryInstallation(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	originals := filepath.Join(root, "originals")
	require.NoError(t, os.WriteFile(filepath.Join(originals, "fresh.txt"), []byte("new unmanaged bytes"), 0o640))
	location := new(entity.CreateLocationResponse)
	cliResult(t, ctx, connection, location, "location", "create", "--name", "Live files", "--root", originals)
	id := decimal(location.Location.Id)
	jobs := new(entity.ListJobsResponse)
	cliResult(t, ctx, connection, jobs, "job", "list")
	require.Empty(t, jobs.Jobs, "Location registration does not collect or create a Job")
	entry := new(entity.FilesDetail)
	cliResult(t, ctx, connection, entry, "files", "get", "--location-id", id, "--path", "fresh.txt")
	require.Nil(t, entry.Entry.AssociatedFileId)
	inspection := new(entity.SelectionInspectionResult)
	cliResult(t, ctx, connection, inspection, "archive", "estimate", "--location", id+":fresh.txt")
	require.EqualValues(t, 1, inspection.FileCount)
	require.EqualValues(t, len("new unmanaged bytes"), inspection.TotalBytes)
	volume := new(entity.InitializeVolumeResponse)
	cliResult(t, ctx, connection, volume, "volume", "initialize", filepath.Join(root, "volumes", "disk"), "--name", "Archive", "--type", "hdd")
	archive := new(entity.CreateArchiveJobResponse)
	cliResult(t, ctx, connection, archive, "archive", "create", "--location", id+":fresh.txt")
	waitCLIJob(t, ctx, connection, archive.Job.Id, true)
	cliResult(t, ctx, connection, new(entity.WriteArchiveMediaResponse), "archive", "write", "volume", decimal(archive.Job.Id), "--uuid", volume.Media.Identity)
	waitCLIJob(t, ctx, connection, archive.Job.Id, false)
	files := new(entity.ListArchiveJobFilesResponse)
	cliResult(t, ctx, connection, files, "archive", "files", decimal(archive.Job.Id))
	require.Len(t, files.Items, 1)
	bytes, err := os.ReadFile(filepath.Join(root, "volumes", "disk", files.Items[0].File.MediaPath))
	require.NoError(t, err)
	require.Equal(t, "new unmanaged bytes", string(bytes))
	cliResult(t, ctx, connection, entry, "files", "get", "--location-id", id, "--path", "fresh.txt")
	require.NotNil(t, entry.Entry.AssociatedFileId)
	versions := new(entity.ListFileVersionsResponse)
	cliResult(t, ctx, connection, versions, "files", "versions", decimal(filesEntryID(entry.Entry)))
	require.Len(t, versions.Versions, 1)
	fileID := filesEntryID(entry.Entry)

	// Resolve all live path owners before native-identity fallback across selection roots.
	require.NoError(t, os.Rename(filepath.Join(originals, "fresh.txt"), filepath.Join(originals, "moved.txt")))
	require.NoError(t, os.WriteFile(filepath.Join(originals, "fresh.txt"), []byte("replacement at the managed path"), 0o640))
	prepared := new(entity.CreateArchiveJobResponse)
	cliResult(t, ctx, connection, prepared, "archive", "create", "--location", id+":moved.txt", "--location", id+":fresh.txt")
	waitCLIJob(t, ctx, connection, prepared.Job.Id, true)
	moved := new(entity.FilesDetail)
	cliResult(t, ctx, connection, moved, "files", "get", "--location-id", id, "--path", "moved.txt")
	require.NotEqual(t, fileID, filesEntryID(moved.Entry))
	cliResult(t, ctx, connection, entry, "files", "get", "--location-id", id, "--path", "fresh.txt")
	require.Equal(t, fileID, filesEntryID(entry.Entry))
	preparedFiles := new(entity.ListArchiveJobFilesResponse)
	cliResult(t, ctx, connection, preparedFiles, "archive", "files", decimal(prepared.Job.Id))
	require.Len(t, preparedFiles.Items, 2)

	// A later arrival remains unassociated until one explicit Files mutation or selection admits it.
	require.NoError(t, os.WriteFile(filepath.Join(originals, "arrival.txt"), []byte("arrived later"), 0o600))
	arrival := new(entity.FilesDetail)
	cliResult(t, ctx, connection, arrival, "files", "get", "--location-id", id, "--path", "arrival.txt")
	require.Nil(t, arrival.Entry.AssociatedFileId)
	cliResult(t, ctx, connection, jobs, "job", "list")
	require.Len(t, jobs.Jobs, 2, "browsing new live files does not create a collection Job")
}
