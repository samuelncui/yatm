//go:build e2e

package e2e

import (
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/samuelncui/acp"
	"github.com/samuelncui/yatm/apis"
	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/executor"
	_ "github.com/samuelncui/yatm/executor/archive"
	_ "github.com/samuelncui/yatm/executor/restore"
	_ "github.com/samuelncui/yatm/executor/scan"
	"github.com/samuelncui/yatm/library"
	mediapkg "github.com/samuelncui/yatm/media"
	"github.com/samuelncui/yatm/resource"
	"github.com/stretchr/testify/require"
)

type volumeE2EFixture struct {
	cli        *cliConnection
	paths      executor.Paths
	volumeRoot string
	filesURL   string
	exe        *executor.Executor
	lib        *library.Library
	job        entity.JobServiceClient
	service    entity.ServiceClient
	archive    entity.ArchiveJobServiceClient
	restore    entity.RestoreJobServiceClient
	scan       entity.ScanJobServiceClient
	online     entity.LocationServiceClient
	sync       entity.ScanJobServiceClient
	catalog    entity.FileCatalogServiceClient
}

func TestVolumeArchiveRestoreScan(t *testing.T) {
	// Bound one complete mounted-Volume lifecycle and isolate all service state.
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	fixture := newVolumeE2EFixture(t, ctx)

	// Initialize one mounted HDD Volume through gRPC and preserve its physical marker for later comparison.
	initialized, err := fixture.service.VolumeInitialize(ctx, &entity.VolumeInitializeRequest{
		MountPoint: fixture.volumeRoot,
		Name:       "Offline Disk",
		Profile: &entity.VolumeMediaProfile{
			SerialNumber: "volume-e2e", Type: entity.VolumeType_VOLUME_TYPE_HDD,
		},
	})
	require.NoError(t, err)
	media := initialized.Media
	require.Equal(t, entity.MediaKind_MEDIA_KIND_VOLUME, media.Kind)
	require.Equal(t, entity.MediaAccess_MEDIA_ACCESS_CONCURRENT_RANDOM, media.Capabilities.Read)
	require.Equal(t, entity.MediaAccess_MEDIA_ACCESS_CONCURRENT_RANDOM, media.Capabilities.Write)
	markerPath := filepath.Join(fixture.volumeRoot, mediapkg.VolumeMarkerName)
	marker, err := os.ReadFile(markerPath)
	require.NoError(t, err)

	// Archive two source files through the Volume Backend and verify their final physical paths and Library positions.
	contents := map[string][]byte{
		"dataset/changed.txt": []byte("before"),
		"dataset/removed.txt": []byte("removed"),
	}
	for name, content := range contents {
		filename := filepath.Join(fixture.paths.Source, filepath.FromSlash(name))
		require.NoError(t, os.MkdirAll(filepath.Dir(filename), 0o755))
		require.NoError(t, os.WriteFile(filename, content, 0o644))
	}
	created, err := fixture.archive.Create(ctx, &entity.CreateArchiveJobRequest{
		Spec: &entity.ArchiveJobSpec{Selections: indexedSelections(t, ctx, fixture.cli, fixture.paths.Source, "dataset")},
	})
	require.NoError(t, err)
	archiveID := created.Job.Id
	waitForVolumeJobPending(t, ctx, fixture, archiveID)
	_, err = fixture.archive.WriteMedia(ctx, &entity.WriteArchiveMediaRequest{
		Id: archiveID, Target: (&entity.ArchiveVolumeTarget{Uuid: strings.ToUpper(media.Identity)}).Pack(),
	})
	require.NoError(t, err)
	waitForVolumeJobCompleted(t, ctx, fixture, archiveID)
	archived, err := fixture.archive.ListFiles(ctx, &entity.ListArchiveJobFilesRequest{Id: archiveID, Limit: 10})
	require.NoError(t, err)
	require.Len(t, archived.Items, len(contents))
	items := make(map[string]*entity.ArchiveItem, len(archived.Items))
	for _, item := range archived.Items {
		require.Equal(t, entity.CopyStatus_SUBMITTED, item.Status)
		require.NotNil(t, item.MediaId)
		require.Equal(t, media.Id, *item.MediaId)
		actual, err := os.ReadFile(filepath.Join(fixture.volumeRoot, filepath.FromSlash(item.File.MediaPath)))
		require.NoError(t, err)
		require.Equal(t, contents[strings.TrimPrefix(item.File.TargetPath, "Unforged/Archive/")], actual)
		wantHash := sha256.Sum256(actual)
		for _, filename := range []string{
			filepath.Join(fixture.paths.Source, filepath.FromSlash(strings.TrimPrefix(item.File.TargetPath, "Unforged/Archive/"))),
			filepath.Join(fixture.volumeRoot, filepath.FromSlash(item.File.MediaPath)),
		} {
			signature, valid, err := acp.ReadCachedSignature(filename)
			require.NoError(t, err)
			require.True(t, valid)
			require.Equal(t, wantHash, signature.SHA256)
		}
		items[strings.TrimPrefix(item.File.TargetPath, "Unforged/Archive/")] = item
	}
	positionParent := path.Dir(items["dataset/changed.txt"].File.MediaPath) + "/"
	positions, err := fixture.service.MediaGetPositions(ctx, &entity.MediaGetPositionsRequest{
		Id: media.Id, Directory: positionParent,
	})
	require.NoError(t, err)
	require.Len(t, positions.Positions, len(contents))

	// Read one archived Position directly from the mounted online Volume, including an HTTP byte range.
	onlineRequest, err := http.NewRequestWithContext(
		ctx, http.MethodGet, fmt.Sprintf("%s/content/%d", fixture.filesURL, positions.Positions[0].Id), nil,
	)
	require.NoError(t, err)
	onlineRequest.Header.Set("Range", "bytes=1-3")
	onlineResponse, err := http.DefaultClient.Do(onlineRequest)
	require.NoError(t, err)
	require.Equal(t, http.StatusPartialContent, onlineResponse.StatusCode)
	onlineData, err := io.ReadAll(onlineResponse.Body)
	require.NoError(t, err)
	require.NoError(t, onlineResponse.Body.Close())
	var expectedOnline []byte
	for targetPath, item := range items {
		if item.File.MediaPath == positions.Positions[0].Path {
			expectedOnline = contents[targetPath]
			break
		}
	}
	require.NotNil(t, expectedOnline)
	require.Equal(t, expectedOnline, readHTTPContent(t, ctx, fmt.Sprintf("%s/content/%d", fixture.filesURL, positions.Positions[0].Id)))
	require.Equal(t, expectedOnline[1:4], onlineData)

	// Restore both archived Files through the same Volume and verify their bytes and SHA-256.
	fileIDs := make([]int64, 0, len(positions.Positions))
	for _, item := range archived.Items {
		fileIDs = append(fileIDs, item.File.Expected.FileId)
	}
	restoreCreated, err := fixture.restore.Create(ctx, &entity.CreateRestoreJobRequest{
		Spec: &entity.RestoreJobSpec{Destination: restoreDestination(t, ctx, fixture.cli, fixture.paths.Target), Selections: librarySelections(fileIDs...)},
	})
	require.NoError(t, err)
	restoreID := restoreCreated.Job.Id
	waitForVolumeJobPending(t, ctx, fixture, restoreID)
	_, err = fixture.restore.RestoreMedia(ctx, &entity.RestoreMediaRequest{
		Id: restoreID, Target: (&entity.ReadVolumeTarget{Uuid: media.Identity}).Pack(),
	})
	require.NoError(t, err)
	waitForVolumeJobCompleted(t, ctx, fixture, restoreID)
	for name, expected := range contents {
		actual, err := os.ReadFile(filepath.Join(fixture.paths.Target, "Unforged", "Archive", name))
		require.NoError(t, err)
		require.Equal(t, expected, actual)
		require.Equal(t, sha256.Sum256(expected), sha256.Sum256(actual))
		signature, valid, err := acp.ReadCachedSignature(filepath.Join(fixture.paths.Target, "Unforged", "Archive", name))
		require.NoError(t, err)
		require.True(t, valid)
		require.Equal(t, sha256.Sum256(expected), signature.SHA256)
	}

	// Create one added, changed, and removed physical fact, then apply the default cached Scan through gRPC.
	changedPath := filepath.Join(fixture.volumeRoot, filepath.FromSlash(items["dataset/changed.txt"].File.MediaPath))
	removedPath := filepath.Join(fixture.volumeRoot, filepath.FromSlash(items["dataset/removed.txt"].File.MediaPath))
	changedContent := []byte("changed-content")
	require.NoError(t, os.WriteFile(changedPath, changedContent, 0o644))
	require.NoError(t, os.Remove(removedPath))
	addedPath := filepath.Join(fixture.volumeRoot, "manual", "added.txt")
	addedContent := []byte("added")
	addedModified := time.Unix(100, 0)
	require.NoError(t, os.MkdirAll(filepath.Dir(addedPath), 0o755))
	require.NoError(t, os.WriteFile(addedPath, addedContent, 0o640))
	require.NoError(t, os.Chtimes(addedPath, addedModified, addedModified))
	cachedID, cachedEntries := createVolumeScan(t, ctx, fixture, media.Id, false)
	require.Len(t, cachedEntries, 3)
	changes := make(map[string]entity.ScanChange, len(cachedEntries))
	for _, entry := range cachedEntries {
		changes[entry.Path] = entry.Change
	}
	require.Equal(t, entity.ScanChange_SCAN_CHANGE_ADDED, changes["manual/added.txt"])
	require.Equal(t, entity.ScanChange_SCAN_CHANGE_CHANGED, changes[items["dataset/changed.txt"].File.MediaPath])
	require.Equal(t, entity.ScanChange_SCAN_CHANGE_REMOVED, changes[items["dataset/removed.txt"].File.MediaPath])
	cachedProgress, err := fixture.scan.GetProgress(ctx, &entity.GetScanJobProgressRequest{Id: cachedID})
	require.NoError(t, err)
	require.Equal(t, int64(1), cachedProgress.Added)
	require.Equal(t, int64(1), cachedProgress.Changed)
	require.Equal(t, int64(1), cachedProgress.Removed)
	waitForVolumeJobCompleted(t, ctx, fixture, cachedID)
	require.FileExists(t, addedPath)
	require.FileExists(t, changedPath)
	require.NoFileExists(t, removedPath)

	// Keep metadata stable while changing content, so Force rehash must detect the difference by SHA-256.
	addedInfo, err := os.Stat(addedPath)
	require.NoError(t, err)
	fullContent := []byte("other")
	require.Len(t, fullContent, len(addedContent))
	require.NoError(t, os.WriteFile(addedPath, fullContent, addedInfo.Mode()))
	require.NoError(t, os.Chmod(addedPath, addedInfo.Mode()))
	require.NoError(t, os.Chtimes(addedPath, addedInfo.ModTime(), addedInfo.ModTime()))
	forcedID, forcedEntries := createVolumeScan(t, ctx, fixture, media.Id, true)
	// Unified Scan exposes the complete manifest, including unchanged observations.
	require.Len(t, forcedEntries, 2)
	var changedEntry *entity.ScanEntry
	for _, entry := range forcedEntries {
		if entry.Change == entity.ScanChange_SCAN_CHANGE_CHANGED {
			require.Nil(t, changedEntry, "only the modified file is a change")
			changedEntry = entry
		}
	}
	require.NotNil(t, changedEntry)
	require.Equal(t, "manual/added.txt", changedEntry.Path)
	forcedHash := sha256.Sum256(fullContent)
	require.Equal(t, forcedHash[:], changedEntry.Sha256)
	waitForVolumeJobCompleted(t, ctx, fixture, forcedID)
	manualPositions, err := fixture.service.MediaGetPositions(ctx, &entity.MediaGetPositionsRequest{
		Id: media.Id, Directory: "manual/",
	})
	require.NoError(t, err)
	require.Len(t, manualPositions.Positions, 1)
	require.Equal(t, forcedHash[:], manualPositions.Positions[0].Hash)
	// Unknown inventory becomes an independently organized saved File only through explicit admission.
	imported := new(entity.ImportArchivePositionsReply)
	cliResult(t, ctx, fixture.cli, imported, "file", "import-positions", decimal(manualPositions.Positions[0].Id))
	require.Len(t, imported.FileIds, 1)
	state := new(entity.FileStateReply)
	cliResult(t, ctx, fixture.cli, state, "file", "state", decimal(imported.FileIds[0]))
	require.NotNil(t, state.LatestVersion)
	cliResult(t, ctx, fixture.cli, new(entity.LibraryTrimReply), "library", "trim", "--files", "--confirm")
	cliResult(t, ctx, fixture.cli, new(entity.FileGetReply), "file", "get", decimal(imported.FileIds[0]))

	// Delete only Media metadata after proving that the marker and every surviving physical file remain intact.
	_, err = fixture.service.MediaDelete(ctx, &entity.MediaDeleteRequest{Ids: []int64{media.Id}})
	require.NoError(t, err)
	listed, err := fixture.service.MediaList(ctx, (&entity.MediaMGetRequest{Ids: []int64{media.Id}}).Pack())
	require.NoError(t, err)
	require.Empty(t, listed.Media)
	actualMarker, err := os.ReadFile(markerPath)
	require.NoError(t, err)
	require.Equal(t, marker, actualMarker)
	require.FileExists(t, changedPath)
	require.FileExists(t, addedPath)
	require.NoFileExists(t, removedPath)

	// Register the existing marker without touching physical files, then explicitly Scan to rebuild Positions.
	registered, err := fixture.service.VolumeRegister(ctx, &entity.VolumeRegisterRequest{
		MountPoint: fixture.volumeRoot, Name: "Registered Disk",
	})
	require.NoError(t, err)
	require.NotEqual(t, media.Id, registered.Media.Id)
	require.Equal(t, media.Identity, registered.Media.Identity)
	registeredMarker, err := os.ReadFile(markerPath)
	require.NoError(t, err)
	require.Equal(t, marker, registeredMarker)
	rebuildID, rebuildEntries := createVolumeScan(t, ctx, fixture, registered.Media.Id, false)
	require.Len(t, rebuildEntries, 2)
	waitForVolumeJobCompleted(t, ctx, fixture, rebuildID)
	rebuilt, err := fixture.lib.ListMediaFilePositions(ctx, registered.Media.Id, "", 10)
	require.NoError(t, err)
	require.Len(t, rebuilt, 2)

	_, err = fixture.job.Delete(ctx, &entity.DeleteJobsRequest{Ids: []int64{archiveID, restoreID, cachedID, forcedID, rebuildID}})
	require.NoError(t, err)
	jobs, err := fixture.job.List(ctx, &entity.ListJobsRequest{Filter: &entity.JobFilter{}})
	require.NoError(t, err)
	require.Len(t, jobs.Jobs, 1, "source scans have an independent Job lifecycle")
	require.Equal(t, entity.JobKind_SCAN, jobs.Jobs[0].Kind)
	_, err = fixture.job.Delete(ctx, &entity.DeleteJobsRequest{Ids: []int64{jobs.Jobs[0].Id}})
	require.NoError(t, err)
}

