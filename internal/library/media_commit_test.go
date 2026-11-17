package library

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/internal/resource/resourcetest"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestCommitMediaIsAtomicAndRejectsExistingIdentity(t *testing.T) {
	ctx := context.Background()
	db, lib := newTestLibrary(t)
	hash := sha256.Sum256([]byte("fixture"))
	requested := testTapeMedia("abc001", TapeFormatLTFSV0)
	requested.Name = "fixture"
	requested.CreatedAtNS = time.Unix(100, 0).UnixNano()

	// Publish one physical inventory and its materialized directory index together.
	created, err := lib.CommitMedia(ctx, requested, mediaFileSource(&MediaFile{
		Path: "folder/file.txt", Size: 7, Mode: 0o644,
		ModTime: time.Unix(1, 123456789), WriteTime: time.Unix(2, 987654321), Hash: hash[:],
	}))
	require.NoError(t, err)
	require.NotZero(t, created.ID)

	// Reject a reused immutable backend identity without changing the first inventory.
	_, err = lib.CommitMedia(ctx, testTapeMedia("ABC001", TapeFormatLTFSV0), mediaFileSource(&MediaFile{
		Path: "other.txt", Size: 1, Mode: 0o644,
	}))
	require.Error(t, err)
	var mediaCount, positionCount int64
	require.NoError(t, db.Model(ModelMedia).Count(&mediaCount).Error)
	require.NoError(t, db.Model(&Position{}).Count(&positionCount).Error)
	require.Equal(t, int64(1), mediaCount)
	require.Equal(t, int64(2), positionCount)

	// Reject an invalid stream entry before the transaction exposes a partial row.
	_, err = lib.CommitMedia(ctx, testTapeMedia("ABC002", TapeFormatLTFSV0), mediaFileSource(nil))
	require.ErrorContains(t, err, "Media file is nil")
	require.NoError(t, db.Model(ModelMedia).Count(&mediaCount).Error)
	require.Equal(t, int64(1), mediaCount)
}

func TestCommitMediaRollsBackPositionAndSourceFailures(t *testing.T) {
	ctx := context.Background()
	db, lib := newTestLibrary(t)

	// Inject a position write failure after the Media row is staged.
	callback := "test:fail-position-create"
	require.NoError(t, db.Callback().Create().Before("gorm:create").Register(callback, func(tx *gorm.DB) {
		if tx.Statement.Schema != nil && tx.Statement.Schema.Name == "Position" {
			tx.AddError(errors.New("injected position failure"))
		}
	}))
	t.Cleanup(func() { _ = db.Callback().Create().Remove(callback) })
	_, err := lib.CommitMedia(ctx, testTapeMedia("ABC001", TapeFormatLTFSV0), mediaFileSource(&MediaFile{
		Path: "file.txt", Size: 7, Mode: 0o644,
	}))
	require.ErrorContains(t, err, "injected position failure")

	// Keep both catalog facts absent when either publication phase fails.
	var mediaCount, positionCount int64
	require.NoError(t, db.Model(ModelMedia).Count(&mediaCount).Error)
	require.NoError(t, db.Model(&Position{}).Count(&positionCount).Error)
	require.Zero(t, mediaCount)
	require.Zero(t, positionCount)

	// Propagate a streamed-source failure through the same transaction boundary.
	sourceErr := errors.New("source failed")
	_, err = lib.CommitMedia(ctx, testTapeMedia("ABC002", TapeFormatLTFSV0), func(_ context.Context, yield func(*MediaFile) error) error {
		if err := yield(&MediaFile{Path: "file.txt", Size: 7, Mode: 0o644}); err != nil {
			return err
		}
		return sourceErr
	})
	require.ErrorIs(t, err, sourceErr)
	require.NoError(t, db.Model(ModelMedia).Count(&mediaCount).Error)
	require.NoError(t, db.Model(&Position{}).Count(&positionCount).Error)
	require.Zero(t, mediaCount)
	require.Zero(t, positionCount)
}

