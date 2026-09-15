package archive

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/samuelncui/acp"
	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/executor"
	scanjob "github.com/samuelncui/yatm/executor/scan"
	"github.com/samuelncui/yatm/library"
	mediapkg "github.com/samuelncui/yatm/media"
	"github.com/samuelncui/yatm/resource"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

type capacityBoundarySession struct{}

type capacityAfterOneSession struct {
	requests int
}

type archivePreviewer struct{}

func (archivePreviewer) Supports(string) bool { return true }

func (archivePreviewer) Generate(context.Context, string, []byte, int64, int64, bool) ([]byte, error) {
	return []byte{1}, nil
}

func (archivePreviewer) Manifest([]byte) (*entity.PreviewManifest, error) { return nil, os.ErrNotExist }

func (archivePreviewer) Open([]byte, string) (io.ReadCloser, error) { return nil, os.ErrNotExist }

func (capacityBoundarySession) Capabilities() mediapkg.Capabilities { return mediapkg.Capabilities{} }

func (capacityBoundarySession) Inspect() *library.Media { return new(library.Media) }

func (capacityBoundarySession) TargetPath(string) (string, error) {
	return "", mediapkg.ErrCapacityBoundary
}

func (capacityBoundarySession) Finalize(context.Context, error) error { return nil }

func (*capacityAfterOneSession) Capabilities() mediapkg.Capabilities {
	return mediapkg.Capabilities{}
}

func (*capacityAfterOneSession) Inspect() *library.Media { return new(library.Media) }

func (s *capacityAfterOneSession) TargetPath(path string) (string, error) {
	s.requests++
	if s.requests > 1 {
		return "", mediapkg.ErrCapacityBoundary
	}
	return path, nil
}

func (*capacityAfterOneSession) Finalize(context.Context, error) error { return nil }

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
	lib := library.New(libraryDB)
	require.NoError(t, lib.AutoMigrate())

	// Initialize the complete storage layout and optional Preview dependency.
	exe := executor.New(mainDB, lib, []string{"/dev/nst0"}, executor.Paths{
		Work: filepath.Join(root, "work"), Source: filepath.Join(root, "source"),
		Target: filepath.Join(root, "target"), Volumes: []string{filepath.Join(root, "volumes")},
	}, scripts, previews)
	require.NoError(t, exe.AutoMigrate())
	require.NoError(t, os.MkdirAll(exe.Paths().Source, 0o755))
	require.NoError(t, os.MkdirAll(exe.Paths().Volumes[0], 0o755))
	return exe
}

func createArchiveJob(t *testing.T, exe *executor.Executor, sources ...*entity.Source) *executor.Job {
	t.Helper()
	// Exercise retained legacy manifest execution without exposing raw paths in new public requests.
	job, err := exe.CreateJob(context.Background(), entity.JobKind_ARCHIVE, 0, func(db *gorm.DB) error {
		// Build the historical manifest bundle before asynchronous indexing starts.
		if err := db.AutoMigrate(&Config{}, &Item{}, &RawInput{}); err != nil {
			return err
		}
		return db.Create(&Config{ID: 1, Spec: &entity.ArchiveJobSpec{Sources: sources}}).Error
	})
	require.NoError(t, err)
	return job
}

func TestArchiveForceRehashRequiresPreview(t *testing.T) {
	// A Preview-only hashing policy must not be accepted without a companion Job.
	exe := setupTestExecutor(t, executor.Scripts{})
	_, err := (&service{exe: exe}).Create(context.Background(), &entity.CreateArchiveJobRequest{
		Spec: &entity.ArchiveJobSpec{Sources: []*entity.Source{{
			Base: exe.Paths().Source, Path: []string{"file.txt"},
		}}},
		ForceRehash: true,
	})
	require.ErrorContains(t, err, "requires Preview generation")
}

func TestArchiveRejectsUnknownPreviewPolicy(t *testing.T) {
	// Reject unknown generation policies before creating a companion or touching source files.
	exe := setupTestExecutor(t, executor.Scripts{})
	_, err := (&service{exe: exe}).Create(context.Background(), &entity.CreateArchiveJobRequest{
		Spec: &entity.ArchiveJobSpec{Sources: []*entity.Source{{
			Base: exe.Paths().Source, Path: []string{"file.txt"},
		}}},
		PreviewPolicy: entity.PreviewPolicy(99),
	})
	require.ErrorContains(t, err, "invalid Preview policy")
}

