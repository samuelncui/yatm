package archive

import (
	"context"
	"testing"

	"github.com/samuelncui/yatm/entity"
	mediapkg "github.com/samuelncui/yatm/internal/media"
	"github.com/samuelncui/yatm/internal/resource"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestKeepVerifiedPrefixStopsAtFirstUnverifiedFile(t *testing.T) {
	// Record three copied candidates whose middle physical result has the wrong size.
	db := newReconciliationItemDB(t)
	for index, path := range []string{"a.txt", "b.txt", "c.txt"} {
		require.NoError(t, db.Create(&Item{
			ID: int64(index + 1), Status: entity.CopyStatus_COPY_STATUS_STAGED, Size: 1,
			TargetPath: path, MediaPath: path, Data: &entity.ArchiveManifestFile{SourcePath: "/source/" + path},
			Result: &entity.ArchiveCopyResult{SizeBytes: 1, Sha256: make([]byte, 32)},
		}).Error)
	}
	require.NoError(t, attachTapeStorage(db, reconciliationIndexEntry("a.txt", 1)))
	require.NoError(t, attachTapeStorage(db, reconciliationIndexEntry("b.txt", 0)))
	require.NoError(t, attachTapeStorage(db, reconciliationIndexEntry("c.txt", 1)))

	// A full Tape retains only the contiguous verified physical prefix.
	kept, err := keepVerifiedPrefix(db)
	require.NoError(t, err)
	require.Equal(t, int64(1), kept)
	var items []*Item
	require.NoError(t, db.Order("target_path").Find(&items).Error)
	require.Equal(t, entity.CopyStatus_COPY_STATUS_STAGED, items[0].Status)
	require.NotNil(t, items[0].Result.Storage)
	for _, item := range items[1:] {
		require.Equal(t, entity.CopyStatus_COPY_STATUS_PENDING, item.Status)
		require.Empty(t, item.MediaPath)
		require.Nil(t, item.Result)
	}
}

func TestResetUnverifiedItemsKeepsEveryCapturedSuccess(t *testing.T) {
	// Record independent successful Index entries around one unverified candidate.
	db := newReconciliationItemDB(t)
	for index, path := range []string{"a.txt", "b.txt", "c.txt"} {
		require.NoError(t, db.Create(&Item{
			ID: int64(index + 1), Status: entity.CopyStatus_COPY_STATUS_STAGED, Size: 1,
			TargetPath: path, MediaPath: path, Data: &entity.ArchiveManifestFile{SourcePath: "/source/" + path},
			Result: &entity.ArchiveCopyResult{SizeBytes: 1, Sha256: make([]byte, 32)},
		}).Error)
	}
	require.NoError(t, attachTapeStorage(db, reconciliationIndexEntry("a.txt", 1)))
	require.NoError(t, attachTapeStorage(db, reconciliationIndexEntry("c.txt", 1)))

	// A non-full Tape retains every individually verified candidate for later publication.
	reset, err := resetUnverifiedItems(db)
	require.NoError(t, err)
	require.Equal(t, int64(1), reset)
	var items []*Item
	require.NoError(t, db.Order("target_path").Find(&items).Error)
	require.Equal(t, entity.CopyStatus_COPY_STATUS_STAGED, items[0].Status)
	require.Equal(t, entity.CopyStatus_COPY_STATUS_PENDING, items[1].Status)
	require.Equal(t, entity.CopyStatus_COPY_STATUS_STAGED, items[2].Status)
}

func newReconciliationItemDB(t *testing.T) *gorm.DB {
	t.Helper()

	// Reconciliation owns Archive's current durable manifest rows.
	db, err := resource.OpenSQLite(":memory:")
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&Item{}))
	return db.WithContext(context.Background())
}

func reconciliationIndexEntry(path string, size int64) *mediapkg.LTFSIndexEntry {
	return &mediapkg.LTFSIndexEntry{
		Path: path, Size: size,
		Storage: &entity.StoragePosition{
			Order: []byte{1}, Metadata: (&entity.LtfsMetadata{Extents: []*entity.LtfsExtent{{Partition: "b"}}}).Pack(),
		},
	}
}
