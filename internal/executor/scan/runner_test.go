package scan

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
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
	"gorm.io/gorm"
)

func TestFailedScanCannotRunOrRetargetReusedMediaID(t *testing.T) {
	// Freeze a registered Volume in a Job whose first attempt cannot find the physical disk.
	ctx := context.Background()
	exe, original, media := setupScanExecutor(t)
	var backup bytes.Buffer
	require.NoError(t, exe.Lib().Export(ctx, &backup, []entity.LibraryEntityType{entity.LibraryEntityType_LIBRARY_ENTITY_TYPE_MEDIA}))
	require.NoError(t, os.Rename(original.Root, filepath.Join(t.TempDir(), "unmounted")))
	job := createScanJob(t, exe, media.ID, false)
	failed := waitScanStatus(t, exe, job.ID, entity.JobStatus_JOB_STATUS_FAILED)
	// Frozen expectations remain diagnostics; they do not permit another Scan execution.
	require.Equal(t, entity.JobStatus_JOB_STATUS_PREPARING, failed.Checkpoint)
	require.Equal(t, entity.JobPhase_JOB_PHASE_UNSPECIFIED, failed.Phase)

	// A valid import may reuse a numeric catalog ID, but cannot retarget this existing Job.
	require.NoError(t, os.Mkdir(original.Root, 0755))
	replacement, err := mediapkg.InitializeVolume(original.Root, media.Profile.GetVolume())
	require.NoError(t, err)
	imported := bytes.ReplaceAll(backup.Bytes(), []byte(original.Marker.UUID), []byte(replacement.Marker.UUID))
	require.NoError(t, exe.Lib().Import(ctx, bytes.NewReader(imported), false))
	stored, err := exe.Lib().GetMedia(ctx, media.ID)
	require.NoError(t, err)
	require.Equal(t, replacement.Marker.UUID, stored.Identity)

	// A failed Job cannot start again, and its retained expectation cannot adopt the replacement Media.
	_, err = (&service{exe: exe}).ReadMedia(ctx, &entity.ReadScanMediaRequest{
		Id:     job.ID,
		Target: (&entity.ReadVolumeTarget{Uuid: replacement.Marker.UUID}).Pack(),
	})
	require.ErrorContains(t, err, "cannot start from status JOB_STATUS_FAILED")
	value, err := exe.GetJobRunner(ctx, job.ID)
	require.NoError(t, err)
	r := value.(*runner)
	var config Config
	require.NoError(t, r.db.First(&config, 1).Error)
	_, err = r.validateMedia(ctx, &config)
	require.ErrorContains(t, err, "Media identity changed")
}

func setupScanExecutor(t *testing.T) (*executor.Executor, *mediapkg.Volume, *library.Media) {
	t.Helper()
	root := t.TempDir()
	executorDB, err := resource.OpenSQLite(filepath.Join(root, "executor.db"))
	require.NoError(t, err)
	libraryDB, err := resource.OpenSQLite(filepath.Join(root, "library.db"))
	require.NoError(t, err)
	lib := library.NewWithSettings(libraryDB, testSettings(t, libraryDB))
	require.NoError(t, lib.AutoMigrate())

	volumesRoot := filepath.Join(root, "volumes")
	volumeRoot := filepath.Join(volumesRoot, "fixture")
	require.NoError(t, os.MkdirAll(volumeRoot, 0o755))
	profile := &entity.VolumeMediaProfile{Type: entity.VolumeType_VOLUME_TYPE_HDD}
	volume, err := mediapkg.InitializeVolume(volumeRoot, profile)
	require.NoError(t, err)
	media, err := lib.CreateMedia(context.Background(), &library.Media{
		Kind: entity.MediaKind_MEDIA_KIND_VOLUME, Identity: volume.Marker.UUID, Name: "fixture",
		Profile: profile.Pack(), CreatedAtNS: volume.Marker.CreatedAtNS,
	})
	require.NoError(t, err)

	exe := executor.New(executorDB, lib, nil, executor.Paths{
		Work: filepath.Join(root, "work"), Volumes: []string{volumesRoot},
	}, executor.Scripts{}, nil)
	require.NoError(t, exe.AutoMigrate())
	return exe, volume, media
}

