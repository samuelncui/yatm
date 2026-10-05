package restore

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"io"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/samuelncui/acp"
	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/internal/executor"
	"github.com/samuelncui/yatm/internal/library"
	mediapkg "github.com/samuelncui/yatm/internal/media"
	previewpkg "github.com/samuelncui/yatm/internal/preview"
	"github.com/samuelncui/yatm/internal/resource"
	settingspkg "github.com/samuelncui/yatm/internal/settings"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func setupTestExecutor(t *testing.T) (*executor.Executor, *library.Library) {
	t.Helper()
	exe, lib, _ := setupTestExecutorWithLibraryDB(t)
	return exe, lib
}

func setupTestExecutorWithLibraryDB(t *testing.T) (*executor.Executor, *library.Library, *gorm.DB) {
	t.Helper()
	// Initialize isolated storage and an explicitly writable destination registration.
	root := t.TempDir()
	mainDB, err := resource.OpenSQLite(filepath.Join(root, "executor.db"))
	require.NoError(t, err)
	libraryDB, err := resource.OpenSQLite(filepath.Join(root, "library.db"))
	require.NoError(t, err)
	lib := library.NewWithSettings(libraryDB, testSettings(t, libraryDB))
	require.NoError(t, lib.AutoMigrate())
	volumeRoot := filepath.Join(root, "volumes")
	require.NoError(t, os.MkdirAll(volumeRoot, 0o755))
	exe := executor.New(mainDB, lib, []string{"/dev/nst0"}, executor.Paths{
		Work: filepath.Join(root, "work"), Source: filepath.Join(root, "source"),
		Target: filepath.Join(root, "target"), Volumes: []string{volumeRoot},
		Access: []executor.AccessRange{{Root: filepath.Join(root, "target")}},
	}, executor.Scripts{}, nil)
	require.NoError(t, exe.AutoMigrate())
	require.NoError(t, os.MkdirAll(exe.Paths().Target, 0o755))
	target, err := exe.LocationRoot(exe.Paths().Target)
	require.NoError(t, err)
	require.NoError(t, lib.CreateLocation(context.Background(), &library.Location{
		ID: 1, Name: "Restore", ExecutorID: "local", RootPath: target,
		RestoreTarget: true,
	}))
	return exe, lib, libraryDB
}

func testSettings(t *testing.T, db *gorm.DB) *settingspkg.Module {
	t.Helper()
	module := settingspkg.New(db, settingspkg.PreviewDefinition{
		Default:  func() (*entity.PreviewSettings, error) { return previewpkg.SettingsFromConfig(previewpkg.Config{}) },
		Validate: previewpkg.ValidateSettings,
	})
	return module
}

func createRestoreJob(t *testing.T, exe *executor.Executor, fileIDs ...int64) *executor.Job {
	t.Helper()
	// Resolve each legacy fixture File to its one explicit saved version.
	versionIDs := make([]int64, 0, len(fileIDs))
	for _, fileID := range fileIDs {
		versions, _, err := exe.Lib().ListFileVersions(context.Background(), fileID, 0, 100)
		require.NoError(t, err)
		require.Len(t, versions, 1)
		versionIDs = append(versionIDs, versions[0].ID)
	}
	// New Restore requests bind the registered target before the Job starts.
	reply, err := (&service{exe: exe}).Create(context.Background(), &entity.CreateRestoreJobRequest{
		Spec: &entity.RestoreJobSpec{FileVersionIds: versionIDs, Destination: &entity.RestoreDestination{LocationId: 1}},
	})
	require.NoError(t, err)
	return &executor.Job{ID: reply.Job.Id}
}

func waitIndexed(t *testing.T, exe *executor.Executor, id int64) {
	t.Helper()
	value, err := exe.GetJobRunner(context.Background(), id)
	require.NoError(t, err)
	runner := value.(*jobRestoreRunner)
	require.Eventually(t, func() bool {
		record := new(executor.JobRecord)
		err := runner.db.First(record, 1).Error
		return err == nil && record.Status == entity.JobStatus_JOB_STATUS_READY && !exe.IsRunning(id)
	}, 5*time.Second, 10*time.Millisecond)
}

