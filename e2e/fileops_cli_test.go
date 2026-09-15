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
	// The real server and CLI operate only this explicitly allocated fixture, without prior analysis.
	connection, root := startBinaryInstallation(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	for _, args := range [][]string{{"--help"}, {"fileops", "--help"}, {"fileops", "run", "--help"}, {"status"}} {
		_, err := connection.run(ctx, args...)
		require.NoError(t, err)
	}
	settings := new(entity.LibrarySettings)
	cliResult(t, ctx, connection, settings, "settings", "library")
	cliResult(t, ctx, connection, new(entity.UpdateLibrarySettingsReply), "settings", "library", "--auto-collect=false", "--confirm-delete=false", "--revision", decimal(settings.Revision))
	originals := filepath.Join(root, "originals")
	require.NoError(t, os.WriteFile(filepath.Join(originals, "one.txt"), []byte("first content"), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(originals, "two.txt"), []byte("second content"), 0644))
	registered := new(entity.LocationReply)
	cliResult(t, ctx, connection, registered, "location", "create", "--name", "Working files", "--root", originals)
	locationID := decimal(registered.Location.Id)
	entries := new(entity.ListLocationEntriesReply)
	cliResult(t, ctx, connection, entries, "location", "entries", locationID)
	for _, entry := range entries.Entries {
		require.Nil(t, entry.File)
		require.NotNil(t, entry.Reference)
	}
	source := new(entity.LocationEntry)
	cliResult(t, ctx, connection, source, "location", "admit", locationID, "--path", "one.txt")
	require.NotNil(t, source.File)
	second := new(entity.LocationEntry)
	cliResult(t, ctx, connection, second, "location", "admit", locationID, "--path", "two.txt")
	require.NotNil(t, second.File)
	cliResult(t, ctx, connection, new(entity.FileMetadataEditReply), "file", "metadata", decimal(second.File.Id), "--note", "Do not clone this annotation")
	require.NoError(t, os.WriteFile(filepath.Join(originals, "chosen.txt"), []byte("previous original"), 0644))
	chosen := new(entity.LocationEntry)
	cliResult(t, ctx, connection, chosen, "location", "admit", locationID, "--path", "chosen.txt")
	require.NotNil(t, chosen.File)
	fileOperationCLI(t, ctx, connection, "--kind", "delete", "--location", locationID, "--source", "chosen.txt", "--confirm-delete")

	// Each ordinary operation completes before its CLI returns, without a persistent Job.
	fileOperationCLI(t, ctx, connection, "--kind", "mkdir", "--location", locationID, "--destination", ".", "--name", "copies")
	_, err := connection.run(ctx, "fileops", "run", "--kind", "copy", "--location", locationID, "--source", "one.txt", "--destination", "copies")
	require.Error(t, err, "neither provider offers user-level copy")
	for _, name := range []string{"one.txt", "two.txt"} {
		expected, err := os.ReadFile(filepath.Join(originals, name))
		require.NoError(t, err)
		// External copies are independent originals when explicitly admitted.
		require.NoError(t, os.WriteFile(filepath.Join(originals, "copies", name), expected, 0644))
	}
	fileOperationCLI(t, ctx, connection, "--kind", "move", "--location", locationID, "--source", "copies/one.txt", "--destination", "copies", "--name", "renamed.txt")
	require.NoFileExists(t, filepath.Join(originals, "copies/one.txt"))
	require.FileExists(t, filepath.Join(originals, "copies/renamed.txt"))
	copy := new(entity.LocationEntry)
	cliResult(t, ctx, connection, copy, "location", "admit", locationID, "--path", "copies/two.txt")
	require.NotNil(t, copy.File)
	require.NotEqual(t, second.File.Id, copy.File.Id)
	require.Empty(t, copy.File.Note)

	// Explicit original relocation chooses an unbound File; subsequent external rename keeps that choice.
	state := new(entity.FileStateReply)
	cliResult(t, ctx, connection, state, "file", "locate-original", decimal(chosen.File.Id), "--location-id", locationID, "--path", "copies/renamed.txt", "--confirm")
	require.NoError(t, os.Rename(filepath.Join(originals, "copies/renamed.txt"), filepath.Join(originals, "copies/relinked.txt")))
	cliResult(t, ctx, connection, copy, "location", "admit", locationID, "--path", "copies/relinked.txt")
	require.Equal(t, chosen.File.Id, copy.File.Id)

	// Browser confirmation settings cannot authorize a script's permanent deletion implicitly.
	_, err = connection.run(ctx, "fileops", "run", "--kind", "delete", "--location", locationID, "--source", "copies")
	require.ErrorContains(t, err, "--confirm-delete")
	require.DirExists(t, filepath.Join(originals, "copies"))
	result := fileOperationCLI(t, ctx, connection, "--kind", "delete", "--location", locationID, "--source", "copies", "--confirm-delete")
	require.EqualValues(t, 3, result.Succeeded)
	require.NoDirExists(t, filepath.Join(originals, "copies"))
	require.FileExists(t, filepath.Join(originals, "one.txt"))
	require.FileExists(t, filepath.Join(originals, "two.txt"))

	// Library organization uses the same stream without renaming or deleting the physical original.
	cliResult(t, ctx, connection, new(entity.FileMetadataEditReply), "file", "metadata", decimal(source.File.Id), "--note", "Working original", "--add-tag", "working")
	_, folders := fileOrganizationCLI(t, ctx, connection, "file", "mkdir", "0", "Organized/nested")
	require.NotEmpty(t, folders)
	parent := folders[len(folders)-1]
	require.NotNil(t, parent.FileId)
	fileOrganizationCLI(t, ctx, connection, "file", "edit", decimal(source.File.Id), "--parent-id", decimal(*parent.FileId), "--name", "logical-name.txt")
	logical := new(entity.FileGetReply)
	cliResult(t, ctx, connection, logical, "file", "get", decimal(source.File.Id))
	require.Equal(t, "logical-name.txt", logical.File.Name)
	require.EqualValues(t, *parent.FileId, logical.File.ParentId)
	require.Equal(t, "Working original", logical.File.Note)
	require.FileExists(t, filepath.Join(originals, "one.txt"))
	require.NoFileExists(t, filepath.Join(originals, "logical-name.txt"))

	// Moving the physical entry updates its original association, not its logical tree or annotation.
	fileOperationCLI(t, ctx, connection, "--kind", "move", "--location", locationID, "--source", "one.txt", "--destination", ".", "--name", "physical-name.txt")
	cliResult(t, ctx, connection, state, "file", "state", decimal(source.File.Id))
	require.Equal(t, "physical-name.txt", state.Original.Path)
	cliResult(t, ctx, connection, logical, "file", "get", decimal(source.File.Id))
	require.Equal(t, "logical-name.txt", logical.File.Name)
	require.Equal(t, "Working original", logical.File.Note)
	require.NoFileExists(t, filepath.Join(originals, "one.txt"))
	require.FileExists(t, filepath.Join(originals, "physical-name.txt"))

	// Logical deletion retains the original; physical deletion later retains the managed File.
	fileOrganizationCLI(t, ctx, connection, "file", "delete", decimal(source.File.Id), "--confirm")
	require.FileExists(t, filepath.Join(originals, "physical-name.txt"))
	cliResult(t, ctx, connection, state, "file", "state", decimal(source.File.Id))
	require.NotNil(t, state.Original)
	fileOperationCLI(t, ctx, connection, "--kind", "delete", "--location", locationID, "--source", "physical-name.txt", "--confirm-delete")
	require.NoFileExists(t, filepath.Join(originals, "physical-name.txt"))
	cliResult(t, ctx, connection, state, "file", "state", decimal(source.File.Id))
	require.Nil(t, state.Original)
	cliResult(t, ctx, connection, logical, "file", "get", decimal(source.File.Id))
	require.Equal(t, "Working original", logical.File.Note)

	// Neither namespace's ordinary organization enters the persistent Jobs catalog.
	jobs := new(entity.ListJobsReply)
	cliResult(t, ctx, connection, jobs, "job", "list")
	require.Empty(t, jobs.Jobs)
}

