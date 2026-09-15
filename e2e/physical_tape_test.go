//go:build e2e

package e2e

import (
	"bufio"
	"bytes"
	"connectrpc.com/connect"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"sort"
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
	previewcore "github.com/samuelncui/yatm/preview"
	"github.com/samuelncui/yatm/resource"
	"github.com/stretchr/testify/require"
)

const (
	physicalStageEnvironment   = "YATM_E2E_PHYSICAL_STAGE"
	physicalRootEnvironment    = "YATM_E2E_PHYSICAL_ROOT"
	physicalDeviceEnvironment  = "YATM_E2E_PHYSICAL_DEVICE"
	physicalBarcodeEnvironment = "YATM_E2E_PHYSICAL_BARCODE"
	physicalScriptsEnvironment = "YATM_E2E_PHYSICAL_SCRIPTS"
)

type physicalTapeState struct {
	Barcode            string                 `json:"barcode"`
	MediaID            int64                  `json:"media_id"`
	DatasetDirectoryID int64                  `json:"dataset_directory_id"`
	AppendFileIDs      []int64                `json:"append_file_ids"`
	FullArchiveJobID   int64                  `json:"full_archive_job_id,omitempty"`
	Files              []physicalExpectedFile `json:"files"`
}

type physicalExpectedFile struct {
	TargetPath   string `json:"target_path"`
	MediaPath    string `json:"media_path"`
	RestorePath  string `json:"restore_path"`
	Size         int64  `json:"size"`
	SHA256       string `json:"sha256"`
	FileID       int64  `json:"file_id"`
	StorageOrder string `json:"storage_order"`
}

type physicalTapeFixture struct {
	cli      *cliConnection
	root     string
	device   string
	barcode  string
	paths    executor.Paths
	exe      *executor.Executor
	lib      *library.Library
	filesURL string
	job      entity.JobServiceClient
	service  entity.ServiceClient
	archive  entity.ArchiveJobServiceClient
	restore  entity.RestoreJobServiceClient
}

func TestPhysicalTapeBaseline(t *testing.T) {
	requirePhysicalStage(t, "baseline")
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Hour)
	defer cancel()
	fixture := newPhysicalTapeFixture(t, ctx)

	files := createPhysicalBaseline(t, fixture.paths.Source)
	// Location indexing below replaces the fixed Source directory browser.
	devices, err := fixture.service.DeviceList(ctx, &entity.DeviceListRequest{})
	require.NoError(t, err)
	require.Equal(t, []string{fixture.device}, devices.Devices)

	inspected, err := fixture.service.MediaInspect(ctx, (&entity.MediaInspectTapeTarget{Device: fixture.device}).Pack())
	require.NoError(t, err)
	require.Equal(t, fixture.barcode, inspected.Identity)
	require.Nil(t, inspected.Media)

	created, err := fixture.archive.Create(ctx, &entity.CreateArchiveJobRequest{
		PreviewPolicy: entity.PreviewPolicy_PREVIEW_MISSING_ONLY,
		Spec:          &entity.ArchiveJobSpec{Selections: indexedSelections(t, ctx, fixture.cli, fixture.paths.Source, "dataset")},
	})
	require.NoError(t, err)
	waitForPhysicalJob(t, ctx, fixture, created.Job.Id, entity.JobStatus_PENDING)
	prepared, err := fixture.archive.GetProgress(ctx, &entity.GetArchiveJobProgressRequest{Id: created.Job.Id})
	require.NoError(t, err)
	require.Empty(t, prepared.PreviewError)
	require.Positive(t, prepared.PreviewJobId)
	waitForPhysicalJob(t, ctx, fixture, prepared.PreviewJobId, entity.JobStatus_COMPLETED)

	_, err = fixture.archive.WriteMedia(ctx, &entity.WriteArchiveMediaRequest{
		Id: created.Job.Id,
		Target: (&entity.ArchiveTapeTarget{
			Device: fixture.device, Barcode: fixture.barcode, Name: "YATM physical E2E",
			Mode: entity.ArchiveTapeWriteMode_ARCHIVE_TAPE_WRITE_MODE_FORMAT,
		}).Pack(),
	})
	require.NoError(t, err)
	waitForPhysicalJob(t, ctx, fixture, created.Job.Id, entity.JobStatus_COMPLETED)
	requirePhysicalDeviceAvailable(t, ctx, fixture)

	archiveItems, err := fixture.archive.ListFiles(ctx, &entity.ListArchiveJobFilesRequest{
		Id: created.Job.Id, Limit: 100,
	})
	require.NoError(t, err)
	require.Len(t, archiveItems.Items, 5)
	media := requirePhysicalMedia(t, ctx, fixture)
	positions := requirePhysicalPositions(t, ctx, fixture, media.ID, archiveItems.Items, files)
	requireBaselinePartitions(t, positions)
	requireTapeWriteOrder(t, archiveItems.Items, positions)
	requireBaselineUIAPIs(t, ctx, fixture, created.Job.Id, prepared.PreviewJobId, media, files)
	positionsBeforeMismatch, err := fixture.lib.ListMediaFilePositions(ctx, media.ID, "", 1000)
	require.NoError(t, err)

	appendFiles := createPhysicalAppend(t, fixture.paths.Source)
	appended, err := fixture.archive.Create(ctx, &entity.CreateArchiveJobRequest{
		Spec: &entity.ArchiveJobSpec{Selections: indexedSelections(t, ctx, fixture.cli, fixture.paths.Source, "append")},
	})
	require.NoError(t, err)
	waitForPhysicalJob(t, ctx, fixture, appended.Job.Id, entity.JobStatus_PENDING)
	requirePhysicalBarcodeMismatch(t, ctx, fixture, appended.Job.Id)
	failed, err := fixture.job.Get(ctx, &entity.GetJobRequest{Id: appended.Job.Id})
	require.NoError(t, err)
	require.Equal(t, entity.JobStatus_PENDING, failed.Job.Status)
	require.Equal(t, entity.JobPhase_JOB_PHASE_WAITING_FOR_MEDIA, failed.Job.Phase)
	failedItems, err := fixture.archive.ListFiles(ctx, &entity.ListArchiveJobFilesRequest{
		Id: appended.Job.Id, Limit: 100,
	})
	require.NoError(t, err)
	require.Len(t, failedItems.Items, 2)
	for _, item := range failedItems.Items {
		require.Equal(t, entity.CopyStatus_PENDING, item.Status)
		require.Nil(t, item.MediaId)
		require.Empty(t, item.File.MediaPath)
	}
	positionsAfterMismatch, err := fixture.lib.ListMediaFilePositions(ctx, media.ID, "", 1000)
	require.NoError(t, err)
	require.Equal(t, positionsBeforeMismatch, positionsAfterMismatch)
	inspected, err = fixture.service.MediaInspect(ctx, (&entity.MediaInspectTapeTarget{Device: fixture.device}).Pack())
	require.NoError(t, err)
	require.Equal(t, fixture.barcode, inspected.Identity)
	require.NotNil(t, inspected.Media)
	require.Equal(t, media.ID, inspected.Media.Id)

	_, err = fixture.archive.WriteMedia(ctx, &entity.WriteArchiveMediaRequest{
		Id: appended.Job.Id,
		Target: (&entity.ArchiveTapeTarget{
			Device: fixture.device, Barcode: fixture.barcode,
			Mode: entity.ArchiveTapeWriteMode_ARCHIVE_TAPE_WRITE_MODE_APPEND,
		}).Pack(),
	})
	require.NoError(t, err)
	waitForPhysicalJob(t, ctx, fixture, appended.Job.Id, entity.JobStatus_COMPLETED)
	requirePhysicalDeviceAvailable(t, ctx, fixture)
	appendItems, err := fixture.archive.ListFiles(ctx, &entity.ListArchiveJobFilesRequest{
		Id: appended.Job.Id, Limit: 100,
	})
	require.NoError(t, err)
	require.Len(t, appendItems.Items, 2)
	appendPositions := requirePhysicalPositions(t, ctx, fixture, media.ID, appendItems.Items, appendFiles)
	requireAppendPartitions(t, appendItems.Items, appendPositions)
	requireTapeWriteOrder(t, appendItems.Items, appendPositions)

	state := physicalTapeState{
		Barcode: fixture.barcode, MediaID: media.ID,
	}
	dataset, err := fixture.lib.GetByPath(ctx, library.Root.ID, "Unforged/Archive/dataset")
	require.NoError(t, err)
	require.NotNil(t, dataset)
	state.DatasetDirectoryID = dataset.ID
	state.Files = append(state.Files, expectedPhysicalFiles(t, archiveItems.Items, positions, files, "dataset")...)
	appendedExpected := expectedPhysicalFiles(t, appendItems.Items, appendPositions, appendFiles, "")
	state.Files = append(state.Files, appendedExpected...)
	for _, file := range appendedExpected {
		state.AppendFileIDs = append(state.AppendFileIDs, file.FileID)
	}
	writePhysicalState(t, fixture.root, &state)
	exportPhysicalLibrary(t, ctx, fixture, "library-baseline.jsonl")
}