func createMediaFile(
	t *testing.T,
	lib *library.Library,
	media *library.Media,
	_ string, mediaPath string,
	content []byte,
	storageOrder []byte,
) (*library.Media, *library.File) {
	t.Helper()
	hash := sha256.Sum256(content)
	stored, err := lib.CommitMedia(context.Background(), media, func(
		_ context.Context,
		yield func(*library.MediaFile) error,
	) error {
		return yield(&library.MediaFile{
			Path: mediaPath, Size: int64(len(content)), Mode: fs.FileMode(0o644),
			ModTime: time.Unix(1, 0), WriteTime: time.Unix(2, 0), Hash: hash[:],
			StorageOrder: storageOrder,
		})
	})
	require.NoError(t, err)
	positions, err := lib.ListMediaFilePositions(context.Background(), stored.ID, "", 100)
	require.NoError(t, err)
	ids := make([]int64, 0, len(positions))
	for _, position := range positions {
		ids = append(ids, position.ID)
	}
	_, err = lib.ImportArchivePositions(context.Background(), ids)
	require.NoError(t, err)
	directory := stored.Identity
	if stored.Kind == entity.MediaKind_MEDIA_KIND_VOLUME && stored.Name != "" {
		directory = stored.Name
	}
	file, err := lib.GetByPath(context.Background(), library.Root.ID, "Unforged/"+directory+"/"+mediaPath)
	require.NoError(t, err)
	require.NotNil(t, file)
	return stored, file
}

func newTapeMedia(identity string) *library.Media {
	return &library.Media{
		Kind: entity.MediaKind_MEDIA_KIND_TAPE, Identity: identity, Name: identity,
		Profile: (&entity.TapeMediaProfile{Format: library.TapeFormatLTFSV0}).Pack(), CreatedAtNS: time.Now().UnixNano(),
	}
}

type testReadSession struct {
	root         string
	capabilities mediapkg.Capabilities
	media        *library.Media
}

func (s *testReadSession) Capabilities() mediapkg.Capabilities { return s.capabilities }

func (s *testReadSession) Media() *mediapkg.Descriptor {
	if s.media == nil {
		return nil
	}
	var destroyed *time.Time
	if s.media.DestroyedAtNS != nil {
		stamp := time.Unix(0, *s.media.DestroyedAtNS)
		destroyed = &stamp
	}
	return &mediapkg.Descriptor{
		ID:            s.media.ID,
		Kind:          s.media.Kind,
		Identity:      s.media.Identity,
		Name:          s.media.Name,
		Profile:       s.media.Profile,
		CreateTime:    time.Unix(0, s.media.CreatedAtNS),
		DestroyTime:   destroyed,
		CapacityBytes: s.media.CapacityBytes,
		WrittenBytes:  s.media.WrittenBytes,
	}
}

func (s *testReadSession) SourcePath(relative string) (string, error) {
	return filepath.Join(s.root, filepath.FromSlash(relative)), nil
}

func (*testReadSession) Finalize(context.Context) error { return nil }

func TestRestoreManifestUsesMediaFields(t *testing.T) {
	exe, lib := setupTestExecutor(t)
	media, file := createMediaFile(t, lib, newTapeMedia("ABC001"), ".", "folder/file.txt", []byte("fixture"), []byte{1})
	job := createRestoreJob(t, exe, file.ID)
	waitIndexed(t, exe, job.ID)
	runnerValue, err := exe.GetJobRunner(context.Background(), job.ID)
	require.NoError(t, err)
	runner := runnerValue.(*jobRestoreRunner)

	mediaReply, err := runner.queryMedia(context.Background(), &entity.ListRestoreJobMediaRequest{})
	require.NoError(t, err)
	require.Len(t, mediaReply.Media, 1)
	require.Equal(t, media.ID, mediaReply.Media[0].MediaId)
	filesReply, err := runner.queryFiles(context.Background(), &entity.ListRestoreJobFilesRequest{MediaId: proto.Int64(media.ID)})
	require.NoError(t, err)
	require.Len(t, filesReply.Items, 1)
	require.Equal(t, media.ID, filesReply.Items[0].Candidate.MediaId)
	require.Equal(t, "folder/file.txt", filesReply.Items[0].Candidate.MediaPath)
}

