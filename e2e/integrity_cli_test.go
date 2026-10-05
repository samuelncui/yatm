//go:build e2e

package e2e

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/samuelncui/yatm/entity"
	"github.com/stretchr/testify/require"
)

type integrityFile struct {
	version  *entity.FileVersion
	position *entity.Position
	content  []byte
}

type integrityFixture struct {
	connection *cliConnection
	root       string
	location   *entity.Location
	media      *entity.Media
	files      map[string]integrityFile
}

func prepareIntegrityFixture(t *testing.T, ctx context.Context) *integrityFixture {
	t.Helper()
	// All catalog and physical publication steps use the shipped server and CLI binaries.
	connection, root := startBinaryInstallation(t)
	for _, args := range [][]string{{"--help"}, {"verify", "--help"}, {"verify", "create", "--help"}, {"verify", "run", "--help"}} {
		_, err := connection.run(ctx, args...)
		require.NoError(t, err)
	}
	contents := map[string][]byte{"good.txt": []byte("good content"), "damaged.txt": []byte("before"), "missing.txt": []byte("missing content")}
	for name, content := range contents {
		require.NoError(t, os.WriteFile(filepath.Join(root, "originals", name), content, 0640))
	}
	registered := new(entity.CreateLocationResponse)
	cliResult(t, ctx, connection, registered, "location", "create", "--name", "Originals", "--root", filepath.Join(root, "originals"))
	scanned := new(entity.CreateScanJobResponse)
	cliResult(t, ctx, connection, scanned, "analyze", "create", decimal(registered.Location.Id))
	waitCLIJob(t, ctx, connection, scanned.Job.Id, false)
	volume := new(entity.InitializeVolumeResponse)
	cliResult(t, ctx, connection, volume, "volume", "initialize", filepath.Join(root, "volumes", "disk"), "--name", "Archive", "--type", "hdd")
	archive := new(entity.CreateArchiveJobResponse)
	cliResult(t, ctx, connection, archive, "archive", "create", "--location", decimal(registered.Location.Id)+":")
	waitCLIJob(t, ctx, connection, archive.Job.Id, true)
	cliResult(t, ctx, connection, new(entity.WriteArchiveMediaResponse), "archive", "write", "volume", decimal(archive.Job.Id), "--uuid", volume.Media.Identity)
	waitCLIJob(t, ctx, connection, archive.Job.Id, false)

	// Resolve expected version and copy identities through the public catalog rather than database fixtures.
	manifest := new(entity.ListArchiveJobFilesResponse)
	cliResult(t, ctx, connection, manifest, "archive", "files", decimal(archive.Job.Id), "--limit", "10")
	require.Len(t, manifest.Items, 3)
	fixture := &integrityFixture{connection: connection, root: root, location: registered.Location, media: volume.Media, files: make(map[string]integrityFile)}
	for _, item := range manifest.Items {
		versions := new(entity.ListFileVersionsResponse)
		cliResult(t, ctx, connection, versions, "files", "versions", decimal(item.File.Expected.FileId))
		require.Len(t, versions.Versions, 1)
		copies := new(entity.ListContentCopiesResponse)
		cliResult(t, ctx, connection, copies, "files", "copies", "--signature", hex.EncodeToString(versions.Versions[0].Signature))
		require.Len(t, copies.Positions, 1)
		name := filepath.Base(item.File.SourcePath)
		fixture.files[name] = integrityFile{version: versions.Versions[0], position: copies.Positions[0], content: contents[name]}
	}
	return fixture
}

func runCLIIntegrityCheck(t *testing.T, ctx context.Context, fixture *integrityFixture) int64 {
	t.Helper()
	// A mounted Volume completes through the same automatic Scan pipeline.
	job := new(entity.CreateScanJobResponse)
	cliResult(t, ctx, fixture.connection, job, "verify", "create", decimal(fixture.media.Id))
	waitCLIJob(t, ctx, fixture.connection, job.Job.Id, false)
	return job.Job.Id
}

