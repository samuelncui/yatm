//go:build e2e

package e2e

import (
	"context"
	"encoding/base64"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/samuelncui/yatm/entity"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protojson"
)

func TestCLISharedFilesQueriesAndMerges(t *testing.T) {
	// Register a real source and exercise pure query pages through the shipped CLI.
	connection, root := startBinaryInstallation(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	originals := filepath.Join(root, "originals")
	for _, name := range []string{"from/package/a.md", "to/package/b.md", "to/package/other.txt"} {
		require.NoError(t, os.MkdirAll(filepath.Dir(filepath.Join(originals, name)), 0755))
		require.NoError(t, os.WriteFile(filepath.Join(originals, name), []byte("abc"), 0644))
	}
	location := new(entity.CreateLocationResponse)
	cliResult(t, ctx, connection, location, "location", "create", "--name", "Queries", "--root", originals)
	id := decimal(location.Location.Id)
	page := new(entity.SearchFilesResponse)
	cliResult(t, ctx, connection, page, "ls", "--location-id", id, "--path", "to/package", "--query", "type:file AND size:3", "--limit", "1", "--long")
	require.Len(t, page.Entries, 1)
	require.Nil(t, page.Entries[0].AssociatedFileId, "queries must not admit entries")
	first := page.Entries[0].Name
	require.NotEmpty(t, page.NextCursor)
	cliResult(t, ctx, connection, page, "ls", "--location-id", id, "--path", "to/package", "--query", "type:file AND size:3", "--limit", "1", "--cursor", page.NextCursor, "--long")
	require.Len(t, page.Entries, 1)
	require.NotEqual(t, first, page.Entries[0].Name)
	require.Empty(t, page.NextCursor)
	_, err := connection.run(ctx, "ls", "--location-id", id, "--query", "unsupported:value")
	require.Error(t, err)

	// Annotations use the same query language in logical and physical views.
	metadata := new(entity.UpdateFilesMetadataResponse)
	cliResult(t, ctx, connection, metadata, "files", "metadata", "--location", id+":from/package/a.md", "--add-tag", "query-e2e", "--note", "preserve this")
	fileID := *metadata.Entries[0].Entry.AssociatedFileId
	file := new(entity.FilesDetail)
	cliResult(t, ctx, connection, file, "files", "get", "--file-id", decimal(fileID))
	require.Equal(t, "preserve this", file.Organization.Note)
	require.Equal(t, []string{"query-e2e"}, file.Organization.Tags)

	// File byte delivery is no longer exposed through HTTP.
	entry := new(entity.FilesDetail)
	cliResult(t, ctx, connection, entry, "files", "get", "--location-id", id, "--path", "to/package/b.md")
	require.Nil(t, entry.Entry.AssociatedFileId)
	require.NotNil(t, entry.ContentReference)
	data, err := protojson.Marshal(entry.ContentReference)
	require.NoError(t, err)
	requireHTTPNotFound(t, ctx, http.MethodGet, connection.url+"/files/content?ref="+base64.RawURLEncoding.EncodeToString(data))

	// Physical and Library moves share recursive same-name directory merge semantics.
	fileOperationCLI(t, ctx, connection, "mv", "--location", id, "--source", "from/package", "--destination", "to")
	require.NoDirExists(t, filepath.Join(originals, "from/package"))
	require.FileExists(t, filepath.Join(originals, "to/package/a.md"))
	require.FileExists(t, filepath.Join(originals, "to/package/b.md"))
	file = new(entity.FilesDetail)
	cliResult(t, ctx, connection, file, "files", "get", "--file-id", decimal(fileID))
	require.Equal(t, "preserve this", file.Organization.Note)
	cliResult(t, ctx, connection, file, "files", "get", "--file-id", decimal(fileID))
	require.Equal(t, "preserve this", file.Organization.Note)

	// Adopt a second file and move both logical branches through the same public operation stream.
	second := new(entity.UpdateFilesMetadataResponse)
	cliResult(t, ctx, connection, second, "files", "metadata", "--location", id+":to/package/b.md", "--add-tag", "query-e2e")
	fileOperationCLI(t, ctx, connection, "mkdir", "--library", "--destination", "0", "--name", "Merge target")
	fileOperationCLI(t, ctx, connection, "mv", "--library", "--source", decimal(fileID), "--destination", "0")
	fileOperationCLI(t, ctx, connection, "mv", "--library", "--source", decimal(*second.Entries[0].Entry.AssociatedFileId), "--destination", "0")
	require.FileExists(t, filepath.Join(originals, "to/package/a.md"))
	require.FileExists(t, filepath.Join(originals, "to/package/b.md"))

	// Known-only keeps an uncached file unknown; fill-missing then obtains actual content facts.
	for _, policy := range []string{"known-only", "fill-missing"} {
		job := new(entity.CreateScanJobResponse)
		cliResult(t, ctx, connection, job, "scan", "create", "--location-id", id, "--path", "to/package/other.txt", "--signature", policy, "--result", "report")
		require.Equal(t, entity.JobKind_JOB_KIND_SCAN, job.Job.Kind)
		waitCLIJob(t, ctx, connection, job.Job.Id, false)

		// Cache-only and actual reads both expose useful Job logs and frozen attempt timing through the CLI.
		logs := new(entity.GetJobLogResponse)
		cliResult(t, ctx, connection, logs, "job", "log", decimal(job.Job.Id))
		require.Contains(t, string(logs.Logs), "Scan phase started")
		require.Contains(t, string(logs.Logs), "Scan scope finished")
		lines := new(entity.ListJobLogLinesResponse)
		cliResult(t, ctx, connection, lines, "job", "log-lines", decimal(job.Job.Id))
		require.NotEmpty(t, lines.Lines)
		require.Contains(t, lines.Lines[len(lines.Lines)-1].Text, "Scan")
		filtered := new(entity.ListJobLogLinesResponse)
		cliResult(t, ctx, connection, filtered, "job", "log-lines", decimal(job.Job.Id), "--level", "info", "--query", "Scan phase")
		require.NotEmpty(t, filtered.Lines)
		for _, line := range filtered.Lines {
			require.Equal(t, "info", line.Level)
			require.Contains(t, line.Text, "Scan phase")
		}
		progress := new(entity.GetScanJobProgressResponse)
		cliResult(t, ctx, connection, progress, "scan", "progress", decimal(job.Job.Id))
		require.NotNil(t, progress.Progress.ElapsedMs)
		require.GreaterOrEqual(t, *progress.Progress.ElapsedMs, int64(0))
		elapsed := *progress.Progress.ElapsedMs
		cliResult(t, ctx, connection, new(entity.GetScanJobProgressResponse), "job", "progress", decimal(job.Job.Id))
		cliResult(t, ctx, connection, progress, "scan", "progress", decimal(job.Job.Id))
		require.Equal(t, elapsed, *progress.Progress.ElapsedMs, "completed elapsed time must remain frozen")

		// Progress reporting must not change the requested content policy.
		results := new(entity.ListScanJobEntriesResponse)
		cliResult(t, ctx, connection, results, "scan", "results", decimal(job.Job.Id))
		require.Len(t, results.Entries, 1)
		if policy == "known-only" {
			require.Empty(t, results.Entries[0].Signature)
			continue
		}
		require.NotEmpty(t, results.Entries[0].Signature)
		require.Len(t, results.Entries[0].Sha256, 32)
	}
}