func TestRestoreMediaQueryReportsMissingCatalogCandidate(t *testing.T) {
	// The frozen Job remains pending when its archive metadata is deliberately unregistered.
	ctx := context.Background()
	exe, lib := setupTestExecutor(t)
	media, file := createMediaFile(t, lib, newTapeMedia("MISSING"), ".", "file.txt", []byte("saved"), nil)
	job := createRestoreJob(t, exe, file.ID)
	waitIndexed(t, exe, job.ID)
	_, err := lib.DeleteMedia(ctx, false, media.ID)
	require.NoError(t, err)
	value, err := exe.GetJobRunner(ctx, job.ID)
	require.NoError(t, err)
	runner := value.(*jobRestoreRunner)

	// Missing metadata must surface as an actionable failure, never an empty candidate page.
	_, err = runner.queryMedia(ctx, &entity.ListRestoreJobMediaRequest{})
	require.ErrorContains(t, err, "Restore Media is no longer in Library")
	var pending int64
	require.NoError(t, runner.db.Model(&Copy{}).Where("status = ?", entity.CopyStatus_COPY_STATUS_PENDING).Count(&pending).Error)
	require.EqualValues(t, 1, pending)
}

func TestRestoreSourceOrdersOnlySequentialMediaByStorageOrder(t *testing.T) {
	// Publish three logical files with realistic LTFS partition, block, and offset orders.
	exe, lib := setupTestExecutor(t)
	firstOrder := testLTFSStorageOrder('b', 3, 0)
	secondOrder := testLTFSStorageOrder('a', 20, 4)
	thirdOrder := testLTFSStorageOrder('a', 10, 8)
	media, first := createMediaFile(t, lib, newTapeMedia("ABC001"), ".", "a.txt", []byte("first"), firstOrder)
	_, second := createMediaFile(t, lib, newTapeMedia("ABC002"), ".", "b.txt", []byte("second"), secondOrder)
	_, third := createMediaFile(t, lib, newTapeMedia("ABC003"), ".", "c.txt", []byte("third"), thirdOrder)
	job := createRestoreJob(t, exe, first.ID, second.ID, third.ID)
	waitIndexed(t, exe, job.ID)
	runnerValue, err := exe.GetJobRunner(context.Background(), job.ID)
	require.NoError(t, err)
	runner := runnerValue.(*jobRestoreRunner)

	// Confirm Restore manifest indexing copied each Library Position order without alteration.
	copies := make(map[int64]*Copy, 3)
	for fileID, wantOrder := range map[int64][]byte{
		first.ID: firstOrder, second.ID: secondOrder, third.ID: thirdOrder,
	} {
		copy := new(Copy)
		require.NoError(t, runner.db.Where("item_id IN (?)", runner.db.Model(&File{}).Select("item_id").Where("file_id = ?", fileID)).First(copy).Error)
		require.Equal(t, wantOrder, copy.StorageOrder)
		copies[fileID] = copy
	}

	// Put the candidates on one test Media and make path order conflict with physical order.
	require.NoError(t, runner.db.Model(&Copy{}).Where("item_id IN (?)", runner.db.Model(&File{}).Select("item_id").Where("file_id = ?", first.ID)).Updates(map[string]any{
		"media_id": media.ID, "media_path": "a.txt",
	}).Error)
	require.NoError(t, runner.db.Model(&Copy{}).Where("item_id IN (?)", runner.db.Model(&File{}).Select("item_id").Where("file_id = ?", second.ID)).Updates(map[string]any{
		"media_id": media.ID, "media_path": "z.txt",
	}).Error)
	require.NoError(t, runner.db.Model(&Copy{}).Where("item_id IN (?)", runner.db.Model(&File{}).Select("item_id").Where("file_id = ?", third.ID)).Updates(map[string]any{
		"media_id": media.ID, "media_path": "m.txt",
	}).Error)

	// Sequential Tape reads follow partition, block, and offset order.
	sequential := newTestCopyItems(runner, media.ID, mediapkg.Capabilities{Read: mediapkg.AccessSequential})
	requireCopySequence(t, sequential, copies[third.ID].ID, copies[second.ID].ID, copies[first.ID].ID)

	// Random-access Media reads retain ordinary path order.
	random := newTestCopyItems(runner, media.ID, mediapkg.Capabilities{Read: mediapkg.AccessRandom})
	requireCopySequence(t, random, copies[first.ID].ID, copies[third.ID].ID, copies[second.ID].ID)
}

// newTestCopyItems builds a manifest pager over one test Media.
func newTestCopyItems(runner *jobRestoreRunner, mediaID int64, capabilities mediapkg.Capabilities) *copyItems {
	return &copyItems{
		runner: runner, mediaID: mediaID,
		session: &testReadSession{root: "/media", capabilities: capabilities},
		batch:   batchSize,
	}
}

