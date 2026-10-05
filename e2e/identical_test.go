//go:build e2e

package e2e

import (
	"context"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/samuelncui/yatm/entity"
	"github.com/stretchr/testify/require"
)

func TestCLIIdenticalHistoryAndLocationKeep(t *testing.T) {
	// Build the A{x,y}, B{y,z}, C{z} graph through public Location and Archive commands.
	connection, root := startBinaryInstallation(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	originals := filepath.Join(root, "originals")
	for name, content := range map[string]string{"a.txt": "x", "b.txt": "y", "c.txt": "z"} {
		require.NoError(t, os.WriteFile(filepath.Join(originals, name), []byte(content), 0o640))
	}

	location := new(entity.CreateLocationResponse)
	cliResult(t, ctx, connection, location, "location", "create", "--name", "Identical", "--root", originals)
	locationID := decimal(location.Location.Id)
	files := make(map[string]int64)
	for _, name := range []string{"a.txt", "b.txt", "c.txt"} {
		metadata := new(entity.UpdateFilesMetadataResponse)
		cliResult(t, ctx, connection, metadata, "files", "metadata", "--location", locationID+":"+name, "--add-tag", "identical-e2e")
		require.Len(t, metadata.Entries, 1)
		files[name] = filesEntryID(metadata.Entries[0].Entry)
	}

	volume := new(entity.InitializeVolumeResponse)
	cliResult(t, ctx, connection, volume, "volume", "initialize", filepath.Join(root, "volumes", "disk"), "--name", "Identical disk", "--type", "hdd")
	archiveCurrent := func(fileID int64) {
		t.Helper()
		created := new(entity.CreateArchiveJobResponse)
		cliResult(t, ctx, connection, created, "archive", "create", "--file-id", decimal(fileID))
		waitCLIJob(t, ctx, connection, created.Job.Id, true)
		cliResult(t, ctx, connection, new(entity.WriteArchiveMediaResponse), "archive", "write", "volume", decimal(created.Job.Id), "--uuid", volume.Media.Identity)
		waitCLIJob(t, ctx, connection, created.Job.Id, false)
	}

	// Saving B while it is x, then saving it again while y, connects it to A and C by history.
	archiveCurrent(files["a.txt"])
	require.NoError(t, os.WriteFile(filepath.Join(originals, "b.txt"), []byte("x"), 0o640))
	archiveCurrent(files["b.txt"])
	require.NoError(t, os.WriteFile(filepath.Join(originals, "b.txt"), []byte("y"), 0o640))
	archiveCurrent(files["b.txt"])
	require.NoError(t, os.WriteFile(filepath.Join(originals, "c.txt"), []byte("y"), 0o640))
	archiveCurrent(files["c.txt"])
	require.NoError(t, os.WriteFile(filepath.Join(originals, "c.txt"), []byte("z"), 0o640))
	archiveCurrent(files["c.txt"])

	found := new(entity.FindIdenticalResponse)
	cliResult(t, ctx, connection, found, "identical", "find")
	require.NotEmpty(t, found.ResultId)
	require.EqualValues(t, 4, found.AllRowCount)
	groups := new(entity.ListIdenticalGroupsResponse)
	cliResult(t, ctx, connection, groups, "identical", "groups", "--result", found.ResultId)
	require.Len(t, groups.Groups, 1)
	require.EqualValues(t, 3, groups.Groups[0].MemberCount)
	require.NotEmpty(t, groups.ResultId)
	require.Equal(t, found.ResultId, groups.ResultId)
	group := groups.Groups[0]
	members := new(entity.ListIdenticalMembersResponse)
	cliResult(t, ctx, connection, members, "identical", "members", "--result", groups.ResultId, "--group", group.Id)
	require.Len(t, members.Members, 3)
	for _, member := range members.Members {
		require.NotEmpty(t, member.Evidence)
	}
	rows := new(entity.ListIdenticalRowsResponse)
	cliResult(t, ctx, connection, rows, "identical", "rows", "--result", groups.ResultId, "--offset", "2", "--limit", "2", "--include-hidden")
	require.EqualValues(t, 4, rows.TotalRowCount)
	require.Len(t, rows.Rows, 2)
	require.EqualValues(t, 2, rows.Rows[0].Position)
	positions := new(entity.LookupIdenticalPositionsResponse)
	cliResult(t, ctx, connection, positions, "identical", "positions", "--result", groups.ResultId, "--file-id", decimal(files["a.txt"]))
	require.Len(t, positions.Positions, 1)
	require.EqualValues(t, 1, positions.Positions[0].AllPosition)

	// Merge receives only one target; the server applies the complete transitive group and retains saved history.
	merged := new(entity.MergeIdenticalResponse)
	cliResult(t, ctx, connection, merged, "identical", "merge", "--group", group.Id, "--fingerprint", group.Fingerprint, "--target-file", decimal(files["a.txt"]))
	require.Equal(t, files["a.txt"], merged.TargetFileId)
	cliResult(t, ctx, connection, rows, "identical", "rows", "--result", groups.ResultId, "--offset", "2", "--limit", "1", "--include-hidden")
	require.EqualValues(t, 4, rows.TotalRowCount, "the retained Find result remains pageable after Merge")
	require.Len(t, rows.Rows, 1)
	require.Equal(t, files["b.txt"], filesEntryID(rows.Rows[0].Member.Entry))
	cliResult(t, ctx, connection, new(entity.CloseIdenticalResultResponse), "identical", "close", "--result", groups.ResultId)
	versions := new(entity.ListFileVersionsResponse)
	cliResult(t, ctx, connection, versions, "files", "versions", decimal(files["a.txt"]))
	require.Len(t, versions.Versions, 3)
	removed := versions.Versions[0]
	copies := new(entity.ListContentCopiesResponse)
	cliResult(t, ctx, connection, copies, "files", "copies", "--signature", hex.EncodeToString(removed.Signature))
	require.NotEmpty(t, copies.Positions)
	cliResult(t, ctx, connection, new(entity.RemoveFileVersionResponse), "files", "remove-version", "--file-id", decimal(files["a.txt"]), "--version-id", decimal(removed.Id))
	cliResult(t, ctx, connection, versions, "files", "versions", decimal(files["a.txt"]))
	require.Len(t, versions.Versions, 2)
	cliResult(t, ctx, connection, copies, "files", "copies", "--signature", hex.EncodeToString(removed.Signature))
	require.NotEmpty(t, copies.Positions)

	// Current-only Location scope removes every matching sibling through the ordinary Location Trash operation.
	for _, name := range []string{"d.txt", "e.txt"} {
		require.NoError(t, os.WriteFile(filepath.Join(originals, name), []byte("location-only"), 0o640))
	}
	indexed := new(entity.CreateScanJobResponse)
	cliResult(t, ctx, connection, indexed, "scan", "create", "--location-id", locationID, "--signature", "fill-missing", "--result", "originals")
	waitCLIJob(t, ctx, connection, indexed.Job.Id, false)
	cliResult(t, ctx, connection, groups, "identical", "groups", "--source", "locations", "--root", locationID)
	require.Len(t, groups.Groups, 1)
	require.EqualValues(t, 2, groups.Groups[0].MemberCount)
	require.NotEmpty(t, groups.ResultId)
	group = groups.Groups[0]
	_, err := connection.run(ctx, "identical", "keep", "--source", "locations", "--root", locationID, "--group", group.Id, "--fingerprint", group.Fingerprint, "--keep-location", locationID, "--keep-path", "d.txt")
	require.NoError(t, err)
	require.FileExists(t, filepath.Join(originals, "d.txt"))
	require.NoFileExists(t, filepath.Join(originals, "e.txt"))
}
