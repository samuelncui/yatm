package legacy

import (
	"bytes"
	"context"
	"fmt"
	"io/fs"
	"testing"

	"github.com/samuelncui/yatm/library"
	"github.com/stretchr/testify/require"
)

func TestPrepareKeepsIndependentLegacyFilesWithDuplicateContent(t *testing.T) {
	// Keep a duplicate across migration pages to prevent page-local validation from missing it.
	ctx := context.Background()
	db := newLegacyTestDB(t)
	root := t.TempDir()
	files := make([]*legacyLibraryFile, 0, 257)
	for index := 1; index <= 257; index++ {
		files = append(files, &legacyLibraryFile{
			ID: int64(index), Name: fmt.Sprintf("file-%d", index), Mode: 0o644,
			Signature: []byte(fmt.Sprintf("opaque-%d", index)),
		})
	}
	files[256].Signature = files[0].Signature
	require.NoError(t, db.CreateInBatches(files, 256).Error)

	// Equal content does not merge independently organized legacy File IDs.
	report, err := Prepare(ctx, db, root)
	require.NoError(t, err)
	require.True(t, report.Success)
	var staged []*stagedLibraryFile
	require.NoError(t, db.Order("id").Find(&staged).Error)
	require.Len(t, staged, len(files))
	require.Equal(t, staged[0].Signature, staged[256].Signature)
	var versions int64
	require.NoError(t, db.Model(&stagedLibraryVersion{}).Count(&versions).Error)
	require.Zero(t, versions, "isolated signatures cannot manufacture archive history")

	// Preparation and its cleanup must leave the complete legacy File table unchanged.
	var stored []*legacyLibraryFile
	require.NoError(t, db.Order("id").Find(&stored).Error)
	require.Equal(t, files, stored)
	require.False(t, db.Migrator().HasTable("files_legacy"))
	require.NoError(t, Abort(db, root))
	stored = nil
	require.NoError(t, db.Order("id").Find(&stored).Error)
	require.Equal(t, files, stored)
}

func TestPreparePreservesOpaqueAndUnsetFileSignatures(t *testing.T) {
	// Legacy signatures are copied as identities, without interpreting their format or file facts.
	ctx := context.Background()
	db := newLegacyTestDB(t)
	root := t.TempDir()
	files := []*legacyLibraryFile{
		{ID: 1, Name: "nil-directory", Mode: uint32(fs.ModeDir | 0o755)},
		{ID: 2, Name: "empty-directory", Mode: uint32(fs.ModeDir | 0o755), Signature: []byte{}},
		{ID: 3, Name: "nil-file", Mode: 0o644},
		{ID: 4, Name: "empty-file", Mode: 0o644, Signature: []byte{}},
		{ID: 5, Name: "zero", Mode: 0o644, Signature: []byte{0}},
		{ID: 6, Name: "future", Mode: 0o644, Hash: []byte{3}, Size: 7, Signature: []byte{2, 255}},
		{ID: 7, Name: "full-width", Mode: 0o644, Signature: bytes.Repeat([]byte{255}, 256)},
	}
	require.NoError(t, db.Create(files).Error)
	var before []*legacyLibraryFile
	require.NoError(t, db.Order("id").Find(&before).Error)
	var unsetBefore int64
	require.NoError(t, db.Model(&legacyLibraryFile{}).Where("signature IS NULL").Count(&unsetBefore).Error)

	// The staging table preserves original facts until archive evidence is classified.
	report, err := Prepare(ctx, db, root)
	require.NoError(t, err)
	require.True(t, report.Success)
	var staged []*stagedLibraryFile
	require.NoError(t, db.Order("id").Find(&staged).Error)
	require.Len(t, staged, len(files))
	for index, file := range staged {
		require.Equal(t, files[index].ID, file.ID)
		if len(files[index].Signature) == 0 {
			require.Nil(t, file.Signature)
			continue
		}
		require.Equal(t, files[index].Signature, file.Signature)
	}
	var unset int64
	require.NoError(t, db.Model(&stagedLibraryFile{}).Where("signature IS NULL").Count(&unset).Error)
	require.Equal(t, int64(4), unset)
	require.NoError(t, Commit(ctx, db, root))
	require.NoError(t, library.New(db).AutoMigrate())
	require.False(t, db.Migrator().HasColumn(&library.File{}, "signature"))
	var versionCount int64
	require.NoError(t, db.Model(&library.FileVersion{}).Count(&versionCount).Error)
	require.Zero(t, versionCount)

	var legacy []*legacyLibraryFile
	require.NoError(t, db.Table("files_legacy").Order("id").Find(&legacy).Error)
	require.Equal(t, before, legacy)
	var unsetAfter int64
	require.NoError(t, db.Table("files_legacy").Where("signature IS NULL").Count(&unsetAfter).Error)
	require.Equal(t, unsetBefore, unsetAfter)
}
