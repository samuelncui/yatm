package library

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestMergeFileMembersStreamsSourcesInDeterministicOrder(t *testing.T) {
	// Seed more than one stream page without constructing a full group in memory.
	db, lib := newTestLibrary(t)
	ctx := context.Background()
	target := File{Name: "target"}
	createFileRows(t, db, &target)
	require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
		for i := int64(0); i < 300; i++ {
			source := File{ID: 1000 + i, Name: fmt.Sprintf("source-%d", i)}
			if err := createFileRow(tx, &source); err != nil {
				return err
			}
			if err := tx.Create(&FileVersion{FileID: source.ID, Signature: []byte("shared"), Size: 4, Mode: uint32(100 + i)}).Error; err != nil {
				return err
			}
		}
		return nil
	}))

	// Each ordered batch is discarded before constructing the next.
	calls := 0
	merged, err := lib.MergeFileMembers(ctx, target.ID, func(yield func([]int64) error) error {
		for start := int64(0); start < 300; start += 100 {
			ids := make([]int64, 0, 100)
			for i := start; i < start+100; i++ {
				ids = append(ids, 1000+i)
			}
			calls++
			if err := yield(ids); err != nil {
				return err
			}
		}
		return nil
	}, false)
	require.NoError(t, err)
	require.Equal(t, int64(300), merged)
	require.Equal(t, 3, calls)
	var versions []FileVersion
	require.NoError(t, db.Where("file_id = ?", target.ID).Find(&versions).Error)
	require.Len(t, versions, 1)
	require.Equal(t, uint32(100), versions[0].Mode)
	var retired int64
	require.NoError(t, db.Model(ModelFile).Where("id >= ? AND id < ? AND parent_id <> 0", 1000, 1300).Count(&retired).Error)
	require.Equal(t, int64(300), retired)
}

func TestMergeFileMembersRollsBackEarlierBatches(t *testing.T) {
	// A later stream failure must restore earlier metadata, version ownership and source organization.
	db, lib := newTestLibrary(t)
	ctx := context.Background()
	target, first := File{Name: "target", Note: "target"}, File{Name: "first", Note: "first"}
	createFileRows(t, db, &target, &first)
	version := FileVersion{FileID: first.ID, Signature: []byte("saved"), Size: 4}
	require.NoError(t, db.Create(&version).Error)
	failure := errors.New("later source page failed")
	merged, err := lib.MergeFileMembers(ctx, target.ID, func(yield func([]int64) error) error {
		if err := yield([]int64{first.ID}); err != nil {
			return err
		}
		return failure
	}, false)
	require.ErrorIs(t, err, failure)
	require.Zero(t, merged)
	require.NoError(t, db.First(&version, version.ID).Error)
	require.Equal(t, first.ID, version.FileID)
	var firstRow fileRow
	require.NoError(t, db.First(&firstRow, first.ID).Error)
	firstRow.apply(&first)
	require.Zero(t, first.ParentID)
	var targetRow fileRow
	require.NoError(t, db.First(&targetRow, target.ID).Error)
	targetRow.apply(&target)
	require.Equal(t, "target", target.Note)

}