func testSettings(t *testing.T, db *gorm.DB) *settingspkg.Module {
	t.Helper()
	module := settingspkg.New(db, settingspkg.PreviewDefinition{
		Default:  func() (*entity.PreviewSettings, error) { return previewpkg.SettingsFromConfig(previewpkg.Config{}) },
		Validate: previewpkg.ValidateSettings,
	})
	return module
}

func commitScanPositions(
	t *testing.T,
	exe *executor.Executor,
	media *library.Media,
	files ...*library.MediaFile,
) {
	t.Helper()
	_, err := exe.Lib().CommitMedia(context.Background(), media, func(
		_ context.Context,
		yield func(*library.MediaFile) error,
	) error {
		for _, file := range files {
			if err := yield(file); err != nil {
				return err
			}
		}
		return nil
	})
	require.NoError(t, err)
}

func writeScanFile(t *testing.T, root, name string, data []byte, mode fs.FileMode, modified time.Time) {
	t.Helper()
	filename := filepath.Join(root, filepath.FromSlash(name))
	require.NoError(t, os.MkdirAll(filepath.Dir(filename), 0o755))
	require.NoError(t, os.WriteFile(filename, data, mode))
	require.NoError(t, os.Chmod(filename, mode))
	require.NoError(t, os.Chtimes(filename, modified, modified))
}

func createScanJob(t *testing.T, exe *executor.Executor, mediaID int64, forceRehash bool) *executor.Job {
	t.Helper()
	policy := entity.ScanSignaturePolicy_SCAN_SIGNATURE_POLICY_FILL_MISSING
	if forceRehash {
		policy = entity.ScanSignaturePolicy_SCAN_SIGNATURE_POLICY_FORCE_READ
	}
	reply, err := (&service{exe: exe}).Create(context.Background(), &entity.CreateScanJobRequest{
		Spec: &entity.ScanJobSpec{MediaId: mediaID, SignaturePolicy: policy, ResultPolicy: entity.ScanResultPolicy_SCAN_RESULT_POLICY_PUBLISH_INVENTORY},
	})
	require.NoError(t, err)
	return &executor.Job{ID: reply.Job.Id}
}

func waitScanStatus(t *testing.T, exe *executor.Executor, id int64, status entity.JobStatus) *executor.Job {
	t.Helper()
	value, err := exe.GetJobRunner(context.Background(), id)
	require.NoError(t, err)
	runner := value.(*runner)
	require.Eventually(t, func() bool {
		record := new(executor.JobRecord)
		err := runner.db.First(record, 1).Error
		return err == nil && record.Status == status && !exe.IsRunning(id)
	}, 15*time.Second, 10*time.Millisecond)
	job, err := exe.GetJob(context.Background(), id)
	require.NoError(t, err)
	return job
}

func listScanEntries(t *testing.T, exe *executor.Executor, id int64) []*entity.ScanEntry {
	t.Helper()
	reply, err := (&service{exe: exe}).ListEntries(context.Background(), &entity.ListScanJobEntriesRequest{
		Id: id, Limit: executor.MaxJobResultPageSize,
	})
	require.NoError(t, err)
	require.False(t, reply.HasMore)
	var changes []*entity.ScanEntry
	for _, entry := range reply.Entries {
		if entry.Change != entity.ScanChange_SCAN_CHANGE_UNCHANGED {
			changes = append(changes, entry)
		}
	}
	sort.Slice(changes, func(i, j int) bool { return changes[i].Path < changes[j].Path })
	return changes
}

