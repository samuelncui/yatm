package library

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/samuelncui/yatm/entity"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestCreateTapeIsAtomicAndRejectsExistingIdentity(t *testing.T) {
	// Create one Tape with sub-millisecond file timestamps.
	db, lib := newTestLibrary(t)

	hash := sha256.Sum256([]byte("fixture"))
	createdAt := time.Unix(100, 0)
	files := []*TapeFile{{
		Path: "folder/file.txt", Size: 7, Mode: 0o644,
		ModTime: time.Unix(1, 123456789), WriteTime: time.Unix(2, 987654321), Hash: hash[:],
	}}
	tape := &Tape{Barcode: "abc001", Name: "fixture", Encryption: "v1:key", CreateTime: createdAt}
	created, err := lib.CreateTape(context.Background(), tape, files)
	require.NoError(t, err)
	require.NotZero(t, created.ID)

	// Reject another FORMAT operation for the same normalized Tape identity.
	_, err = lib.CreateTape(context.Background(), &Tape{
		Barcode: "ABC001", Name: "fixture", Encryption: "v1:key", CreateTime: createdAt,
	}, files)
	require.Error(t, err)

	// Preserve the original Tape and its materialized directory index after the rejected FORMAT.
	var tapeCount, positionCount int64
	require.NoError(t, db.Model(ModelMedia).Count(&tapeCount).Error)
	require.NoError(t, db.Model(&Position{}).Count(&positionCount).Error)
	require.Equal(t, int64(1), tapeCount)
	require.Equal(t, int64(2), positionCount)

	_, err = lib.CreateTape(context.Background(), &Tape{
		Barcode: "ABC001", Name: "different", Encryption: "v1:key", CreateTime: createdAt,
	}, files)
	require.Error(t, err)
	require.NoError(t, db.Model(ModelMedia).Count(&tapeCount).Error)
	require.Equal(t, int64(1), tapeCount)

	// Reject invalid input before sorting dereferences it.
	_, err = lib.CreateTape(context.Background(), &Tape{Barcode: "ABC002"}, []*TapeFile{files[0], nil})
	require.ErrorContains(t, err, "file is nil")
}

func TestCreateTapeRollsBackWhenPositionWriteFails(t *testing.T) {
	ctx := context.Background()
	db, lib := newTestLibrary(t)

	// Inject a failure after the Tape row is inserted but before its positions are published.
	callback := "test:fail-position-create"
	require.NoError(t, db.Callback().Create().Before("gorm:create").Register(callback, func(tx *gorm.DB) {
		if tx.Statement.Schema != nil && tx.Statement.Schema.Name == "Position" {
			tx.AddError(errors.New("injected position failure"))
		}
	}))
	t.Cleanup(func() { _ = db.Callback().Create().Remove(callback) })
	hash := sha256.Sum256([]byte("fixture"))
	_, err := lib.CreateTape(ctx, &Tape{Barcode: "ABC001", Name: "fixture"}, []*TapeFile{{
		Path: "file.txt", Size: 7, Mode: 0o644, ModTime: time.Unix(1, 0), WriteTime: time.Unix(2, 0), Hash: hash[:],
	}})
	require.ErrorContains(t, err, "injected position failure")

	// Library visibility is all-or-nothing across Tape and Position tables.
	var tapeCount, positionCount int64
	require.NoError(t, db.Model(ModelMedia).Count(&tapeCount).Error)
	require.NoError(t, db.Model(&Position{}).Count(&positionCount).Error)
	require.Zero(t, tapeCount)
	require.Zero(t, positionCount)
}

func TestCreateTapeFromSourceStreamsAndRejectsExistingIdentity(t *testing.T) {
	ctx := context.Background()
	db, lib := newTestLibrary(t)

	const fileCount = batchSize*2 + 17
	hash := sha256.Sum256([]byte("fixture"))
	readCount := 0
	source := func(_ context.Context, yield func(*TapeFile) error) error {
		readCount = 0
		for index := 0; index < fileCount; index++ {
			readCount++
			if err := yield(&TapeFile{
				Path: fmt.Sprintf("files/%04d", index), Size: int64(index + 1), Mode: 0o644,
				ModTime: time.Unix(1, 0), WriteTime: time.Unix(2, 0), Hash: hash[:],
			}); err != nil {
				return err
			}
		}
		return nil
	}
	tape := &Tape{Barcode: "abc001", Name: "stream", Encryption: "v1:key"}
	created, err := lib.CreateTapeFromSource(ctx, tape, source)
	require.NoError(t, err)
	require.Equal(t, fileCount, readCount)
	require.Equal(t, int64(fileCount*(fileCount+1)/2), created.WritenBytes)

	var positions int64
	require.NoError(t, db.Model(&Position{}).Where("media_id = ?", created.ID).Count(&positions).Error)
	require.Equal(t, int64(fileCount+1), positions)
	_, err = lib.CreateTapeFromSource(ctx, &Tape{
		Barcode: "ABC001", Name: "stream", Encryption: "v1:key",
	}, source)
	require.Error(t, err)
	require.Equal(t, fileCount, readCount)
}

