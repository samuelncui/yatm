//go:build e2e

package e2e

import (
	"context"
	"encoding/hex"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"syscall"
	"testing"
	"time"

	"github.com/samuelncui/yatm/config"
	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/executor"
	"github.com/samuelncui/yatm/library"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"gopkg.in/yaml.v2"
)

func startBinaryInstallation(t *testing.T) (*cliConnection, string) {
	t.Helper()
	// Bind only loopback and allocate every installation resource under the test root.
	root, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	for _, path := range []string{"originals/docs", "restore", "volumes/disk"} {
		require.NoError(t, os.MkdirAll(filepath.Join(root, path), 0o755))
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	address := listener.Addr().String()
	require.NoError(t, listener.Close())
	conf := config.Config{Domain: "http://" + address, Listen: address, Paths: executor.Paths{
		Work: filepath.Join(root, "work"), Volumes: []string{filepath.Join(root, "volumes")},
		Access: []executor.AccessRange{{Root: filepath.Join(root, "originals")}, {Root: filepath.Join(root, "restore")}},
	}}
	conf.Database.Dialect, conf.Database.DSN = "sqlite", filepath.Join(root, "catalog.db")
	data, err := yaml.Marshal(conf)
	require.NoError(t, err)
	filename := filepath.Join(root, "config.yaml")
	require.NoError(t, os.WriteFile(filename, data, 0o600))

	// Start the production server binary, with managed termination and retained failure logs.
	log, err := os.Create(filepath.Join(root, "server.log"))
	require.NoError(t, err)
	command := exec.Command(testBinary(t, "yatm-httpd"), "-config", filename)
	command.Dir, command.Stdout, command.Stderr = root, log, log
	require.NoError(t, command.Start())
	done := make(chan error, 1)
	go func() { done <- command.Wait() }()
	t.Cleanup(func() {
		_ = command.Process.Signal(syscall.SIGTERM)
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			_ = command.Process.Kill()
			<-done
		}
		require.NoError(t, log.Close())
		if t.Failed() {
			content, _ := os.ReadFile(log.Name())
			t.Logf("httpd log:\n%s", content)
		}
	})
	connection := &cliConnection{binary: testBinary(t, "yatm-cli"), url: conf.Domain, directory: root}
	require.Eventually(t, func() bool {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_, err := connection.run(ctx, "status")
		return err == nil
	}, 15*time.Second, 100*time.Millisecond)
	setFixtureAutoCollect(t, context.Background(), connection, false)
	return connection, root
}

func setFixtureAutoCollect(t *testing.T, ctx context.Context, connection *cliConnection, enabled bool) *entity.UpdateLibrarySettingsReply {
	t.Helper()
	// Individual scenarios opt into collection; unrelated jobs must not race explicit analysis fixtures.
	settings := new(entity.LibrarySettings)
	cliResult(t, ctx, connection, settings, "settings", "library")
	reply := new(entity.UpdateLibrarySettingsReply)
	cliResult(t, ctx, connection, reply, "settings", "library", "--auto-collect="+strconv.FormatBool(enabled), "--revision", decimal(settings.Revision))
	return reply
}

func cliResult(t *testing.T, ctx context.Context, connection *cliConnection, reply proto.Message, args ...string) {
	t.Helper()
	// Assert both process success and the machine-readable public response.
	output, err := connection.run(ctx, args...)
	require.NoError(t, err)
	require.NoError(t, decodeCLIOutput(output, reply), string(output))
}

func waitCLIJob(t *testing.T, ctx context.Context, connection *cliConnection, id int64, pending bool) *entity.Job {
	t.Helper()
	// Waiting is observational; Media selection remains a separate explicit command.
	output, err := connection.run(ctx, "job", "wait", decimal(id), "--wait-timeout", "1m", "--poll-interval", "100ms")
	if pending {
		require.Error(t, err)
	} else {
		require.NoError(t, err)
	}
	reply := new(entity.GetJobReply)
	require.NoError(t, protojson.Unmarshal(output, reply), string(output))
	if pending {
		require.Equal(t, entity.JobPhase_JOB_PHASE_WAITING_FOR_MEDIA, reply.Job.Phase)
	} else {
		require.Equal(t, entity.JobStatus_COMPLETED, reply.Job.Status)
	}
	return reply.Job
}

