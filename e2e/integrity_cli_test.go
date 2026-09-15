//go:build e2e

package e2e

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
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
	registered := new(entity.LocationReply)
	cliResult(t, ctx, connection, registered, "location", "create", "--name", "Originals", "--root", filepath.Join(root, "originals"))
	scanned := new(entity.CreateScanJobReply)
	cliResult(t, ctx, connection, scanned, "analyze", "create", decimal(registered.Location.Id))
	waitCLIJob(t, ctx, connection, scanned.Job.Id, false)
	volume := new(entity.VolumeInitializeReply)
	cliResult(t, ctx, connection, volume, "volume", "initialize", filepath.Join(root, "volumes", "disk"), "--name", "Archive", "--type", "hdd")
	archive := new(entity.CreateArchiveJobReply)
	cliResult(t, ctx, connection, archive, "archive", "create", "--location", decimal(registered.Location.Id)+":")
	waitCLIJob(t, ctx, connection, archive.Job.Id, true)
	cliResult(t, ctx, connection, new(entity.WriteArchiveMediaReply), "archive", "write", "volume", decimal(archive.Job.Id), "--uuid", volume.Media.Identity)
	waitCLIJob(t, ctx, connection, archive.Job.Id, false)

	// Resolve expected version and copy identities through the public catalog rather than database fixtures.
	manifest := new(entity.ListArchiveJobFilesReply)
	cliResult(t, ctx, connection, manifest, "archive", "files", decimal(archive.Job.Id), "--limit", "10")
	require.Len(t, manifest.Items, 3)
	fixture := &integrityFixture{connection: connection, root: root, location: registered.Location, media: volume.Media, files: make(map[string]integrityFile)}
	for _, item := range manifest.Items {
		versions := new(entity.ListFileVersionsReply)
		cliResult(t, ctx, connection, versions, "file", "versions", decimal(item.File.Expected.FileId))
		require.Len(t, versions.Versions, 1)
		copies := new(entity.ListContentCopiesReply)
		cliResult(t, ctx, connection, copies, "file", "copies", "--signature", hex.EncodeToString(versions.Versions[0].Signature))
		require.Len(t, copies.Positions, 1)
		name := filepath.Base(item.File.SourcePath)
		fixture.files[name] = integrityFile{version: versions.Versions[0], position: copies.Positions[0], content: contents[name]}
	}
	return fixture
}

