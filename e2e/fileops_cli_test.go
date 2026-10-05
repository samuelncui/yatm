//go:build e2e

package e2e

import (
	"bufio"
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/samuelncui/yatm/entity"
	"github.com/stretchr/testify/require"
)

func TestCLIPhysicalLocationOperations(t *testing.T) {
	// Exercise the public Files commands against an unscanned, isolated Location.
	connection, root := startBinaryInstallation(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	originals := filepath.Join(root, "originals")
	require.NoError(t, os.WriteFile(filepath.Join(originals, "one.txt"), []byte("first content"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(originals, "two.txt"), []byte("second content"), 0o644))
	location := new(entity.CreateLocationResponse)
	cliResult(t, ctx, connection, location, "location", "create", "--name", "Working files", "--root", originals)
	id := decimal(location.Location.Id)

	// Physical metadata admission and operations use one Files surface without creating Jobs.
	metadata := new(entity.UpdateFilesMetadataResponse)
	cliResult(t, ctx, connection, metadata, "files", "metadata", "--location", id+":one.txt", "--note", "Working original", "--add-tag", "working")
	require.Len(t, metadata.Entries, 1)
	detail := metadata.Entries[0]
	require.NotNil(t, detail.Organization)
	require.Equal(t, "Working original", detail.Organization.Note)
	fileID := detail.Entry.GetAssociatedFileId()
	require.Positive(t, fileID)
	_, folders := fileOperationEntriesCLI(t, ctx, connection, "mkdir", "--library", "--destination", "0", "--name", "Organized/nested")
	parentID := folders[len(folders)-1].GetFileId()
	require.Positive(t, parentID)
	fileOperationCLI(t, ctx, connection, "mv", "--library", "--source", decimal(fileID), "--destination", decimal(parentID), "--name", "logical-name.txt")
	logical := new(entity.FilesDetail)
	cliResult(t, ctx, connection, logical, "files", "get", "--file-id", decimal(fileID))
	require.Equal(t, "logical-name.txt", logical.Entry.Name)
	require.Equal(t, parentID, logical.Organization.Parent.GetFileId())
	require.Equal(t, "Working original", logical.Organization.Note)
	require.FileExists(t, filepath.Join(originals, "one.txt"))
	require.NoFileExists(t, filepath.Join(originals, "logical-name.txt"))

	fileOperationCLI(t, ctx, connection, "mkdir", "--location", id, "--destination", ".", "--name", "copies")
	fileOperationCLI(t, ctx, connection, "mv", "--location", id, "--source", "one.txt", "--destination", "copies", "--name", "renamed.txt")
	require.NoFileExists(t, filepath.Join(originals, "one.txt"))
	require.FileExists(t, filepath.Join(originals, "copies", "renamed.txt"))
	cliResult(t, ctx, connection, logical, "files", "get", "--file-id", decimal(fileID))
	require.Equal(t, "copies/renamed.txt", logical.Original.Path)
	require.Equal(t, "logical-name.txt", logical.Entry.Name)
	require.Equal(t, "Working original", logical.Organization.Note)
	require.NoError(t, os.WriteFile(filepath.Join(originals, "copies", "external.txt"), []byte("first content"), 0o644))
	external := new(entity.UpdateFilesMetadataResponse)
	cliResult(t, ctx, connection, external, "files", "metadata", "--location", id+":copies/external.txt", "--note", "external copy")
	require.NotEqual(t, fileID, *external.Entries[0].Entry.AssociatedFileId)

	// An explicit relink retains the File identity across a subsequent external rename.
	cliResult(t, ctx, connection, detail, "files", "locate-original", decimal(fileID), "--location-id", id, "--path", "copies/renamed.txt")
	require.NoError(t, os.Rename(filepath.Join(originals, "copies", "renamed.txt"), filepath.Join(originals, "copies", "relinked.txt")))
	cliResult(t, ctx, connection, detail, "files", "get", "--location-id", id, "--path", "copies/relinked.txt")
	require.Nil(t, detail.Entry.AssociatedFileId, "reads do not publish external-move continuity")
	cliResult(t, ctx, connection, metadata, "files", "metadata", "--location", id+":copies/relinked.txt", "--note", "Working original")
	require.EqualValues(t, fileID, metadata.Entries[0].Entry.GetAssociatedFileId())

	// A bare removal writes; --dryrun reports the same resolved targets without touching them.
	previewSummary, previewEntries := fileOperationDryRunCLI(t, ctx, connection, "rm", "--location", id, "--source", "copies", "--dryrun")
	require.True(t, previewSummary.GetDryrun())
	require.Len(t, previewEntries, 1, "the report names the scope a real removal would move")
	require.Equal(t, entity.FileOperationOutcome_FILE_OPERATION_OUTCOME_UNPROCESSED, previewEntries[0].Outcome)
	require.DirExists(t, filepath.Join(originals, "copies"), "a report changes nothing")
	require.NoDirExists(t, filepath.Join(originals, ".trash"))

	// Location removal is recoverable in its owned Trash.
	require.NoError(t, os.WriteFile(filepath.Join(originals, "copies/.hidden"), []byte("keep bytes"), 0644))
	_, removed := fileOperationEntriesCLI(t, ctx, connection, "rm", "--location", id, "--source", "copies")
	require.Len(t, removed, 1, "a whole-directory rename is one successful scope")
	trashPath := removed[0].TargetPath
	require.Contains(t, trashPath, ".trash/")
	require.FileExists(t, filepath.Join(originals, trashPath, ".hidden"))
	require.FileExists(t, filepath.Join(originals, trashPath, "relinked.txt"))
	cliResult(t, ctx, connection, logical, "files", "get", "--file-id", decimal(fileID))
	require.Nil(t, logical.Original)
	require.Equal(t, "Working original", logical.Organization.Note)
	_, err := connection.run(ctx, "rm", "--location", id, "--source", trashPath)
	require.Error(t, err, "Trash has no permanent-removal operation")
	fileOperationCLI(t, ctx, connection, "mv", "--location", id, "--source", trashPath, "--destination", ".", "--name", "recovered")
	require.FileExists(t, filepath.Join(originals, "recovered/relinked.txt"))
	cliResult(t, ctx, connection, detail, "files", "get", "--location-id", id, "--path", "recovered/relinked.txt")
	require.Nil(t, detail.Entry.AssociatedFileId, "moving out does not reconnect a File")
	require.NoDirExists(t, filepath.Join(originals, "copies"))
	require.DirExists(t, filepath.Join(originals, ".trash"))

	// Logical organization uses the same operation stream and never alters original bytes.
	detail = new(entity.FilesDetail)
	cliResult(t, ctx, connection, detail, "files", "get", "--file-id", decimal(fileID))
	require.Equal(t, "Working original", detail.Organization.Note)
	fileOperationCLI(t, ctx, connection, "rm", "--library", "--source", decimal(fileID))
	require.FileExists(t, filepath.Join(originals, "recovered/relinked.txt"))
	cliResult(t, ctx, connection, metadata, "files", "metadata", "--location", id+":two.txt", "--note", "Logical Trash keeps the original")
	secondID := metadata.Entries[0].Entry.GetAssociatedFileId()
	require.Positive(t, secondID)
	fileOperationCLI(t, ctx, connection, "rm", "--library", "--source", decimal(secondID))
	require.FileExists(t, filepath.Join(originals, "two.txt"))
	cliResult(t, ctx, connection, detail, "files", "get", "--file-id", decimal(secondID))
	require.NotNil(t, detail.Original)
	fileOperationCLI(t, ctx, connection, "rm", "--location", id, "--source", "two.txt")
	cliResult(t, ctx, connection, detail, "files", "get", "--file-id", decimal(secondID))
	require.Nil(t, detail.Original)
	require.Equal(t, "Logical Trash keeps the original", detail.Organization.Note)

	// Ordinary Files operations do not enter the persistent Job catalog.
	jobs := new(entity.ListJobsResponse)
	cliResult(t, ctx, connection, jobs, "job", "list")
	require.Empty(t, jobs.Jobs)
}

// fileOperationDryRunCLI requires a successful read-only report without performing the operation.
func fileOperationDryRunCLI(t *testing.T, ctx context.Context, connection *cliConnection, args ...string) (*entity.FileOperationSummary, []*entity.FileOperationEntry) {
	t.Helper()
	// A complete dry run succeeds even when its proposed entries remain unprocessed.
	output, err := connection.run(ctx, args...)
	require.NoError(t, err)
	return decodeFileOperationStream(t, output)
}

func fileOperationCLI(t *testing.T, ctx context.Context, connection *cliConnection, args ...string) *entity.FileOperationSummary {
	t.Helper()
	summary, _ := fileOperationEntriesCLI(t, ctx, connection, args...)
	return summary
}

func fileOperationEntriesCLI(t *testing.T, ctx context.Context, connection *cliConnection, args ...string) (*entity.FileOperationSummary, []*entity.FileOperationEntry) {
	t.Helper()
	output, err := connection.run(ctx, args...)
	require.NoError(t, err)
	summary, entries := decodeFileOperationStream(t, output)
	require.Positive(t, summary.SucceededCount)
	return summary, entries
}

func decodeFileOperationStream(t *testing.T, output []byte) (*entity.FileOperationSummary, []*entity.FileOperationEntry) {
	t.Helper()
	scanner := bufio.NewScanner(bytes.NewReader(output))
	var summary *entity.FileOperationSummary
	var entries []*entity.FileOperationEntry
	for scanner.Scan() {
		update := new(entity.FileOperationResult)
		require.NoError(t, decodeCLIOutput(scanner.Bytes(), update))
		if update.Entry != nil {
			entries = append(entries, update.Entry)
		}
		if update.GetSummary().GetCompleted() {
			summary = update.Summary
		}
	}
	require.NoError(t, scanner.Err())
	require.NotNil(t, summary)
	return summary, entries
}
