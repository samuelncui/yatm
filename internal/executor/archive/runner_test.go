package archive

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/samuelncui/acp"
	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/internal/executor"
	scanjob "github.com/samuelncui/yatm/internal/executor/scan"
	"github.com/samuelncui/yatm/internal/library"
	mediapkg "github.com/samuelncui/yatm/internal/media"
	previewpkg "github.com/samuelncui/yatm/internal/preview"
	"github.com/samuelncui/yatm/internal/resource"
	settingspkg "github.com/samuelncui/yatm/internal/settings"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

type capacityBoundarySession struct{}

type capacityAfterOneSession struct {
	requests int
}

type archivePreviewer struct{}

type disappearingPreviewer struct {
	archivePreviewer
	checks atomic.Int32
}

func (p *disappearingPreviewer) CheckGeneration(context.Context) error {
	if p.checks.Add(1) > 1 {
		return fmt.Errorf("Preview helper disappeared after Archive admission")
	}
	return nil
}

func (archivePreviewer) Supports(string, *entity.PreviewJobSettings) bool { return true }

func (archivePreviewer) Generate(context.Context, string, []byte, int64, int64, bool, *entity.PreviewJobSettings) ([]byte, error) {
	return []byte{1}, nil
}

func (archivePreviewer) Manifest([]byte) (*entity.PreviewManifest, error) { return nil, os.ErrNotExist }
func (archivePreviewer) Exists([]byte) (bool, error)                      { return false, nil }

func (archivePreviewer) Open([]byte, string) (io.ReadCloser, error) { return nil, os.ErrNotExist }

func (capacityBoundarySession) Capabilities() mediapkg.Capabilities { return mediapkg.Capabilities{} }

func (capacityBoundarySession) Media() *mediapkg.Descriptor { return new(mediapkg.Descriptor) }

func (capacityBoundarySession) TargetPath(string, int64) (string, error) {
	return "", mediapkg.ErrCapacityBoundary
}

func (s capacityBoundarySession) Finalize(context.Context, bool) (*mediapkg.WriteResult, error) {
	return &mediapkg.WriteResult{Media: s.Media()}, nil
}

func (*capacityAfterOneSession) Capabilities() mediapkg.Capabilities {
	return mediapkg.Capabilities{}
}

func (*capacityAfterOneSession) Media() *mediapkg.Descriptor { return new(mediapkg.Descriptor) }

func (s *capacityAfterOneSession) TargetPath(path string, _ int64) (string, error) {
	s.requests++
	if s.requests > 1 {
		return "", mediapkg.ErrCapacityBoundary
	}
	return path, nil
}

func (s *capacityAfterOneSession) Finalize(context.Context, bool) (*mediapkg.WriteResult, error) {
	return &mediapkg.WriteResult{Media: s.Media()}, nil
}

func setupTestExecutor(t *testing.T, scripts executor.Scripts) *executor.Executor {
	return setupTestExecutorWithPreviewer(t, scripts, nil)
}

func setupTestExecutorWithPreviewer(
	t *testing.T,
	scripts executor.Scripts,
	previews executor.Previewer,
) *executor.Executor {
	t.Helper()

	// Create isolated Executor and Library stores for the requested backend scripts.
	root := t.TempDir()
	mainDB, err := resource.OpenSQLite(filepath.Join(root, "executor.db"))
	require.NoError(t, err)
	libraryDB, err := resource.OpenSQLite(filepath.Join(root, "library.db"))
	require.NoError(t, err)
	lib := library.NewWithSettings(libraryDB, testSettings(t, libraryDB))
	require.NoError(t, lib.AutoMigrate())

	// Initialize the complete storage layout and optional Preview dependency.
	exe := executor.New(mainDB, lib, []string{"/dev/nst0"}, executor.Paths{
		Work: filepath.Join(root, "work"), Source: filepath.Join(root, "source"),
		Target: filepath.Join(root, "target"), Volumes: []string{filepath.Join(root, "volumes")},
		Access: []executor.AccessRange{{Root: filepath.Join(root, "source")}, {Root: filepath.Join(root, "target")}},
	}, scripts, previews)
	require.NoError(t, exe.AutoMigrate())
	require.NoError(t, os.MkdirAll(exe.Paths().Source, 0o755))
	require.NoError(t, os.MkdirAll(exe.Paths().Volumes[0], 0o755))
	return exe
}

func testSettings(t *testing.T, db *gorm.DB) *settingspkg.Module {
	t.Helper()
	module := settingspkg.New(db, settingspkg.PreviewDefinition{
		Default:  func() (*entity.PreviewSettings, error) { return previewpkg.SettingsFromConfig(previewpkg.Config{}) },
		Validate: previewpkg.ValidateSettings,
	})
	return module
}

type archiveTestSource struct {
	Base string
	Path []string
}

func archiveSelections(ids ...int64) []*entity.FileSelection {
	selections := make([]*entity.FileSelection, 0, len(ids))
	for _, id := range ids {
		selections = append(selections, &entity.FileSelection{Target: &entity.FileSelection_Library{Library: &entity.LibrarySelection{FileId: id}}, Scope: entity.FileScope_FILE_SCOPE_ALL})
	}
	return selections
}

func createArchiveJob(t *testing.T, exe *executor.Executor, sources ...*archiveTestSource) *executor.Job {
	t.Helper()
	items := archiveFixtureItems(t, exe, sources)

	// Seed the canonical bundle shape produced by current admission or migration.
	job, err := exe.CreateJob(context.Background(), entity.JobKind_JOB_KIND_ARCHIVE, 0, func(db *gorm.DB) error {
		if err := db.AutoMigrate(&Config{}, &Item{}); err != nil {
			return err
		}
		if err := db.Create(&items).Error; err != nil {
			return err
		}
		return db.Create(&Config{ID: 1, Spec: &entity.ArchiveJobSpec{}}).Error
	})
	require.NoError(t, err)
	return job
}

