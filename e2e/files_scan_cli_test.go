//go:build e2e

package e2e

import (
	"context"
	"encoding/base64"
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
	location := new(entity.LocationReply)
	cliResult(t, ctx, connection, location, "location", "create", "--name", "Queries", "--root", originals)
	id := decimal(location.Location.Id)
	page := new(entity.ListFilesReply)
	cliResult(t, ctx, connection, page, "files", "list", "--location-id", id, "--path", "to/package", "--query", "type:file AND size:3", "--limit", "1")
	require.Len(t, page.Entries, 1)
	require.Nil(t, page.Entries[0].File, "queries must not admit entries")
	first := page.Entries[0].Name
	require.NotEmpty(t, page.NextCursor)
	cliResult(t, ctx, connection, page, "files", "list", "--location-id", id, "--path", "to/package", "--query", "type:file AND size:3", "--limit", "1", "--cursor", page.NextCursor)
	require.Len(t, page.Entries, 1)
	require.NotEqual(t, first, page.Entries[0].Name)
	require.Empty(t, page.NextCursor)
	_, err := connection.run(ctx, "files", "list", "--location-id", id, "--query", "unsupported:value")
	require.Error(t, err)

	// Annotations use the same query language in logical and physical views.
	file := new(entity.FilesEntry)
	cliResult(t, ctx, connection, file, "files", "metadata", "--location-id", id, "--path", "from/package/a.md", "--add-tag", "query-e2e", "--note", "preserve this")
	fileID := file.File.Id
	for _, args := range [][]string{{"--location-id", id, "--path", "from/package"}, {"--file-id", decimal(file.File.ParentId)}} {
		cliResult(t, ctx, connection, page, append([]string{"files", "list", "--query", "tag:query-e2e AND note:preserve"}, args...)...)
		require.Len(t, page.Entries, 1)
		require.Equal(t, fileID, page.Entries[0].File.Id)
	}

	// Open reads use the exact current reference returned by Get, without admitting the entry.
	entry := new(entity.FilesEntry)
	cliResult(t, ctx, connection, entry, "files", "get", "--location-id", id, "--path", "to/package/b.md")
	require.Nil(t, entry.File)
	require.NotNil(t, entry.ContentReference)
	data, err := protojson.Marshal(entry.ContentReference)
	require.NoError(t, err)
	content := readHTTPContent(t, ctx, connection.url+"/files/content?ref="+base64.RawURLEncoding.EncodeToString(data))
	require.Equal(t, "abc", string(content))

	// Physical and Library moves share recursive same-name directory merge semantics.
	fileOperationCLI(t, ctx, connection, "--kind", "move", "--location", id, "--source", "from/package", "--destination", "to")
	require.NoDirExists(t, filepath.Join(originals, "from/package"))
	require.FileExists(t, filepath.Join(originals, "to/package/a.md"))
	require.FileExists(t, filepath.Join(originals, "to/package/b.md"))
	cliResult(t, ctx, connection, file, "files", "get", "--file-id", decimal(fileID))
	require.Equal(t, "preserve this", file.File.Note)
	cliResult(t, ctx, connection, page, "files", "list", "--location-id", id, "--path", "to/package", "--query", "tag:query-e2e")
	require.Len(t, page.Entries, 1)
	require.Equal(t, fileID, page.Entries[0].File.Id)

	// Adopt a second file and move both logical branches through the same public operation stream.
	second := new(entity.FilesEntry)
	cliResult(t, ctx, connection, second, "files", "metadata", "--location-id", id, "--path", "to/package/b.md", "--add-tag", "query-e2e")
	_, sourceDirs := fileOrganizationCLI(t, ctx, connection, "file", "mkdir", "0", "Merge source/package")
	_, targetDirs := fileOrganizationCLI(t, ctx, connection, "file", "mkdir", "0", "Merge target/package")
	sourceDir := *sourceDirs[len(sourceDirs)-1].FileId
	targetDir := *targetDirs[len(targetDirs)-1].FileId
	fileOperationCLI(t, ctx, connection, "--kind", "move", "--library", "--source", decimal(fileID), "--destination", decimal(sourceDir))
	fileOperationCLI(t, ctx, connection, "--kind", "move", "--library", "--source", decimal(second.File.Id), "--destination", decimal(targetDir))
	target := new(entity.FilesEntry)
	cliResult(t, ctx, connection, target, "files", "get", "--file-id", decimal(targetDir))
	fileOperationCLI(t, ctx, connection, "--kind", "move", "--library", "--source", decimal(sourceDir), "--destination", decimal(target.File.ParentId))
	cliResult(t, ctx, connection, page, "files", "list", "--file-id", decimal(targetDir))
	require.Len(t, page.Entries, 2)
	require.FileExists(t, filepath.Join(originals, "to/package/a.md"))
	require.FileExists(t, filepath.Join(originals, "to/package/b.md"))

	// Known-only keeps an uncached file unknown; fill-missing then obtains actual content facts.
	for _, policy := range []string{"known-only", "fill-missing"} {
		job := new(entity.CreateScanJobReply)
		cliResult(t, ctx, connection, job, "scan", "create", "--location-id", id, "--path", "to/package/other.txt", "--signature", policy, "--result", "report")
		require.Equal(t, entity.JobKind_SCAN, job.Job.Kind)
		waitCLIJob(t, ctx, connection, job.Job.Id, false)
		results := new(entity.ListScanJobEntriesReply)
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
