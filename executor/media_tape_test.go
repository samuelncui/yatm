package executor

import (
	"context"
	"crypto/sha256"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/executor/jobstorage"
	mediapkg "github.com/samuelncui/yatm/media"
	"github.com/samuelncui/yatm/resource"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestWaitForTapeIndexAcceptsDelayedCapture(t *testing.T) {
	filename := filepath.Join(t.TempDir(), "ABC001.schema")
	written := make(chan error, 1)
	go func() {
		time.Sleep(50 * time.Millisecond)
		written <- os.WriteFile(filename, []byte("index"), 0o600)
	}()
	require.NoError(t, waitForTapeIndex(context.Background(), filename))
	require.NoError(t, <-written)
}

func TestKeepVerifiedArchivePrefixStopsAtFirstUnverifiedFile(t *testing.T) {
	db := newArchiveItemDB(t)
	for index, path := range []string{"a.txt", "b.txt", "c.txt"} {
		require.NoError(t, db.Create(&jobstorage.ArchiveItem{
			ID: int64(index + 1), Status: entity.CopyStatus_STAGED, Size: 1,
			TargetPath: path, MediaPath: path, Data: &entity.ArchiveManifestFile{SourcePath: "/source/" + path},
			Result: &entity.ArchiveCopyResult{Size: 1, Sha256: make([]byte, 32)},
		}).Error)
	}
	require.NoError(t, attachTapeStorage(db, tapeIndexEntry("a.txt", 1)))
	require.NoError(t, attachTapeStorage(db, tapeIndexEntry("b.txt", 0)))
	require.NoError(t, attachTapeStorage(db, tapeIndexEntry("c.txt", 1)))

	kept, err := keepVerifiedArchivePrefix(db)
	require.NoError(t, err)
	require.Equal(t, int64(1), kept)
	var items []*jobstorage.ArchiveItem
	require.NoError(t, db.Order("target_path").Find(&items).Error)
	require.Equal(t, entity.CopyStatus_STAGED, items[0].Status)
	require.NotNil(t, items[0].Result.Storage)
	for _, item := range items[1:] {
		require.Equal(t, entity.CopyStatus_PENDING, item.Status)
		require.Empty(t, item.MediaPath)
		require.Nil(t, item.Result)
	}
}

func TestResetUnverifiedArchiveItemsKeepsEveryCapturedSuccess(t *testing.T) {
	db := newArchiveItemDB(t)
	for index, path := range []string{"a.txt", "b.txt", "c.txt"} {
		require.NoError(t, db.Create(&jobstorage.ArchiveItem{
			ID: int64(index + 1), Status: entity.CopyStatus_STAGED, Size: 1,
			TargetPath: path, MediaPath: path, Data: &entity.ArchiveManifestFile{SourcePath: "/source/" + path},
			Result: &entity.ArchiveCopyResult{Size: 1, Sha256: make([]byte, 32)},
		}).Error)
	}
	require.NoError(t, attachTapeStorage(db, tapeIndexEntry("a.txt", 1)))
	require.NoError(t, attachTapeStorage(db, tapeIndexEntry("c.txt", 1)))

	reset, err := resetUnverifiedArchiveItems(db)
	require.NoError(t, err)
	require.Equal(t, int64(1), reset)
	var items []*jobstorage.ArchiveItem
	require.NoError(t, db.Order("target_path").Find(&items).Error)
	require.Equal(t, entity.CopyStatus_STAGED, items[0].Status)
	require.Equal(t, entity.CopyStatus_PENDING, items[1].Status)
	require.Equal(t, entity.CopyStatus_STAGED, items[2].Status)
}

func TestVolumeFinalizeRejectsChangedMarkerWithoutDeletingFiles(t *testing.T) {
	// Stage one successfully copied file on the originally opened Volume.
	ctx := context.Background()
	db := newArchiveItemDB(t)
	root := t.TempDir()
	volume, err := mediapkg.InitializeVolume(root, &entity.VolumeMediaProfile{Type: entity.VolumeType_VOLUME_TYPE_HDD})
	require.NoError(t, err)
	physicalPath := filepath.Join(root, "prefix", "file.txt")
	require.NoError(t, os.MkdirAll(filepath.Dir(physicalPath), 0o755))
	require.NoError(t, os.WriteFile(physicalPath, []byte("fixture"), 0o644))
	require.NoError(t, db.Create(&jobstorage.ArchiveItem{
		ID: 1, Status: entity.CopyStatus_STAGED, Size: 7, TargetPath: "file.txt", MediaPath: "file.txt",
		Data:   &entity.ArchiveManifestFile{SourcePath: "/source/file.txt"},
		Result: &entity.ArchiveCopyResult{Size: 7, Sha256: make([]byte, 32)},
	}).Error)

	// Replace the marker without touching the copied file.
	require.NoError(t, os.Remove(filepath.Join(root, mediapkg.VolumeMarkerName)))
	_, err = mediapkg.InitializeVolume(root, &entity.VolumeMediaProfile{Type: entity.VolumeType_VOLUME_TYPE_HDD})
	require.NoError(t, err)

	// Finalization must make the result unpublishable and return the candidate to PENDING.
	session := &volumeWriteSession{db: db, volume: volume, pathPrefix: "prefix"}
	err = session.Finalize(ctx, nil)
	require.ErrorIs(t, err, mediapkg.ErrFinalizeUnusable)
	require.ErrorContains(t, err, "Volume marker changed")
	item := new(jobstorage.ArchiveItem)
	require.NoError(t, db.First(item, 1).Error)
	require.Equal(t, entity.CopyStatus_PENDING, item.Status)
	require.Empty(t, item.MediaPath)
	require.FileExists(t, physicalPath)
}

func TestVolumeSessionsRejectSymlinkPaths(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	volume, err := mediapkg.InitializeVolume(root, &entity.VolumeMediaProfile{
		Type: entity.VolumeType_VOLUME_TYPE_HDD,
	})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(outside, "source.txt"), []byte("source"), 0o644))
	require.NoError(t, os.Symlink(outside, filepath.Join(root, "linked")))

	read := &volumeReadSession{volume: volume}
	_, err = read.SourcePath("linked/source.txt")
	require.ErrorContains(t, err, "contains a symlink")

	db := newArchiveItemDB(t)
	require.NoError(t, db.Create(&jobstorage.ArchiveItem{
		ID: 1, Status: entity.CopyStatus_PENDING, Size: 6, TargetPath: "linked/target.txt",
		Data: &entity.ArchiveManifestFile{SourcePath: "/source/target.txt"},
	}).Error)
	write := &volumeWriteSession{
		db: db, volume: volume, pathPrefix: "", available: 1 << 20,
	}
	_, err = write.TargetPath("linked/target.txt")
	require.ErrorContains(t, err, "contains a symlink")
}