func TestArchivePreviewPoliciesPropagateToCompanionPreview(t *testing.T) {
	// Create an Archive with both companion Preview policies enabled.
	exe := setupTestExecutorWithPreviewer(t, executor.Scripts{}, archivePreviewer{})
	_, file, _ := publishArchiveOriginal(t, exe, "preview", []byte("preview source"))
	reply, err := (&service{exe: exe}).Create(context.Background(), &entity.CreateArchiveJobRequest{
		Spec:          &entity.ArchiveJobSpec{FileIds: []int64{file.ID}},
		PreviewPolicy: entity.PreviewPolicy_PREVIEW_REGENERATE_ALL,
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
	require.Equal(t, entity.ScanSignaturePolicy_FORCE_READ, config.Spec.SignaturePolicy)
	require.Equal(t, entity.PreviewPolicy_PREVIEW_REGENERATE_ALL, config.Spec.PreviewPolicy)
	require.True(t, config.IndexedInput)
	require.Empty(t, config.Spec.Selections, "the companion must not re-admit Archive selections")
	var record executor.JobRecord
	require.NoError(t, db.First(&record, 1).Error)
	require.Equal(t, entity.JobKind_SCAN, record.Kind)
	require.Equal(t, entity.JobStatus_COMPLETED, record.Status)
}

func TestArchiveCompanionFailurePreservesPreparedArchive(t *testing.T) {
	// A missing Preview generator must not prevent the selected Archive from being prepared.
	exe := setupTestExecutor(t, executor.Scripts{})
	_, file, _ := publishArchiveOriginal(t, exe, "without-previewer", []byte("archive content"))
	api := &service{exe: exe}
	reply, err := api.Create(context.Background(), &entity.CreateArchiveJobRequest{
		Spec: &entity.ArchiveJobSpec{FileIds: []int64{file.ID}}, PreviewPolicy: entity.PreviewPolicy_PREVIEW_MISSING_ONLY,
	})
	require.NoError(t, err)
	job := waitIndexed(t, exe, reply.Job.Id)
	require.Equal(t, entity.JobStatus_PENDING, job.Status)
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
		return err == nil && record.Status == entity.JobStatus_PENDING && !exe.IsRunning(id)
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
	job := createArchiveJob(t, exe, &entity.Source{Base: exe.Paths().Source, Path: []string{"file.txt"}})
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
		&entity.Source{Base: exe.Paths().Source, Path: []string{"z.txt"}},
		&entity.Source{Base: exe.Paths().Source, Path: []string{"a.txt"}},
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
		return err == nil && record.Status == entity.JobStatus_COMPLETED && !exe.IsRunning(job.ID)
	}, 15*time.Second, 10*time.Millisecond)
	stored, err := exe.GetJob(ctx, job.ID)
	require.NoError(t, err)
	require.Equal(t, entity.JobStatus_COMPLETED, stored.Status)

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
		Profile: volume.Marker.Profile.Pack(), CreateTime: volume.Marker.CreatedAt,
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
		&entity.Source{Base: exe.Paths().Source, Path: []string{"a.txt"}},
		&entity.Source{Base: exe.Paths().Source, Path: []string{"folder"}},
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
		return err == nil && record.Status == entity.JobStatus_COMPLETED && !exe.IsRunning(job.ID)
	}, 15*time.Second, 10*time.Millisecond)

	// Verify copied bytes, source and target caches, and absence of sequential-read metadata.
	reply, err := value.(*jobArchiveRunner).queryFiles(ctx, &entity.ListArchiveJobFilesRequest{})
	require.NoError(t, err)
	require.Len(t, reply.Items, 2)
	for _, item := range reply.Items {
		require.Equal(t, entity.CopyStatus_SUBMITTED, item.Status)
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
		require.Zero(t, position.CheckedAt, "writing is not an independent read-back check")
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
	job := createArchiveJob(t, exe, &entity.Source{Base: exe.Paths().Source, Path: []string{"file.txt"}})
	waitIndexed(t, exe, job.ID)
	value, err := exe.GetJobRunner(ctx, job.ID)
	require.NoError(t, err)
	runner := value.(*jobArchiveRunner)
	item := new(Item)
	require.NoError(t, runner.db.First(item).Error)

	failed := &acp.StreamResult{ID: item.ID, Job: &acp.Job{
		FullPath: item.Data.SourcePath, Status: acp.JobStatusFinished,
		FailTargets: map[string]error{"target": acp.ErrTargetNoSpace},
	}}
	require.NoError(t, runner.stageCopyResult(ctx, failed))
	require.NoError(t, runner.db.First(item, item.ID).Error)
	require.Equal(t, entity.CopyStatus_PENDING, item.Status)
	require.Empty(t, item.MediaPath)

	hash := sha256.Sum256(content)
	success := &acp.StreamResult{ID: item.ID, Job: &acp.Job{
		FullPath: item.Data.SourcePath, Status: acp.JobStatusFinished,
		SuccessTargets: []string{"target"}, Size: int64(len(content)), Mode: 0o644,
		SHA256: fmt.Sprintf("%x", hash[:]),
	}}
	require.NoError(t, runner.stageCopyResult(ctx, success))
	require.NoError(t, runner.db.First(item, item.ID).Error)
	require.Equal(t, entity.CopyStatus_STAGED, item.Status)
	require.Equal(t, item.TargetPath, item.MediaPath)
}