func archiveFixtureItems(t *testing.T, exe *executor.Executor, sources []*archiveTestSource) []*Item {
	t.Helper()
	ctx := context.Background()
	var items []*Item
	for _, source := range sources {
		require.NotNil(t, source)
		root := filepath.Join(append([]string{source.Base}, source.Path...)...)
		mediaRoot := path.Join(source.Path...)
		require.NoError(t, filepath.Walk(root, func(filename string, info os.FileInfo, err error) error {
			if err != nil {
				return err
			}
			if info.IsDir() || info.Name() == ".DS_Store" || info.Mode()&acp.UnexpectFileMode != 0 {
				return nil
			}
			if !info.Mode().IsRegular() {
				return nil
			}
			relative, err := filepath.Rel(root, filename)
			if err != nil {
				return err
			}
			target := mediaRoot
			if relative != "." {
				target = path.Join(mediaRoot, filepath.ToSlash(relative))
			}
			if err := entity.ValidateRelativePath(target); err != nil {
				return err
			}
			expected, err := executor.HashObservedContent(ctx, filename, info)
			if err != nil {
				return err
			}
			file, err := exe.Lib().CreateArchiveFile(ctx, target)
			if err != nil {
				return err
			}
			expected.FileId = file.ID
			items = append(items, &Item{
				Status: entity.CopyStatus_COPY_STATUS_PENDING, Size: expected.SizeBytes, TargetPath: target,
				Data: &entity.ArchiveManifestFile{SourcePath: filename, Expected: expected},
			})
			return nil
		}))
	}
	require.NotEmpty(t, items)
	return items
}

func TestArchiveForceRehashRequiresPreview(t *testing.T) {
	// A Preview-only hashing policy must not be accepted without a companion Job.
	exe := setupTestExecutor(t, executor.Scripts{})
	_, err := (&service{exe: exe}).Create(context.Background(), &entity.CreateArchiveJobRequest{
		Spec:        &entity.ArchiveJobSpec{Selections: archiveSelections(1)},
		ForceRehash: true,
	})
	require.ErrorContains(t, err, "requires Preview generation")
}

func TestArchiveRejectsUnknownPreviewPolicy(t *testing.T) {
	// Reject unknown generation policies before creating a companion or touching source files.
	exe := setupTestExecutor(t, executor.Scripts{})
	_, err := (&service{exe: exe}).Create(context.Background(), &entity.CreateArchiveJobRequest{
		Spec:          &entity.ArchiveJobSpec{Selections: archiveSelections(1)},
		PreviewPolicy: entity.PreviewPolicy(99),
	})
	require.ErrorContains(t, err, "invalid Preview policy")
}

func TestArchivePreviewPoliciesPropagateToCompanionPreview(t *testing.T) {
	// Create an Archive with both companion Preview policies enabled.
	exe := setupTestExecutorWithPreviewer(t, executor.Scripts{}, archivePreviewer{})
	_, file, _ := publishArchiveOriginal(t, exe, "preview", []byte("preview source"))
	reply, err := (&service{exe: exe}).Create(context.Background(), &entity.CreateArchiveJobRequest{
		Spec:          &entity.ArchiveJobSpec{Selections: archiveSelections(file.ID)},
		PreviewPolicy: entity.PreviewPolicy_PREVIEW_POLICY_REGENERATE_ALL,
		ForceRehash:   true,
	})
	require.NoError(t, err)
	waitIndexed(t, exe, reply.Job.Id)
	progress, err := (&service{exe: exe}).GetProgress(context.Background(), &entity.GetArchiveJobProgressRequest{Id: reply.Job.Id})
	require.NoError(t, err)
	require.Empty(t, progress.PreviewError)
	require.Positive(t, progress.PreviewJobId)
	require.Eventually(t, func() bool {
		return !exe.IsRunning(progress.PreviewJobId)
	}, 5*time.Second, time.Millisecond)

	// Read the Preview Job's durable spec and verify the policy was propagated.
	db, err := exe.NewStateDB(context.Background(), progress.PreviewJobId)
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	defer sqlDB.Close()
	config := new(scanjob.Config)
	require.NoError(t, db.First(config, 1).Error)
	require.NotNil(t, config.Spec)
	require.Equal(t, entity.ScanSignaturePolicy_SCAN_SIGNATURE_POLICY_FORCE_READ, config.Spec.SignaturePolicy)
	require.Equal(t, entity.PreviewPolicy_PREVIEW_POLICY_REGENERATE_ALL, config.Spec.PreviewPolicy)
	require.True(t, config.IndexedInput)
	require.Empty(t, config.Spec.Selections, "the companion must not re-admit Archive selections")
	var record executor.JobRecord
	require.NoError(t, db.First(&record, 1).Error)
	require.Equal(t, entity.JobKind_JOB_KIND_SCAN, record.Kind)
	require.Equal(t, entity.JobStatus_JOB_STATUS_COMPLETED, record.Status)
}