func TestCreateTapeFromSourceRollsBackSourceFailure(t *testing.T) {
	ctx := context.Background()
	db, lib := newTestLibrary(t)

	sourceErr := errors.New("source failed")
	_, err := lib.CreateTapeFromSource(ctx, &Tape{Barcode: "ABC001"}, func(_ context.Context, yield func(*TapeFile) error) error {
		hash := sha256.Sum256([]byte("fixture"))
		for index := 0; index < batchSize+1; index++ {
			if err := yield(&TapeFile{
				Path: fmt.Sprintf("files/%04d", index), Size: 7, Mode: 0o644,
				ModTime: time.Unix(1, 0), WriteTime: time.Unix(2, 0), Hash: hash[:],
			}); err != nil {
				return err
			}
		}
		return sourceErr
	})
	require.ErrorIs(t, err, sourceErr)

	var tapes, positions int64
	require.NoError(t, db.Model(ModelMedia).Count(&tapes).Error)
	require.NoError(t, db.Model(&Position{}).Count(&positions).Error)
	require.Zero(t, tapes)
	require.Zero(t, positions)
}

func TestListPositionsReadsOneMaterializedDirectory(t *testing.T) {
	ctx := context.Background()
	_, lib := newTestLibrary(t)

	files := []*TapeFile{
		{Path: "a/b/first.txt", Size: 2, Mode: 0o644, ModTime: time.Unix(2, 0)},
		{Path: "a/second.txt", Size: 3, Mode: 0o644, ModTime: time.Unix(3, 0)},
		{Path: "root.txt", Size: 5, Mode: 0o644, ModTime: time.Unix(5, 0)},
	}
	tape, err := lib.CreateTape(ctx, &Tape{Barcode: "ABC001"}, files)
	require.NoError(t, err)

	root, err := lib.ListPositions(ctx, tape.ID, "")
	require.NoError(t, err)
	require.Len(t, root, 2)
	require.Equal(t, "a/", root[0].Path)
	require.True(t, root[0].IsDir)
	require.Equal(t, int64(5), root[0].Size)
	require.Equal(t, "root.txt", root[1].Path)

	a, err := lib.ListPositions(ctx, tape.ID, "a/")
	require.NoError(t, err)
	require.Len(t, a, 2)
	require.Equal(t, "a/b/", a[0].Path)
	require.Equal(t, "a/second.txt", a[1].Path)
	b, err := lib.ListPositions(ctx, tape.ID, "a/b/")
	require.NoError(t, err)
	require.Len(t, b, 1)
	require.Equal(t, "a/b/first.txt", b[0].Path)
}

func TestAppendTapeKeepsIdentityAndRebuildsPositions(t *testing.T) {
	ctx := context.Background()
	db, lib := newTestLibrary(t)
	hash := sha256.Sum256([]byte("fixture"))
	firstWrite := time.Unix(10, 0)
	tape, err := lib.CreateTape(ctx, &Tape{
		Barcode: "ABC001", Name: "fixture", Format: TapeFormatLTFSV1,
	}, []*TapeFile{testLTFSTapeFile("original/first.txt", 7, firstWrite, hash[:])})
	require.NoError(t, err)
	require.Equal(t, TapeFormatLTFSV1, tape.Format)

	secondWrite := time.Unix(20, 0)
	appended, err := lib.AppendTapeFromSource(ctx, tape.ID, func(_ context.Context, yield func(*TapeFile) error) error {
		return yield(testLTFSTapeFile("other/second.txt", 11, secondWrite, hash[:]))
	})
	require.NoError(t, err)
	require.Equal(t, tape.ID, appended.ID)
	require.Equal(t, int64(18), appended.WritenBytes)
	require.Equal(t, int64(18), appended.CapacityBytes)

	var files int64
	require.NoError(t, db.Model(&Position{}).Where("media_id = ? AND is_dir = ?", tape.ID, false).Count(&files).Error)
	require.Equal(t, int64(2), files)
	root, err := lib.ListPositions(ctx, tape.ID, "")
	require.NoError(t, err)
	require.Len(t, root, 2)
	require.Equal(t, "original/", root[0].Path)
	require.Equal(t, "other/", root[1].Path)

	stats, err := lib.GetTapeStats(ctx, tape.ID)
	require.NoError(t, err)
	require.Equal(t, int64(2), stats.FileCount)
	require.NotNil(t, stats.LastWriteTime)
	require.True(t, secondWrite.Equal(*stats.LastWriteTime))
}