func TestPhysicalTapeRestartRestore(t *testing.T) {
	requirePhysicalStage(t, "restore")
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Hour)
	defer cancel()
	fixture := newPhysicalTapeFixture(t, ctx)
	state := readPhysicalState(t, fixture.root)
	require.Equal(t, fixture.barcode, state.Barcode)

	inspected, err := fixture.service.MediaInspect(ctx, (&entity.MediaInspectTapeTarget{Device: fixture.device}).Pack())
	require.NoError(t, err)
	require.Equal(t, fixture.barcode, inspected.Identity)
	require.NotNil(t, inspected.Media)
	require.Equal(t, state.MediaID, inspected.Media.Id)
	require.Equal(t, int64(len(state.Files)), inspected.FileCount)

	fileIDs := append([]int64{state.DatasetDirectoryID}, state.AppendFileIDs...)
	created, err := fixture.restore.Create(ctx, &entity.CreateRestoreJobRequest{
		Spec: &entity.RestoreJobSpec{Destination: restoreDestination(t, ctx, fixture.cli, fixture.paths.Target), Selections: librarySelections(fileIDs...)},
	})
	require.NoError(t, err)
	waitForPhysicalJob(t, ctx, fixture, created.Job.Id, entity.JobStatus_PENDING)
	mediaReply, err := fixture.restore.ListMedia(ctx, &entity.ListRestoreJobMediaRequest{
		Id: created.Job.Id, Limit: 10,
	})
	require.NoError(t, err)
	require.Len(t, mediaReply.Media, 1)
	fileReply, err := fixture.restore.ListFiles(ctx, &entity.ListRestoreJobFilesRequest{
		Id: created.Job.Id, MediaId: state.MediaID, Limit: 100,
	})
	require.NoError(t, err)
	require.Len(t, fileReply.Items, len(state.Files))

	expectedOrder := readRestoreStorageOrder(t, fixture, created.Job.Id, state.MediaID)
	_, err = fixture.restore.RestoreMedia(ctx, &entity.RestoreMediaRequest{
		Id: created.Job.Id, Target: (&entity.ReadTapeTarget{Device: fixture.device}).Pack(),
	})
	require.NoError(t, err)
	waitForPhysicalJob(t, ctx, fixture, created.Job.Id, entity.JobStatus_COMPLETED)
	requirePhysicalDeviceAvailable(t, ctx, fixture)
	requireRestoredFiles(t, fixture.paths.Target, state.Files)
	t.Logf("restored Media paths in storage order: %s", strings.Join(expectedOrder, ", "))
	progress, err := fixture.restore.GetProgress(ctx, &entity.GetRestoreJobProgressRequest{Id: created.Job.Id})
	require.NoError(t, err)
	require.Equal(t, int64(len(state.Files)), progress.Progress.CopiedFiles)
}

