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

func TestCLILiveAdmissionAnalyzeAndBackup(t *testing.T) {
	// Browse and back up a real unadmitted file using only the production server and CLI.
	connection, root := startBinaryInstallation(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	originals := filepath.Join(root, "originals")
	require.NoError(t, os.WriteFile(filepath.Join(originals, "fresh.txt"), []byte("new unmanaged bytes"), 0o640))
	location := new(entity.LocationReply)
	cliResult(t, ctx, connection, location, "location", "create", "--name", "Live files", "--root", originals)
	id := location.Location.Id
	entries := new(entity.ListLocationEntriesReply)
	cliResult(t, ctx, connection, entries, "location", "entries", decimal(id), "--name", "fresh.txt")
	require.Len(t, entries.Entries, 1)
	require.Nil(t, entries.Entries[0].Original)
	require.NotNil(t, entries.Entries[0].Reference.Facts)
	inspection := new(entity.InspectSelectionReply)
	cliResult(t, ctx, connection, inspection, "file", "inspect-selection", "--location", decimal(id)+":fresh.txt")
	require.EqualValues(t, 1, inspection.Files)
	require.EqualValues(t, len("new unmanaged bytes"), inspection.Bytes)
	volume := new(entity.VolumeInitializeReply)
	cliResult(t, ctx, connection, volume, "volume", "initialize", filepath.Join(root, "volumes", "disk"), "--name", "Archive", "--type", "hdd")
	archive := new(entity.CreateArchiveJobReply)
	cliResult(t, ctx, connection, archive, "archive", "create", "--location", decimal(id)+":fresh.txt")
	waitCLIJob(t, ctx, connection, archive.Job.Id, true)
	cliResult(t, ctx, connection, new(entity.WriteArchiveMediaReply), "archive", "write", "volume", decimal(archive.Job.Id), "--uuid", volume.Media.Identity)
	waitCLIJob(t, ctx, connection, archive.Job.Id, false)
	files := new(entity.ListArchiveJobFilesReply)
	cliResult(t, ctx, connection, files, "archive", "files", decimal(archive.Job.Id))
	require.Len(t, files.Items, 1)
	bytes, err := os.ReadFile(filepath.Join(root, "volumes", "disk", files.Items[0].File.MediaPath))
	require.NoError(t, err)
	require.Equal(t, "new unmanaged bytes", string(bytes))
	cliResult(t, ctx, connection, entries, "location", "entries", decimal(id), "--name", "fresh.txt")
	fileID := entries.Entries[0].Original.FileId
	versions := new(entity.ListFileVersionsReply)
	cliResult(t, ctx, connection, versions, "file", "versions", decimal(fileID))
	require.Len(t, versions.Versions, 1)

	// Multiple live selection roots resolve every path owner before native fallback, without Analyze.
	require.NoError(t, os.Rename(filepath.Join(originals, "fresh.txt"), filepath.Join(originals, "moved.txt")))
	require.NoError(t, os.WriteFile(filepath.Join(originals, "fresh.txt"), []byte("replacement at the managed path"), 0o640))
	prepared := new(entity.CreateArchiveJobReply)
	cliResult(t, ctx, connection, prepared, "archive", "create", "--location", decimal(id)+":moved.txt", "--location", decimal(id)+":fresh.txt")
	waitCLIJob(t, ctx, connection, prepared.Job.Id, true)
	cliResult(t, ctx, connection, entries, "location", "entries", decimal(id), "--name", "fresh.txt")
	require.Equal(t, fileID, entries.Entries[0].Original.FileId)
	cliResult(t, ctx, connection, entries, "location", "entries", decimal(id), "--name", "moved.txt")
	require.NotEqual(t, fileID, entries.Entries[0].Original.FileId)
	cliResult(t, ctx, connection, files, "archive", "files", decimal(prepared.Job.Id))
	require.Len(t, files.Items, 2)

	// A successful selected scope publishes even when another selected scope cannot be read.
	require.NoError(t, os.WriteFile(filepath.Join(originals, "basic.txt"), []byte("metadata only"), 0o600))
	analysis := new(entity.CreateScanJobReply)
	cliResult(t, ctx, connection, analysis, "analyze", "create", decimal(id), "--mode", "basic", "--path", "basic.txt", "--path", "missing")
	output, err := connection.run(ctx, "job", "wait", decimal(analysis.Job.Id), "--wait-timeout", "30s", "--poll-interval", "100ms")
	require.Error(t, err)
	job := new(entity.GetJobReply)
	require.NoError(t, decodeCLIOutput(output, job))
	require.Equal(t, entity.JobPhase_JOB_PHASE_WAITING_FOR_INDEX_RETRY, job.Job.Phase)
	progress := new(entity.GetScanJobProgressReply)
	cliResult(t, ctx, connection, progress, "analyze", "progress", decimal(analysis.Job.Id))
	require.Len(t, progress.Scopes, 2)
	require.Positive(t, progress.Scopes[0].PublishedAtMs)
	require.NotEmpty(t, progress.Scopes[1].Error)
	cliResult(t, ctx, connection, entries, "location", "entries", decimal(id), "--name", "basic.txt")
	basicID := entries.Entries[0].Original.FileId
	require.Empty(t, entries.Entries[0].Original.Signature)
	require.NoError(t, os.Mkdir(filepath.Join(originals, "missing"), 0o755))
	cliResult(t, ctx, connection, new(entity.RetryJobIndexReply), "job", "retry-index", decimal(analysis.Job.Id))
	waitCLIJob(t, ctx, connection, analysis.Job.Id, false)
	cliResult(t, ctx, connection, entries, "location", "entries", decimal(id), "--name", "basic.txt")
	require.Equal(t, basicID, entries.Entries[0].Original.FileId)

	// Enabling collection starts one basic Job; browse remains pure until explicit collection.
	changed := setFixtureAutoCollect(t, ctx, connection, true)
	require.Empty(t, changed.CollectionErrors)
	require.Len(t, changed.CollectionJobIds, 1)
	waitCLIJob(t, ctx, connection, changed.CollectionJobIds[0], false)
	require.NoError(t, os.WriteFile(filepath.Join(originals, "arrival.txt"), []byte("arrived after collection"), 0o600))
	cliResult(t, ctx, connection, entries, "location", "entries", decimal(id), "--name", "arrival.txt")
	require.Nil(t, entries.Entries[0].Original)
	cliResult(t, ctx, connection, new(entity.CollectFilesReply), "files", "collect", "--location", decimal(id)+":arrival.txt", "--automatic")
	cliResult(t, ctx, connection, entries, "location", "entries", decimal(id), "--name", "arrival.txt")
	require.Positive(t, entries.Entries[0].Original.FileId)
	require.Empty(t, entries.Entries[0].Original.Signature)
}