func TestCLIIntegrityVerification(t *testing.T) {
	// Establish independent saved versions and healthy physical copies through ordinary Backup.
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	fixture := prepareIntegrityFixture(t, ctx)
	connection := fixture.connection
	firstID := runCLIIntegrityCheck(t, ctx, fixture)
	progress := new(entity.GetScanJobProgressResponse)
	cliResult(t, ctx, connection, progress, "job", "progress", decimal(firstID))
	require.EqualValues(t, 3, progress.MatchedCount)
	require.Zero(t, progress.DamagedCount)

	// External tampering and missing data must not silently become a new expected inventory baseline.
	damaged, missing := fixture.files["damaged.txt"], fixture.files["missing.txt"]
	damagedPath := filepath.Join(fixture.root, "volumes", "disk", filepath.FromSlash(damaged.position.Path))
	missingPath := filepath.Join(fixture.root, "volumes", "disk", filepath.FromSlash(missing.position.Path))
	require.NoError(t, os.WriteFile(damagedPath, []byte("damage"), 0640))
	require.NoError(t, os.Remove(missingPath))
	checkedID := runCLIIntegrityCheck(t, ctx, fixture)
	cliResult(t, ctx, connection, progress, "job", "progress", decimal(checkedID))
	require.EqualValues(t, 1, progress.MatchedCount)
	require.EqualValues(t, 1, progress.DamagedCount)
	require.EqualValues(t, 1, progress.MissingCount)
	require.Equal(t, progress.Progress.TotalFileCount, progress.Progress.CopiedFileCount)

	// Page all findings forward by order key and confirm the same rows in reverse.
	var after string
	found := make(map[int64]*entity.ScanEntry)
	order := []string{}
	for {
		page := new(entity.ListScanJobEntriesResponse)
		cliResult(t, ctx, connection, page, "verify", "entries", decimal(checkedID), "--limit", "1", "--cursor", after, "--include-total")
		require.EqualValues(t, 3, page.GetTotalEntryCount())
		for _, entry := range page.Entries {
			require.NotContains(t, found, entry.PositionId)
			found[entry.PositionId] = entry
			after = strconv.FormatInt(entry.Id, 10)
			order = append(order, after)
		}
		if !page.HasMore {
			break
		}
	}
	// A descending page walks the same keys backwards, and an offset anchors the last row.
	backward := new(entity.ListScanJobEntriesResponse)
	cliResult(t, ctx, connection, backward, "verify", "entries", decimal(checkedID), "--limit", "1", "--cursor", order[len(order)-1], "--order", "desc")
	require.Len(t, backward.Entries, 1)
	require.Equal(t, order[len(order)-2], strconv.FormatInt(backward.Entries[0].Id, 10))
	anchored := new(entity.ListScanJobEntriesResponse)
	cliResult(t, ctx, connection, anchored, "verify", "entries", decimal(checkedID), "--limit", "1", "--offset", "2")
	require.Len(t, anchored.Entries, 1)
	require.Equal(t, order[2], strconv.FormatInt(anchored.Entries[0].Id, 10))
	require.Len(t, found, 3)
	require.Equal(t, entity.ScanFinding_SCAN_FINDING_MISMATCH, found[damaged.position.Id].Finding)
	require.Equal(t, damaged.position.Sha256, found[damaged.position.Id].Sha256)
	require.NotEqual(t, damaged.position.Sha256, found[damaged.position.Id].ActualHash)
	copies := new(entity.ListContentCopiesResponse)
	cliResult(t, ctx, connection, copies, "files", "copies", "--signature", hex.EncodeToString(damaged.version.Signature))
	require.Len(t, copies.Positions, 1)
	require.Equal(t, entity.PositionHealth_POSITION_HEALTH_DAMAGED, copies.Positions[0].Health)
	require.Equal(t, damaged.position.Sha256, copies.Positions[0].Sha256)
	require.Equal(t, checkedID, copies.Positions[0].HealthJobId)

	// Repair is external and explicit; only another actual verification can clear prior bad observations.
	require.NoError(t, os.WriteFile(damagedPath, damaged.content, 0640))
	require.NoError(t, os.WriteFile(missingPath, missing.content, 0640))
	lastID := runCLIIntegrityCheck(t, ctx, fixture)
	cliResult(t, ctx, connection, progress, "job", "progress", decimal(lastID))
	require.EqualValues(t, 3, progress.MatchedCount)
	cliResult(t, ctx, connection, copies, "files", "copies", "--signature", hex.EncodeToString(damaged.version.Signature))
	require.Equal(t, entity.PositionHealth_POSITION_HEALTH_HEALTHY, copies.Positions[0].Health)
	require.Equal(t, damaged.position.Sha256, copies.Positions[0].Sha256)

	// Related Jobs are filtered before pagination by immutable resource association and kind.
	jobs := new(entity.ListJobsResponse)
	cliResult(t, ctx, connection, jobs, "job", "list", "--media-id", decimal(fixture.media.Id), "--kind", "SCAN", "--limit", "2")
	require.Len(t, jobs.Jobs, 2)
	for _, job := range jobs.Jobs {
		require.Equal(t, entity.JobKind_JOB_KIND_SCAN, job.Kind)
		require.Equal(t, entity.JobStatus_JOB_STATUS_COMPLETED, job.Status)
	}
	require.Equal(t, lastID, jobs.Jobs[0].Id)
	cliResult(t, ctx, connection, jobs, "job", "list", "--location-id", decimal(fixture.location.Id), "--kind", "ARCHIVE")
	require.Len(t, jobs.Jobs, 1)
}

