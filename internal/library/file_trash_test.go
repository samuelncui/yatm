package library

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"testing"

	"github.com/samuelncui/yatm/entity"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestLogicalRemoveAndMergeShareTrashPlacement(t *testing.T) {
	// Independent same-name Files retain their content history and original until their own policy retires it.
	ctx := context.Background()
	db, lib := newTestLibrary(t)
	parentA, parentB := &File{Name: "a", Kind: entity.FileKind_FILE_KIND_DIRECTORY}, &File{Name: "b", Kind: entity.FileKind_FILE_KIND_DIRECTORY}
	createFileRows(t, db, parentA, parentB)
	first, second := &File{ParentID: parentA.ID, Name: "same", Note: "retain"}, &File{ParentID: parentB.ID, Name: "same"}
	target := &File{Name: "target"}
	createFileRows(t, db, first, second, target)
	require.NoError(t, db.Create(&FileLocation{FileID: first.ID, LocationID: 1, Path: "first", Mode: 0644}).Error)
	require.NoError(t, db.Create(&FileTrackingKey{FileID: first.ID, Kind: TrackingNative, LocationID: 1, KeyValue: []byte("first")}).Error)
	version := &FileVersion{FileID: first.ID, Signature: []byte("saved")}
	require.NoError(t, db.Create(version).Error)
	require.NoError(t, lib.Delete(ctx, []int64{first.ID, second.ID}))
	storedFirst, err := lib.GetFile(ctx, first.ID)
	require.NoError(t, err)
	storedSecond, err := lib.GetFile(ctx, second.ID)
	require.NoError(t, err)
	require.NotEqual(t, storedFirst.ParentID, storedSecond.ParentID)
	require.Equal(t, "same", storedFirst.Name)
	require.NoError(t, db.First(new(FileLocation), "file_id = ?", first.ID).Error)
	require.NoError(t, db.First(new(FileTrackingKey), "file_id = ?", first.ID).Error)
	require.NoError(t, db.First(new(FileVersion), version.ID).Error)

	// Move out and merge uses the same container layout, while Merge alone retires originals and reassigns history.
	storedFirst.ParentID = parentA.ID
	require.NoError(t, lib.MoveFile(ctx, storedFirst))
	require.NoError(t, lib.MergeFiles(ctx, target.ID, []int64{first.ID}))
	retired, err := lib.GetFile(ctx, first.ID)
	require.NoError(t, err)
	parents, err := lib.ListParents(ctx, first.ID)
	require.NoError(t, err)
	require.Equal(t, int64(TrashFileID), parents[0].ID)
	require.Len(t, parents, 4)
	require.Equal(t, "same", retired.Name)
	require.ErrorIs(t, db.First(new(FileLocation), "file_id = ?", first.ID).Error, gorm.ErrRecordNotFound)
	require.ErrorIs(t, db.First(new(FileTrackingKey), "file_id = ?", first.ID).Error, gorm.ErrRecordNotFound)
	require.NoError(t, db.First(version, version.ID).Error)
	require.Equal(t, target.ID, version.FileID)
}

func TestTrashPlacementSuffixesOccupiedContainersAndBatchesLookups(t *testing.T) {
	// Force collisions independently of wall-clock checkpoint naming, including a non-directory occupant.
	ctx := context.Background()
	db, _ := newTestLibrary(t)
	checkpoint := &File{Name: "checkpoint", Kind: entity.FileKind_FILE_KIND_DIRECTORY}
	createFileRows(t, db, checkpoint)
	files := make([]*File, 0, 2*batchSize+1)
	for index := 0; index < cap(files); index++ {
		file := &File{Name: fmt.Sprintf("file-%d", index)}
		createFileRows(t, db, file)
		files = append(files, file)
	}
	name := strconv.FormatInt(files[0].ID, 10)
	createFileRows(t, db, &File{ParentID: checkpoint.ID, Name: name}, &File{ParentID: checkpoint.ID, Name: name + " (2)", Kind: entity.FileKind_FILE_KIND_DIRECTORY})
	queries := 0
	require.NoError(t, db.Callback().Query().Before("gorm:query").Register("count-trash-reads", func(tx *gorm.DB) {
		if tx.Statement.Table == "files" {
			queries++
		}
	}))
	require.NoError(t, db.Transaction(func(tx *gorm.DB) error { return placeInTrash(ctx, tx, checkpoint.ID, files) }))

	// One lookup per batch plus collision probes replaces a lookup for every File.
	require.LessOrEqual(t, queries, 5)
	folder := new(fileRow)
	require.NoError(t, db.First(folder, files[0].ParentID).Error)
	require.Equal(t, name+" (3)", folder.Name)
	for _, file := range files {
		var row fileRow
		require.NoError(t, db.First(&row, file.ID).Error)
		require.Equal(t, file.ParentID, row.ParentID)
		require.Equal(t, file.Name, row.Name)
	}
}

func TestRetirementPrimitivesRollBackTheirDependentRows(t *testing.T) {
	// A late delete failure must roll back both sides of either paired metadata retirement.
	for _, model := range []string{"file_locations", "file_versions"} {
		t.Run(model, func(t *testing.T) {
			db, lib := newTestLibrary(t)
			file := &File{Name: "file"}
			createFileRows(t, db, file)
			require.NoError(t, db.Create(&FileLocation{FileID: file.ID, LocationID: 1, Path: "original", Mode: 0644}).Error)
			require.NoError(t, db.Create(&FileTrackingKey{FileID: file.ID, LocationID: 1, Kind: TrackingNative, KeyValue: []byte("key")}).Error)
			version := &FileVersion{FileID: file.ID, Signature: []byte("saved")}
			require.NoError(t, db.Create(version).Error)
			date := int64(123)
			require.NoError(t, recordVersionArchives(db, version.ID, &date))
			failure := errors.New("retirement failed")
			require.NoError(t, db.Callback().Delete().Before("gorm:delete").Register("fail-retirement", func(tx *gorm.DB) {
				if tx.Statement.Table == model {
					tx.AddError(failure)
				}
			}))

			// Exercise the same primitives used by Remove/Merge and explicit version deletion.
			var err error
			if model == "file_locations" {
				err = db.Transaction(func(tx *gorm.DB) error { return retireOriginals(tx, []int64{file.ID}) })
			} else {
				_, err = lib.RemoveFileVersion(context.Background(), file.ID, version.ID, false)
			}
			require.ErrorIs(t, err, failure)
			require.NoError(t, db.First(new(FileLocation), "file_id = ?", file.ID).Error)
			require.NoError(t, db.First(new(FileTrackingKey), "file_id = ?", file.ID).Error)
			require.NoError(t, db.First(new(FileVersion), version.ID).Error)
			require.NoError(t, db.First(new(FileVersionArchive), "version_id = ?", version.ID).Error)
		})
	}
}
