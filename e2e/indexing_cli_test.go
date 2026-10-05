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
	// A failed Scan leaves Files browsing live and a successful Scan publishes selected originals.
	connection, root := startBinaryInstallation(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	originals := filepath.Join(root, "originals")
	require.NoError(t, os.WriteFile(filepath.Join(originals, "kept.txt"), []byte("kept"), 0o644))
	location := new(entity.CreateLocationResponse)
	cliResult(t, ctx, connection, location, "location", "create", "--name", "Originals", "--root", originals)
	id := decimal(location.Location.Id)
	job := new(entity.CreateScanJobResponse)
	cliResult(t, ctx, connection, job, "scan", "create", "--location-id", id, "--path", "kept.txt", "--signature", "fill-missing", "--result", "originals")
	waitCLIJob(t, ctx, connection, job.Job.Id, false)
	page := new(entity.SearchFilesResponse)
	cliResult(t, ctx, connection, page, "ls", "--location-id", id, "--path", "", "--query", "name:kept.txt")
	require.Len(t, page.Entries, 1)
	detail := new(entity.FilesDetail)
	cliResult(t, ctx, connection, detail, "files", "get", "--location-id", id, "--path", "kept.txt")
	fileID := filesEntryID(detail.Entry)
	require.Positive(t, fileID)

	// An inaccessible root fails the Scan preparation for good, and ordinary browsing never uses cached rows.
	unavailable := filepath.Join(root, "disconnected")
	require.NoError(t, os.Rename(originals, unavailable))
	cliResult(t, ctx, connection, job, "scan", "create", "--location-id", id, "--signature", "fill-missing", "--result", "originals")
	output, err := connection.run(ctx, "job", "wait", decimal(job.Job.Id), "--wait-timeout", "10s", "--poll-interval", "100ms")
	require.Error(t, err)
	failed := new(entity.GetJobResponse)
	require.NoError(t, decodeCLIOutput(output, failed))
	require.Equal(t, entity.JobStatus_JOB_STATUS_FAILED, failed.Job.Status)
	_, err = connection.run(ctx, "ls", "--location-id", id)
	require.Error(t, err)

	// A failed preparation is terminal: a new Scan Job is what publishes the reconnected original.
	require.NoError(t, os.Rename(unavailable, originals))
	cliResult(t, ctx, connection, job, "scan", "create", "--location-id", id, "--signature", "fill-missing", "--result", "originals")
	waitCLIJob(t, ctx, connection, job.Job.Id, false)
	cliResult(t, ctx, connection, detail, "files", "get", "--file-id", decimal(fileID))
	require.Equal(t, fileID, filesEntryID(detail.Entry), "Library organization survives an unavailable original")

	// A cancelled forced read does not publish its selected original, while visible filesystem rows remain live.
	large := filepath.Join(originals, "unfinished.bin")
	require.NoError(t, os.WriteFile(large, nil, 0o600))
	require.NoError(t, os.Truncate(large, 2<<30))
	cliResult(t, ctx, connection, job, "scan", "create", "--location-id", id, "--path", "unfinished.bin", "--signature", "force-read", "--result", "originals")
	cliResult(t, ctx, connection, new(entity.CancelJobResponse), "job", "cancel", decimal(job.Job.Id))
	output, err = connection.run(ctx, "job", "wait", decimal(job.Job.Id), "--wait-timeout", "20s", "--poll-interval", "100ms")
	require.Error(t, err)
	require.NoError(t, decodeCLIOutput(output, failed))
	require.Equal(t, entity.JobStatus_JOB_STATUS_FAILED, failed.Job.Status)
	// The cancelled Job reports the operator's action, not the runner's internal context error.
	require.Contains(t, failed.Job.Error, "Cancelled by the operator")
	cliResult(t, ctx, connection, page, "ls", "--location-id", id, "--query", "name:unfinished.bin")
	require.Len(t, page.Entries, 1)
	cliResult(t, ctx, connection, detail, "files", "get", "--location-id", id, "--path", "unfinished.bin")
	require.Zero(t, filesEntryID(detail.Entry))
	require.NoError(t, os.Remove(large))

	// Known-only publication retains unknown content and settings scope hides it without removing organization.
	require.NoError(t, os.WriteFile(filepath.Join(originals, "unknown.txt"), []byte("not hashed"), 0o644))
	cliResult(t, ctx, connection, job, "scan", "create", "--location-id", id, "--path", "unknown.txt", "--signature", "known-only", "--result", "originals")
	waitCLIJob(t, ctx, connection, job.Job.Id, false)
	cliResult(t, ctx, connection, page, "ls", "--location-id", id, "--query", "name:unknown.txt")
	require.Len(t, page.Entries, 1)
	cliResult(t, ctx, connection, detail, "files", "get", "--location-id", id, "--path", "unknown.txt")
	unknown := filesEntryID(detail.Entry)
	require.Positive(t, unknown)
	cliResult(t, ctx, connection, detail, "files", "get", "--file-id", decimal(unknown))
	require.NotNil(t, detail.Entry.Status)
	require.Equal(t, entity.FilesCoverage_FILES_COVERAGE_UNSPECIFIED, detail.Entry.GetStatus().GetCurrent())
	metadata := new(entity.UpdateFilesMetadataResponse)
	cliResult(t, ctx, connection, metadata, "files", "metadata", "--file-id", decimal(unknown), "--add-tag", "keep", "--note", "saved independently of visibility")

	// A failed range stops this Location before any selected original is published.
	require.NoError(t, os.WriteFile(filepath.Join(originals, "basic.txt"), []byte("metadata only"), 0o600))
	cliResult(t, ctx, connection, job, "scan", "create", "--location-id", id, "--path", "basic.txt", "--path", "missing", "--signature", "known-only", "--result", "originals")
	output, err = connection.run(ctx, "job", "wait", decimal(job.Job.Id), "--wait-timeout", "20s", "--poll-interval", "100ms")
	require.Error(t, err)
	require.NoError(t, decodeCLIOutput(output, failed))
	require.Equal(t, entity.JobStatus_JOB_STATUS_FAILED, failed.Job.Status)
	require.Contains(t, failed.Job.Error, "missing")
	cliResult(t, ctx, connection, detail, "files", "get", "--location-id", id, "--path", "basic.txt")
	require.Zero(t, filesEntryID(detail.Entry))
	require.NoError(t, os.Mkdir(filepath.Join(originals, "missing"), 0o755))
	// The failed preparation is terminal: a new Scan Job publishes the now-complete selection.
	cliResult(t, ctx, connection, job, "scan", "create", "--location-id", id, "--path", "basic.txt", "--path", "missing", "--signature", "known-only", "--result", "originals")
	waitCLIJob(t, ctx, connection, job.Job.Id, false)
	cliResult(t, ctx, connection, detail, "files", "get", "--location-id", id, "--path", "basic.txt")
	require.Positive(t, filesEntryID(detail.Entry))

	// Settings scope changes visibility only; it never removes the retained association or metadata.
	cliResult(t, ctx, connection, new(entity.LibrarySettings), "settings", "library", "--include-unbacked=false")
	cliResult(t, ctx, connection, page, "ls", "--file-id", "0", "--scope", "default", "--query", "tag:keep", "--recursive")
	require.Empty(t, page.Entries)
	cliResult(t, ctx, connection, page, "ls", "--file-id", "0", "--scope", "all", "--query", "tag:keep", "--recursive")
	require.Len(t, page.Entries, 1)
	cliResult(t, ctx, connection, detail, "files", "get", "--file-id", decimal(unknown))
	require.Equal(t, "saved independently of visibility", detail.Organization.Note)
	require.Equal(t, []string{"keep"}, detail.Organization.Tags)
}
