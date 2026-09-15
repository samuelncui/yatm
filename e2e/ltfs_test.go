//go:build e2e

package e2e

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
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
	previewcore "github.com/samuelncui/yatm/preview"
	"github.com/samuelncui/yatm/resource"
	"github.com/stretchr/testify/require"
)

type legacyLibraryBackup struct {
	Files     []legacyFile     `json:"files"`
	Tapes     []*library.Tape  `json:"tapes"`
	Positions []legacyPosition `json:"positions"`
}

type legacyFile struct {
	ID        int64     `json:"id"`
	ParentID  int64     `json:"parent_id"`
	Name      string    `json:"name"`
	Mode      uint32    `json:"mode"`
	ModTime   time.Time `json:"mod_time"`
	Hash      []byte    `json:"hash"`
	Signature []byte    `json:"signature"`
	Size      int64     `json:"size"`
}

type legacyPosition struct {
	ID        int64     `json:"id,omitempty"`
	FileID    int64     `json:"file_id,omitempty"`
	TapeID    int64     `json:"tape_id,omitempty"`
	Path      string    `json:"path,omitempty"`
	Mode      uint32    `json:"mode,omitempty"`
	ModTime   time.Time `json:"mod_time,omitempty"`
	WriteTime time.Time `json:"write_time,omitempty"`
	Size      int64     `json:"size,omitempty"`
	Hash      []byte    `json:"hash,omitempty"`
}

type previewFixtureGenerator struct{}