func TestCommitMediaStreamsLTFSInventoryAndAppendsIt(t *testing.T) {
	ctx := context.Background()
	db, lib := newTestLibrary(t)
	hash := sha256.Sum256([]byte("fixture"))

	// Publish a stream larger than one insert batch with the physical LTFS facts required by v1.
	const fileCount = batchSize*2 + 17
	readCount := 0
	created, err := lib.CommitMedia(ctx, testTapeMedia("abc001", TapeFormatLTFSV1), func(_ context.Context, yield func(*MediaFile) error) error {
		for index := 0; index < fileCount; index++ {
			readCount++
			if err := yield(testLTFSFile(fmt.Sprintf("files/%04d", index), int64(index+1), time.Unix(2, 0), hash[:])); err != nil {
				return err
			}
		}
		return nil
	})
	require.NoError(t, err)
	require.Equal(t, fileCount, readCount)
	require.Equal(t, int64(fileCount*(fileCount+1)/2), created.WrittenBytes)

	// Append another ordered LTFS entry to the immutable Media identity.
	secondWrite := time.Unix(20, 0)
	appended, err := lib.CommitMedia(ctx, created, mediaFileSource(testLTFSFile("other/second.txt", 11, secondWrite, hash[:])))
	require.NoError(t, err)
	require.Equal(t, created.ID, appended.ID)
	require.Equal(t, created.WrittenBytes+11, appended.WrittenBytes)
	var files int64
	require.NoError(t, db.Model(&Position{}).Where("media_id = ? AND is_dir = ?", created.ID, false).Count(&files).Error)
	require.Equal(t, int64(fileCount+1), files)
	stats, err := lib.GetMediaStats(ctx, created.ID)
	require.NoError(t, err)
	require.Equal(t, files, stats.FileCount)
	require.NotNil(t, stats.LastWrittenAtNS)
	require.Equal(t, secondWrite.UnixNano(), *stats.LastWrittenAtNS)
}

func TestCommitMediaAppendMatchesFullPositionIndexRebuild(t *testing.T) {
	ctx := context.Background()
	db, lib := newTestLibrary(t)
	file := func(path string, size int64, mtime, written int64) *MediaFile {
		value := &MediaFile{Path: path, Size: size, Mode: 0o644}
		if mtime != 0 {
			value.ModTime = time.Unix(0, mtime)
		}
		if written != 0 {
			value.WriteTime = time.Unix(0, written)
		}
		return value
	}
	media, err := lib.CommitMedia(ctx, testTapeMedia("ABC001", TapeFormatLTFSV0), mediaFileSource(
		file("a/b/one", 1, 50, 0), file("a/c/two", 2, 0, 70), file("a/three", 4, -10, 5), file("root", 8, 1, 1),
	))
	require.NoError(t, err)

	// Append into existing, new and sibling directories, including unknown and pre-epoch times.
	_, err = lib.CommitMedia(ctx, media, mediaFileSource(
		file("a/b-x/six", 64, -20, 0), file("a/b/c/four", 16, 40, 90), file("a/b/five", 32, 0, 0),
		file("a/c/seven", 128, 60, 10), file("d/e/eight", 256, 0, -5), file("root2", 512, 2, 2),
	))
	require.NoError(t, err)

	// Directory IDs are derived; compare every other column against a full rebuild.
	snapshot := func() []Position {
		var rows []Position
		require.NoError(t, db.Where("media_id = ?", media.ID).Order("path").Find(&rows).Error)
		for index := range rows {
			if rows[index].IsDir {
				rows[index].ID = 0
			}
		}
		return rows
	}
	appended := snapshot()
	require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
		return rebuildMediaPositionIndex(ctx, tx, media.ID)
	}))
	require.Equal(t, snapshot(), appended)
	var directories int64
	require.NoError(t, db.Model(&Position{}).Where("media_id = ? AND is_dir = ?", media.ID, true).Count(&directories).Error)
	require.Equal(t, int64(7), directories)
}

func TestCommitMediaRejectsDuplicateLTFSPathAtomically(t *testing.T) {
	ctx := context.Background()
	db, lib := newTestLibrary(t)
	media, err := lib.CommitMedia(ctx, testTapeMedia("ABC001", TapeFormatLTFSV1), mediaFileSource(testLTFSFile("file.txt", 1, time.Time{}, nil)))
	require.NoError(t, err)

	// A duplicate position leaves the previously committed physical inventory unchanged.
	_, err = lib.CommitMedia(ctx, media, mediaFileSource(testLTFSFile("file.txt", 2, time.Time{}, nil)))
	require.Error(t, err)
	stored, err := lib.GetMedia(ctx, media.ID)
	require.NoError(t, err)
	require.Equal(t, int64(1), stored.WrittenBytes)
	var files int64
	require.NoError(t, db.Model(&Position{}).Where("media_id = ? AND is_dir = ?", media.ID, false).Count(&files).Error)
	require.Equal(t, int64(1), files)
}