func newVolumeE2EFixture(t *testing.T, ctx context.Context) *volumeE2EFixture {
	t.Helper()

	// Create isolated storage and register one mounted-Volume discovery root.
	root := t.TempDir()
	executorDB, err := resource.OpenSQLite(filepath.Join(root, "executor.db"))
	require.NoError(t, err)
	libraryDB, err := resource.OpenSQLite(filepath.Join(root, "library.db"))
	require.NoError(t, err)
	lib := library.New(libraryDB)
	require.NoError(t, lib.AutoMigrate())
	paths := executor.Paths{
		Work: filepath.Join(root, "work"), Source: filepath.Join(root, "source"),
		Target: filepath.Join(root, "target"), Volumes: []string{filepath.Join(root, "volumes")},
	}
	volumeRoot := filepath.Join(paths.Volumes[0], "offline-disk")
	require.NoError(t, os.MkdirAll(volumeRoot, 0o755))
	exe := executor.New(executorDB, lib, nil, paths, executor.Scripts{}, nil)
	require.NoError(t, exe.AutoMigrate())
	require.NoError(t, exe.ReconcileStorage(ctx))

	// Run every business operation through the shipped CLI against a loopback service.
	api := apis.New(lib, exe)
	conn := serveCLI(t, api, exe)
	// Expose only generated clients to the E2E flow.
	return &volumeE2EFixture{
		paths: paths, volumeRoot: volumeRoot, filesURL: conn.url + "/files", cli: conn, exe: exe, lib: lib,
		job: entity.NewJobServiceClient(conn), service: entity.NewServiceClient(conn),
		archive: entity.NewArchiveJobServiceClient(conn), restore: entity.NewRestoreJobServiceClient(conn),
		scan:    entity.NewScanJobServiceClient(conn),
		catalog: entity.NewFileCatalogServiceClient(conn), online: entity.NewLocationServiceClient(conn), sync: entity.NewScanJobServiceClient(conn),
	}
}