func (*previewFixtureGenerator) Generate(_ context.Context, _ string, outputDir string) ([]*previewcore.Asset, error) {
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

func init() {
	previewcore.RegisterGenerator("e2e-fixture", func(map[string]any) (previewcore.Generator, error) {
		return new(previewFixtureGenerator), nil
	})
}

func TestLTFSArchiveRestore(t *testing.T) {
	// Require the explicitly enabled official LTFS file-backend environment.
	if os.Getenv("YATM_E2E_LTFS") != "1" {
		t.Skip("set YATM_E2E_LTFS=1 to run the LTFS file-backend E2E test")
	}
	for _, command := range []string{"mkltfs", "ltfs", "fusermount"} {
		_, err := exec.LookPath(command)
		require.NoErrorf(t, err, "%s is required", command)
	}

	// Create isolated Library, Executor, source, target, and virtual-cartridge storage.
	root := t.TempDir()
	executorDB, err := resource.OpenSQLite(filepath.Join(root, "executor.db"))
	require.NoError(t, err)
	libraryDB, err := resource.OpenSQLite(filepath.Join(root, "library.db"))
	require.NoError(t, err)
	lib := library.New(libraryDB)
	require.NoError(t, lib.AutoMigrate())
	scripts := executor.Scripts{
		Encrypt:  testScript(t, "encrypt-noop.sh"),
		Mkfs:     testScript(t, "mkfs-file.sh"),
		Mount:    testScript(t, "mount-file.sh"),
		Umount:   testScript(t, "umount-file-failures.sh"),
		ReadInfo: testScript(t, "read-info-file.sh"),
	}
	paths := executor.Paths{
		Work: filepath.Join(root, "work"), Source: filepath.Join(root, "source"), Target: filepath.Join(root, "target"),
	}
	failedDevice := filepath.Join(root, "failed-tapes", "ABC001")
	device := filepath.Join(root, "tapes", "ABC001")
	previews, err := previewcore.New(previewcore.Config{Generators: []previewcore.GeneratorConfig{{
		Kind: "e2e-fixture", Extensions: []string{"fixture"},
	}}}, paths.Work)
	require.NoError(t, err)
	exe := executor.New(executorDB, lib, []string{failedDevice, device}, paths, scripts, previews)
	require.NoError(t, exe.AutoMigrate())
	require.NoError(t, exe.ReconcileStorage(context.Background()))
	require.NoError(t, os.MkdirAll(filepath.Join(paths.Source, "dataset"), 0o755))
	fixtures := map[string][]byte{
		"a.bin":         bytes.Repeat([]byte("a"), 700*1024),
		"image.fixture": []byte("Preview source fixture"),
		"m.bin":         bytes.Repeat([]byte("middle"), 140*1024),
		"z.bin":         bytes.Repeat([]byte("z"), 900*1024),
	}
	for name, content := range fixtures {
		require.NoError(t, os.WriteFile(filepath.Join(paths.Source, "dataset", name), content, 0o644))
	}

	// Serve the real generated gRPC and HTTP transfer APIs over loopback.
	api := apis.New(lib, exe)
	conn := serveCLI(t, api, exe)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	jobClient := entity.NewJobServiceClient(conn)
	serviceClient := entity.NewServiceClient(conn)
	archiveClient := entity.NewArchiveJobServiceClient(conn)
	restoreClient := entity.NewRestoreJobServiceClient(conn)

	// Create and index the Archive and its independent Preview Job.
	created, err := archiveClient.Create(ctx, &entity.CreateArchiveJobRequest{
		PreviewPolicy: entity.PreviewPolicy_PREVIEW_MISSING_ONLY,
		Spec:          &entity.ArchiveJobSpec{Selections: indexedSelections(t, ctx, conn, paths.Source, "dataset")},
	})
	require.NoError(t, err)
	archiveID := created.Job.Id
	waitForPendingJob(t, ctx, jobClient, archiveID)
	prepared, err := archiveClient.GetProgress(ctx, &entity.GetArchiveJobProgressRequest{Id: archiveID})
	require.NoError(t, err)
	require.Empty(t, prepared.PreviewError)
	previewID := prepared.PreviewJobId
	require.Positive(t, previewID)
	waitForCompletedJob(t, ctx, jobClient, previewID)

	// Fail once after a real LTFS unmount and verify that no checkpoint was published.
	_, err = archiveClient.WriteMedia(ctx, &entity.WriteArchiveMediaRequest{
		Id: archiveID,
		Target: (&entity.ArchiveTapeTarget{
			Device: failedDevice, Barcode: "ABC001", Name: "E2E virtual tape",
			Mode: entity.ArchiveTapeWriteMode_ARCHIVE_TAPE_WRITE_MODE_FORMAT,
		}).Pack(),
	})
	require.NoError(t, err)
	require.Eventually(t, func() bool { return !exe.IsRunning(archiveID) }, time.Minute, 100*time.Millisecond)
	failed, err := jobClient.Get(ctx, &entity.GetJobRequest{Id: archiveID})
	require.NoError(t, err)
	require.Equal(t, entity.JobStatus_PENDING, failed.Job.Status)
	require.Equal(t, entity.JobPhase_JOB_PHASE_WAITING_FOR_MEDIA, failed.Job.Phase)
	failedItems, err := archiveClient.ListFiles(ctx, &entity.ListArchiveJobFilesRequest{Id: archiveID})
	require.NoError(t, err)
	require.Len(t, failedItems.Items, len(fixtures))
	for _, item := range failedItems.Items {
		require.Equal(t, entity.CopyStatus_PENDING, item.Status)
		require.Nil(t, item.MediaId)
		require.Empty(t, item.File.MediaPath)
	}
	tapesByBarcode, err := lib.MGetTapeByBarcode(ctx, "ABC001")
	require.NoError(t, err)
	require.Nil(t, tapesByBarcode["ABC001"])
	require.Equal(t, []string{device}, exe.ListAvailableDevices())

	// Make the next normal unmount expose an invalid final Index and reject that checkpoint too.
	_, err = archiveClient.WriteMedia(ctx, &entity.WriteArchiveMediaRequest{
		Id: archiveID,
		Target: (&entity.ArchiveTapeTarget{
			Device: device, Barcode: "ABC001", Name: "E2E virtual tape",
			Mode: entity.ArchiveTapeWriteMode_ARCHIVE_TAPE_WRITE_MODE_FORMAT,
		}).Pack(),
	})
	require.NoError(t, err)
	require.Eventually(t, func() bool { return !exe.IsRunning(archiveID) }, time.Minute, 100*time.Millisecond)
	invalidIndexJob, err := jobClient.Get(ctx, &entity.GetJobRequest{Id: archiveID})
	require.NoError(t, err)
	require.Equal(t, entity.JobStatus_PENDING, invalidIndexJob.Job.Status)
	require.Equal(t, entity.JobPhase_JOB_PHASE_WAITING_FOR_MEDIA, invalidIndexJob.Job.Phase)
	invalidIndexItems, err := archiveClient.ListFiles(ctx, &entity.ListArchiveJobFilesRequest{Id: archiveID})
	require.NoError(t, err)
	require.Len(t, invalidIndexItems.Items, len(fixtures))
	for _, item := range invalidIndexItems.Items {
		require.Equal(t, entity.CopyStatus_PENDING, item.Status)
		require.Nil(t, item.MediaId)
		require.Empty(t, item.File.MediaPath)
	}
	tapesByBarcode, err = lib.MGetTapeByBarcode(ctx, "ABC001")
	require.NoError(t, err)
	require.Nil(t, tapesByBarcode["ABC001"])
	require.Equal(t, []string{device}, exe.ListAvailableDevices())
	invalidIndex, err := os.ReadFile(filepath.Join(
		paths.Work, "jobs", fmt.Sprint(archiveID), "tapes", "ABC001", "ABC001.schema",
	))
	require.NoError(t, err)
	require.Equal(t, "invalid final index\n", string(invalidIndex))

	// A third attempt clears the stale Index and commits through the same fresh virtual device.
	_, err = archiveClient.WriteMedia(ctx, &entity.WriteArchiveMediaRequest{
		Id: archiveID,
		Target: (&entity.ArchiveTapeTarget{
			Device: device, Barcode: "ABC001", Name: "E2E virtual tape",
			Mode: entity.ArchiveTapeWriteMode_ARCHIVE_TAPE_WRITE_MODE_FORMAT,
		}).Pack(),
	})
	require.NoError(t, err)
	waitForCompletedJob(t, ctx, jobClient, archiveID)
	reply, err := archiveClient.ListFiles(ctx, &entity.ListArchiveJobFilesRequest{Id: archiveID})
	require.NoError(t, err)
	archiveItems := reply.Items
	require.Len(t, archiveItems, len(fixtures))
	for _, item := range archiveItems {
		require.Equal(t, entity.CopyStatus_SUBMITTED, item.Status)
		require.NotNil(t, item.MediaId)
	}
	tapesByBarcode, err = lib.MGetTapeByBarcode(ctx, "ABC001")
	require.NoError(t, err)
	originalTape := tapesByBarcode["ABC001"]
	require.NotNil(t, originalTape)
	require.Equal(t, library.TapeFormatLTFSV1, originalTape.Format)

	// Verify the format-time placement policy put only the matching small file in the index partition.
	positions, err := lib.ListMediaFilePositions(ctx, originalTape.ID, "", len(fixtures))
	require.NoError(t, err)
	positionsByTarget := make(map[string]*library.Position, len(positions))
	targetByMediaPath := make(map[string]string, len(archiveItems))
	for _, item := range archiveItems {
		targetByMediaPath[item.File.MediaPath] = item.File.TargetPath
	}
	wantPartitions := map[string]string{
		"dataset/a.bin":         "b",
		"dataset/image.fixture": "a",
		"dataset/m.bin":         "b",
		"dataset/z.bin":         "b",
	}
	require.Len(t, positions, len(wantPartitions))
	for _, position := range positions {
		want, ok := wantPartitions[strings.TrimPrefix(targetByMediaPath[position.Path], "Unforged/Archive/")]
		require.True(t, ok, position.Path)
		require.Len(t, position.StorageOrder, 17, position.Path)
		require.Equal(t, want[0], position.StorageOrder[0], position.Path)
		require.NotNil(t, position.StorageMetadata, position.Path)
		metadata := position.StorageMetadata.GetLtfs()
		require.NotNil(t, metadata, position.Path)
		extents := metadata.Extents
		require.NotEmpty(t, extents, position.Path)
		firstExtent := extents[0]
		for _, extent := range extents {
			require.Equal(t, want, extent.Partition, position.Path)
			if extent.FileOffset < firstExtent.FileOffset {
				firstExtent = extent
			}
		}
		require.Equal(t, firstExtent.StartBlock, binary.BigEndian.Uint64(position.StorageOrder[1:9]), position.Path)
		require.Equal(t, firstExtent.ByteOffset, binary.BigEndian.Uint64(position.StorageOrder[9:17]), position.Path)
		targetPath, ok := targetByMediaPath[position.Path]
		require.True(t, ok, position.Path)
		positionsByTarget[targetPath] = position
	}
	requireTapeWriteOrder(t, archiveItems, positionsByTarget)

	// Keep the captured Index available for remount and durable metadata checks.
	archiveTapeDir := filepath.Join(paths.Work, "jobs", fmt.Sprint(archiveID), "tapes", "ABC001")
	schema, err := os.ReadFile(filepath.Join(archiveTapeDir, "ABC001.schema"))
	require.NoError(t, err)
	require.Contains(t, string(schema), "acp.signature")

	// Remount the cartridge and verify that LTFS persisted each canonical signature xattr.
	remountPoint, err := os.MkdirTemp(root, "ltfs-remount-")
	require.NoError(t, err)
	mountCmd := exec.CommandContext(ctx, scripts.Mount)
	mountCmd.Env = append(os.Environ(),
		"DEVICE="+device, "MOUNT_POINT="+remountPoint, "TAPE_DIR="+archiveTapeDir,
	)
	mountOutput, err := mountCmd.CombinedOutput()
	require.NoErrorf(t, err, "remount LTFS cartridge:\n%s", mountOutput)
	for _, item := range archiveItems {
		expected := fixtures[filepath.Base(item.File.TargetPath)]
		cached, hit, readErr := acp.ReadCachedSignature(filepath.Join(remountPoint, filepath.FromSlash(item.File.MediaPath)))
		require.NoError(t, readErr)
		require.True(t, hit, item.File.MediaPath)
		require.Equal(t, int64(len(expected)), cached.Size)
		require.Equal(t, sha256.Sum256(expected), cached.SHA256)
	}
	unmountCmd := exec.CommandContext(ctx, testScript(t, "umount-file.sh"))
	unmountCmd.Env = append(os.Environ(), "MOUNT_POINT="+remountPoint, "TAPE_DIR="+archiveTapeDir)
	unmountOutput, err := unmountCmd.CombinedOutput()
	require.NoErrorf(t, err, "unmount remounted LTFS cartridge:\n%s", unmountOutput)
	require.NoError(t, os.Remove(remountPoint))

	inspected, err := serviceClient.MediaInspect(ctx, (&entity.MediaInspectTapeTarget{Device: device}).Pack())
	require.NoError(t, err)
	require.Equal(t, "ABC001", inspected.Identity)
	require.NotNil(t, inspected.Media)
	require.Equal(t, originalTape.ID, inspected.Media.Id)
	require.Equal(t, int64(len(fixtures)), inspected.FileCount)

	// Create a second Archive Job and append it to the same physical cartridge.
	appendedContent := []byte("cross-job append fixture")
	require.NoError(t, os.MkdirAll(filepath.Join(paths.Source, "additional"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(paths.Source, "additional", "appended.bin"), appendedContent, 0o644))
	appended, err := archiveClient.Create(ctx, &entity.CreateArchiveJobRequest{
		Spec: &entity.ArchiveJobSpec{Selections: indexedSelections(t, ctx, conn, paths.Source, "additional/appended.bin")},
	})
	require.NoError(t, err)
	appendArchiveID := appended.Job.Id
	waitForPendingJob(t, ctx, jobClient, appendArchiveID)
	_, err = archiveClient.WriteMedia(ctx, &entity.WriteArchiveMediaRequest{
		Id: appendArchiveID,
		Target: (&entity.ArchiveTapeTarget{
			Device: device, Barcode: "ABC001", Mode: entity.ArchiveTapeWriteMode_ARCHIVE_TAPE_WRITE_MODE_APPEND,
		}).Pack(),
	})
	require.NoError(t, err)
	waitForCompletedJob(t, ctx, jobClient, appendArchiveID)
	appendedItems, err := archiveClient.ListFiles(ctx, &entity.ListArchiveJobFilesRequest{Id: appendArchiveID})
	require.NoError(t, err)
	require.Len(t, appendedItems.Items, 1)
	appendedItem := appendedItems.Items[0]
	require.Equal(t, entity.CopyStatus_SUBMITTED, appendedItem.Status)
	require.Equal(t, "Unforged/Archive/additional/appended.bin", appendedItem.File.TargetPath)
	require.NotEqual(t, appendedItem.File.TargetPath, appendedItem.File.MediaPath)
	require.Equal(t, originalTape.ID, *appendedItem.MediaId)
	tapesByBarcode, err = lib.MGetTapeByBarcode(ctx, "ABC001")
	require.NoError(t, err)
	require.Equal(t, originalTape.ID, tapesByBarcode["ABC001"].ID)
	appendedFile, err := lib.GetByPath(ctx, library.Root.ID, appendedItem.File.TargetPath)
	require.NoError(t, err)
	require.NotNil(t, appendedFile)
	require.FileExists(t, filepath.Join(archiveTapeDir, "yatm-report.json"))
	require.FileExists(t, filepath.Join(archiveTapeDir, "ltfs.log"))
	require.FileExists(t, filepath.Join(archiveTapeDir, "ABC001.schema"))
	archivedImage, err := lib.GetByPath(ctx, library.Root.ID, "Unforged/Archive/dataset/image.fixture")
	require.NoError(t, err)
	require.NotNil(t, archivedImage)
	imageDetail, err := serviceClient.FileGet(ctx, &entity.FileGetRequest{Id: archivedImage.ID})
	require.NoError(t, err)
	require.NotNil(t, imageDetail.Preview)
	require.Len(t, imageDetail.Preview.Assets, 1)
	previewData := readHTTPContent(t, ctx, fmt.Sprintf("%s/files/previews/%d/thumbnail", conn.url, archivedImage.ID))
	require.NotEmpty(t, previewData)

	// Attach annotations through the public API before crossing the JSON Lines boundary.
	note := "LTFS JSONL round-trip"
	_, err = serviceClient.FileMetadataEdit(ctx, &entity.FileMetadataEditRequest{
		Ids: []int64{archivedImage.ID}, AddTags: []string{"archive", "preview"}, Note: &note,
	})
	require.NoError(t, err)

	// Transfer the complete metadata snapshot through the real CLI.
	snapshotPath := filepath.Join(root, "library.jsonl")
	_, err = conn.run(ctx, "library", "export", "--output", snapshotPath)
	require.NoError(t, err)
	_, err = conn.run(ctx, "library", "import", "--input", snapshotPath, "--confirm")
	require.NoError(t, err)

	// Verify annotations survived the complete public RPC and HTTP round trip.
	importedImage, err := serviceClient.FileGet(ctx, &entity.FileGetRequest{Id: archivedImage.ID})
	require.NoError(t, err)
	require.Equal(t, note, importedImage.File.Note)
	require.Equal(t, []string{"archive", "preview"}, importedImage.File.Tags)

	// Re-import the same Library through the exact legacy whole-object backup shape.
	var files []*library.File
	require.NoError(t, libraryDB.Order("id").Find(&files).Error)
	legacyFiles := make([]legacyFile, 0, len(files))
	for _, file := range files {
		legacyFiles = append(legacyFiles, legacyFile{ID: file.ID, ParentID: file.ParentID, Name: file.Name,
			Mode: file.Mode, ModTime: file.ModTime, Hash: file.Hash, Signature: file.Signature, Size: file.Size})
	}
	// Materialize the legacy Tape view from the current Media row; current has no tapes table.
	currentTape, err := lib.GetTape(ctx, originalTape.ID)
	require.NoError(t, err)
	tapes := []*library.Tape{currentTape}
	var physicalPositions []*library.Position
	require.NoError(t, libraryDB.Where("is_dir = ?", false).Order("id").Find(&physicalPositions).Error)
	for _, position := range physicalPositions {
		require.NotEmpty(t, position.StorageOrder)
		require.NotNil(t, position.StorageMetadata)
		require.NotEmpty(t, position.StorageMetadata.GetLtfs().Extents)
	}
	legacyPositions := make([]legacyPosition, 0, len(physicalPositions))
	for _, position := range physicalPositions {
		owners, more, err := lib.ListContentDuplicates(ctx, position.Signature, 0, 2)
		require.NoError(t, err)
		require.False(t, more)
		require.Len(t, owners, 1, "the legacy fixture uses unambiguous distinct contents")
		legacyPositions = append(legacyPositions, legacyPosition{
			ID: position.ID, FileID: owners[0].ID, TapeID: position.MediaID, Path: position.Path,
			Mode: position.Mode, ModTime: position.ModTime, WriteTime: position.WriteTime,
			Size: position.Size, Hash: position.Hash,
		})
	}
	legacyBackup, err := json.Marshal(legacyLibraryBackup{Files: legacyFiles, Tapes: tapes, Positions: legacyPositions})
	require.NoError(t, err)
	legacyDB, err := resource.OpenSQLite(filepath.Join(root, "legacy-import.db"))
	require.NoError(t, err)
	legacyLibrary := library.New(legacyDB)
	require.NoError(t, legacyLibrary.AutoMigrate())
	legacyExecutor := executor.New(legacyDB, legacyLibrary, nil, executor.Paths{Work: filepath.Join(root, "legacy-work")}, executor.Scripts{}, nil)
	require.NoError(t, legacyExecutor.AutoMigrate())
	legacyCLI := serveCLI(t, apis.New(legacyLibrary, legacyExecutor), legacyExecutor)
	legacyPath := filepath.Join(root, "legacy-library.json")
	require.NoError(t, os.WriteFile(legacyPath, legacyBackup, 0o600))
	_, err = legacyCLI.run(ctx, "library", "import", "--input", legacyPath, "--confirm")
	require.NoError(t, err)
	rootPositions, err := legacyLibrary.ListPositions(ctx, tapes[0].ID, "")
	require.NoError(t, err)
	require.NotEmpty(t, rootPositions)
	for _, position := range rootPositions {
		require.True(t, position.IsDir)
	}
	legacyTape, err := legacyLibrary.GetTape(ctx, tapes[0].ID)
	require.NoError(t, err)
	require.Equal(t, library.TapeFormatLTFSV0, legacyTape.Format)

	// Restore the archived directory from the same virtual cartridge through a second Job.
	directory, err := lib.GetByPath(ctx, library.Root.ID, "Unforged/Archive/dataset")
	require.NoError(t, err)
	require.NotNil(t, directory)
	restored, err := restoreClient.Create(ctx, &entity.CreateRestoreJobRequest{
		Spec: &entity.RestoreJobSpec{Destination: restoreDestination(t, ctx, conn, paths.Target), Selections: librarySelections(directory.ID, appendedFile.ID)},
	})
	require.NoError(t, err)
	restoreID := restored.Job.Id
	waitForPendingJob(t, ctx, jobClient, restoreID)
	_, err = restoreClient.RestoreMedia(ctx, &entity.RestoreMediaRequest{
		Id: restoreID, Target: (&entity.ReadTapeTarget{Device: device}).Pack(),
	})
	require.NoError(t, err)
	waitForCompletedJob(t, ctx, jobClient, restoreID)
	require.FileExists(t, filepath.Join(paths.Work, "jobs", fmt.Sprint(restoreID), "tapes", "ABC001", "ltfs.log"))
	for name, expected := range fixtures {
		restoredPath := filepath.Join(paths.Target, "Unforged", "Archive", "dataset", name)
		actual, err := os.ReadFile(restoredPath)
		require.NoError(t, err)
		require.Equal(t, expected, actual)
		require.Equal(t, sha256.Sum256(expected), sha256.Sum256(actual))
		cached, hit, readErr := acp.ReadCachedSignature(restoredPath)
		require.NoError(t, readErr)
		require.True(t, hit, restoredPath)
		require.Equal(t, sha256.Sum256(expected), cached.SHA256)
	}
	actualAppend, err := os.ReadFile(filepath.Join(paths.Target, "Unforged", "Archive", "additional", "appended.bin"))
	require.NoError(t, err)
	require.Equal(t, appendedContent, actualAppend)

	// Read the complete recorded cartridge through the public integrity workflow before reuse.
	verified := new(entity.CreateScanJobReply)
	cliResult(t, ctx, conn, verified, "verify", "create", decimal(originalTape.ID))
	verifyID := verified.Job.Id
	waitCLIJob(t, ctx, conn, verifyID, true)
	cliResult(t, ctx, conn, new(entity.ReadScanMediaReply), "verify", "run", decimal(verifyID), "--device", device)
	waitCLIJob(t, ctx, conn, verifyID, false)
	verifyProgress := new(entity.GetScanJobProgressReply)
	cliResult(t, ctx, conn, verifyProgress, "job", "progress", decimal(verifyID))
	require.EqualValues(t, len(fixtures)+1, verifyProgress.Matched)
	require.Equal(t, verifyProgress.Progress.TotalFiles, verifyProgress.Progress.CopiedFiles)
	require.Zero(t, verifyProgress.Damaged)
	require.Zero(t, verifyProgress.Missing)
	require.Zero(t, verifyProgress.Unreadable)
	require.Zero(t, verifyProgress.Unverifiable)
	var verifiedCount int
	var afterEntry int64
	for {
		page := new(entity.ListScanJobEntriesReply)
		cliResult(t, ctx, conn, page, "verify", "entries", decimal(verifyID), "--limit", "2", "--after-id", decimal(afterEntry))
		for _, entry := range page.Entries {
			require.Greater(t, entry.Id, afterEntry)
			require.Equal(t, entity.ScanFinding_MATCH, entry.Finding)
			require.Equal(t, entry.Sha256, entry.ActualHash)
			require.Equal(t, entry.Size, entry.ActualSize)
			require.Positive(t, entry.CheckedAtMs)
			require.False(t, entry.Stale)
			copies := new(entity.ListContentCopiesReply)
			cliResult(t, ctx, conn, copies, "file", "copies", "--signature", hex.EncodeToString(entry.Signature))
			require.Len(t, copies.Positions, 1)
			require.Equal(t, entry.PositionId, copies.Positions[0].Id)
			require.Equal(t, entity.PositionHealth_HEALTHY, copies.Positions[0].Health)
			require.Equal(t, verifyID, copies.Positions[0].HealthJobId)
			verifiedCount++
			afterEntry = entry.Id
		}
		if !page.HasMore {
			break
		}
	}
	require.Equal(t, len(fixtures)+1, verifiedCount)
	require.Equal(t, []string{device}, exe.ListAvailableDevices())

	// Delete the old Media metadata explicitly, then format the same physical barcode.
	replacementContent := []byte("replacement tape fixture")
	require.NoError(t, os.WriteFile(filepath.Join(paths.Source, "replacement.bin"), replacementContent, 0o644))
	overwritten, err := archiveClient.Create(ctx, &entity.CreateArchiveJobRequest{
		Spec: &entity.ArchiveJobSpec{Selections: indexedSelections(t, ctx, conn, paths.Source, "replacement.bin")},
	})
	require.NoError(t, err)
	overwriteArchiveID := overwritten.Job.Id
	waitForPendingJob(t, ctx, jobClient, overwriteArchiveID)
	_, err = serviceClient.MediaDelete(ctx, &entity.MediaDeleteRequest{Ids: []int64{originalTape.ID}})
	require.NoError(t, err)
	_, err = archiveClient.WriteMedia(ctx, &entity.WriteArchiveMediaRequest{
		Id: overwriteArchiveID,
		Target: (&entity.ArchiveTapeTarget{
			Device: device, Barcode: "ABC001", Name: "Replacement tape",
			Mode: entity.ArchiveTapeWriteMode_ARCHIVE_TAPE_WRITE_MODE_FORMAT,
		}).Pack(),
	})
	require.NoError(t, err)
	waitForCompletedJob(t, ctx, jobClient, overwriteArchiveID)
	tapesByBarcode, err = lib.MGetTapeByBarcode(ctx, "ABC001")
	require.NoError(t, err)
	replacementTape := tapesByBarcode["ABC001"]
	require.NotNil(t, replacementTape)
	require.NotEqual(t, originalTape.ID, replacementTape.ID)
	require.Equal(t, library.TapeFormatLTFSV1, replacementTape.Format)
	replacementStats, err := lib.GetTapeStats(ctx, replacementTape.ID)
	require.NoError(t, err)
	require.Equal(t, int64(1), replacementStats.FileCount)
	oldLogicalFile, err := lib.GetByPath(ctx, library.Root.ID, "Unforged/Archive/dataset/image.fixture")
	require.NoError(t, err)
	require.NotNil(t, oldLogicalFile)

	// Exercise API cleanup only after both durable results have been verified.
	_, err = jobClient.Delete(ctx, &entity.DeleteJobsRequest{Ids: []int64{
		archiveID, previewID, appendArchiveID, restoreID, verifyID, overwriteArchiveID,
	}})
	require.NoError(t, err)
	listed, err := jobClient.List(ctx, &entity.ListJobsRequest{Filter: &entity.JobFilter{}})
	require.NoError(t, err)
	var scans []int64
	for _, job := range listed.Jobs {
		require.Equal(t, entity.JobKind_SCAN, job.Kind)
		scans = append(scans, job.Id)
	}
	_, err = jobClient.Delete(ctx, &entity.DeleteJobsRequest{Ids: scans})
	require.NoError(t, err)
}

func testScript(t *testing.T, name string) string {
	t.Helper()
	working, err := os.Getwd()
	require.NoError(t, err)
	filename := filepath.Join(working, "testdata", name)
	require.FileExists(t, filename)
	return filename
}

func waitForCompletedJob(t *testing.T, ctx context.Context, client entity.JobServiceClient, id int64) {
	t.Helper()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()

	// A stopped PENDING attempt is a failed E2E run; include its durable log in the failure.
	for {
		reply, err := client.Get(ctx, &entity.GetJobRequest{Id: id})
		require.NoError(t, err)
		job := reply.Job
		if job.Status == entity.JobStatus_COMPLETED {
			return
		}
		if !isActivePhase(job.Phase) {
			logs, logErr := client.GetLog(ctx, &entity.GetJobLogRequest{Id: id})
			require.NoError(t, logErr)
			t.Fatalf("job %d stopped at %s:\n%s", id, job.Status, logs.Logs)
		}
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-ticker.C:
		}
	}
}

func waitForPendingJob(t *testing.T, ctx context.Context, client entity.JobServiceClient, id int64) {
	t.Helper()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()

	for {
		reply, err := client.Get(ctx, &entity.GetJobRequest{Id: id})
		require.NoError(t, err)
		job := reply.Job
		if job.Status == entity.JobStatus_PENDING && job.Phase == entity.JobPhase_JOB_PHASE_WAITING_FOR_MEDIA {
			return
		}
		if job.Status == entity.JobStatus_INDEXING && job.Phase == entity.JobPhase_JOB_PHASE_WAITING_FOR_INDEX_RETRY {
			logs, logErr := client.GetLog(ctx, &entity.GetJobLogRequest{Id: id})
			require.NoError(t, logErr)
			t.Fatalf("job %d stopped while indexing:\n%s", id, logs.Logs)
		}
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-ticker.C:
		}
	}
}

func isActivePhase(phase entity.JobPhase) bool {
	switch phase {
	case entity.JobPhase_JOB_PHASE_INDEXING,
		entity.JobPhase_JOB_PHASE_GENERATING_PREVIEWS,
		entity.JobPhase_JOB_PHASE_PREPARING_MEDIA,
		entity.JobPhase_JOB_PHASE_COPYING_TO_MEDIA,
		entity.JobPhase_JOB_PHASE_COPYING_FROM_MEDIA,
		entity.JobPhase_JOB_PHASE_FINALIZING_MEDIA:
		return true
	default:
		return false
	}
}