func TestArchiveCompanionFailurePreservesPreparedArchive(t *testing.T) {
	// A helper that disappears after admission cannot undo the successfully prepared Archive.
	exe := setupTestExecutorWithPreviewer(t, executor.Scripts{}, &disappearingPreviewer{})
	_, file, _ := publishArchiveOriginal(t, exe, "without-previewer", []byte("archive content"))
	api := &service{exe: exe}
	reply, err := api.Create(context.Background(), &entity.CreateArchiveJobRequest{
		Spec: &entity.ArchiveJobSpec{Selections: archiveSelections(file.ID)}, PreviewPolicy: entity.PreviewPolicy_PREVIEW_POLICY_MISSING_ONLY,
	})
	require.NoError(t, err)
	job := waitIndexed(t, exe, reply.Job.Id)
	require.Equal(t, entity.JobStatus_JOB_STATUS_READY, job.Status)
	progress, err := api.GetProgress(context.Background(), &entity.GetArchiveJobProgressRequest{Id: job.ID})
	require.NoError(t, err)
	require.Zero(t, progress.PreviewJobId)
	require.NotEmpty(t, progress.PreviewError)
	items, err := api.ListFiles(context.Background(), &entity.ListArchiveJobFilesRequest{Id: job.ID})
	require.NoError(t, err)
	require.Len(t, items.Items, 1)
	require.Equal(t, file.ID, items.Items[0].File.Expected.FileId)
}

func waitIndexed(t *testing.T, exe *executor.Executor, id int64) *executor.Job {
	t.Helper()
	value, err := exe.GetJobRunner(context.Background(), id)
	require.NoError(t, err)
	runner := value.(*jobArchiveRunner)
	require.Eventually(t, func() bool {
		record := new(executor.JobRecord)
		err := runner.db.First(record, 1).Error
		return err == nil && record.Status == entity.JobStatus_JOB_STATUS_READY && !exe.IsRunning(id)
	}, 5*time.Second, time.Millisecond)
	job, err := exe.GetJob(context.Background(), id)
	require.NoError(t, err)
	return job
}

func writeTestScript(t *testing.T, name, body string) string {
	t.Helper()
	filename := filepath.Join(t.TempDir(), name)
	require.NoError(t, os.WriteFile(filename, []byte("#!/bin/sh\nset -eu\n"+body+"\n"), 0o755))
	return filename
}

func fakeTapeScripts(t *testing.T) executor.Scripts {
	t.Helper()
	return executor.Scripts{
		ReadInfo: writeTestScript(t, "read-info", `printf '%s\n' '{"barcode":"ABC001"}' > "$OUT"`),
		Encrypt:  writeTestScript(t, "encrypt", `printf 'encrypt\n' >> "$TAPE_DIR/ltfs.log"`),
		Mkfs:     writeTestScript(t, "mkfs", `printf 'mkfs\n' >> "$TAPE_DIR/ltfs.log"`),
		Mount: writeTestScript(t, "mount", `
printf 'mount\n' >> "$TAPE_DIR/ltfs.log"
barcode=${TAPE_DIR##*/}
printf 'mount-time snapshot\n' > "$TAPE_DIR/$barcode.schema"
`),
		Umount: writeTestScript(t, "umount", `
printf 'umount\n' >> "$TAPE_DIR/ltfs.log"
/usr/bin/find "$MOUNT_POINT" -mindepth 1 -delete
barcode=${TAPE_DIR##*/}
test ! -e "$TAPE_DIR/$barcode.schema"
printf '%s\n' '<?xml version="1.0" encoding="UTF-8"?>' \
  '<ltfsindex><directory><name>'"$barcode"'</name><contents>' \
  '<file><name>a.txt</name><length>5</length><extentinfo><extent><fileoffset>0</fileoffset><partition>b</partition><startblock>20</startblock><byteoffset>0</byteoffset><bytecount>5</bytecount></extent></extentinfo></file>' \
  '<file><name>z.txt</name><length>4</length><extentinfo><extent><fileoffset>0</fileoffset><partition>b</partition><startblock>10</startblock><byteoffset>0</byteoffset><bytecount>4</bytecount></extent></extentinfo></file>' \
  '</contents></directory></ltfsindex>' > "$TAPE_DIR/$barcode.schema"
`),
	}
}

func TestArchiveManifestUsesMediaFields(t *testing.T) {
	ctx := context.Background()
	exe := setupTestExecutor(t, executor.Scripts{})
	source := filepath.Join(exe.Paths().Source, "file.txt")
	require.NoError(t, os.WriteFile(source, []byte("fixture"), 0o644))
	job := createArchiveJob(t, exe, &archiveTestSource{Base: exe.Paths().Source, Path: []string{"file.txt"}})
	waitIndexed(t, exe, job.ID)
	value, err := exe.GetJobRunner(ctx, job.ID)
	require.NoError(t, err)
	reply, err := value.(*jobArchiveRunner).queryFiles(ctx, &entity.ListArchiveJobFilesRequest{})
	require.NoError(t, err)
	require.Len(t, reply.Items, 1)
	require.Equal(t, "file.txt", reply.Items[0].File.TargetPath)
	require.Empty(t, reply.Items[0].File.MediaPath)
	require.Nil(t, reply.Items[0].MediaId)
}