func createVolumeScan(
	t *testing.T,
	ctx context.Context,
	fixture *volumeE2EFixture,
	mediaID int64,
	forceRehash bool,
) (int64, []*entity.ScanEntry) {
	t.Helper()

	// Wait for automatic publication before reading the retained difference result.
	policy := entity.ScanSignaturePolicy_FILL_MISSING
	if forceRehash {
		policy = entity.ScanSignaturePolicy_FORCE_READ
	}
	created, err := fixture.scan.Create(ctx, &entity.CreateScanJobRequest{
		Spec: &entity.ScanJobSpec{MediaId: mediaID, SignaturePolicy: policy, ResultPolicy: entity.ScanResultPolicy_PUBLISH_INVENTORY},
	})
	require.NoError(t, err)
	waitForVolumeJobCompleted(t, ctx, fixture, created.Job.Id)
	reply, err := fixture.scan.ListEntries(ctx, &entity.ListScanJobEntriesRequest{
		Id: created.Job.Id, Limit: 100,
	})
	require.NoError(t, err)
	require.False(t, reply.HasMore)
	return created.Job.Id, reply.Entries
}

func waitForVolumeJobPending(t *testing.T, ctx context.Context, fixture *volumeE2EFixture, id int64) {
	// Poll within the caller's deadline without retaining a ticker afterward.
	t.Helper()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()

	for {
		// Observe runner lifetime first so a stopped decision uses a newer durable checkpoint.
		running := fixture.exe.IsRunning(id)
		reply, err := fixture.job.Get(ctx, &entity.GetJobRequest{Id: id})
		require.NoError(t, err)
		job := reply.Job
		if job.Status == entity.JobStatus_PENDING && job.Phase == entity.JobPhase_JOB_PHASE_WAITING_FOR_MEDIA {
			return
		}
		if !running {
			failVolumeJob(t, ctx, fixture.job, job)
		}
		waitVolumeJobTick(t, ctx, ticker)
	}
}