func fileOperationCLI(t *testing.T, ctx context.Context, connection *cliConnection, args ...string) *entity.FileOperationSummary {
	t.Helper()
	summary, _ := fileOrganizationCLI(t, ctx, connection, append([]string{"fileops", "run"}, args...)...)
	return summary
}

func fileOrganizationCLI(t *testing.T, ctx context.Context, connection *cliConnection, args ...string) (*entity.FileOperationSummary, []*entity.FileOperationEntry) {
	t.Helper()

	// Decode each emitted result independently, matching the public streaming command contract.
	output, err := connection.run(ctx, args...)
	require.NoError(t, err)
	scanner := bufio.NewScanner(bytes.NewReader(output))
	var summary *entity.FileOperationSummary
	var entries []*entity.FileOperationEntry
	for scanner.Scan() {
		update := new(entity.FileOperationUpdate)
		require.NoError(t, decodeCLIOutput(scanner.Bytes(), update))
		if update.Entry != nil {
			require.Equal(t, entity.FileOperationOutcome_SUCCEEDED, update.Entry.Outcome)
			entries = append(entries, update.Entry)
		}
		if update.GetSummary().GetCompleted() {
			summary = update.Summary
		}
	}
	require.NoError(t, scanner.Err())
	require.NotNil(t, summary)
	require.Equal(t, int64(len(entries)), summary.Succeeded)
	require.Zero(t, summary.Failed)
	require.Zero(t, summary.Unprocessed)
	require.Zero(t, summary.PublicationPending)
	return summary, entries
}