func TestArchiveTapeBackendCommitsVerifiedFiles(t *testing.T) {
	ctx := context.Background()
	exe := setupTestExecutor(t, fakeTapeScripts(t))
	for name, content := range map[string]string{"z.txt": "last", "a.txt": "first"} {
		require.NoError(t, os.WriteFile(filepath.Join(exe.Paths().Source, name), []byte(content), 0o644))
	}
	job := createArchiveJob(t, exe,
		&archiveTestSource{Base: exe.Paths().Source, Path: []string{"z.txt"}},
		&archiveTestSource{Base: exe.Paths().Source, Path: []string{"a.txt"}},
	)
	waitIndexed(t, exe, job.ID)
	value, err := exe.GetJobRunner(ctx, job.ID)
	require.NoError(t, err)
	runner := value.(*jobArchiveRunner)

	request := &entity.WriteArchiveMediaRequest{Id: job.ID, Target: (&entity.ArchiveTapeTarget{
		Device: "/dev/nst0", Barcode: "ABC001", Name: "fixture",
		Mode: entity.ArchiveTapeWriteMode_ARCHIVE_TAPE_WRITE_MODE_FORMAT,
	}).Pack()}
	_, err = (&service{exe: exe}).WriteMedia(ctx, request)
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		record := new(executor.JobRecord)
		err := runner.db.First(record, 1).Error
		return err == nil && record.Status == entity.JobStatus_JOB_STATUS_COMPLETED && !exe.IsRunning(job.ID)
	}, 15*time.Second, 10*time.Millisecond)
	stored, err := exe.GetJob(ctx, job.ID)
	require.NoError(t, err)
	require.Equal(t, entity.JobStatus_JOB_STATUS_COMPLETED, stored.Status)

	reply, err := runner.queryFiles(ctx, &entity.ListArchiveJobFilesRequest{})
	require.NoError(t, err)
	require.Len(t, reply.Items, 2)
	require.NotNil(t, reply.Items[0].MediaId)
	media, err := exe.Lib().GetMedia(ctx, *reply.Items[0].MediaId)
	require.NoError(t, err)
	require.Equal(t, entity.MediaKind_MEDIA_KIND_TAPE, media.Kind)
	positions, err := exe.Lib().ListPositions(ctx, media.ID, "")
	require.NoError(t, err)
	require.Len(t, positions, 2)
	require.Len(t, positions[0].StorageOrder, 17)
	require.FileExists(t, filepath.Join(
		exe.Paths().Work, "jobs", fmt.Sprint(job.ID), "tapes", "ABC001", "ABC001.schema",
	))
}

func TestArchiveHMSMRVolumeUsesSequentialWriteWithoutStorageOrder(t *testing.T) {
	// Initialize an HM-SMR profile and verify its asymmetric access capabilities.
	ctx := context.Background()
	exe := setupTestExecutor(t, executor.Scripts{})
	volumeRoot := filepath.Join(exe.Paths().Volumes[0], "offline-disk")
	require.NoError(t, os.Mkdir(volumeRoot, 0o755))
	volume, err := mediapkg.InitializeVolume(volumeRoot, &entity.VolumeMediaProfile{
		SerialNumber: "serial-1", Type: entity.VolumeType_VOLUME_TYPE_HM_SMR,
	})
	require.NoError(t, err)
	capabilities, err := mediapkg.CapabilitiesForProfile(volume.Marker.Profile.Pack())
	require.NoError(t, err)
	require.Equal(t, mediapkg.AccessConcurrentRandom, capabilities.Read)
	require.Equal(t, mediapkg.AccessSequential, capabilities.Write)
	stored, err := exe.Lib().CreateMedia(ctx, &library.Media{
		Kind: entity.MediaKind_MEDIA_KIND_VOLUME, Identity: volume.Marker.UUID, Name: "Offline Disk",
		Profile: volume.Marker.Profile.Pack(), CreatedAtNS: volume.Marker.CreatedAtNS,
	})
	require.NoError(t, err)

	// Archive through the sequential writer while retaining concurrent-random read semantics.
	contents := map[string][]byte{"a.txt": []byte("first"), "folder/z.txt": []byte("last")}
	for name, content := range contents {
		filename := filepath.Join(exe.Paths().Source, filepath.FromSlash(name))
		require.NoError(t, os.MkdirAll(filepath.Dir(filename), 0o755))
		require.NoError(t, os.WriteFile(filename, content, 0o644))
	}
	job := createArchiveJob(t, exe,
		&archiveTestSource{Base: exe.Paths().Source, Path: []string{"a.txt"}},
		&archiveTestSource{Base: exe.Paths().Source, Path: []string{"folder"}},
	)
	waitIndexed(t, exe, job.ID)
	value, err := exe.GetJobRunner(ctx, job.ID)
	require.NoError(t, err)
	_, err = (&service{exe: exe}).WriteMedia(ctx, &entity.WriteArchiveMediaRequest{
		Id: job.ID, Target: (&entity.ArchiveVolumeTarget{Uuid: strings.ToUpper(volume.Marker.UUID)}).Pack(),
	})
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		record := new(executor.JobRecord)
		err := value.(*jobArchiveRunner).db.First(record, 1).Error
		return err == nil && record.Status == entity.JobStatus_JOB_STATUS_COMPLETED && !exe.IsRunning(job.ID)
	}, 15*time.Second, 10*time.Millisecond)

	// Verify copied bytes, source and target caches, and absence of sequential-read metadata.
	reply, err := value.(*jobArchiveRunner).queryFiles(ctx, &entity.ListArchiveJobFilesRequest{})
	require.NoError(t, err)
	require.Len(t, reply.Items, 2)
	for _, item := range reply.Items {
		require.Equal(t, entity.CopyStatus_COPY_STATUS_SUBMITTED, item.Status)
		require.NotNil(t, item.MediaId)
		require.Equal(t, stored.ID, *item.MediaId)
		require.NotEqual(t, item.File.TargetPath, item.File.MediaPath)
		content, err := os.ReadFile(filepath.Join(volumeRoot, filepath.FromSlash(item.File.MediaPath)))
		require.NoError(t, err)
		require.Equal(t, contents[item.File.TargetPath], content)
		wantHash := sha256.Sum256(content)
		for _, path := range []string{
			filepath.Join(exe.Paths().Source, filepath.FromSlash(item.File.TargetPath)),
			filepath.Join(volumeRoot, filepath.FromSlash(item.File.MediaPath)),
		} {
			signature, valid, err := acp.ReadCachedSignature(path)
			require.NoError(t, err)
			require.True(t, valid)
			require.Equal(t, wantHash, signature.SHA256)
		}
	}
	positions, err := exe.Lib().ListMediaFilePositions(ctx, stored.ID, "", 10)
	require.NoError(t, err)
	require.Len(t, positions, 2)
	for _, position := range positions {
		require.Empty(t, position.StorageOrder)
		require.Nil(t, position.StorageMetadata)
		require.Equal(t, entity.PositionHealth_POSITION_HEALTH_UNKNOWN, position.Health)
		require.Zero(t, position.CheckedAtNS, "writing is not an independent read-back check")
		require.Zero(t, position.HealthJobID)
	}
	require.FileExists(t, filepath.Join(volumeRoot, mediapkg.VolumeMarkerName))
}