func TestMediaLeaseLivesUntilAttemptEnds(t *testing.T) {
	exe := New(nil, nil, []string{"/dev/nst0"}, Paths{}, Scripts{}, nil)
	require.True(t, exe.beginAttempt(1, func() {}))
	require.True(t, exe.beginAttempt(2, func() {}))
	require.True(t, exe.LeaseVolume(1, "volume-id"))
	require.False(t, exe.LeaseVolume(2, "volume-id"))
	require.True(t, exe.LeaseTapeDevice(1, "/dev/nst0"))
	require.False(t, exe.LeaseTapeDevice(2, "/dev/nst0"))

	exe.endAttempt(1)
	require.True(t, exe.LeaseVolume(2, "volume-id"))
	require.True(t, exe.LeaseTapeDevice(2, "/dev/nst0"))
	exe.endAttempt(2)
}

func TestTapeFinalizeStopsAfterUnmountFailureAndKeepsDeviceUnavailable(t *testing.T) {
	// Stage one completed copy under a leased Tape device.
	ctx := context.Background()
	db := newArchiveItemDB(t)
	require.NoError(t, db.Create(&jobstorage.ArchiveItem{
		ID: 1, Status: entity.CopyStatus_STAGED, Size: 7,
		TargetPath: "file.txt", MediaPath: "file.txt",
		Data: &entity.ArchiveManifestFile{SourcePath: "/source/file.txt"},
		Result: &entity.ArchiveCopyResult{
			Size: 7, Sha256: make([]byte, sha256.Size),
		},
	}).Error)
	unmount := filepath.Join(t.TempDir(), "unmount")
	require.NoError(t, os.WriteFile(unmount, []byte("#!/bin/sh\nexit 1\n"), 0o755))
	device := "/dev/nst0"
	exe := New(nil, nil, []string{device}, Paths{}, Scripts{Umount: unmount}, nil)
	require.True(t, exe.beginAttempt(1, func() {}))
	require.True(t, exe.LeaseTapeDevice(1, device))
	backend := exe.NewMediaBackend(1, nil).(*mediaBackend)
	indexPath := filepath.Join(t.TempDir(), "ABC001.schema")
	require.NoError(t, os.WriteFile(indexPath, []byte("invalid index"), 0o600))
	session := &tapeWriteSession{
		backend: backend, db: db, device: device,
		mountPoint: t.TempDir(), tapeDir: t.TempDir(), indexPath: indexPath,
		pathPrefix: ".", recycleKey: func() {},
	}

	// A failed normal unmount invalidates the attempt without waiting for or parsing an Index.
	err := session.Finalize(ctx, mediapkg.ErrTargetNoSpace)
	require.ErrorIs(t, err, mediapkg.ErrFinalizeUnusable)
	require.ErrorIs(t, err, mediapkg.ErrTargetNoSpace)
	require.ErrorContains(t, err, "unmount Tape failed")
	require.NotErrorIs(t, err, errInvalidTapeIndex)
	item := new(jobstorage.ArchiveItem)
	require.NoError(t, db.First(item, 1).Error)
	require.Equal(t, entity.CopyStatus_PENDING, item.Status)
	require.Empty(t, item.MediaPath)
	require.Nil(t, item.Result)

	// Ending the attempt releases the lease key but not the uncertain physical device.
	exe.endAttempt(1)
	require.Empty(t, exe.ListAvailableDevices())
}

