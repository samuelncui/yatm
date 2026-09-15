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

func TestCLIIndexFailureCancellationAndVisibility(t *testing.T) {
	// Establish a small published index through production binaries.
	connection, root := startBinaryInstallation(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	originals := filepath.Join(root, "originals")
	require.NoError(t, os.WriteFile(filepath.Join(originals, "kept.txt"), []byte("kept"), 0o644))
	location := new(entity.LocationReply)
	cliResult(t, ctx, connection, location, "location", "create", "--name", "Originals", "--root", originals)
	id := location.Location.Id
	job := new(entity.CreateScanJobReply)
	cliResult(t, ctx, connection, job, "analyze", "create", decimal(id))
	waitCLIJob(t, ctx, connection, job.Job.Id, false)
	before := new(entity.ListLocationEntriesReply)
	cliResult(t, ctx, connection, before, "location", "entries", decimal(id), "--name", "kept.txt")
	require.Len(t, before.Entries, 1)
	fileID := before.Entries[0].Original.FileId

	// An inaccessible root is a retryable Job failure, never a successful empty observation.
	unavailable := filepath.Join(root, "disconnected")
	require.NoError(t, os.Rename(originals, unavailable))
	cliResult(t, ctx, connection, job, "analyze", "create", decimal(id))
	output, err := connection.run(ctx, "job", "wait", decimal(job.Job.Id), "--wait-timeout", "10s", "--poll-interval", "100ms")
	require.Error(t, err)
	failed := new(entity.GetJobReply)
	require.NoError(t, decodeCLIOutput(output, failed))
	require.Equal(t, entity.JobPhase_JOB_PHASE_WAITING_FOR_INDEX_RETRY, failed.Job.Phase)
	_, err = connection.run(ctx, "location", "entries", decimal(id))
	require.Error(t, err, "an unavailable Location must not offer a cached file list")
	state := new(entity.FileStateReply)
	cliResult(t, ctx, connection, state, "file", "state", decimal(fileID))
	require.Equal(t, fileID, state.Original.FileId, "Library organization survives an unavailable original")
	require.NoError(t, os.Rename(unavailable, originals))
	cliResult(t, ctx, connection, new(entity.RetryJobIndexReply), "job", "retry-index", decimal(job.Job.Id))
	waitCLIJob(t, ctx, connection, job.Job.Id, false)

	// Cancel a forced real read before publication; the sparse input avoids allocating gigabytes of disk blocks.
	large := filepath.Join(originals, "unfinished.bin")
	require.NoError(t, os.WriteFile(large, nil, 0o600))
	require.NoError(t, os.Truncate(large, 2<<30))
	cliResult(t, ctx, connection, job, "analyze", "create", decimal(id), "--mode", "force")
	cliResult(t, ctx, connection, new(entity.CancelJobReply), "job", "cancel", decimal(job.Job.Id))
	output, err = connection.run(ctx, "job", "wait", decimal(job.Job.Id), "--wait-timeout", "10s", "--poll-interval", "100ms")
	require.Error(t, err)
	require.NoError(t, decodeCLIOutput(output, failed))
	require.Equal(t, entity.JobPhase_JOB_PHASE_WAITING_FOR_INDEX_RETRY, failed.Job.Phase)
	retained := new(entity.ListLocationEntriesReply)
	cliResult(t, ctx, connection, retained, "location", "entries", decimal(id), "--name", "unfinished.bin")
	require.Len(t, retained.Entries, 1)
	require.Nil(t, retained.Entries[0].Original, "cancelled analysis does not publish, but real files remain visible")
	require.NoError(t, os.Remove(large))
	cliResult(t, ctx, connection, new(entity.RetryJobIndexReply), "job", "retry-index", decimal(job.Job.Id))
	waitCLIJob(t, ctx, connection, job.Job.Id, false)

	// Metadata-only admission has no invented content identity and remains hidden only from the saved-only view.
	require.NoError(t, os.WriteFile(filepath.Join(originals, "unknown.txt"), []byte("not hashed"), 0o644))
	cliResult(t, ctx, connection, job, "analyze", "create", decimal(id), "--mode", "basic")
	waitCLIJob(t, ctx, connection, job.Job.Id, false)
	results := new(entity.FileSearchReply)
	cliResult(t, ctx, connection, results, "file", "search", "unknown.txt", "--scope", "all")
	require.Len(t, results.Results, 1)
	unknown := results.Results[0].File.Id
	cliResult(t, ctx, connection, state, "file", "state", decimal(unknown))
	require.Empty(t, state.Original.Signature)
	require.Equal(t, entity.ContentCoverage_CONTENT_UNKNOWN, state.Coverage)
	cliResult(t, ctx, connection, results, "file", "search", "unknown.txt", "--scope", "saved")
	require.Empty(t, results.Results)
	cliResult(t, ctx, connection, new(entity.FileMetadataEditReply), "file", "metadata", decimal(unknown), "--add-tag", "keep", "--note", "saved independently of visibility")
	snapshot := filepath.Join(root, "complete.jsonl")
	_, err = connection.run(ctx, "library", "export", "--output", snapshot)
	require.NoError(t, err)
	_, err = connection.run(ctx, "library", "import", "--input", snapshot, "--confirm")
	require.NoError(t, err)
	detail := new(entity.FileGetReply)
	cliResult(t, ctx, connection, detail, "file", "get", decimal(unknown), "--scope", "saved")
	require.Equal(t, "saved independently of visibility", detail.File.Note)
	require.Equal(t, []string{"keep"}, detail.File.Tags)

	// Configuration edits use the inspected revision and never scan implicitly.
	cliResult(t, ctx, connection, location, "location", "get", decimal(id))
	ignoreFile := filepath.Join(root, "ignore")
	require.NoError(t, os.WriteFile(ignoreFile, []byte("# retained text\n/unknown.txt\n"), 0o600))
	cliResult(t, ctx, connection, location, "location", "update", decimal(id), "--revision", decimal(location.Location.Revision), "--name", "Renamed", "--root", originals, "--ignore-file", ignoreFile)
	require.Equal(t, "# retained text\n/unknown.txt\n", location.Location.Ignore.Text)
	cliResult(t, ctx, connection, location, "location", "confirm", decimal(id), "--revision", decimal(location.Location.Revision))
	cliResult(t, ctx, connection, job, "analyze", "create", decimal(id))
	waitCLIJob(t, ctx, connection, job.Job.Id, false)
	cliResult(t, ctx, connection, retained, "location", "entries", decimal(id), "--name", "unknown.txt")
	require.Len(t, retained.Entries, 1, "Ignore controls collection, not real directory visibility")
	cliResult(t, ctx, connection, location, "location", "get", decimal(id))
	cliResult(t, ctx, connection, new(entity.DeleteLocationReply), "location", "delete", decimal(id), "--revision", decimal(location.Location.Revision), "--confirm")
	require.FileExists(t, filepath.Join(originals, "kept.txt"))
	cliResult(t, ctx, connection, detail, "file", "get", decimal(unknown))
	require.Equal(t, []string{"keep"}, detail.File.Tags)
}