func TestStageCopyResultRecordsPathOnlyAfterSuccess(t *testing.T) {
	ctx := context.Background()
	exe := setupTestExecutor(t, executor.Scripts{})
	content := []byte("fixture")
	source := filepath.Join(exe.Paths().Source, "file.txt")
	require.NoError(t, os.WriteFile(source, content, 0o644))
	job := createArchiveJob(t, exe, &archiveTestSource{Base: exe.Paths().Source, Path: []string{"file.txt"}})
	waitIndexed(t, exe, job.ID)
	value, err := exe.GetJobRunner(ctx, job.ID)
	require.NoError(t, err)
	runner := value.(*jobArchiveRunner)
	item := new(Item)
	require.NoError(t, runner.db.First(item).Error)
	target := &copyItem{runner: runner, item: item, mediaTarget: "target"}

	// A target that produced nothing is a completion whose row stays PENDING, because no durable
	// result can express it.
	reporter := startTestReporter(t, runner)
	failed := acp.Result{
		Job: target, Size: item.Size, Mode: 0o644,
		Targets: []acp.TargetResult{{Path: "target", Err: acp.ErrTargetNoSpace}},
	}
	require.NoError(t, reporter.onResults([]acp.Result{failed}))
	require.NoError(t, reporter.Close())
	require.NoError(t, runner.db.First(item, item.ID).Error)
	require.Equal(t, entity.CopyStatus_COPY_STATUS_PENDING, item.Status)
	require.Empty(t, item.MediaPath)
	require.ErrorIs(t, reporter.err(), mediapkg.ErrTargetNoSpace)

	hash := sha256.Sum256(content)
	success := acp.Result{
		Job: target, Size: int64(len(content)), Mode: 0o644,
		WriteTime: time.Unix(0, 1), SHA256: hash[:],
		Targets: []acp.TargetResult{{Path: "target", Size: int64(len(content))}},
	}
	// A later successful completion is what stages the row, in its own attempt.
	reporter = startTestReporter(t, runner)
	require.NoError(t, reporter.onResults([]acp.Result{success}))
	require.NoError(t, reporter.Close())
	require.NoError(t, runner.db.First(item, item.ID).Error)
	require.Equal(t, entity.CopyStatus_COPY_STATUS_STAGED, item.Status)
	require.Equal(t, item.TargetPath, item.MediaPath)
}

// startTestReporter builds the result path of a runner whose copy attempt is not running. The
// writer is drained and closed when the test ends.
func startTestReporter(t *testing.T, runner *jobArchiveRunner) *reporter {
	t.Helper()
	value, err := newReporter(context.Background(), runner)
	require.NoError(t, err)
	t.Cleanup(func() { _ = value.Close() })
	return value
}

// newItemTestRunner opens the Job bundle schema a copy attempt writes through.
func newItemTestRunner(t *testing.T, items ...*Item) *jobArchiveRunner {
	t.Helper()
	db, err := resource.OpenSQLite(filepath.Join(t.TempDir(), "items.db"))
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })
	require.NoError(t, ensureSchema(context.Background(), db))
	for _, item := range items {
		require.NoError(t, db.Create(item).Error)
	}
	return &jobArchiveRunner{db: db, logger: logrus.New()}
}

func TestArchiveReporterPersistsTheBatchItReceives(t *testing.T) {
	// The callback only enqueues, and the writer stages everything it accepted before the drain
	// returns.
	runner := newItemTestRunner(t, &Item{
		ID: 1, Status: entity.CopyStatus_COPY_STATUS_PENDING, Size: 4, TargetPath: "file.txt",
		Data: &entity.ArchiveManifestFile{SourcePath: "/source/file.txt"},
	})
	reporter := startTestReporter(t, runner)
	target := &copyItem{runner: runner, item: runnerItem(t, runner, 1), mediaTarget: "file.txt"}

	require.NoError(t, reporter.onResults([]acp.Result{{
		Job: target, Size: 4, Mode: 0o644, WriteTime: time.Unix(0, 1),
		SHA256:  sha256Sum([]byte("data")),
		Targets: []acp.TargetResult{{Path: "file.txt", Size: 4}},
	}}))
	require.NoError(t, reporter.Close())
	require.Equal(t, entity.CopyStatus_COPY_STATUS_STAGED, readItemStatus(t, runner, 1))
	flushed, failures := reporter.counters()
	require.Equal(t, int64(1), flushed)
	require.Zero(t, failures)
}