func TestPhysicalTapeFullBoundaryWrite(t *testing.T) {
	requirePhysicalStage(t, "full-write")
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Hour)
	defer cancel()
	fixture := newPhysicalTapeFixture(t, ctx)
	state := readPhysicalState(t, fixture.root)
	expected := readEOMManifest(t, filepath.Join(fixture.paths.Source, "eom"))
	require.GreaterOrEqual(t, len(expected), 2)

	// Persist the Job identity before touching the Tape, but never resume an ambiguous write attempt.
	createdFullArchive := false
	if state.FullArchiveJobID == 0 {
		created, err := fixture.archive.Create(ctx, &entity.CreateArchiveJobRequest{
			Spec: &entity.ArchiveJobSpec{Selections: indexedSelections(t, ctx, fixture.cli, fixture.paths.Source, "eom")},
		})
		require.NoError(t, err)
		state.FullArchiveJobID = created.Job.Id
		writePhysicalState(t, fixture.root, state)
		createdFullArchive = true
	}
	waitForPhysicalJob(t, ctx, fixture, state.FullArchiveJobID, entity.JobStatus_PENDING)
	if !createdFullArchive {
		requirePhysicalFullBoundary(t, ctx, fixture, state.FullArchiveJobID, len(expected))
		t.Logf("physical full boundary already recorded for Job %d", state.FullArchiveJobID)
		return
	}

	// Write only pending items and checkpoint the usable no-space result before later validation.
	_, err := fixture.archive.WriteMedia(ctx, &entity.WriteArchiveMediaRequest{
		Id: state.FullArchiveJobID,
		Target: (&entity.ArchiveTapeTarget{
			Device: fixture.device, Barcode: fixture.barcode,
			Mode: entity.ArchiveTapeWriteMode_ARCHIVE_TAPE_WRITE_MODE_APPEND,
		}).Pack(),
	})
	require.NoError(t, err)
	waitForStoppedJob(t, ctx, fixture.exe, state.FullArchiveJobID)
	requirePhysicalDeviceAvailable(t, ctx, fixture)
	requirePhysicalFullBoundary(t, ctx, fixture, state.FullArchiveJobID, len(expected))
}

func TestPhysicalTapeFullBoundaryVerify(t *testing.T) {
	requirePhysicalStage(t, "full-verify")
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Hour)
	defer cancel()
	fixture := newPhysicalTapeFixture(t, ctx)
	state := readPhysicalState(t, fixture.root)
	expected := readEOMManifest(t, filepath.Join(fixture.paths.Source, "eom"))
	require.NotZero(t, state.FullArchiveJobID)
	requirePhysicalFullBoundary(t, ctx, fixture, state.FullArchiveJobID, len(expected))

	reply, err := fixture.archive.ListFiles(ctx, &entity.ListArchiveJobFilesRequest{
		Id: state.FullArchiveJobID, Limit: 1000,
	})
	require.NoError(t, err)
	require.Len(t, reply.Items, len(expected))

	var submitted []*entity.ArchiveItem
	var submittedBytes int64
	prefixEnded := false
	for _, item := range reply.Items {
		if item.Status == entity.CopyStatus_SUBMITTED {
			require.False(t, prefixEnded, item.File.TargetPath)
			require.NotNil(t, item.MediaId)
			require.Equal(t, state.MediaID, *item.MediaId)
			submitted = append(submitted, item)
			submittedBytes += item.Size
			continue
		}
		prefixEnded = true
		require.Equal(t, entity.CopyStatus_PENDING, item.Status)
		require.Nil(t, item.MediaId)
		require.Empty(t, item.File.MediaPath)
	}
	require.NotEmpty(t, submitted)
	require.Less(t, len(submitted), len(reply.Items))
	t.Logf(
		"physical full boundary: submitted=%d pending=%d submitted_bytes=%d",
		len(submitted), len(reply.Items)-len(submitted), submittedBytes,
	)

	positions, err := fixture.lib.ListMediaFilePositions(ctx, state.MediaID, "", 1000)
	require.NoError(t, err)
	positionByPath := make(map[string]*library.Position, len(positions))
	submittedPositions := make(map[string]*library.Position, len(submitted))
	for _, position := range positions {
		positionByPath[position.Path] = position
	}
	for _, item := range submitted {
		position := positionByPath[item.File.MediaPath]
		require.NotNil(t, position, item.File.MediaPath)
		require.NotEmpty(t, position.StorageOrder, item.File.MediaPath)
		submittedPositions[item.File.TargetPath] = position
	}
	requireTapeWriteOrder(t, submitted, submittedPositions)
	for _, item := range reply.Items[len(submitted):] {
		_, exists := positionByPath[item.File.MediaPath]
		require.False(t, exists, item.File.TargetPath)
	}
	progress, err := fixture.archive.GetProgress(ctx, &entity.GetArchiveJobProgressRequest{Id: state.FullArchiveJobID})
	require.NoError(t, err)
	require.Equal(t, int64(len(submitted)), progress.Progress.CopiedFiles)
	require.Equal(t, submittedBytes, progress.Progress.CopiedBytes)
	require.Equal(t, int64(len(expected)), progress.Progress.TotalFiles)
	requirePhysicalReport(t, fixture, state.FullArchiveJobID, len(submitted), submittedBytes)

	boundaryFiles := []*entity.ArchiveItem{submitted[0], submitted[len(submitted)-1]}
	boundaryExpected := make([]physicalExpectedFile, 0, len(boundaryFiles))
	boundaryIDs := make([]int64, 0, len(boundaryFiles))
	for _, item := range boundaryFiles {
		position := positionByPath[item.File.MediaPath]
		name := filepath.Base(item.File.TargetPath)
		boundaryIDs = append(boundaryIDs, item.File.Expected.FileId)
		boundaryExpected = append(boundaryExpected, physicalExpectedFile{
			TargetPath: item.File.TargetPath, MediaPath: item.File.MediaPath, RestorePath: filepath.FromSlash(item.File.TargetPath),
			Size: item.Size, SHA256: expected[name], FileID: item.File.Expected.FileId,
			StorageOrder: hex.EncodeToString(position.StorageOrder),
		})
	}
	restoreJob, err := fixture.restore.Create(ctx, &entity.CreateRestoreJobRequest{
		Spec: &entity.RestoreJobSpec{Destination: restoreDestination(t, ctx, fixture.cli, fixture.paths.Target), Selections: librarySelections(boundaryIDs...)},
	})
	require.NoError(t, err)
	waitForPhysicalJob(t, ctx, fixture, restoreJob.Job.Id, entity.JobStatus_PENDING)
	expectedOrder := readRestoreStorageOrder(t, fixture, restoreJob.Job.Id, state.MediaID)
	_, err = fixture.restore.RestoreMedia(ctx, &entity.RestoreMediaRequest{
		Id: restoreJob.Job.Id, Target: (&entity.ReadTapeTarget{Device: fixture.device}).Pack(),
	})
	require.NoError(t, err)
	waitForPhysicalJob(t, ctx, fixture, restoreJob.Job.Id, entity.JobStatus_COMPLETED)
	requirePhysicalDeviceAvailable(t, ctx, fixture)
	requireRestoredFiles(t, fixture.paths.Target, boundaryExpected)
	t.Logf("restored boundary Media paths in storage order: %s", strings.Join(expectedOrder, ", "))

	exportPhysicalLibrary(t, ctx, fixture, "library-full.jsonl")
	listed, err := fixture.job.List(ctx, &entity.ListJobsRequest{Filter: &entity.JobFilter{
		Limit: int64Pointer(200),
	}})
	require.NoError(t, err)
	require.False(t, listed.HasMore)
	preservePhysicalJobEvidence(t, ctx, fixture, listed.Jobs)
	ids := make([]int64, 0, len(listed.Jobs))
	for _, job := range listed.Jobs {
		ids = append(ids, job.Id)
	}
	require.NotEmpty(t, ids)
	_, err = fixture.job.Delete(ctx, &entity.DeleteJobsRequest{Ids: ids})
	require.NoError(t, err)
	listed, err = fixture.job.List(ctx, &entity.ListJobsRequest{Filter: &entity.JobFilter{}})
	require.NoError(t, err)
	require.Empty(t, listed.Jobs)
}