func TestAppendTapeRejectsDuplicatePathAtomically(t *testing.T) {
	ctx := context.Background()
	db, lib := newTestLibrary(t)
	tape, err := lib.CreateTape(ctx, &Tape{
		Barcode: "ABC001", Format: TapeFormatLTFSV1,
	}, []*TapeFile{testLTFSTapeFile("file.txt", 1, time.Time{}, nil)})
	require.NoError(t, err)

	_, err = lib.AppendTapeFromSource(ctx, tape.ID, func(_ context.Context, yield func(*TapeFile) error) error {
		return yield(testLTFSTapeFile("file.txt", 2, time.Time{}, nil))
	})
	require.Error(t, err)

	stored, err := lib.GetTape(ctx, tape.ID)
	require.NoError(t, err)
	require.Equal(t, int64(1), stored.WritenBytes)
	var files int64
	require.NoError(t, db.Model(&Position{}).Where("media_id = ? AND is_dir = ?", tape.ID, false).Count(&files).Error)
	require.Equal(t, int64(1), files)
}

func testLTFSTapeFile(path string, size int64, writeTime time.Time, hash []byte) *TapeFile {
	order := make([]byte, 17)
	order[0] = 'b'
	return &TapeFile{
		Path: path, Size: size, Mode: 0o644, WriteTime: writeTime, Hash: hash,
		StorageOrder: order,
		StorageMetadata: (&entity.LTFSMetadata{Extents: []*entity.LTFSExtent{{
			Partition: "b", StartBlock: 1, ByteCount: uint64(size),
		}}}).Pack(),
	}
}

func TestDeleteTapePreservesFiles(t *testing.T) {
	ctx := context.Background()
	db, lib := newTestLibrary(t)
	hash := sha256.Sum256([]byte("fixture"))
	tape, err := lib.CreateTape(ctx, &Tape{Barcode: "ABC001"}, []*TapeFile{{
		Path: "file.txt", Size: 1, Mode: 0o644, Hash: hash[:],
	}})
	require.NoError(t, err)
	require.NoError(t, lib.ImportArchivedInventory(ctx))

	var before int64
	require.NoError(t, db.Model(&File{}).Count(&before).Error)
	require.Positive(t, before)
	require.NoError(t, lib.DeleteTapes(ctx, tape.ID))

	var tapes, positions, files int64
	require.NoError(t, db.Model(ModelMedia).Count(&tapes).Error)
	require.NoError(t, db.Model(&Position{}).Count(&positions).Error)
	require.NoError(t, db.Model(&File{}).Count(&files).Error)
	require.Zero(t, tapes)
	require.Zero(t, positions)
	require.Equal(t, before, files)
}