func TestArchiveReporterReportsAFailedFlush(t *testing.T) {
	// A failed staging write is the writer's terminal error, which stops the feed: the item keeps
	// its PENDING row for a later attempt, and the first failure is what the phase reports.
	runner := newItemTestRunner(t, &Item{
		ID: 1, Status: entity.CopyStatus_COPY_STATUS_PENDING, Size: 4, TargetPath: "file.txt",
		Data: &entity.ArchiveManifestFile{SourcePath: "/source/file.txt"},
	})
	reporter := startTestReporter(t, runner)
	failure := errors.New("job database is unavailable")
	reporter.store = func(context.Context, []itemOutcome) error {
		return failure
	}
	target := &copyItem{runner: runner, item: runnerItem(t, runner, 1), mediaTarget: "file.txt"}

	// The asynchronous write may fail before Enqueue returns. Either observation must retain
	// the same terminal failure, rather than depend on the scheduler.
	err := reporter.onResults([]acp.Result{{
		Job: target, Size: 4, Mode: 0o644, WriteTime: time.Unix(0, 1),
		SHA256:  sha256Sum([]byte("data")),
		Targets: []acp.TargetResult{{Path: "file.txt", Size: 4}},
	}})
	if err != nil {
		require.ErrorIs(t, err, failure)
	}

	// Draining always observes the error and must leave the manifest item unchanged.
	require.ErrorIs(t, reporter.Close(), failure)
	require.ErrorIs(t, reporter.err(), failure)
	flushed, _ := reporter.counters()
	require.Zero(t, flushed)
	require.Equal(t, entity.CopyStatus_COPY_STATUS_PENDING, readItemStatus(t, runner, 1))
}

// runnerItem loads one manifest row the way the batch source does.
func runnerItem(t *testing.T, runner *jobArchiveRunner, id int64) *Item {
	t.Helper()
	item := new(Item)
	require.NoError(t, runner.db.First(item, id).Error)
	return item
}

func readItemStatus(t *testing.T, runner *jobArchiveRunner, id int64) entity.CopyStatus {
	t.Helper()
	return runnerItem(t, runner, id).Status
}

func sha256Sum(content []byte) []byte {
	sum := sha256.Sum256(content)
	return sum[:]
}

// recordingSession is a WriteSession whose targets are ordinary files in a test root.
type recordingSession struct {
	db     *gorm.DB
	root   string
	mutex  sync.Mutex
	target []string
}

func (s *recordingSession) Capabilities() mediapkg.Capabilities { return mediapkg.Capabilities{} }

func (s *recordingSession) Media() *mediapkg.Descriptor {
	return &mediapkg.Descriptor{ID: 1, Kind: entity.MediaKind_MEDIA_KIND_VOLUME, Identity: "volume-1", Name: "fixture"}
}

func (s *recordingSession) TargetPath(relative string, _ int64) (string, error) {
	item := new(Item)
	if err := s.db.Where("status = ? AND target_path = ?", entity.CopyStatus_COPY_STATUS_PENDING, relative).First(item).Error; err != nil {
		return "", err
	}
	if item.Size > 16 {
		return "", mediapkg.ErrCapacityBoundary
	}
	s.mutex.Lock()
	s.target = append(s.target, relative)
	s.mutex.Unlock()
	return filepath.Join(s.root, filepath.FromSlash(relative)), nil
}

func (s *recordingSession) Finalize(context.Context, bool) (*mediapkg.WriteResult, error) {
	return &mediapkg.WriteResult{Media: s.Media()}, nil
}

func (s *recordingSession) targets() []string {
	s.mutex.Lock()
	defer s.mutex.Unlock()
	return append([]string(nil), s.target...)
}

func TestArchiveCopyConsumesItemsThroughACP(t *testing.T) {
	// ACP drives the caller-owned items end to end: the manifest is copied, every item
	// reaches exactly one terminal callback, and the buffered result is persisted by the
	// flush path after Run returns.
	ctx := context.Background()
	exe := setupTestExecutor(t, executor.Scripts{})
	for name, content := range map[string]string{"a.txt": "first", "b.txt": "second"} {
		require.NoError(t, os.WriteFile(filepath.Join(exe.Paths().Source, name), []byte(content), 0o644))
	}
	job := createArchiveJob(t, exe,
		&archiveTestSource{Base: exe.Paths().Source, Path: []string{"a.txt"}},
		&archiveTestSource{Base: exe.Paths().Source, Path: []string{"b.txt"}},
	)
	waitIndexed(t, exe, job.ID)
	value, err := exe.GetJobRunner(ctx, job.ID)
	require.NoError(t, err)
	runner := value.(*jobArchiveRunner)
	reporter := startTestReporter(t, runner)

	root := t.TempDir()
	session := &recordingSession{db: runner.db, root: root}
	engine, err := acp.NewStream(
		ctx,
		reporter.onResults,
		acp.WithHashPolicy(acp.HashReadRefresh),
		acp.WithLogger(runner.logger),
	)
	require.NoError(t, err)
	require.NoError(t, (&copySource{runner: runner, session: session, pageSize: batchSize}).consume(ctx, engine))

	// ACP wrote the targets and reported both requested items in request order.
	require.Equal(t, []string{"a.txt", "b.txt"}, session.targets())
	for name, content := range map[string]string{"a.txt": "first", "b.txt": "second"} {
		copied, err := os.ReadFile(filepath.Join(root, name))
		require.NoError(t, err)
		require.Equal(t, content, string(copied))
	}

	// Every result was staged by the writer that received it.
	require.NoError(t, reporter.Close())
	flushed, failures := reporter.counters()
	require.Equal(t, int64(2), flushed)
	require.Zero(t, failures)
	require.NoError(t, reporter.err())
	var staged []*Item
	require.NoError(t, runner.db.Order("target_path").Find(&staged).Error)
	require.Len(t, staged, 2)
	for _, item := range staged {
		require.Equal(t, entity.CopyStatus_COPY_STATUS_STAGED, item.Status)
		require.Equal(t, item.TargetPath, item.MediaPath)
		require.NotNil(t, item.Result)
		require.Len(t, item.Result.Sha256, 32)
		require.Equal(t, item.Size, item.Result.SizeBytes)
	}
}