func TestScanManifestPagesAndInventoryGlobalPathOrder(t *testing.T) {
	ctx := context.Background()
	exe, volume, media := setupScanExecutor(t)
	paths := []string{"a/x", "a.txt", "a-/x", "a0/x", "z"}
	for index := 0; index < batchSize+17; index++ {
		paths = append(paths, fmt.Sprintf("file-%04d", index))
	}
	for index := len(paths) - 1; index >= 0; index-- {
		writeScanFile(t, volume.Root, paths[index], nil, 0644, time.Unix(1, 0))
	}
	job := createScanJob(t, exe, media.ID, false)
	waitScanStatus(t, exe, job.ID, entity.JobStatus_JOB_STATUS_COMPLETED)
	var after string
	var observed []string
	for {
		page, err := (&service{exe: exe}).ListEntries(ctx, &entity.ListScanJobEntriesRequest{Id: job.ID, Limit: 17, Cursor: after})
		require.NoError(t, err)
		for _, entry := range page.Entries {
			require.Greater(t, entry.Id, int64(0))
			after = strconv.FormatInt(entry.Id, 10)
			observed = append(observed, entry.Path)
		}
		if !page.HasMore {
			break
		}
	}
	sort.Strings(paths)
	sort.Strings(observed)
	require.Equal(t, paths, observed)
	positions, err := exe.Lib().ListMediaFilePositions(ctx, media.ID, "", 1000)
	require.NoError(t, err)
	var published []string
	for _, position := range positions {
		published = append(published, position.Path)
	}
	require.Equal(t, paths, published)
}

func TestCachedScanPublishesAutomatically(t *testing.T) {
	// Establish Library positions and physical added, changed, removed, and unchanged paths.
	ctx := context.Background()
	exe, volume, media := setupScanExecutor(t)
	modified := time.Unix(10, 0)
	oldChanged := sha256.Sum256([]byte("old-changed"))
	removed := sha256.Sum256([]byte("removed"))
	stable := sha256.Sum256([]byte("stable"))
	commitScanPositions(t, exe, media,
		&library.MediaFile{Path: "changed.txt", Size: 11, Mode: 0o644, ModTime: modified, Hash: oldChanged[:]},
		&library.MediaFile{Path: "removed.txt", Size: 7, Mode: 0o644, ModTime: modified, Hash: removed[:]},
		&library.MediaFile{Path: "stable.txt", Size: 6, Mode: 0o644, ModTime: modified, Hash: stable[:]},
	)
	writeScanFile(t, volume.Root, "added.txt", []byte("added"), 0o600, modified)
	writeScanFile(t, volume.Root, "changed.txt", []byte("new-changed"), 0o644, modified.Add(time.Second))
	writeScanFile(t, volume.Root, "stable.txt", []byte("stable"), 0o644, modified)
	require.NoError(t, os.Symlink("stable.txt", filepath.Join(volume.Root, "ignored-link")))
	require.NoError(t, os.Mkdir(filepath.Join(volume.Root, "empty"), 0o755))

	// Diff only physical metadata changes and verify hashes are cached.
	job := createScanJob(t, exe, media.ID, false)
	indexed := waitScanStatus(t, exe, job.ID, entity.JobStatus_JOB_STATUS_COMPLETED)
	require.Equal(t, entity.JobPhase_JOB_PHASE_UNSPECIFIED, indexed.Phase)
	entries := listScanEntries(t, exe, job.ID)
	require.Len(t, entries, 3)
	require.Equal(t, []string{"added.txt", "changed.txt", "removed.txt"}, []string{
		entries[0].Path, entries[1].Path, entries[2].Path,
	})
	require.Equal(t, entity.ScanChange_SCAN_CHANGE_ADDED, entries[0].Change)
	require.Equal(t, entity.ScanChange_SCAN_CHANGE_CHANGED, entries[1].Change)
	require.Equal(t, entity.ScanChange_SCAN_CHANGE_REMOVED, entries[2].Change)
	require.Len(t, entries[0].Sha256, sha256.Size)
	require.Len(t, entries[1].Sha256, sha256.Size)
	for _, entry := range entries[:2] {
		signature, valid, err := acp.ReadCachedSignature(filepath.Join(volume.Root, entry.Path))
		require.NoError(t, err)
		require.True(t, valid)
		require.Equal(t, entry.Sha256, signature.SHA256[:])
	}

	// Inventory has already been published; reading results needs no Apply operation.
	completed := waitScanStatus(t, exe, job.ID, entity.JobStatus_JOB_STATUS_COMPLETED)
	require.Equal(t, entity.JobPhase_JOB_PHASE_UNSPECIFIED, completed.Phase)
	positions, err := exe.Lib().ListMediaFilePositions(ctx, media.ID, "", 10)
	require.NoError(t, err)
	require.Equal(t, []string{"added.txt", "changed.txt", "stable.txt"}, []string{
		positions[0].Path, positions[1].Path, positions[2].Path,
	})
	require.Empty(t, positions[0].StorageOrder)
	data, err := os.ReadFile(filepath.Join(volume.Root, "changed.txt"))
	require.NoError(t, err)
	require.Equal(t, []byte("new-changed"), data)
	require.FileExists(t, filepath.Join(volume.Root, mediapkg.VolumeMarkerName))
}