func TestOverwriteTapePreservesOldTreeAndMaterializesReplacement(t *testing.T) {
	ctx := context.Background()
	_, lib := newTestLibrary(t)
	oldHash := sha256.Sum256([]byte("old"))
	oldTape, err := lib.CreateTape(ctx, &Tape{Barcode: "ABC001"}, []*TapeFile{{
		Path: "file.txt", Size: 3, Mode: 0o644, Hash: oldHash[:],
	}})
	require.NoError(t, err)
	require.NoError(t, lib.ImportArchivedInventory(ctx))
	oldFile, err := lib.GetByPath(ctx, Root.ID, "Unforged/ABC001/file.txt")
	require.NoError(t, err)
	require.NotNil(t, oldFile)

	require.NoError(t, lib.DeleteTapes(ctx, oldTape.ID))
	newHash := sha256.Sum256([]byte("replacement"))
	newTape, err := lib.CreateTape(ctx, &Tape{Barcode: "ABC001"}, []*TapeFile{{
		Path: "file.txt", Size: 11, Mode: 0o644, Hash: newHash[:],
	}})
	require.NoError(t, err)
	require.NoError(t, lib.ImportArchivedInventory(ctx))

	newFile, err := lib.GetByPath(ctx, Root.ID, "Unforged/ABC001/file (1).txt")
	require.NoError(t, err)
	require.NotNil(t, newFile)
	require.NotEqual(t, oldFile.ID, newFile.ID)
	require.Equal(t, newHash[:], newFile.Hash)
	positions, err := lib.GetPositionByFileID(ctx, newFile.ID)
	require.NoError(t, err)
	require.Len(t, positions, 1)
	require.Equal(t, newTape.ID, positions[0].MediaID)

	preserved, err := lib.GetFile(ctx, oldFile.ID)
	require.NoError(t, err)
	parents, err := lib.ListParents(ctx, preserved.ID)
	require.NoError(t, err)
	require.GreaterOrEqual(t, len(parents), 2)
	require.NotEqual(t, int64(TrashFileID), parents[0].ID)
}

func TestOverwriteTapeReplacesFileWithDirectoryTree(t *testing.T) {
	ctx := context.Background()
	_, lib := newTestLibrary(t)
	oldHash := sha256.Sum256([]byte("old"))
	oldTape, err := lib.CreateTape(ctx, &Tape{Barcode: "ABC001"}, []*TapeFile{{
		Path: "folder", Size: 3, Mode: 0o644, Hash: oldHash[:],
	}})
	require.NoError(t, err)
	require.NoError(t, lib.ImportArchivedInventory(ctx))
	oldFile, err := lib.GetByPath(ctx, Root.ID, "Unforged/ABC001/folder")
	require.NoError(t, err)
	require.NotNil(t, oldFile)

	require.NoError(t, lib.DeleteTapes(ctx, oldTape.ID))
	newHash := sha256.Sum256([]byte("replacement"))
	newTape, err := lib.CreateTape(ctx, &Tape{Barcode: "ABC001"}, []*TapeFile{{
		Path: "folder/file.txt", Size: 11, Mode: 0o644, Hash: newHash[:],
	}})
	require.NoError(t, err)
	require.NoError(t, lib.ImportArchivedInventory(ctx))

	newFile, err := lib.GetByPath(ctx, Root.ID, "Unforged/ABC001/folder (1)/file.txt")
	require.NoError(t, err)
	require.NotNil(t, newFile)
	positions, err := lib.GetPositionByFileID(ctx, newFile.ID)
	require.NoError(t, err)
	require.Len(t, positions, 1)
	require.Equal(t, newTape.ID, positions[0].MediaID)
	preserved, err := lib.GetFile(ctx, oldFile.ID)
	require.NoError(t, err)
	parents, err := lib.ListParents(ctx, preserved.ID)
	require.NoError(t, err)
	require.NotEqual(t, int64(TrashFileID), parents[0].ID)
}

func TestCreateLTFSV1TapeRequiresPhysicalMetadata(t *testing.T) {
	ctx := context.Background()
	_, lib := newTestLibrary(t)
	_, err := lib.CreateTape(ctx, &Tape{
		Barcode: "ABC001", Format: TapeFormatLTFSV1,
	}, []*TapeFile{{Path: "file.txt", Size: 7, Mode: 0o644}})
	require.ErrorContains(t, err, "no storage position")
}

func TestPositionMediaIndexes(t *testing.T) {
	db, _ := newTestLibrary(t)
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
	require.Equal(t, []string{"media_id", "is_dir", "write_time"}, columns["idx_positions_media_files"])
	require.True(t, unique["idx_positions_media_path"])
}

func TestAutoMigrateReplacesLegacyPositionParentIndex(t *testing.T) {
	db, lib := newTestLibrary(t)
	require.NoError(t, db.Migrator().DropIndex(&Position{}, "idx_positions_media_parent"))
	require.NoError(t, db.Exec("CREATE INDEX idx_positions_media_parent ON positions(media_id, parent_path)").Error)

	require.NoError(t, lib.AutoMigrate())
	indexes, err := db.Migrator().GetIndexes(&Position{})
	require.NoError(t, err)
	for _, index := range indexes {
		if index.Name() == "idx_positions_media_parent" {
			require.Equal(t, []string{"media_id", "parent_path", "path"}, index.Columns())
			return
		}
	}
	t.Fatal("idx_positions_media_parent was not recreated")
}