func TestCLIRegisteredLocationWorkflows(t *testing.T) {
	// Run the real server and CLI; fixture writes represent the external filesystem, not catalog shortcuts.
	connection, root := startBinaryInstallation(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	for name, data := range map[string]string{"a.txt": "same", "b.txt": "same", "docs/c.txt": "second", "docs/d.txt": "second", "docs/e.txt": "second", "docs/unique.txt": "unique"} {
		require.NoError(t, os.WriteFile(filepath.Join(root, "originals", name), []byte(data), 0o640))
	}

	// A physical path selection must not expand an unrelated differently cased prefix.
	upper := filepath.Join(root, "originals", "Photos")
	lower := filepath.Join(root, "originals", "photos")
	require.NoError(t, os.Mkdir(upper, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(upper, "upper.txt"), []byte("upper"), 0o644))
	_, lowerErr := os.Stat(lower)
	distinctCase := os.IsNotExist(lowerErr)
	if distinctCase {
		require.NoError(t, os.Mkdir(lower, 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(lower, "lower.txt"), []byte("lowercase"), 0o644))
	}

	// Register and scan through the public CLI, then inspect case-sensitive selection totals.
	access := new(entity.GetAccessReply)
	cliResult(t, ctx, connection, access, "settings", "access")
	require.Len(t, access.Ranges, 2)
	browse := new(entity.BrowsePathsReply)
	cliResult(t, ctx, connection, browse, "settings", "browse", "--path", filepath.Join(root, "originals"), "--limit", "1")
	require.Len(t, browse.Directories, 1)
	registered := new(entity.LocationReply)
	cliResult(t, ctx, connection, registered, "location", "create", "--name", "Documents", "--root", filepath.Join(root, "originals"), "--restore-target")
	id := registered.Location.Id
	scanned := new(entity.CreateScanJobReply)
	cliResult(t, ctx, connection, scanned, "analyze", "create", decimal(id))
	waitCLIJob(t, ctx, connection, scanned.Job.Id, false)
	cliResult(t, ctx, connection, registered, "location", "get", decimal(id))
	require.NotEqual(t, entity.OnlineBinding_UNCONFIRMED, registered.Location.Binding)
	require.Equal(t, scanned.Job.Id, registered.Location.LastSyncJobId)
	caseSelection := new(entity.InspectSelectionReply)
	cliResult(t, ctx, connection, caseSelection, "file", "inspect-selection", "--location", decimal(id)+":Photos")
	require.EqualValues(t, 1, caseSelection.Files)
	require.EqualValues(t, 5, caseSelection.Bytes)
	if distinctCase {
		cliResult(t, ctx, connection, caseSelection, "file", "inspect-selection", "--location", decimal(id)+":photos")
		require.EqualValues(t, 1, caseSelection.Files)
		require.EqualValues(t, 9, caseSelection.Bytes)
	}

	// Page the live directory, then annotate and organize only the associated logical tree.
	entries := new(entity.ListLocationEntriesReply)
	cliResult(t, ctx, connection, entries, "location", "entries", decimal(id), "--name", ".txt", "--limit", "1")
	require.True(t, entries.HasMore)
	a := entries.Entries[0].Original.FileId
	next := new(entity.ListLocationEntriesReply)
	cliResult(t, ctx, connection, next, "location", "entries", decimal(id), "--name", ".txt", "--cursor", entries.NextCursor, "--limit", "1")
	b := next.Entries[0].Original.FileId
	cliResult(t, ctx, connection, new(entity.FileMetadataEditReply), "file", "metadata", decimal(a), "--add-tag", "important", "--note", "retained organization")
	_, folders := fileOrganizationCLI(t, ctx, connection, "file", "mkdir", "0", "Organized")
	require.Len(t, folders, 1)
	require.NotNil(t, folders[0].FileId)
	fileOrganizationCLI(t, ctx, connection, "file", "edit", decimal(a), "--parent-id", decimal(*folders[0].FileId), "--name", "renamed.txt")
	require.FileExists(t, filepath.Join(root, "originals", "a.txt"))
	settings := new(entity.LibrarySettings)
	cliResult(t, ctx, connection, settings, "settings", "library")
	cliResult(t, ctx, connection, new(entity.UpdateLibrarySettingsReply), "settings", "library", "--include-unbacked=false", "--revision", decimal(settings.Revision))
	results := new(entity.FileSearchReply)
	cliResult(t, ctx, connection, results, "file", "search", "tag:important", "--scope", "default", "--limit", "1")
	require.Empty(t, results.Results)
	cliResult(t, ctx, connection, results, "file", "search", "tag:important", "--scope", "all", "--limit", "1")
	require.Len(t, results.Results, 1)
	require.Equal(t, "/Organized/renamed.txt", results.Results[0].Path)
	groups := new(entity.ListDuplicateGroupsReply)
	cliResult(t, ctx, connection, groups, "file", "duplicate-groups", "--limit", "1")
	require.Len(t, groups.Groups, 1)
	require.NotEmpty(t, groups.NextCursor)
	members := new(entity.ListDuplicateMembersReply)
	cliResult(t, ctx, connection, members, "file", "duplicate-members", "--signature", hex.EncodeToString(groups.Groups[0].Signature), "--limit", "1")
	require.Len(t, members.Members, 1)
	require.NotEmpty(t, members.NextCursor)

	// One cross-directory Todo combines File and Location selections, with overlap deduplicated on the server.
	selected := []string{"--file-id", decimal(a), "--file-id", decimal(b), "--location", decimal(id) + ":docs", "--location", decimal(id) + ":docs/c.txt"}
	inspection := new(entity.InspectSelectionReply)
	cliResult(t, ctx, connection, inspection, append([]string{"file", "inspect-selection"}, selected...)...)
	require.EqualValues(t, 6, inspection.Files)
	require.Zero(t, inspection.MissingOriginals)
	volume := new(entity.VolumeInitializeReply)
	cliResult(t, ctx, connection, volume, "volume", "initialize", filepath.Join(root, "volumes", "disk"), "--name", "Backup disk", "--type", "hdd")
	archive := new(entity.CreateArchiveJobReply)
	cliResult(t, ctx, connection, archive, append([]string{"archive", "create"}, selected...)...)
	waitCLIJob(t, ctx, connection, archive.Job.Id, true)
	cliResult(t, ctx, connection, new(entity.WriteArchiveMediaReply), "archive", "write", "volume", decimal(archive.Job.Id), "--uuid", volume.Media.Identity)
	waitCLIJob(t, ctx, connection, archive.Job.Id, false)
	manifest := new(entity.ListArchiveJobFilesReply)
	cliResult(t, ctx, connection, manifest, "archive", "files", decimal(archive.Job.Id), "--limit", "10")
	require.Len(t, manifest.Items, 6)
	require.FileExists(t, filepath.Join(root, "volumes", "disk", manifest.Items[0].File.MediaPath))
	cliResult(t, ctx, connection, results, "file", "search", "tag:important", "--scope", "saved", "--limit", "1")
	require.Len(t, results.Results, 1)

	// Backup observes an edited Library original without requiring an intervening Analyze Job.
	state := new(entity.FileStateReply)
	cliResult(t, ctx, connection, state, "file", "state", decimal(a))
	firstVersion := state.LatestVersion.Id
	require.NoError(t, os.WriteFile(filepath.Join(root, "originals", "a.txt"), []byte("edited original"), 0o640))
	cliResult(t, ctx, connection, archive, "archive", "create", "--file-id", decimal(a))
	waitCLIJob(t, ctx, connection, archive.Job.Id, true)
	cliResult(t, ctx, connection, new(entity.WriteArchiveMediaReply), "archive", "write", "volume", decimal(archive.Job.Id), "--uuid", volume.Media.Identity)
	waitCLIJob(t, ctx, connection, archive.Job.Id, false)
	versions := new(entity.ListFileVersionsReply)
	cliResult(t, ctx, connection, versions, "file", "versions", decimal(a))
	require.Len(t, versions.Versions, 2)

	// Restore multiple versions into a preferred unscanned Location, preserving conflicting output and retry paths.
	target := new(entity.LocationReply)
	cliResult(t, ctx, connection, target, "location", "create", "--name", "Restored files", "--root", filepath.Join(root, "restore"), "--restore-target")
	require.True(t, target.Location.RestoreTarget)
	require.Zero(t, target.Location.LastSyncAtMs)
	output := filepath.Join(root, "restore", "results", "Organized", "renamed.txt")
	require.NoError(t, os.MkdirAll(filepath.Dir(output), 0o755))
	require.NoError(t, os.WriteFile(output, []byte("keep existing"), 0o600))
	restored := new(entity.CreateRestoreJobReply)
	cliResult(t, ctx, connection, restored, "restore", "create", decimal(versions.Versions[0].Id), decimal(versions.Versions[1].Id), "--file-id", decimal(b), "--target-location", decimal(target.Location.Id), "--directory", "results")
	waitCLIJob(t, ctx, connection, restored.Job.Id, true)
	cliResult(t, ctx, connection, new(entity.RestoreMediaReply), "restore", "run", "volume", decimal(restored.Job.Id), "--uuid", volume.Media.Identity)
	waitCLIJob(t, ctx, connection, restored.Job.Id, false)
	existing, err := os.ReadFile(output)
	require.NoError(t, err)
	require.Equal(t, "keep existing", string(existing))
	for _, version := range versions.Versions {
		expected := "edited original"
		if version.Id == firstVersion {
			expected = "same"
		}
		data, err := os.ReadFile(filepath.Join(filepath.Dir(output), library.RestoredName("renamed.txt", version.Id)))
		require.NoError(t, err)
		require.Equal(t, expected, string(data))
	}

	// Matching content is already fulfilled, without another copy or suffix.
	require.NoError(t, os.WriteFile(output, []byte("same"), 0o600))
	cliResult(t, ctx, connection, restored, "restore", "create", decimal(firstVersion), "--target-location", decimal(target.Location.Id), "--directory", "results")
	waitCLIJob(t, ctx, connection, restored.Job.Id, false)
	adopted := new(entity.FileStateReply)
	cliResult(t, ctx, connection, adopted, "file", "state", decimal(a))
	require.Equal(t, id, adopted.Original.LocationId, "recovering elsewhere never steals an existing original")
	cliResult(t, ctx, connection, target, "location", "get", decimal(target.Location.Id))
	require.Zero(t, target.Location.LastSyncAtMs)
	require.Equal(t, entity.OnlineBinding_CONFIRMED, target.Location.Binding)

	// Both scan kinds publish automatically and remain reachable in the ordinary Job catalog.
	mediaScan := new(entity.CreateScanJobReply)
	cliResult(t, ctx, connection, mediaScan, "scan", "media", decimal(volume.Media.Id))
	waitCLIJob(t, ctx, connection, mediaScan.Job.Id, false)
	cliResult(t, ctx, connection, new(entity.ListScanJobEntriesReply), "scan", "results", decimal(mediaScan.Job.Id))
	jobs := new(entity.ListJobsReply)
	cliResult(t, ctx, connection, jobs, "job", "list", "--location-id", decimal(id), "--kind", "ARCHIVE", "--status", "COMPLETED", "--limit", "2")
	require.NotEmpty(t, jobs.Jobs)
	for _, job := range jobs.Jobs {
		require.Equal(t, entity.JobKind_ARCHIVE, job.Kind)
		require.Equal(t, entity.JobStatus_COMPLETED, job.Status)
	}
	cliResult(t, ctx, connection, new(entity.GetJobLogReply), "job", "log", decimal(mediaScan.Job.Id))

	// Export remains complete despite hidden unbacked items; imported paths require confirmation.
	snapshot := filepath.Join(root, "snapshot.jsonl")
	_, err = connection.run(ctx, "library", "export", "--output", snapshot)
	require.NoError(t, err)
	_, err = connection.run(ctx, "library", "import", "--input", snapshot, "--confirm")
	require.NoError(t, err)
	cliResult(t, ctx, connection, registered, "location", "get", decimal(id))
	require.Equal(t, entity.OnlineBinding_UNCONFIRMED, registered.Location.Binding)
	cliResult(t, ctx, connection, registered, "location", "confirm", decimal(id), "--revision", decimal(registered.Location.Revision))
	cliResult(t, ctx, connection, scanned, "analyze", "create", decimal(id))
	waitCLIJob(t, ctx, connection, scanned.Job.Id, false)
	cliResult(t, ctx, connection, new(entity.DeleteJobsReply), "job", "delete", decimal(mediaScan.Job.Id), "--confirm")
}