func requirePhysicalFullBoundary(
	t *testing.T,
	ctx context.Context,
	fixture *physicalTapeFixture,
	jobID int64,
	expectedFiles int,
) {
	t.Helper()

	// Require the stable retryable Job boundary before inspecting its committed files.
	job, err := fixture.job.Get(ctx, &entity.GetJobRequest{Id: jobID})
	require.NoError(t, err)
	require.Equal(t, entity.JobStatus_PENDING, job.Job.Status)
	require.Equal(t, entity.JobPhase_JOB_PHASE_WAITING_FOR_MEDIA, job.Job.Phase)
	reply, err := fixture.archive.ListFiles(ctx, &entity.ListArchiveJobFilesRequest{Id: jobID, Limit: 1000})
	require.NoError(t, err)
	require.Len(t, reply.Items, expectedFiles)
	mediaID := fixture.libMediaID(t, fixture.barcode)
	var submitted, pending int64
	var submittedBytes int64
	prefixEnded := false
	for _, item := range reply.Items {
		switch item.Status {
		case entity.CopyStatus_SUBMITTED:
			require.False(t, prefixEnded, item.File.TargetPath)
			require.NotNil(t, item.MediaId)
			require.Equal(t, mediaID, *item.MediaId)
			require.NotEmpty(t, item.File.MediaPath)
			submitted++
			submittedBytes += item.Size
		case entity.CopyStatus_PENDING:
			prefixEnded = true
			require.Nil(t, item.MediaId)
			require.Empty(t, item.File.MediaPath)
			pending++
		default:
			t.Fatalf("full boundary item %q has status %s", item.File.TargetPath, item.Status)
		}
	}
	require.Positive(t, submitted)
	require.Positive(t, pending)
	index, err := os.Stat(filepath.Join(
		fixture.paths.Work, "jobs", fmt.Sprint(jobID), "tapes", fixture.barcode, fixture.barcode+".schema",
	))
	require.NoError(t, err)
	require.Positive(t, index.Size())

	// Every submitted item must have a physical Position before the no-space event is accepted.
	positions, err := fixture.lib.ListMediaFilePositions(ctx, mediaID, "", 1000)
	require.NoError(t, err)
	positionByPath := make(map[string]*library.Position, len(positions))
	for _, position := range positions {
		positionByPath[position.Path] = position
	}
	for _, item := range reply.Items[:submitted] {
		position := positionByPath[item.File.MediaPath]
		require.NotNil(t, position, item.File.MediaPath)
		require.NotEmpty(t, position.StorageOrder, item.File.MediaPath)
	}
	requireArchiveNoSpaceCheckpointLog(
		t, ctx, fixture.job, jobID, mediaID, submitted, submittedBytes,
	)
}

func requirePhysicalStage(t *testing.T, stage string) {
	t.Helper()
	if os.Getenv(physicalStageEnvironment) != stage {
		t.Skipf("set %s=%s to run this destructive physical Tape stage", physicalStageEnvironment, stage)
	}
}