func waitForVolumeJobCompleted(t *testing.T, ctx context.Context, fixture *volumeE2EFixture, id int64) {
	// Poll within the caller's deadline without retaining a ticker afterward.
	t.Helper()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()

	for {
		// Observe runner lifetime first so a stopped decision uses a newer durable checkpoint.
		running := fixture.exe.IsRunning(id)
		reply, err := fixture.job.Get(ctx, &entity.GetJobRequest{Id: id})
		require.NoError(t, err)
		job := reply.Job
		if job.Status == entity.JobStatus_COMPLETED {
			return
		}
		if !running {
			failVolumeJob(t, ctx, fixture.job, job)
		}
		waitVolumeJobTick(t, ctx, ticker)
	}
}

func failVolumeJob(t *testing.T, ctx context.Context, client entity.JobServiceClient, job *entity.Job) {
	t.Helper()
	logs, err := client.GetLog(ctx, &entity.GetJobLogRequest{Id: job.Id})
	require.NoError(t, err)
	t.Fatalf("job %d stopped at %s/%s:\n%s", job.Id, job.Status, job.Phase, logs.Logs)
}

func waitVolumeJobTick(t *testing.T, ctx context.Context, ticker *time.Ticker) {
	t.Helper()
	select {
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	case <-ticker.C:
	}
}