func TestCLIDamagedCopyRecovery(t *testing.T) {
	// Mark a fully readable but corrupted archived copy through the actual verification workflow.
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	fixture := prepareIntegrityFixture(t, ctx)
	connection := fixture.connection
	file := fixture.files["damaged.txt"]
	require.NoError(t, os.WriteFile(filepath.Join(fixture.root, "volumes", "disk", filepath.FromSlash(file.position.Path)), []byte("damage"), 0640))
	runCLIIntegrityCheck(t, ctx, fixture)
	destination := new(entity.CreateLocationResponse)
	cliResult(t, ctx, connection, destination, "location", "create", "--name", "Recovery", "--root", filepath.Join(fixture.root, "restore"))
	require.False(t, destination.Location.RestoreTarget)
	// Recommendations do not restrict the actual target directory browser.
	cliResult(t, ctx, connection, new(entity.SearchFilesResponse), "ls", "--location-id", decimal(destination.Location.Id), "--query", "type:dir", "--include", "navigation", "--include", "operations")
	recommendations := new(entity.ListLocationsResponse)
	cliResult(t, ctx, connection, recommendations, "location", "list", "--restore-target", "true", "--limit", "1")
	require.Empty(t, recommendations.Locations)
	require.False(t, recommendations.HasMore)
	ordinaryLocations := new(entity.ListLocationsResponse)
	cliResult(t, ctx, connection, ordinaryLocations, "location", "list", "--restore-target", "false", "--limit", "1")
	require.Len(t, ordinaryLocations.Locations, 1)
	require.True(t, ordinaryLocations.HasMore)
	cliResult(t, ctx, connection, ordinaryLocations, "location", "list", "--restore-target", "false", "--after-id", decimal(ordinaryLocations.Locations[0].Id), "--limit", "1")
	require.Len(t, ordinaryLocations.Locations, 1)
	require.Equal(t, destination.Location.Id, ordinaryLocations.Locations[0].Id)
	require.False(t, ordinaryLocations.HasMore)

	// Bad copies are not silently selected by the ordinary Restore path.
	blocked := new(entity.CreateRestoreJobResponse)
	output, err := connection.run(ctx, "restore", "create", decimal(file.version.Id), "--target-location", decimal(destination.Location.Id), "--directory", "ordinary")
	if err == nil {
		require.NoError(t, decodeCLIOutput(output, blocked))
		output, err = connection.run(ctx, "job", "wait", decimal(blocked.Job.Id), "--wait-timeout", "1m", "--poll-interval", "100ms")
		require.Error(t, err)
		job := new(entity.GetJobResponse)
		require.NoError(t, decodeCLIOutput(output, job))
		require.Equal(t, entity.JobStatus_JOB_STATUS_FAILED, job.Job.Status)
		logs := new(entity.GetJobLogResponse)
		cliResult(t, ctx, connection, logs, "job", "log", decimal(blocked.Job.Id))
		require.Contains(t, string(logs.Logs), "has no archived copy")
	} else {
		require.ErrorContains(t, err, "has no archived copy")
	}

	// Explicit advanced consent permits salvage but does not relax identity or restore-content reporting.
	restored := new(entity.CreateRestoreJobResponse)
	cliResult(t, ctx, connection, restored, "restore", "create", decimal(file.version.Id), "--target-location", decimal(destination.Location.Id), "--directory", "salvage", "--allow-damaged-copies")
	waitCLIJob(t, ctx, connection, restored.Job.Id, true)
	cliResult(t, ctx, connection, new(entity.RestoreMediaResponse), "restore", "run", "volume", decimal(restored.Job.Id), "--uuid", fixture.media.Identity)
	waitCLIJob(t, ctx, connection, restored.Job.Id, false)
	results := new(entity.ListRestoreJobFilesResponse)
	cliResult(t, ctx, connection, results, "restore", "files", decimal(restored.Job.Id), "--media-id", decimal(fixture.media.Id))
	require.Len(t, results.Items, 1)
	item := results.Items[0]
	require.True(t, item.Damaged)
	require.False(t, item.Linked)
	require.Zero(t, item.ResultFileId)
	actualHash := sha256.Sum256([]byte("damage"))
	require.Equal(t, actualHash[:], item.ActualSha256)
	data, err := os.ReadFile(filepath.Join(fixture.root, "restore", "salvage", filepath.FromSlash(item.File.TargetPath)))
	require.NoError(t, err)
	require.Equal(t, []byte("damage"), data)
	progress := new(entity.GetRestoreJobProgressResponse)
	cliResult(t, ctx, connection, progress, "job", "progress", decimal(restored.Job.Id))
	require.EqualValues(t, 1, progress.Summary.DamagedFiles)
	require.Zero(t, progress.Summary.VerifiedFiles)

	// Salvaged bytes remain visible on disk, without replacing the original or gaining its expected version.
	versions := new(entity.ListFileVersionsResponse)
	cliResult(t, ctx, connection, versions, "files", "versions", decimal(file.version.FileId))
	require.Len(t, versions.Versions, 1)
	entries := new(entity.ListFilesResponse)
	cliResult(t, ctx, connection, entries, "ls", "--location-id", decimal(destination.Location.Id), "--path", "")
	require.Len(t, entries.Entries, 1)
	require.Equal(t, "salvage", entries.Entries[0].Path)
	require.Equal(t, entity.EntryKind_ENTRY_KIND_DIRECTORY, entries.Entries[0].Kind)
	cliResult(t, ctx, connection, destination, "location", "get", decimal(destination.Location.Id))
	require.Zero(t, destination.Location.LastSyncAtNs)
}