func TestArchiveResultsArePersistedByTheSharedWriter(t *testing.T) {
	// The results callback is not the flush path: the shared writer stages the batch it accepted,
	// and one batch of results is one database transaction.
	runner := newItemTestRunner(t, &Item{
		ID: 1, Status: entity.CopyStatus_COPY_STATUS_PENDING, Size: 4, TargetPath: "file.txt",
		Data: &entity.ArchiveManifestFile{SourcePath: "/source/file.txt"},
	})
	counting := &countingLogger{Interface: runner.db.Logger}
	runner.db.Logger = counting
	reporter := startTestReporter(t, runner)
	target := &copyItem{runner: runner, item: runnerItem(t, runner, 1), mediaTarget: "file.txt"}
	baseline := counting.count()

	require.NoError(t, reporter.onResults([]acp.Result{{
		Job: target, Size: 4, Mode: 0o644, WriteTime: time.Unix(0, 1),
		SHA256:  sha256Sum([]byte("data")),
		Targets: []acp.TargetResult{{Path: "file.txt", Size: 4}},
	}}))
	require.NoError(t, reporter.Close())
	require.Greater(t, counting.count(), baseline, "the batch reaches the database")
	require.Equal(t, entity.CopyStatus_COPY_STATUS_STAGED, readItemStatus(t, runner, 1))
}

// countingLogger counts the statements the runner's database issues.
type countingLogger struct {
	logger.Interface
	mutex sync.Mutex
	total int
}

func (l *countingLogger) Trace(ctx context.Context, begin time.Time, fc func() (string, int64), err error) {
	l.mutex.Lock()
	l.total++
	l.mutex.Unlock()
	l.Interface.Trace(ctx, begin, fc, err)
}

func (l *countingLogger) count() int {
	l.mutex.Lock()
	defer l.mutex.Unlock()
	return l.total
}

func TestArchiveSourceRejectsFirstFileBeyondMediaCapacity(t *testing.T) {
	ctx := context.Background()
	exe := setupTestExecutor(t, executor.Scripts{})
	sourcePath := filepath.Join(exe.Paths().Source, "large.bin")
	require.NoError(t, os.WriteFile(sourcePath, []byte("fixture"), 0o644))
	job := createArchiveJob(t, exe, &archiveTestSource{Base: exe.Paths().Source, Path: []string{"large.bin"}})
	waitIndexed(t, exe, job.ID)
	value, err := exe.GetJobRunner(ctx, job.ID)
	require.NoError(t, err)
	runner := value.(*jobArchiveRunner)
	reporter := startTestReporter(t, runner)

	// The refused file is still handed over, so ACP reports one outcome for it and no item
	// is silently dropped.
	items, err := (&copySource{
		runner: runner, session: capacityBoundarySession{}, pageSize: batchSize,
	}).nextPage(ctx)
	require.NoError(t, err)
	require.Len(t, items, 1)
	target := items[0].(*copyItem)
	require.Empty(t, target.Targets())

	require.NoError(t, reporter.onResults([]acp.Result{{Job: target, Size: target.item.Size, Mode: 0o644}}))
	err = reporter.err()
	require.ErrorIs(t, err, mediapkg.ErrTargetNoSpace)
	require.ErrorContains(t, err, "large.bin")

	item := new(Item)
	require.NoError(t, runner.db.First(item).Error)
	require.Equal(t, entity.CopyStatus_COPY_STATUS_PENDING, item.Status)
	require.Empty(t, item.MediaPath)
}

func TestArchiveSourceStopsHandingOverItemsAfterCapacityBoundary(t *testing.T) {
	ctx := context.Background()
	exe := setupTestExecutor(t, executor.Scripts{})
	for _, name := range []string{"a.bin", "b.bin"} {
		require.NoError(t, os.WriteFile(filepath.Join(exe.Paths().Source, name), []byte(name), 0o644))
	}
	job := createArchiveJob(t, exe,
		&archiveTestSource{Base: exe.Paths().Source, Path: []string{"a.bin"}},
		&archiveTestSource{Base: exe.Paths().Source, Path: []string{"b.bin"}},
	)
	waitIndexed(t, exe, job.ID)
	value, err := exe.GetJobRunner(ctx, job.ID)
	require.NoError(t, err)
	runner := value.(*jobArchiveRunner)
	reporter := startTestReporter(t, runner)
	source := &copySource{runner: runner, session: new(capacityAfterOneSession), pageSize: batchSize}

	items, err := source.nextPage(ctx)
	require.NoError(t, err)
	require.Len(t, items, 2)
	first := items[0].(*copyItem)
	require.Equal(t, "a.bin", first.item.TargetPath)
	require.Equal(t, []string{"a.bin"}, first.Targets())

	// The second file does not fit: it is still handed over, so it receives one outcome and
	// its target failure surfaces ErrTargetNoSpace. Nothing is silently dropped.
	second := items[1].(*copyItem)
	require.Equal(t, "b.bin", second.item.TargetPath)
	require.Empty(t, second.Targets())

	content := []byte("a")
	require.NoError(t, reporter.onResults([]acp.Result{{
		Job: second, Size: int64(len(content)), Mode: 0o644,
	}}))
	failure := reporter.err()
	require.ErrorIs(t, failure, mediapkg.ErrTargetNoSpace)
	require.ErrorContains(t, failure, "b.bin")

	// The later PENDING suffix is never read.
	items, err = source.nextPage(ctx)
	require.ErrorIs(t, err, io.EOF)
	require.Empty(t, items)

	// The refused file and the unread suffix keep their PENDING rows for the next attempt.
	var pending []*Item
	require.NoError(t, runner.db.Order("target_path").Find(&pending).Error)
	require.Len(t, pending, 2)
	for _, item := range pending {
		require.Equal(t, entity.CopyStatus_COPY_STATUS_PENDING, item.Status)
		require.Empty(t, item.MediaPath)
		require.Nil(t, item.Result)
	}
}