func testLTFSStorageOrder(partition byte, block, offset uint64) []byte {
	order := make([]byte, 17)
	order[0] = partition
	binary.BigEndian.PutUint64(order[1:9], block)
	binary.BigEndian.PutUint64(order[9:17], offset)
	return order
}

func requireCopySequence(t *testing.T, source *copyItems, wantIDs ...int64) {
	t.Helper()
	ids := make([]int64, 0, len(wantIDs))
	for {
		items, err := source.nextPage(context.Background())
		if err != nil {
			require.ErrorIs(t, err, io.EOF)
			break
		}
		for _, item := range items {
			job, ok := item.(*copyJob)
			require.True(t, ok)
			ids = append(ids, job.copy.ID)
		}
	}
	require.Equal(t, wantIDs, ids)
}

func TestRestoreCompletionUsesSessionSourcePath(t *testing.T) {
	// Build a one-copy Restore whose completion also exhausts the pending set.
	exe, lib := setupTestExecutor(t)
	content := []byte("fixture")
	media, file := createMediaFile(t, lib, newTapeMedia("ABC001"), ".", "folder/file.txt", content, []byte{1})
	job := createRestoreJob(t, exe, file.ID)
	waitIndexed(t, exe, job.ID)
	runnerValue, err := exe.GetJobRunner(context.Background(), job.ID)
	require.NoError(t, err)
	runner := runnerValue.(*jobRestoreRunner)
	var copy Copy
	require.NoError(t, runner.db.First(&copy).Error)
	candidate := loadTestCopyCandidate(t, runner, &copy)
	session := &testReadSession{root: "/mounted", capabilities: mediapkg.Capabilities{Read: mediapkg.AccessSequential}}
	source, err := session.SourcePath(candidate.MediaPath)
	require.NoError(t, err)
	target := runner.restoreTarget(candidate.TargetPath)
	require.NoError(t, os.MkdirAll(filepath.Dir(target), 0755))
	require.NoError(t, os.WriteFile(target, content, 0644))
	output := new(bytes.Buffer)
	runner.db = runner.db.Session(&gorm.Session{Logger: logger.New(
		log.New(output, "", 0), logger.Config{LogLevel: logger.Info},
	)})

	// Completing the last copy uses the Session path and treats the empty pending query as normal.
	require.NoError(t, completeTestCopy(t, runner, media.ID, session, candidate, source, target, file.Hash, content))
	require.NoError(t, runner.finalizeOutputs(context.Background(), media.ID))
	stored, err := exe.GetJob(context.Background(), job.ID)
	require.NoError(t, err)
	require.Equal(t, entity.JobStatus_JOB_STATUS_COMPLETED, stored.Status)
	require.False(t, strings.Contains(strings.ToLower(output.String()), "record not found"), output.String())
}

// completeTestCopy reports one successful ACP completion through the attempt's results callback,
// drains the writer, and returns the attempt's terminal error.
func completeTestCopy(
	t *testing.T,
	runner *jobRestoreRunner,
	mediaID int64,
	session mediapkg.ReadSession,
	copy *copyCandidate,
	source string,
	target string,
	hash []byte,
	content []byte,
) error {
	t.Helper()
	buffer := startTestBuffer(t, runner, mediaID, session)
	job := &copyJob{source: source, target: target, copy: copy}
	if err := buffer.onResults([]acp.Result{{
		Job: job, Size: int64(len(content)), Mode: 0o644, SHA256: hash,
		Targets: []acp.TargetResult{{Path: target, Size: int64(len(content))}},
	}}); err != nil {
		return err
	}
	return buffer.Close()
}