func TestCommitMediaRequiresLTFSV1PhysicalMetadata(t *testing.T) {
	_, lib := newTestLibrary(t)

	// v1 sequential inventory cannot be committed without its physical order and metadata.
	_, err := lib.CommitMedia(context.Background(), testTapeMedia("ABC001", TapeFormatLTFSV1), mediaFileSource(&MediaFile{
		Path: "file.txt", Size: 7, Mode: 0o644,
	}))
	require.ErrorContains(t, err, "no storage position")
}

func TestPositionMediaIndexes(t *testing.T) {
	db, _ := newTestLibrary(t)

	// Keep path and bounded file scans on the durable Media-owned indexes.
	indexes, err := db.Migrator().GetIndexes(&Position{})
	require.NoError(t, err)
	columns := make(map[string][]string, len(indexes))
	unique := make(map[string]bool, len(indexes))
	for _, index := range indexes {
		columns[index.Name()] = index.Columns()
		unique[index.Name()], _ = index.Unique()
	}
	require.Equal(t, []string{"media_id", "path"}, columns["idx_positions_media_path"])
	require.Equal(t, []string{"media_id", "parent_path", "path"}, columns["idx_positions_media_parent"])
	require.Equal(t, []string{"media_id", "is_dir", "written_at_ns"}, columns["idx_positions_media_files"])
	require.True(t, unique["idx_positions_media_path"])
}

func BenchmarkCommitMediaAppend(b *testing.B) {
	// Append selected Archive files to a Media that already holds a large inventory.
	ctx := context.Background()
	db := resourcetest.OpenCatalog(b)
	lib := New(db)
	require.NoError(b, lib.PrepareTemporary(b.TempDir()))
	require.NoError(b, lib.AutoMigrate())
	const existing, appended = 20000, 100
	media, err := lib.CommitMedia(ctx, &Media{Kind: entity.MediaKind_MEDIA_KIND_VOLUME,
		Identity: "11111111-1111-1111-1111-111111111111", Profile: (&entity.VolumeMediaProfile{Type: entity.VolumeType_VOLUME_TYPE_HDD}).Pack()},
		func(_ context.Context, yield func(*MediaFile) error) error {
			for index := 0; index < existing; index++ {
				if err := yield(&MediaFile{Path: fmt.Sprintf("old/%03d/%05d", index/100, index), Size: 1, Mode: 0o644}); err != nil {
					return err
				}
			}
			return nil
		})
	require.NoError(b, err)
	files := make([]*File, appended)
	for index := range files {
		files[index] = &File{Name: fmt.Sprintf("file-%03d", index)}
		require.NoError(b, lib.SaveFile(ctx, files[index]))
	}

	// Each append publishes new paths and new content for the same selected Files.
	b.ResetTimer()
	for iteration := 0; iteration < b.N; iteration++ {
		_, err := lib.CommitMedia(ctx, media, func(_ context.Context, yield func(*MediaFile) error) error {
			for index, file := range files {
				hash := sha256.Sum256(fmt.Appendf(nil, "%d/%d", iteration, index))
				if err := yield(&MediaFile{Path: fmt.Sprintf("new/%06d/%03d", iteration, index), Size: 7, Mode: 0o644,
					Hash: hash[:], Expected: &entity.ExpectedFile{FileId: file.ID}}); err != nil {
					return err
				}
			}
			return nil
		})
		require.NoError(b, err)
	}
}

func testTapeMedia(identity, format string) *Media {
	return &Media{
		Kind: entity.MediaKind_MEDIA_KIND_TAPE, Identity: identity,
		Profile: (&entity.TapeMediaProfile{Format: format}).Pack(),
	}
}

func mediaFileSource(files ...*MediaFile) MediaFileSource {
	return func(_ context.Context, yield func(*MediaFile) error) error {
		for _, file := range files {
			if err := yield(file); err != nil {
				return err
			}
		}
		return nil
	}
}

func testLTFSFile(path string, size int64, writeTime time.Time, hash []byte) *MediaFile {
	order := make([]byte, 17)
	order[0] = 'b'
	return &MediaFile{
		Path: path, Size: size, Mode: 0o644, WriteTime: writeTime, Hash: hash,
		StorageOrder: order,
		StorageMetadata: (&entity.LtfsMetadata{Extents: []*entity.LtfsExtent{{
			Partition: "b", StartBlock: 1, ByteCount: uint64(size),
		}}}).Pack(),
	}
}
