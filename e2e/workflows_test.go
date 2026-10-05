//go:build e2e

package e2e

import (
	"context"
	"encoding/hex"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/internal/config"
	"github.com/samuelncui/yatm/internal/executor"
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
	return connection, root
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
	reply := new(entity.GetJobResponse)
	require.NoError(t, protojson.Unmarshal(output, reply), string(output))
	if pending {
		require.Equal(t, entity.JobPhase_JOB_PHASE_UNSPECIFIED, reply.Job.Phase)
	} else {
		require.Equal(t, entity.JobStatus_JOB_STATUS_COMPLETED, reply.Job.Status)
	}
	return reply.Job
}

func filesEntryID(entry *entity.FilesEntry) int64 {
	if id := entry.GetReference().GetFileId(); id != 0 {
		return id
	}
	return entry.GetAssociatedFileId()
}

func TestCLIRegisteredLocationWorkflows(t *testing.T) {
	// Run a complete Files, Archive and Restore workflow through production binaries.
	connection, root := startBinaryInstallation(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	for name, data := range map[string]string{"a.txt": "same", "b.txt": "same", "docs/c.txt": "second"} {
		require.NoError(t, os.WriteFile(filepath.Join(root, "originals", name), []byte(data), 0o640))
	}
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

	// Register a real Location and prove initial browsing is pure before explicit metadata admission.
	access := new(entity.GetLocationAccessResponse)
	cliResult(t, ctx, connection, access, "settings", "access")
	require.Len(t, access.Ranges, 2)
	registered := new(entity.CreateLocationResponse)
	cliResult(t, ctx, connection, registered, "location", "create", "--name", "Documents", "--root", filepath.Join(root, "originals"))
	id := decimal(registered.Location.Id)
	caseSelection := new(entity.SelectionInspectionResult)
	cliResult(t, ctx, connection, caseSelection, "archive", "estimate", "--location", id+":Photos")
	require.EqualValues(t, 1, caseSelection.FileCount)
	require.EqualValues(t, 5, caseSelection.TotalBytes)
	if distinctCase {
		cliResult(t, ctx, connection, caseSelection, "archive", "estimate", "--location", id+":photos")
		require.EqualValues(t, 1, caseSelection.FileCount)
		require.EqualValues(t, 9, caseSelection.TotalBytes)
	}
	// One listing is the whole directory, and reading it admits nothing.
	page := new(entity.ListFilesResponse)
	cliResult(t, ctx, connection, page, "ls", "--location-id", id, "--path", "")
	expected := []string{"a.txt", "b.txt", "docs", "Photos"}
	if distinctCase {
		expected = append(expected, "photos")
	}
	names := make([]string, 0, len(page.Entries))
	for _, entry := range page.Entries {
		names = append(names, entry.Path)
		require.Nil(t, entry.AssociatedFileId)
	}
	require.ElementsMatch(t, expected, names)
	require.EqualValues(t, len(expected), page.GetTotalEntryCount())

	// Metadata establishes only the selected association, then Library organization remains independent of bytes.
	metadata := new(entity.UpdateFilesMetadataResponse)
	cliResult(t, ctx, connection, metadata, "files", "metadata", "--location", id+":a.txt", "--add-tag", "important", "--note", "retained organization")
	require.Len(t, metadata.Entries, 1)
	a := filesEntryID(metadata.Entries[0].Entry)
	require.Positive(t, a)
	fileOperationCLI(t, ctx, connection, "mkdir", "--library", "--destination", "0", "--name", "Organized")
	folders := new(entity.SearchFilesResponse)
	cliResult(t, ctx, connection, folders, "ls", "--file-id", "0", "--query", "name:Organized")
	require.Len(t, folders.Entries, 1)
	fileOperationCLI(t, ctx, connection, "mv", "--library", "--source", decimal(a), "--destination", decimal(folders.Entries[0].GetReference().GetFileId()), "--name", "renamed.txt")
	require.FileExists(t, filepath.Join(root, "originals", "a.txt"))
	detail := new(entity.FilesDetail)
	cliResult(t, ctx, connection, detail, "files", "get", "--file-id", decimal(a))
	require.Equal(t, "retained organization", detail.Organization.Note)
	require.Equal(t, []string{"important"}, detail.Organization.Tags)

	// Explicit Archive selection deduplicates overlapping roots and produces version/copy projections.
	inspection := new(entity.SelectionInspectionResult)
	cliResult(t, ctx, connection, inspection, "archive", "estimate", "--file-id", decimal(a), "--location", id+":b.txt", "--location", id+":docs", "--location", id+":docs/c.txt")
	require.EqualValues(t, 3, inspection.FileCount)
	volume := new(entity.InitializeVolumeResponse)
	cliResult(t, ctx, connection, volume, "volume", "initialize", filepath.Join(root, "volumes", "disk"), "--name", "Backup disk", "--type", "hdd")
	archive := new(entity.CreateArchiveJobResponse)
	cliResult(t, ctx, connection, archive, "archive", "create", "--file-id", decimal(a), "--location", id+":b.txt", "--location", id+":docs", "--location", id+":docs/c.txt")
	waitCLIJob(t, ctx, connection, archive.Job.Id, true)
	cliResult(t, ctx, connection, new(entity.WriteArchiveMediaResponse), "archive", "write", "volume", decimal(archive.Job.Id), "--uuid", volume.Media.Identity)
	waitCLIJob(t, ctx, connection, archive.Job.Id, false)
	manifest := new(entity.ListArchiveJobFilesResponse)
	cliResult(t, ctx, connection, manifest, "archive", "files", decimal(archive.Job.Id), "--limit", "10")
	require.Len(t, manifest.Items, 3)
	versions := new(entity.ListFileVersionsResponse)
	cliResult(t, ctx, connection, versions, "files", "versions", decimal(a))
	require.Len(t, versions.Versions, 1)
	copies := new(entity.ListContentCopiesResponse)
	cliResult(t, ctx, connection, copies, "files", "copies", "--signature", hex.EncodeToString(versions.Versions[0].Signature))
	require.NotEmpty(t, copies.Positions)
	duplicates := new(entity.ListContentDuplicatesResponse)
	cliResult(t, ctx, connection, duplicates, "files", "duplicates", "--signature", hex.EncodeToString(versions.Versions[0].Signature))
	require.Len(t, duplicates.Entries, 2)

	// A changed original creates a second version without an intermediate Scan.
	require.NoError(t, os.WriteFile(filepath.Join(root, "originals", "a.txt"), []byte("edited original"), 0o640))
	cliResult(t, ctx, connection, archive, "archive", "create", "--file-id", decimal(a))
	waitCLIJob(t, ctx, connection, archive.Job.Id, true)
	cliResult(t, ctx, connection, new(entity.WriteArchiveMediaResponse), "archive", "write", "volume", decimal(archive.Job.Id), "--uuid", volume.Media.Identity)
	waitCLIJob(t, ctx, connection, archive.Job.Id, false)
	cliResult(t, ctx, connection, versions, "files", "versions", decimal(a))
	require.Len(t, versions.Versions, 2)

	// Restore both versions without overwriting a conflicting output and retain the existing original association.
	target := new(entity.CreateLocationResponse)
	cliResult(t, ctx, connection, target, "location", "create", "--name", "Restored files", "--root", filepath.Join(root, "restore"), "--restore-target")
	output := filepath.Join(root, "restore", "results", "Organized", "renamed.txt")
	require.NoError(t, os.MkdirAll(filepath.Dir(output), 0o755))
	require.NoError(t, os.WriteFile(output, []byte("keep existing"), 0o600))
	restored := new(entity.CreateRestoreJobResponse)
	cliResult(t, ctx, connection, restored, "restore", "create", decimal(versions.Versions[0].Id), decimal(versions.Versions[1].Id), "--target-location", decimal(target.Location.Id), "--directory", "results")
	waitCLIJob(t, ctx, connection, restored.Job.Id, true)
	cliResult(t, ctx, connection, new(entity.RestoreMediaResponse), "restore", "run", "volume", decimal(restored.Job.Id), "--uuid", volume.Media.Identity)
	waitCLIJob(t, ctx, connection, restored.Job.Id, false)
	existing, err := os.ReadFile(output)
	require.NoError(t, err)
	require.Equal(t, "keep existing", string(existing))
	resultEntries, err := os.ReadDir(filepath.Dir(output))
	require.NoError(t, err)
	require.GreaterOrEqual(t, len(resultEntries), 2, "each conflicting version keeps a recoverable output")
	existing, err = os.ReadFile(output)
	require.NoError(t, err)
	require.Equal(t, "keep existing", string(existing))

	// A byte-identical target fulfills the selected old version without another Media attempt.
	require.NoError(t, os.WriteFile(output, []byte("same"), 0o600))
	cliResult(t, ctx, connection, restored, "restore", "create", decimal(versions.Versions[0].Id), "--target-location", decimal(target.Location.Id), "--directory", "results")
	waitCLIJob(t, ctx, connection, restored.Job.Id, false)
	cliResult(t, ctx, connection, detail, "files", "get", "--file-id", decimal(a))
	require.Equal(t, registered.Location.Id, detail.Original.Reference.GetLocation().LocationId)

	// Media inventory Scan and the ordinary Job catalog retain completed work independently of Files reads.
	mediaScan := new(entity.CreateScanJobResponse)
	cliResult(t, ctx, connection, mediaScan, "scan", "media", decimal(volume.Media.Id))
	waitCLIJob(t, ctx, connection, mediaScan.Job.Id, false)
	results := new(entity.ListScanJobEntriesResponse)
	cliResult(t, ctx, connection, results, "scan", "results", decimal(mediaScan.Job.Id))
	jobs := new(entity.ListJobsResponse)
	cliResult(t, ctx, connection, jobs, "job", "list", "--location-id", id, "--kind", "ARCHIVE", "--limit", "2")
	require.NotEmpty(t, jobs.Jobs)
	for _, job := range jobs.Jobs {
		require.Equal(t, entity.JobKind_JOB_KIND_ARCHIVE, job.Kind)
		require.Equal(t, entity.JobStatus_JOB_STATUS_COMPLETED, job.Status)
	}
	cliResult(t, ctx, connection, new(entity.GetJobLogResponse), "job", "log", decimal(mediaScan.Job.Id))
	cliResult(t, ctx, connection, detail, "files", "get", "--file-id", decimal(a))
	require.Equal(t, registered.Location.Id, detail.Original.Reference.GetLocation().LocationId)

	// Export/import retains metadata, and the imported registration stays usable without confirmation.
	snapshot := filepath.Join(root, "snapshot.jsonl")
	_, err = connection.run(ctx, "library", "export", "--output", snapshot)
	require.NoError(t, err)
	_, err = connection.run(ctx, "library", "import", "--input", snapshot)
	require.NoError(t, err)
	cliResult(t, ctx, connection, registered, "location", "get", decimal(registered.Location.Id))

	// Ignore hides matching entries from live browsing and Scan publication; unregistration retains bytes.
	ignored := filepath.Join(root, "originals", "ignored.txt")
	require.NoError(t, os.WriteFile(ignored, []byte("ignore me"), 0o600))
	ignoreFile := filepath.Join(root, "ignore")
	require.NoError(t, os.WriteFile(ignoreFile, []byte("# retained text\n/ignored.txt\n"), 0o600))
	cliResult(t, ctx, connection, registered, "location", "get", decimal(registered.Location.Id))
	cliResult(t, ctx, connection, registered, "location", "update", decimal(registered.Location.Id), "--name", "Renamed", "--root", filepath.Join(root, "originals"), "--ignore-file", ignoreFile)
	require.Equal(t, "# retained text\n/ignored.txt\n", registered.Location.Config.GetIgnore().GetText())
	cliResult(t, ctx, connection, new(entity.CreateScanJobResponse), "scan", "create", "--location-id", id, "--signature", "known-only", "--result", "originals")
	// The returned Scan is the latest catalog Job and completes before live reads below.
	cliResult(t, ctx, connection, jobs, "job", "list", "--kind", "SCAN", "--limit", "1")
	require.NotEmpty(t, jobs.Jobs)
	waitCLIJob(t, ctx, connection, jobs.Jobs[0].Id, false)
	ignoredQuery := new(entity.SearchFilesResponse)
	cliResult(t, ctx, connection, ignoredQuery, "ls", "--location-id", id, "--query", "name:ignored.txt")
	require.Empty(t, ignoredQuery.Entries)
	// Explicit path access bypasses user Ignore without admitting the entry.
	ignoredDetail := new(entity.FilesDetail)
	cliResult(t, ctx, connection, ignoredDetail, "files", "get", "--location-id", id, "--path", "ignored.txt")
	require.Zero(t, filesEntryID(ignoredDetail.Entry))
	cliResult(t, ctx, connection, registered, "location", "get", decimal(registered.Location.Id))
	cliResult(t, ctx, connection, new(entity.DeleteLocationResponse), "location", "delete", decimal(registered.Location.Id))
	require.FileExists(t, filepath.Join(root, "originals", "a.txt"))
	cliResult(t, ctx, connection, detail, "files", "get", "--file-id", decimal(a))
	require.Equal(t, []string{"important"}, detail.Organization.Tags)
	cliResult(t, ctx, connection, new(entity.DeleteJobsResponse), "job", "delete", decimal(mediaScan.Job.Id))
}