func TestArchiveSourceRejectsFirstFileBeyondMediaCapacity(t *testing.T) {
	ctx := context.Background()
	exe := setupTestExecutor(t, executor.Scripts{})
	sourcePath := filepath.Join(exe.Paths().Source, "large.bin")
	require.NoError(t, os.WriteFile(sourcePath, []byte("fixture"), 0o644))
	job := createArchiveJob(t, exe, &entity.Source{Base: exe.Paths().Source, Path: []string{"large.bin"}})
	waitIndexed(t, exe, job.ID)
	value, err := exe.GetJobRunner(ctx, job.ID)
	require.NoError(t, err)

	_, err = (&copySource{
		runner: value.(*jobArchiveRunner), session: capacityBoundarySession{},
	}).Next(ctx)
	require.Error(t, err)
	require.True(t, errors.Is(err, mediapkg.ErrCapacityBoundary))
	require.ErrorContains(t, err, "large.bin")
}

func TestArchiveSourceReportsCapacityAfterCompletedPrefix(t *testing.T) {
	ctx := context.Background()
	exe := setupTestExecutor(t, executor.Scripts{})
	for _, name := range []string{"a.bin", "b.bin"} {
		require.NoError(t, os.WriteFile(filepath.Join(exe.Paths().Source, name), []byte(name), 0o644))
	}
	job := createArchiveJob(t, exe,
		&entity.Source{Base: exe.Paths().Source, Path: []string{"a.bin"}},
		&entity.Source{Base: exe.Paths().Source, Path: []string{"b.bin"}},
	)
	waitIndexed(t, exe, job.ID)
	value, err := exe.GetJobRunner(ctx, job.ID)
	require.NoError(t, err)
	source := &copySource{runner: value.(*jobArchiveRunner), session: new(capacityAfterOneSession)}

	request, err := source.Next(ctx)
	require.NoError(t, err)
	require.NotNil(t, request)
	_, err = source.Next(ctx)
	require.ErrorIs(t, err, mediapkg.ErrCapacityBoundary)
	require.NotErrorIs(t, err, io.EOF)
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

func TestArchiveCompletionLogReflectsTargetResult(t *testing.T) {
	tests := []struct {
		name       string
		job        *acp.Job
		want       string
		wantFinish bool
	}{
		{
			name: "one successful target",
			job: &acp.Job{
				FullPath: "/source/file", Size: 7, Status: acp.JobStatusFinished,
				SuccessTargets: []string{"/target/file"},
			},
			wantFinish: true,
		},
		{
			name: "no space",
			job: &acp.Job{
				FullPath: "/source/file", Size: 7, Status: acp.JobStatusFinished,
				FailTargets: map[string]error{"/target/file": acp.ErrTargetNoSpace},
			},
			want: "reason=no_space",
		},
		{
			name: "ordinary failure",
			job: &acp.Job{
				FullPath: "/source/file", Size: 7, Status: acp.JobStatusFinished,
				FailTargets: map[string]error{"/target/file": errors.New("copy failed")},
			},
			want: "reason=failed",
		},
		{
			name: "skipped",
			job: &acp.Job{
				FullPath: "/source/file", Size: 7, Status: acp.JobStatusFinished,
			},
			want: "reason=skipped",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			// Capture one finished ACP result through the production event handler.
			output := new(bytes.Buffer)
			logger := logrus.New()
			logger.SetOutput(output)
			logger.SetFormatter(&logrus.TextFormatter{DisableColors: true, DisableTimestamp: true})
			runner := &jobArchiveRunner{logger: logger}
			runner.archiveEventHandler(context.Background())(&acp.EventUpdateJob{Job: test.job})

			// A success message is reserved for an unambiguous single-target success.
			line := output.String()
			if test.wantFinish {
				require.Contains(t, line, "archive file finished")
				require.NotContains(t, line, "archive file not written")
				return
			}
			require.NotContains(t, line, "archive file finished")
			require.Contains(t, line, "archive file not written")
			require.Contains(t, line, test.want)
		})
	}
}