func TestForceScanHashesMetadataStableFiles(t *testing.T) {
	// Preserve metadata while changing content relative to the Library Position.
	exe, volume, media := setupScanExecutor(t)
	modified := time.Unix(20, 0)
	oldHash := sha256.Sum256([]byte("before"))
	commitScanPositions(t, exe, media, &library.MediaFile{
		Path: "same.txt", Size: 6, Mode: 0o644, ModTime: modified, Hash: oldHash[:],
	})
	stableHash := sha256.Sum256([]byte("stable"))
	commitScanPositions(t, exe, media, &library.MediaFile{
		Path: "stable.txt", Size: 6, Mode: 0o644, ModTime: modified, Hash: stableHash[:],
	})
	writeScanFile(t, volume.Root, "same.txt", []byte("change"), 0o644, modified)
	writeScanFile(t, volume.Root, "stable.txt", []byte("stable"), 0o644, modified)

	// Cached Diff intentionally skips the metadata-stable path.
	cached := createScanJob(t, exe, media.ID, false)
	waitScanStatus(t, exe, cached.ID, entity.JobStatus_JOB_STATUS_COMPLETED)
	require.Empty(t, listScanEntries(t, exe, cached.ID))

	// Forced Diff rereads content, emits the change, and refreshes the cache.
	forced := createScanJob(t, exe, media.ID, true)
	waitScanStatus(t, exe, forced.ID, entity.JobStatus_JOB_STATUS_COMPLETED)
	entries := listScanEntries(t, exe, forced.ID)
	require.Len(t, entries, 1)
	require.Equal(t, entity.ScanChange_SCAN_CHANGE_CHANGED, entries[0].Change)
	newHash := sha256.Sum256([]byte("change"))
	require.Equal(t, newHash[:], entries[0].Sha256)
	signature, valid, err := acp.ReadCachedSignature(filepath.Join(volume.Root, "same.txt"))
	require.NoError(t, err)
	require.True(t, valid)
	require.Equal(t, newHash, signature.SHA256)
	// Repeating a forced read with no further changes must leave the inventory unchanged.
	repeated := createScanJob(t, exe, media.ID, true)
	waitScanStatus(t, exe, repeated.ID, entity.JobStatus_JOB_STATUS_COMPLETED)
	require.Empty(t, listScanEntries(t, exe, repeated.ID))
}

func TestScanUnavailableIsNotEmptyInventory(t *testing.T) {
	// An unavailable marker cannot authorize removing a known archived position.
	ctx := context.Background()
	exe, volume, media := setupScanExecutor(t)
	hash := sha256.Sum256([]byte("old"))
	commitScanPositions(t, exe, media, &library.MediaFile{Path: "old.txt", Size: 3, Mode: 0644, ModTime: time.Unix(1, 0), Hash: hash[:]})
	offline := filepath.Join(t.TempDir(), "offline-volume")
	require.NoError(t, os.Rename(volume.Root, offline))
	job := createScanJob(t, exe, media.ID, false)
	waitScanStatus(t, exe, job.ID, entity.JobStatus_JOB_STATUS_FAILED)
	positions, err := exe.Lib().ListMediaFilePositions(ctx, media.ID, "", 10)
	require.NoError(t, err)
	require.Len(t, positions, 1)

	// A failed Media step publishes nothing: an unavailable marker never removes a known position.
	require.NoError(t, os.Rename(offline, volume.Root))
	failedJob, err := exe.GetJob(ctx, job.ID)
	require.NoError(t, err)
	require.Equal(t, entity.JobStatus_JOB_STATUS_FAILED, failedJob.Status)
	positions, err = exe.Lib().ListMediaFilePositions(ctx, media.ID, "", 10)
	require.NoError(t, err)
	require.Len(t, positions, 1)
}