func TestTapeFinalizeRejectsInvalidPostUnmountIndex(t *testing.T) {
	// Stage one completed copy and make normal unmount produce a non-empty invalid Index.
	ctx := context.Background()
	db := newArchiveItemDB(t)
	require.NoError(t, db.Create(&jobstorage.ArchiveItem{
		ID: 1, Status: entity.CopyStatus_STAGED, Size: 7,
		TargetPath: "file.txt", MediaPath: "file.txt",
		Data: &entity.ArchiveManifestFile{SourcePath: "/source/file.txt"},
		Result: &entity.ArchiveCopyResult{
			Size: 7, Sha256: make([]byte, sha256.Size),
		},
	}).Error)
	tapeDir := t.TempDir()
	unmount := filepath.Join(t.TempDir(), "unmount")
	require.NoError(t, os.WriteFile(unmount, []byte(
		"#!/bin/sh\ntest \"$DEVICE\" = \"/dev/nst0\"\nprintf 'invalid index\\n' > \"$TAPE_DIR/ABC001.schema\"\n",
	), 0o755))
	device := "/dev/nst0"
	exe := New(nil, nil, []string{device}, Paths{}, Scripts{Umount: unmount}, nil)
	require.True(t, exe.beginAttempt(1, func() {}))
	require.True(t, exe.LeaseTapeDevice(1, device))
	backend := exe.NewMediaBackend(1, nil).(*mediaBackend)
	session := &tapeWriteSession{
		backend: backend, db: db, barcode: "ABC001", device: device,
		mountPoint: t.TempDir(), tapeDir: tapeDir, indexPath: filepath.Join(tapeDir, "ABC001.schema"),
		pathPrefix: ".", recycleKey: func() {},
	}

	// Invalid final physical facts invalidate every staged candidate but not the device lease itself.
	err := session.Finalize(ctx, nil)
	require.ErrorIs(t, err, mediapkg.ErrFinalizeUnusable)
	require.ErrorIs(t, err, errInvalidTapeIndex)
	item := new(jobstorage.ArchiveItem)
	require.NoError(t, db.First(item, 1).Error)
	require.Equal(t, entity.CopyStatus_PENDING, item.Status)
	require.Empty(t, item.MediaPath)
	require.Nil(t, item.Result)
	exe.endAttempt(1)
	require.Equal(t, []string{device}, exe.ListAvailableDevices())
}

func newArchiveItemDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := resource.OpenSQLite(":memory:")
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&jobstorage.ArchiveItem{}))
	return db.WithContext(context.Background())
}

func tapeIndexEntry(path string, size int64) *mediapkg.LTFSIndexEntry {
	return &mediapkg.LTFSIndexEntry{
		Path: path, Size: size,
		Storage: &entity.StoragePosition{
			Order: []byte{1}, Metadata: (&entity.LTFSMetadata{Extents: []*entity.LTFSExtent{{Partition: "b"}}}).Pack(),
		},
	}
}