func TestArchiveNoSpaceCheckpointUsesStableStructuredFields(t *testing.T) {
	output := new(bytes.Buffer)
	logger := logrus.New()
	logger.SetOutput(output)
	logger.SetFormatter(&logrus.TextFormatter{DisableColors: true, DisableTimestamp: true})
	runner := &jobArchiveRunner{logger: logger}
	terminationErr := fmt.Errorf("copy stopped: %w", mediapkg.ErrTargetNoSpace)

	runner.logArchiveMediaNoSpaceCheckpoint(context.Background(), 42, 3, 1024, terminationErr)
	line := output.String()
	for _, field := range []string{
		"event=archive_media_checkpoint", "reason=no_space", "media_id=42", "files=3", "bytes=1024",
		"error=\"copy stopped: Media target has no space\"",
	} {
		require.Contains(t, line, field)
	}

	output.Reset()
	runner.logArchiveMediaNoSpaceCheckpoint(context.Background(), 42, 3, 1024, errors.New("copy failed"))
	require.Empty(t, output.String())
}

func TestArchiveUnwrittenLogReflectsTargetResult(t *testing.T) {
	// A file this attempt did not write is reported with a stable diagnostic reason, classified
	// through error identity rather than a report row.
	tests := []struct {
		name string
		err  error
		want []string
	}{
		{name: "no space", err: fmt.Errorf("target refused: %w", acp.ErrTargetNoSpace),
			want: []string{"archive file not written", "reason=no_space"}},
		{name: "capacity boundary", err: fmt.Errorf("target refused: %w", mediapkg.ErrCapacityBoundary),
			want: []string{"archive file not written", "reason=no_space"}},
		{name: "ordinary failure", err: errors.New("copy failed"),
			want: []string{"archive file could not be processed", "copy failed"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			output := new(bytes.Buffer)
			logger := logrus.New()
			logger.SetOutput(output)
			logger.SetFormatter(&logrus.TextFormatter{DisableColors: true, DisableTimestamp: true})
			runner := &jobArchiveRunner{logger: logger}
			item := &Item{ID: 7, Size: 7, TargetPath: "file.txt", Data: &entity.ArchiveManifestFile{SourcePath: "/source/file"}}

			runner.logUnwritten(item, test.err)
			for _, want := range test.want {
				require.Contains(t, output.String(), want)
			}
		})
	}

	// A graceful stop abandons items without reporting them as findings.
	output := new(bytes.Buffer)
	logger := logrus.New()
	logger.SetOutput(output)
	logger.SetLevel(logrus.DebugLevel)
	logger.SetFormatter(&logrus.TextFormatter{DisableColors: true, DisableTimestamp: true})
	runner := &jobArchiveRunner{logger: logger}
	item := &Item{ID: 7, Size: 7, TargetPath: "file.txt", Data: &entity.ArchiveManifestFile{SourcePath: "/source/file"}}
	runner.logUnwritten(item, context.Canceled)
	require.Contains(t, output.String(), "archive copy stopped before file")
	require.NotContains(t, output.String(), "archive file not written")
}

func TestArchiveCompletedItemLogsOneTerminalOutcome(t *testing.T) {
	// Every accepted item reaches exactly one terminal outcome, and the runner records the
	// ones that could not be staged as a failed item rather than a copied one.
	output := new(bytes.Buffer)
	logger := logrus.New()
	logger.SetOutput(output)
	logger.SetFormatter(&logrus.TextFormatter{DisableColors: true, DisableTimestamp: true})
	runner := &jobArchiveRunner{logger: logger}
	reporter := startTestReporter(t, runner)
	item := &Item{ID: 7, Size: 7, TargetPath: "file.txt", Data: &entity.ArchiveManifestFile{SourcePath: "/source/file"}}

	// A refused target path reports the capacity boundary as the item's own outcome.
	failed := &copyItem{runner: runner, item: item, preparedErr: fmt.Errorf("no space, %w", mediapkg.ErrTargetNoSpace)}
	require.NoError(t, reporter.onResults([]acp.Result{{Job: failed, Size: 7}}))
	// An item abandoned by a graceful stop is counted as unprocessed, not as a result.
	require.NoError(t, reporter.onResults([]acp.Result{{
		Job: failed, Err: fmt.Errorf("abandoned, %w", mediapkg.ErrTargetNoSpace),
	}}))

	err := reporter.err()
	require.ErrorIs(t, err, mediapkg.ErrTargetNoSpace)
	flushed, failures := reporter.counters()
	require.Zero(t, flushed)
	require.Equal(t, int64(1), failures)
	require.Contains(t, output.String(), "reason=no_space")
}

func TestArchiveCopySettingsMapToACPOptions(t *testing.T) {
	// Every validated Job execution setting maps onto one ACP or result-writer option.
	settings := copySettingsFrom(&entity.JobExecutionSettings{
		ReadBatch: 4, ReadBufferMax: 16, WriteBufferMax: 8, WriteBatchSize: 4, FlushIntervalMs: 500,
	})
	require.Equal(t, 4, settings.page)
	require.Equal(t, 16, settings.readBuffer)
	require.Equal(t, 500*time.Millisecond, settings.resultFlushInterval)
	require.Equal(t, 8, settings.resultBuffer)
	require.Equal(t, 4, settings.resultBatch)
}