func requirePhysicalBarcodeMismatch(
	t *testing.T,
	ctx context.Context,
	fixture *physicalTapeFixture,
	jobID int64,
) {
	t.Helper()
	want := fmt.Sprintf(`archive Tape changed, requested=%q device=%q`, "BAD999", fixture.barcode)
	// This fault probe intentionally bypasses the CLI's earlier barcode preflight to test the runner boundary.
	client := connect.NewClient[entity.WriteArchiveMediaRequest, entity.WriteArchiveMediaReply](http.DefaultClient,
		fixture.cli.url+"/services"+entity.ArchiveJobService_WriteMedia_FullMethodName, connect.WithGRPCWeb())
	for attempt := 0; attempt < 2; attempt++ {
		// Start the deliberately mismatched APPEND and wait for the asynchronous attempt to stop.
		_, err := client.CallUnary(ctx, connect.NewRequest(&entity.WriteArchiveMediaRequest{
			Id: jobID,
			Target: (&entity.ArchiveTapeTarget{
				Device: fixture.device, Barcode: "BAD999",
				Mode: entity.ArchiveTapeWriteMode_ARCHIVE_TAPE_WRITE_MODE_APPEND,
			}).Pack(),
		}))
		require.NoError(t, err)
		waitForStoppedJob(t, ctx, fixture.exe, jobID)
		logs, err := fixture.job.GetLog(ctx, &entity.GetJobLogRequest{Id: jobID})
		require.NoError(t, err)

		// Only an exact requested/device mismatch proves PT-04; one transient MAM failure may be retried.
		text := string(logs.Logs)
		if strings.Contains(text, want) {
			require.NoDirExists(t, filepath.Join(
				fixture.paths.Work, "jobs", fmt.Sprint(jobID), "tapes", "BAD999",
			))
			return
		}
		unavailable := strings.Contains(text, "Tape barcode is unavailable") ||
			strings.Contains(text, "Tape identity is unavailable")
		if attempt == 0 && unavailable {
			continue
		}
		t.Fatalf("PT-04 did not observe %q after attempt %d:\n%s", want, attempt+1, text)
	}
}

func newPhysicalTapeFixture(t *testing.T, ctx context.Context) *physicalTapeFixture {
	t.Helper()
	root := requiredPhysicalEnvironment(t, physicalRootEnvironment)
	device := requiredPhysicalEnvironment(t, physicalDeviceEnvironment)
	barcode := requiredPhysicalEnvironment(t, physicalBarcodeEnvironment)
	scripts := requiredPhysicalEnvironment(t, physicalScriptsEnvironment)
	require.True(t, filepath.IsAbs(root))
	require.True(t, filepath.IsAbs(device))
	require.True(t, filepath.IsAbs(scripts))

	paths := executor.Paths{
		Work: filepath.Join(root, "work"), Source: filepath.Join(root, "source"),
		Target: filepath.Join(root, "restore"),
	}
	executorDB, err := resource.OpenSQLite(filepath.Join(root, "executor.db"))
	require.NoError(t, err)
	libraryDB, err := resource.OpenSQLite(filepath.Join(root, "library.db"))
	require.NoError(t, err)
	lib := library.New(libraryDB)
	require.NoError(t, lib.AutoMigrate())
	previews, err := previewcore.New(previewcore.Config{Generators: []previewcore.GeneratorConfig{{
		Kind: "physical-e2e-fixture", Extensions: []string{"fixture"},
	}}}, paths.Work)
	require.NoError(t, err)
	exe := executor.New(executorDB, lib, []string{device}, paths, executor.Scripts{
		Encrypt: filepath.Join(scripts, "encrypt"), Mkfs: filepath.Join(scripts, "mkfs-physical.sh"),
		Mount: filepath.Join(scripts, "mount"), Umount: filepath.Join(scripts, "umount"),
		ReadInfo: filepath.Join(scripts, "readinfo"),
	}, previews)
	require.NoError(t, exe.AutoMigrate())
	require.NoError(t, exe.ReconcileStorage(ctx))
	require.NoError(t, os.MkdirAll(paths.Source, 0o755))
	require.NoError(t, os.MkdirAll(paths.Target, 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(root, "evidence"), 0o755))

	// Keep hardware gates unchanged and route authorized business operations through CLI.
	conn := serveCLI(t, apis.New(lib, exe), exe)

	return &physicalTapeFixture{
		root: root, device: device, barcode: barcode, paths: paths, exe: exe, lib: lib,
		filesURL: conn.url + "/files", cli: conn, job: entity.NewJobServiceClient(conn), service: entity.NewServiceClient(conn),
		archive: entity.NewArchiveJobServiceClient(conn), restore: entity.NewRestoreJobServiceClient(conn),
	}
}

func requiredPhysicalEnvironment(t *testing.T, name string) string {
	t.Helper()
	value := strings.TrimSpace(os.Getenv(name))
	require.NotEmpty(t, value, name)
	return value
}

func createPhysicalBaseline(t *testing.T, source string) map[string][]byte {
	t.Helper()
	files := map[string][]byte{
		"dataset/index-small.txt":        bytes.Repeat([]byte("index-small\n"), 5*1024),
		"dataset/data-small.bin":         bytes.Repeat([]byte{0, 1, 2, 3}, 16*1024),
		"dataset/data-large.txt":         bytes.Repeat([]byte("large-data\n"), 200*1024),
		"dataset/empty.bin":              {},
		"dataset/nested/payload.fixture": bytes.Repeat([]byte("physical-preview-payload\n"), 256*1024),
	}
	writePhysicalFiles(t, source, files)
	return files
}

func createPhysicalAppend(t *testing.T, source string) map[string][]byte {
	t.Helper()
	files := map[string][]byte{
		"append/index-small.txt": bytes.Repeat([]byte("append-index\n"), 5*1024),
		"append/data.bin":        bytes.Repeat([]byte("append-data\n"), 200*1024),
	}
	writePhysicalFiles(t, source, files)
	return files
}

func writePhysicalFiles(t *testing.T, source string, files map[string][]byte) {
	t.Helper()
	for name, data := range files {
		filename := filepath.Join(source, filepath.FromSlash(name))
		require.NoError(t, os.MkdirAll(filepath.Dir(filename), 0o755))
		require.NoError(t, os.WriteFile(filename, data, 0o644))
	}
}

func requirePhysicalMedia(t *testing.T, ctx context.Context, fixture *physicalTapeFixture) *library.Media {
	t.Helper()
	media, err := fixture.lib.GetMediaByIdentity(ctx, entity.MediaKind_MEDIA_KIND_TAPE, fixture.barcode)
	require.NoError(t, err)
	require.NotNil(t, media)
	require.Equal(t, library.TapeFormatLTFSV1, media.Profile.GetTape().Format)
	require.NotEmpty(t, media.Profile.GetTape().Encryption)
	return media
}