func runCLIIntegrityCheck(t *testing.T, ctx context.Context, fixture *integrityFixture) int64 {
	t.Helper()
	// A mounted Volume completes through the same automatic Scan pipeline.
	job := new(entity.CreateScanJobReply)
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
	progress := new(entity.GetScanJobProgressReply)
	cliResult(t, ctx, connection, progress, "job", "progress", decimal(firstID))
	require.EqualValues(t, 3, progress.Matched)
	require.Zero(t, progress.Damaged)

	// External tampering and missing data must not silently become a new expected inventory baseline.
	damaged, missing := fixture.files["damaged.txt"], fixture.files["missing.txt"]
	damagedPath := filepath.Join(fixture.root, "volumes", "disk", filepath.FromSlash(damaged.position.Path))
	missingPath := filepath.Join(fixture.root, "volumes", "disk", filepath.FromSlash(missing.position.Path))
	require.NoError(t, os.WriteFile(damagedPath, []byte("damage"), 0640))
	require.NoError(t, os.Remove(missingPath))
	checkedID := runCLIIntegrityCheck(t, ctx, fixture)
	cliResult(t, ctx, connection, progress, "job", "progress", decimal(checkedID))
	require.EqualValues(t, 1, progress.Matched)
	require.EqualValues(t, 1, progress.Damaged)
	require.EqualValues(t, 1, progress.Missing)
	require.Equal(t, progress.Progress.TotalFiles, progress.Progress.CopiedFiles)

	// Page all findings using public stable IDs and verify recorded expectations and healthy counts.
	var after int64
	found := make(map[int64]*entity.ScanEntry)
	for {
		page := new(entity.ListScanJobEntriesReply)
		cliResult(t, ctx, connection, page, "verify", "entries", decimal(checkedID), "--limit", "1", "--after-id", decimal(after))
		for _, entry := range page.Entries {
			require.NotContains(t, found, entry.PositionId)
			found[entry.PositionId] = entry
			after = entry.Id
		}
		if !page.HasMore {
			break
		}
	}
	require.Len(t, found, 3)
	require.Equal(t, entity.ScanFinding_MISMATCH, found[damaged.position.Id].Finding)
	require.Equal(t, damaged.position.Hash, found[damaged.position.Id].Sha256)
	require.NotEqual(t, damaged.position.Hash, found[damaged.position.Id].ActualHash)
	state := new(entity.FileStateReply)
	cliResult(t, ctx, connection, state, "file", "state", decimal(damaged.version.FileId))
	require.EqualValues(t, 1, state.ArchivedCopies)
	require.Zero(t, state.HealthyCopies)
	require.EqualValues(t, 1, state.UnhealthyCopies)
	copies := new(entity.ListContentCopiesReply)
	cliResult(t, ctx, connection, copies, "file", "copies", "--signature", hex.EncodeToString(damaged.version.Signature))
	require.Len(t, copies.Positions, 1)
	require.Equal(t, entity.PositionHealth_DAMAGED, copies.Positions[0].Health)
	require.Equal(t, damaged.position.Hash, copies.Positions[0].Hash)
	require.Equal(t, checkedID, copies.Positions[0].HealthJobId)

	// Repair is external and explicit; only another actual verification can clear prior bad observations.
	require.NoError(t, os.WriteFile(damagedPath, damaged.content, 0640))
	require.NoError(t, os.WriteFile(missingPath, missing.content, 0640))
	lastID := runCLIIntegrityCheck(t, ctx, fixture)
	cliResult(t, ctx, connection, progress, "job", "progress", decimal(lastID))
	require.EqualValues(t, 3, progress.Matched)
	cliResult(t, ctx, connection, copies, "file", "copies", "--signature", hex.EncodeToString(damaged.version.Signature))
	require.Equal(t, entity.PositionHealth_HEALTHY, copies.Positions[0].Health)
	require.Equal(t, damaged.position.Hash, copies.Positions[0].Hash)

	// Related Jobs are filtered before pagination by immutable resource association and durable state.
	jobs := new(entity.ListJobsReply)
	cliResult(t, ctx, connection, jobs, "job", "list", "--media-id", decimal(fixture.media.Id), "--kind", "SCAN", "--status", "COMPLETED", "--limit", "2")
	require.Len(t, jobs.Jobs, 2)
	for _, job := range jobs.Jobs {
		require.Equal(t, entity.JobKind_SCAN, job.Kind)
		require.Equal(t, entity.JobStatus_COMPLETED, job.Status)
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
	destination := new(entity.LocationReply)
	cliResult(t, ctx, connection, destination, "location", "create", "--name", "Recovery", "--root", filepath.Join(fixture.root, "restore"))
	require.False(t, destination.Location.RestoreTarget)
	// Recommendations do not restrict the actual target directory browser.
	cliResult(t, ctx, connection, new(entity.BrowsePathsReply), "settings", "browse", "--location-id", decimal(destination.Location.Id))
	recommendations := new(entity.ListLocationsReply)
	cliResult(t, ctx, connection, recommendations, "location", "list", "--restore-target", "true", "--limit", "1")
	require.Empty(t, recommendations.Locations)
	require.False(t, recommendations.HasMore)
	ordinaryLocations := new(entity.ListLocationsReply)
	cliResult(t, ctx, connection, ordinaryLocations, "location", "list", "--restore-target", "false", "--limit", "1")
	require.Len(t, ordinaryLocations.Locations, 1)
	require.True(t, ordinaryLocations.HasMore)
	cliResult(t, ctx, connection, ordinaryLocations, "location", "list", "--restore-target", "false", "--after-id", decimal(ordinaryLocations.Locations[0].Id), "--limit", "1")
	require.Len(t, ordinaryLocations.Locations, 1)
	require.Equal(t, destination.Location.Id, ordinaryLocations.Locations[0].Id)
	require.False(t, ordinaryLocations.HasMore)

	// Bad copies are not silently selected by the ordinary Restore path.
	blocked := new(entity.CreateRestoreJobReply)
	output, err := connection.run(ctx, "restore", "create", decimal(file.version.Id), "--target-location", decimal(destination.Location.Id), "--directory", "ordinary")
	if err == nil {
		require.NoError(t, decodeCLIOutput(output, blocked))
		output, err = connection.run(ctx, "job", "wait", decimal(blocked.Job.Id), "--wait-timeout", "1m", "--poll-interval", "100ms")
		require.Error(t, err)
		job := new(entity.GetJobReply)
		require.NoError(t, decodeCLIOutput(output, job))
		require.Equal(t, entity.JobPhase_JOB_PHASE_WAITING_FOR_INDEX_RETRY, job.Job.Phase)
		logs := new(entity.GetJobLogReply)
		cliResult(t, ctx, connection, logs, "job", "log", decimal(blocked.Job.Id))
		require.Contains(t, string(logs.Logs), "has no archived copy")
	} else {
		require.ErrorContains(t, err, "has no archived copy")
	}

	// Explicit advanced consent permits salvage but does not relax identity or restore-content reporting.
	restored := new(entity.CreateRestoreJobReply)
	cliResult(t, ctx, connection, restored, "restore", "create", decimal(file.version.Id), "--target-location", decimal(destination.Location.Id), "--directory", "salvage", "--allow-damaged-copies")
	waitCLIJob(t, ctx, connection, restored.Job.Id, true)
	cliResult(t, ctx, connection, new(entity.RestoreMediaReply), "restore", "run", "volume", decimal(restored.Job.Id), "--uuid", fixture.media.Identity)
	waitCLIJob(t, ctx, connection, restored.Job.Id, false)
	results := new(entity.ListRestoreJobFilesReply)
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
	progress := new(entity.GetRestoreJobProgressReply)
	cliResult(t, ctx, connection, progress, "job", "progress", decimal(restored.Job.Id))
	require.EqualValues(t, 1, progress.Summary.DamagedFiles)
	require.Zero(t, progress.Summary.VerifiedFiles)

	// Salvaged bytes remain visible on disk, without replacing the original or gaining its expected version.
	state := new(entity.FileStateReply)
	cliResult(t, ctx, connection, state, "file", "state", decimal(file.version.FileId))
	require.Equal(t, fixture.location.Id, state.Original.LocationId)
	versions := new(entity.ListFileVersionsReply)
	cliResult(t, ctx, connection, versions, "file", "versions", decimal(file.version.FileId))
	require.Len(t, versions.Versions, 1)
	entries := new(entity.ListLocationEntriesReply)
	cliResult(t, ctx, connection, entries, "location", "entries", decimal(destination.Location.Id))
	require.Len(t, entries.Entries, 1)
	require.Equal(t, "salvage", entries.Entries[0].Path)
	require.True(t, entries.Entries[0].IsDir)
	outputEntry := new(entity.LocationEntry)
	cliResult(t, ctx, connection, outputEntry, "location", "entry", decimal(destination.Location.Id), "--path", "salvage/"+item.File.TargetPath)
	require.Nil(t, outputEntry.File, "salvage must not publish the expected File identity")
	require.Nil(t, outputEntry.Original)
	cliResult(t, ctx, connection, destination, "location", "get", decimal(destination.Location.Id))
	require.Zero(t, destination.Location.LastSyncAtMs)
}