type inventoryFixture struct {
	checkSession
	paths   []string
	storage *entity.StoragePosition
}

func (s *inventoryFixture) WalkInventory(ctx context.Context, yield func(*mediapkg.InventoryEntry) error) error {
	for _, name := range s.paths {
		info, err := os.Stat(filepath.Join(s.root, name))
		if err != nil {
			return err
		}
		if err := yield(&mediapkg.InventoryEntry{Path: name, Info: info, Storage: s.storage}); err != nil {
			return err
		}
	}
	return ctx.Err()
}

func TestScanInventoryDetectsChangedPlacementWithoutRehash(t *testing.T) {
	ctx := context.Background()
	exe, volume, media := setupScanExecutor(t)
	writeScanFile(t, volume.Root, "stable.txt", []byte("stable"), 0644, time.Unix(1, 0))
	job := createScanJob(t, exe, media.ID, false)
	waitScanStatus(t, exe, job.ID, entity.JobStatus_JOB_STATUS_COMPLETED)
	value, err := exe.GetJobRunner(ctx, job.ID)
	require.NoError(t, err)
	r := value.(*runner)
	var config Config
	require.NoError(t, r.db.First(&config, 1).Error)
	require.NoError(t, r.freezeMedia(ctx, &config))
	session := &inventoryFixture{checkSession: checkSession{root: volume.Root, media: media},
		paths: []string{"stable.txt"}, storage: &entity.StoragePosition{Order: []byte{1, 2, 3}}}
	require.NoError(t, r.enumerateMedia(ctx, &config, &Scope{}, session))
	var row Entry
	require.NoError(t, r.db.First(&row).Error)
	require.Equal(t, entity.ScanChange_SCAN_CHANGE_CHANGED, row.Change)
	require.False(t, row.NeedsHash)
	require.Equal(t, session.storage.Order, row.Storage.Order)
}

func TestScanTypeChangesRemoveOnlyOldRegularInventory(t *testing.T) {
	for _, kind := range []string{"directory", "symlink"} {
		t.Run(kind, func(t *testing.T) {
			// A known archived regular path now denotes a non-managed filesystem object.
			ctx := context.Background()
			exe, volume, media := setupScanExecutor(t)
			hash := sha256.Sum256([]byte("old"))
			commitScanPositions(t, exe, media, &library.MediaFile{Path: "old.txt", Size: 3, Mode: 0644, ModTime: time.Unix(1, 0), Hash: hash[:]})
			filename := filepath.Join(volume.Root, "old.txt")
			if kind == "directory" {
				require.NoError(t, os.Mkdir(filename, 0755))
			} else {
				outside := filepath.Join(t.TempDir(), "outside.txt")
				require.NoError(t, os.WriteFile(outside, []byte("not archive content"), 0644))
				require.NoError(t, os.Symlink(outside, filename))
			}

			// A complete scan removes only the old catalog Position without deleting the replacement.
			job := createScanJob(t, exe, media.ID, false)
			waitScanStatus(t, exe, job.ID, entity.JobStatus_JOB_STATUS_COMPLETED)
			entries := listScanEntries(t, exe, job.ID)
			require.Len(t, entries, 1)
			require.Equal(t, entity.ScanChange_SCAN_CHANGE_REMOVED, entries[0].Change)
			positions, err := exe.Lib().ListMediaFilePositions(ctx, media.ID, "", 10)
			require.NoError(t, err)
			require.Empty(t, positions)
			_, err = os.Lstat(filename)
			require.NoError(t, err)
		})
	}
}