func requirePhysicalPositions(
	t *testing.T,
	ctx context.Context,
	fixture *physicalTapeFixture,
	mediaID int64,
	items []*entity.ArchiveItem,
	files map[string][]byte,
) map[string]*library.Position {
	t.Helper()
	positions, err := fixture.lib.ListMediaFilePositions(ctx, mediaID, "", 1000)
	require.NoError(t, err)
	byPath := make(map[string]*library.Position, len(positions))
	for _, position := range positions {
		byPath[position.Path] = position
	}
	result := make(map[string]*library.Position, len(items))
	for _, item := range items {
		require.Equal(t, entity.CopyStatus_SUBMITTED, item.Status)
		require.NotNil(t, item.MediaId)
		position := byPath[item.File.MediaPath]
		require.NotNil(t, position, item.File.MediaPath)
		want := files[strings.TrimPrefix(item.File.TargetPath, "Unforged/Archive/")]
		require.Equal(t, int64(len(want)), position.Size)
		hash := sha256.Sum256(want)
		require.Equal(t, hash[:], position.Hash)
		if len(want) == 0 {
			require.Empty(t, position.StorageOrder)
		} else {
			require.Len(t, position.StorageOrder, 17)
			require.NotEmpty(t, position.StorageMetadata.GetLtfs().Extents)
		}
		result[item.File.TargetPath] = position
	}
	return result
}

func requireBaselinePartitions(t *testing.T, positions map[string]*library.Position) {
	t.Helper()
	want := map[string]byte{
		"dataset/index-small.txt": 'a', "dataset/data-small.bin": 'b',
		"dataset/data-large.txt": 'b', "dataset/nested/payload.fixture": 'b',
	}
	for name, partition := range want {
		require.Equal(t, partition, positions["Unforged/Archive/"+name].StorageOrder[0], name)
		requireStorageOrderMatchesExtent(t, positions["Unforged/Archive/"+name])
	}
}

func requireAppendPartitions(
	t *testing.T,
	items []*entity.ArchiveItem,
	positions map[string]*library.Position,
) {
	t.Helper()
	want := map[string]byte{"append/index-small.txt": 'a', "append/data.bin": 'b'}
	for _, item := range items {
		require.NotEqual(t, item.File.TargetPath, item.File.MediaPath)
		position := positions[item.File.TargetPath]
		require.Equal(t, want[strings.TrimPrefix(item.File.TargetPath, "Unforged/Archive/")], position.StorageOrder[0], item.File.TargetPath)
		requireStorageOrderMatchesExtent(t, position)
	}
}

func requireStorageOrderMatchesExtent(t *testing.T, position *library.Position) {
	t.Helper()
	first := position.StorageMetadata.GetLtfs().Extents[0]
	for _, extent := range position.StorageMetadata.GetLtfs().Extents[1:] {
		if extent.FileOffset < first.FileOffset {
			first = extent
		}
	}
	require.Equal(t, first.Partition[0], position.StorageOrder[0])
	require.Equal(t, first.StartBlock, binary.BigEndian.Uint64(position.StorageOrder[1:9]))
	require.Equal(t, first.ByteOffset, binary.BigEndian.Uint64(position.StorageOrder[9:17]))
}

func requireBaselineUIAPIs(
	t *testing.T,
	ctx context.Context,
	fixture *physicalTapeFixture,
	archiveID int64,
	previewID int64,
	media *library.Media,
	files map[string][]byte,
) {
	t.Helper()
	jobs, err := fixture.job.List(ctx, &entity.ListJobsRequest{Filter: &entity.JobFilter{}})
	require.NoError(t, err)
	require.Len(t, jobs.Jobs, 3, "the source scan is an independent background Job")
	require.Positive(t, jobs.Revision)
	_, err = fixture.job.Get(ctx, &entity.GetJobRequest{Id: archiveID})
	require.NoError(t, err)
	logReply, err := fixture.job.GetLog(ctx, &entity.GetJobLogRequest{Id: archiveID})
	require.NoError(t, err)
	require.NotEmpty(t, logReply.Logs)
	progress, err := fixture.archive.GetProgress(ctx, &entity.GetArchiveJobProgressRequest{Id: archiveID})
	require.NoError(t, err)
	require.Equal(t, int64(len(files)), progress.Progress.CopiedFiles)

	listedMedia, err := fixture.service.MediaList(ctx, (&entity.MediaFilter{
		Kinds: []entity.MediaKind{entity.MediaKind_MEDIA_KIND_TAPE},
	}).Pack())
	require.NoError(t, err)
	require.Len(t, listedMedia.Media, 1)
	gotMedia, err := fixture.service.MediaList(ctx, (&entity.MediaMGetRequest{Ids: []int64{media.ID}}).Pack())
	require.NoError(t, err)
	require.Len(t, gotMedia.Media, 1)
	rootPositions, err := fixture.service.MediaGetPositions(ctx, &entity.MediaGetPositionsRequest{
		Id: media.ID, Directory: "", Limit: int64Pointer(1),
	})
	require.NoError(t, err)
	require.Len(t, rootPositions.Positions, 1)

	file, err := fixture.lib.GetByPath(ctx, library.Root.ID, "Unforged/Archive/dataset/nested/payload.fixture")
	require.NoError(t, err)
	require.NotNil(t, file)
	note := "physical Tape UI API"
	_, err = fixture.service.FileMetadataEdit(ctx, &entity.FileMetadataEditRequest{
		Ids: []int64{file.ID}, AddTags: []string{"e2e", "physical"}, Note: &note,
	})
	require.NoError(t, err)
	detail, err := fixture.service.FileGet(ctx, &entity.FileGetRequest{Id: file.ID})
	require.NoError(t, err)
	require.Equal(t, note, detail.File.Note)
	require.Equal(t, []string{"e2e", "physical"}, detail.File.Tags)
	require.NotNil(t, detail.Preview)
	parents, err := fixture.service.FileListParents(ctx, &entity.FileListParentsRequest{Id: file.ID})
	require.NoError(t, err)
	require.NotEmpty(t, parents.Parents)
	search, err := fixture.service.FileSearch(ctx, &entity.FileSearchRequest{Query: "tag:physical AND note:Tape"})
	require.NoError(t, err)
	require.Len(t, search.Results, 1)
	tags, err := fixture.service.TagList(ctx, &entity.TagListRequest{Prefix: stringPointer("phys")})
	require.NoError(t, err)
	require.Len(t, tags.Tags, 1)

	// Preview bytes use the same HTTP path as browser rendering, independently of CLI control.
	data := readHTTPContent(t, ctx, fmt.Sprintf("%s/files/previews/%d/thumbnail", fixture.cli.url, file.ID))
	require.NotEmpty(t, data)
	preview, err := fixture.job.Get(ctx, &entity.GetJobRequest{Id: previewID})
	require.NoError(t, err)
	require.Equal(t, entity.JobStatus_COMPLETED, preview.Job.Status)
}