func TestRestoreVolumeEndToEnd(t *testing.T) {
	// Create a mounted Volume and publish one physical file into the Library.
	exe, lib := setupTestExecutor(t)
	root := filepath.Join(exe.Paths().Volumes[0], "fixture")
	require.NoError(t, os.MkdirAll(filepath.Join(root, "batch"), 0o755))
	volume, err := mediapkg.InitializeVolume(root, &entity.VolumeMediaProfile{
		SerialNumber: "serial", Type: entity.VolumeType_VOLUME_TYPE_HDD,
	})
	require.NoError(t, err)
	content := []byte("restored from Volume")
	require.NoError(t, os.WriteFile(filepath.Join(root, "batch", "file.txt"), content, 0o644))
	media, file := createMediaFile(t, lib, &library.Media{
		Kind: entity.MediaKind_MEDIA_KIND_VOLUME, Identity: volume.Marker.UUID, Name: "fixture",
		Profile: volume.Marker.Profile.Pack(), CreatedAtNS: volume.Marker.CreatedAtNS,
	}, "batch", "batch/file.txt", content, nil)

	// Restore the file through the Volume Backend and wait for the durable checkpoint.
	job := createRestoreJob(t, exe, file.ID)
	waitIndexed(t, exe, job.ID)
	runnerValue, err := exe.GetJobRunner(context.Background(), job.ID)
	require.NoError(t, err)
	runner := runnerValue.(*jobRestoreRunner)
	_, err = (&service{exe: exe}).RestoreMedia(context.Background(), &entity.RestoreMediaRequest{
		Id:     job.ID,
		Target: (&entity.ReadVolumeTarget{Uuid: volume.Marker.UUID}).Pack(),
	})
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		record := new(executor.JobRecord)
		err := runner.db.First(record, 1).Error
		return err == nil && record.Status == entity.JobStatus_JOB_STATUS_COMPLETED && !exe.IsRunning(job.ID)
	}, 5*time.Second, 10*time.Millisecond)

	// Verify the transferred bytes and the source's reusable signature cache.
	var restoredCopy Copy
	require.NoError(t, runner.db.First(&restoredCopy).Error)
	restoredCandidate := loadTestCopyCandidate(t, runner, &restoredCopy)
	restored, err := os.ReadFile(runner.restoreTarget(restoredCandidate.TargetPath))
	require.NoError(t, err)
	require.Equal(t, content, restored)
	wantHash := sha256.Sum256(content)
	signature, valid, err := acp.ReadCachedSignature(filepath.Join(root, "batch", "file.txt"))
	require.NoError(t, err)
	require.True(t, valid)
	require.Equal(t, wantHash, signature.SHA256)

	// Own-version metadata takes precedence over a shared copy's disposable cache snapshot.
	restoredInfo, err := os.Stat(runner.restoreTarget(restoredCandidate.TargetPath))
	require.NoError(t, err)
	require.Equal(t, restoredCandidate.MtimeNS, restoredInfo.ModTime().UnixNano())
	require.Equal(t, fs.FileMode(restoredCandidate.Mode).Perm(), restoredInfo.Mode().Perm())
	_, valid, err = acp.ReadCachedSignature(runner.restoreTarget(restoredCandidate.TargetPath))
	require.NoError(t, err)
	require.False(t, valid, "a cache from the physical copy must not survive different version metadata")

	// Confirm completion without sequential-read storage metadata.
	stored, err := exe.GetJob(context.Background(), job.ID)
	require.NoError(t, err)
	require.Equal(t, entity.JobStatus_JOB_STATUS_COMPLETED, stored.Status)
	positions, err := lib.ListPositions(context.Background(), media.ID, "")
	require.NoError(t, err)
	require.Empty(t, positions[0].StorageOrder)
}

func TestRestoreSourceReturnsEOF(t *testing.T) {
	db, err := resource.OpenSQLite(filepath.Join(t.TempDir(), "state.db"))
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&Copy{}))
	source := &copyItems{
		runner: &jobRestoreRunner{db: db}, mediaID: 1,
		session: &testReadSession{capabilities: mediapkg.Capabilities{Read: mediapkg.AccessConcurrentRandom}},
		batch:   batchSize,
	}
	items, err := source.nextPage(context.Background())
	require.ErrorIs(t, err, io.EOF)
	require.Empty(t, items)
}

func loadTestCopyCandidate(t *testing.T, runner *jobRestoreRunner, copy *Copy) *copyCandidate {
	t.Helper()
	candidates, err := runner.copyCandidates(context.Background(), []*Copy{copy})
	require.NoError(t, err)
	require.Len(t, candidates, 1)
	return candidates[0]
}

func loadTestCopyCandidates(t *testing.T, runner *jobRestoreRunner, copies []Copy) []*copyCandidate {
	t.Helper()
	rows := make([]*Copy, len(copies))
	for i := range copies {
		rows[i] = &copies[i]
	}
	candidates, err := runner.copyCandidates(context.Background(), rows)
	require.NoError(t, err)
	return candidates
}