func expectedPhysicalFiles(
	t *testing.T,
	items []*entity.ArchiveItem,
	positions map[string]*library.Position,
	files map[string][]byte,
	directory string,
) []physicalExpectedFile {
	t.Helper()
	result := make([]physicalExpectedFile, 0, len(items))
	for _, item := range items {
		position := positions[item.File.TargetPath]
		data := files[strings.TrimPrefix(item.File.TargetPath, "Unforged/Archive/")]
		hash := sha256.Sum256(data)
		restorePath := filepath.FromSlash(item.File.TargetPath)
		result = append(result, physicalExpectedFile{
			TargetPath: item.File.TargetPath, MediaPath: item.File.MediaPath, RestorePath: restorePath,
			Size: int64(len(data)), SHA256: hex.EncodeToString(hash[:]), FileID: item.File.Expected.FileId,
			StorageOrder: hex.EncodeToString(position.StorageOrder),
		})
	}
	return result
}

func readRestoreStorageOrder(t *testing.T, fixture *physicalTapeFixture, jobID, mediaID int64) []string {
	t.Helper()
	db, err := resource.OpenSQLite(filepath.Join(fixture.paths.Work, "jobs", fmt.Sprint(jobID), "state.db"))
	require.NoError(t, err)
	var copies []struct {
		MediaPath    string
		StorageOrder []byte
		ID           int64
	}
	require.NoError(t, db.Table("copies").Where("media_id = ?", mediaID).
		Order("storage_order, media_path, id").Find(&copies).Error)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	require.NoError(t, sqlDB.Close())
	paths := make([]string, 0, len(copies))
	for _, copy := range copies {
		paths = append(paths, copy.MediaPath)
	}
	return paths
}

func requireRestoredFiles(t *testing.T, target string, expected []physicalExpectedFile) {
	t.Helper()
	for _, file := range expected {
		filename := filepath.Join(target, filepath.FromSlash(file.RestorePath))
		info, err := os.Stat(filename)
		require.NoError(t, err, filename)
		require.Equal(t, file.Size, info.Size(), filename)
		hash, err := sha256File(filename)
		require.NoError(t, err)
		require.Equal(t, file.SHA256, hex.EncodeToString(hash), filename)
		cached, valid, err := acp.ReadCachedSignature(filename)
		require.NoError(t, err)
		require.True(t, valid, filename)
		require.Equal(t, file.Size, cached.Size)
		require.Equal(t, file.SHA256, hex.EncodeToString(cached.SHA256[:]))
	}
}

func readEOMManifest(t *testing.T, directory string) map[string]string {
	t.Helper()
	file, err := os.Open(directory + ".sha256sum")
	require.NoError(t, err)
	defer file.Close()
	result := make(map[string]string)
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		require.Len(t, fields, 2)
		result[fields[1]] = fields[0]
	}
	require.NoError(t, scanner.Err())
	return result
}

func requirePhysicalReport(t *testing.T, fixture *physicalTapeFixture, jobID int64, files int, size int64) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(
		fixture.paths.Work, "jobs", fmt.Sprint(jobID), "tapes", fixture.barcode, "yatm-report.json",
	))
	require.NoError(t, err)
	var report struct {
		MediaID          int64  `json:"media_id"`
		FileCount        int64  `json:"file_count"`
		Bytes            int64  `json:"bytes"`
		TerminationError string `json:"termination_error"`
	}
	require.NoError(t, json.Unmarshal(data, &report))
	require.Equal(t, fixture.libMediaID(t, fixture.barcode), report.MediaID)
	require.Equal(t, int64(files), report.FileCount)
	require.Equal(t, size, report.Bytes)
	require.NotEmpty(t, report.TerminationError)
}

func (fixture *physicalTapeFixture) libMediaID(t *testing.T, barcode string) int64 {
	t.Helper()
	media, err := fixture.lib.GetMediaByIdentity(context.Background(), entity.MediaKind_MEDIA_KIND_TAPE, barcode)
	require.NoError(t, err)
	require.NotNil(t, media)
	return media.ID
}

func waitForStoppedJob(t *testing.T, ctx context.Context, exe *executor.Executor, id int64) {
	t.Helper()
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for exe.IsRunning(id) {
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-ticker.C:
		}
	}
}

func waitForPhysicalJob(
	t *testing.T,
	ctx context.Context,
	fixture *physicalTapeFixture,
	id int64,
	want entity.JobStatus,
) {
	t.Helper()
	waitForStoppedJob(t, ctx, fixture.exe, id)
	reply, err := fixture.job.Get(ctx, &entity.GetJobRequest{Id: id})
	require.NoError(t, err)
	if reply.Job.Status == want {
		return
	}
	logs, logErr := fixture.job.GetLog(ctx, &entity.GetJobLogRequest{Id: id})
	require.NoError(t, logErr)
	t.Fatalf("job %d stopped at %s, want %s:\n%s", id, reply.Job.Status, want, logs.Logs)
}

func requirePhysicalDeviceAvailable(t *testing.T, ctx context.Context, fixture *physicalTapeFixture) {
	t.Helper()
	reply, err := fixture.service.DeviceList(ctx, &entity.DeviceListRequest{})
	require.NoError(t, err)
	require.Equal(t, []string{fixture.device}, reply.Devices)
}

func writePhysicalState(t *testing.T, root string, state *physicalTapeState) {
	t.Helper()
	data, err := json.MarshalIndent(state, "", "  ")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(root, "state.json"), data, 0o600))
}

func readPhysicalState(t *testing.T, root string) *physicalTapeState {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, "state.json"))
	require.NoError(t, err)
	state := new(physicalTapeState)
	require.NoError(t, json.Unmarshal(data, state))
	return state
}

func exportPhysicalLibrary(t *testing.T, ctx context.Context, fixture *physicalTapeFixture, name string) {
	t.Helper()
	// Capture the same complete snapshot available to operators through CLI.
	_, err := fixture.cli.run(ctx, "library", "export", "--output", filepath.Join(fixture.root, "evidence", name))
	require.NoError(t, err)
}

func preservePhysicalJobEvidence(
	t *testing.T,
	ctx context.Context,
	fixture *physicalTapeFixture,
	jobs []*entity.Job,
) {
	t.Helper()
	evidenceRoot := filepath.Join(fixture.root, "evidence", "jobs")
	require.NoError(t, os.MkdirAll(evidenceRoot, 0o755))
	artifacts := make([]string, 0, len(jobs)*5)
	for _, job := range jobs {
		// Preserve catalog and bundle metadata plus the complete public Job log.
		jobRoot := filepath.Join(fixture.paths.Work, "jobs", fmt.Sprint(job.Id))
		catalog, err := json.MarshalIndent(job, "", "  ")
		require.NoError(t, err)
		catalog = append(catalog, '\n')
		artifacts = append(artifacts, writePhysicalEvidence(
			t, evidenceRoot, filepath.Join(fmt.Sprint(job.Id), "catalog.json"), catalog,
		))
		artifacts = append(artifacts, copyPhysicalEvidence(
			t, evidenceRoot, filepath.Join(fmt.Sprint(job.Id), "job.json"), filepath.Join(jobRoot, "job.json"),
		))
		artifacts = append(artifacts, copyPhysicalEvidence(
			t, evidenceRoot, filepath.Join(fmt.Sprint(job.Id), "state.db"), filepath.Join(jobRoot, "state.db"),
		))
		artifacts = append(artifacts, writePhysicalEvidence(
			t, evidenceRoot, filepath.Join(fmt.Sprint(job.Id), "job.log"),
			readPhysicalJobLog(t, ctx, fixture, job.Id),
		))

		// Retain the LTFS log, report, and every captured final Index in their bundle-relative layout.
		err = filepath.WalkDir(filepath.Join(jobRoot, "tapes"), func(path string, entry fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if entry.IsDir() {
				return nil
			}
			name := entry.Name()
			if name != "ltfs.log" && name != "yatm-report.json" && !strings.HasSuffix(name, ".schema") {
				return nil
			}
			relative, err := filepath.Rel(jobRoot, path)
			if err != nil {
				return err
			}
			artifacts = append(artifacts, copyPhysicalEvidence(
				t, evidenceRoot, filepath.Join(fmt.Sprint(job.Id), "bundle", relative), path,
			))
			return nil
		})
		require.NoError(t, err)
	}

	// A sorted manifest makes evidence completeness and later transfer verification deterministic.
	sort.Strings(artifacts)
	manifest := new(strings.Builder)
	for _, relative := range artifacts {
		hash, err := sha256File(filepath.Join(evidenceRoot, filepath.FromSlash(relative)))
		require.NoError(t, err)
		fmt.Fprintf(manifest, "%s  %s\n", hex.EncodeToString(hash), relative)
	}
	require.NoError(t, os.WriteFile(
		filepath.Join(fixture.root, "evidence", "jobs.sha256sum"), []byte(manifest.String()), 0o600,
	))
}

func readPhysicalJobLog(
	t *testing.T,
	ctx context.Context,
	fixture *physicalTapeFixture,
	jobID int64,
) []byte {
	t.Helper()
	var result []byte
	offset := int64(0)
	for {
		reply, err := fixture.job.GetLog(ctx, &entity.GetJobLogRequest{Id: jobID, Offset: &offset})
		require.NoError(t, err)
		if len(reply.Logs) == 0 {
			return result
		}
		result = append(result, reply.Logs...)
		offset = reply.Offset
	}
}

func writePhysicalEvidence(t *testing.T, root, relative string, data []byte) string {
	t.Helper()
	destination := filepath.Join(root, relative)
	require.NoError(t, os.MkdirAll(filepath.Dir(destination), 0o755))
	require.NoError(t, os.WriteFile(destination, data, 0o600))
	return filepath.ToSlash(relative)
}

func copyPhysicalEvidence(t *testing.T, root, relative, source string) string {
	t.Helper()
	data, err := os.ReadFile(source)
	require.NoError(t, err)
	return writePhysicalEvidence(t, root, relative, data)
}

func int64Pointer(value int64) *int64 { return &value }

func stringPointer(value string) *string { return &value }

func init() {
	previewcore.RegisterGenerator("physical-e2e-fixture", func(map[string]any) (previewcore.Generator, error) {
		return new(physicalPreviewGenerator), nil
	})
}

type physicalPreviewGenerator struct{}

func (*physicalPreviewGenerator) Generate(_ context.Context, _ string, outputDir string) ([]*previewcore.Asset, error) {
	data, err := base64.StdEncoding.DecodeString(
		"iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mNk+A8AAQUBAScY42YAAAAASUVORK5CYII=",
	)
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(filepath.Join(outputDir, "thumbnail.png"), data, 0o644); err != nil {
		return nil, err
	}
	return []*previewcore.Asset{{Name: "thumbnail.png", Role: "thumbnail", MediaType: "image/png"}}, nil
}
