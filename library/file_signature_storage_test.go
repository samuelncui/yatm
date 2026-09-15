package library

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/samuelncui/yatm/entity"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestFileVersionSignatureUniquenessIsPerFile(t *testing.T) {
	db, lib := newTestLibrary(t)
	require.NoError(t, db.Create([]*File{{ID: 1, Name: "first", Note: "keep"}, {ID: 2, Name: "second"}}).Error)
	first := &FileVersion{FileID: 1, Signature: []byte("opaque"), Size: 7}
	require.NoError(t, db.Create(first).Error)
	require.NoError(t, db.Create(&FileVersion{FileID: 2, Signature: first.Signature, Size: 7}).Error)
	require.Error(t, db.Session(&gorm.Session{SkipHooks: true}).Create(&FileVersion{FileID: 1, Signature: first.Signature}).Error)
	require.NoError(t, db.Save(first).Error)
	require.NoError(t, lib.AutoMigrate())
	require.Error(t, db.Create(&FileVersion{FileID: 1, Signature: first.Signature}).Error)
	kept, err := lib.GetFile(context.Background(), 1)
	require.NoError(t, err)
	require.Equal(t, "keep", kept.Note)
	require.False(t, db.Migrator().HasColumn(ModelFile, "signature"))
}

func TestOriginalAndPositionEmptySignaturesStoredAsNull(t *testing.T) {
	db, lib := newTestLibrary(t)
	location := onlineTestSource(t, lib)
	require.NoError(t, db.Create([]*File{{ID: 1, Name: "one"}, {ID: 2, Name: "two"}}).Error)
	rows := []*FileLocation{{FileID: 1, LocationID: location.ID, Path: "one"}, {FileID: 2, LocationID: location.ID, Path: "two", Signature: []byte{}}}
	require.NoError(t, db.Create(rows).Error)
	require.NoError(t, db.Create([]*Position{{MediaID: 1, Path: "one"}, {MediaID: 1, Path: "two", Signature: []byte{}}}).Error)
	var count int64
	require.NoError(t, db.Model(&FileLocation{}).Where("signature IS NULL").Count(&count).Error)
	require.EqualValues(t, 2, count)
	require.NoError(t, db.Model(&Position{}).Where("signature IS NULL").Count(&count).Error)
	require.EqualValues(t, 2, count)
	rows[0].Signature = []byte("temporary")
	require.NoError(t, db.Save(rows[0]).Error)
	rows[0].Signature = []byte{}
	require.NoError(t, db.Save(rows[0]).Error)
	require.NoError(t, db.Model(&FileLocation{}).Where("signature IS NULL").Count(&count).Error)
	require.EqualValues(t, 2, count)
}

func TestLibraryOpaqueVersionSignaturesRoundTrip(t *testing.T) {
	ctx := context.Background()
	db, source := newTestLibrary(t)
	encoded, err := NewFileSignature(bytes.Repeat([]byte{1}, 32), 99)
	require.NoError(t, err)
	signatures := [][]byte{{0}, {2, 0, 255}, bytes.Repeat([]byte{255}, 256), encoded}
	for index, signature := range signatures {
		id := int64(index + 1)
		require.NoError(t, db.Create(&File{ID: id, Name: fmt.Sprint(index)}).Error)
		require.NoError(t, db.Create(&FileVersion{FileID: id, Signature: signature, Hash: []byte{2}, Size: 7}).Error)
	}
	var snapshot bytes.Buffer
	require.NoError(t, source.Export(ctx, &snapshot, []entity.LibraryEntityType{entity.LibraryEntityType_FILE}))
	targetDB, target := newTestLibrary(t)
	require.NoError(t, target.Import(ctx, &snapshot))
	var versions []*FileVersion
	require.NoError(t, targetDB.Order("file_id").Find(&versions).Error)
	require.Len(t, versions, len(signatures))
	for index, version := range versions {
		require.Equal(t, signatures[index], version.Signature)
	}
}

func TestLibraryImportVersionConflictRollsBackAcrossBatches(t *testing.T) {
	ctx := context.Background()
	db, lib := newTestLibrary(t)
	require.NoError(t, db.Create(&File{ID: 900, Name: "existing", Note: "keep"}).Error)
	require.NoError(t, db.Create(&FileTag{FileID: 900, Tag: "kept"}).Error)
	var snapshot bytes.Buffer
	encoder := json.NewEncoder(&snapshot)
	require.NoError(t, encoder.Encode(jsonlOutputRecord{Type: recordTypeHeader, Format: jsonlFormat, Version: jsonlFormatVersion, Entities: []string{recordTypeFile, recordTypeFileVersion}}))
	require.NoError(t, encoder.Encode(jsonlOutputRecord{Type: recordTypeFile, Data: &File{ID: 1, Name: "replacement"}}))
	for index := 0; index <= batchSize; index++ {
		signature := []byte(fmt.Sprint(index))
		if index == batchSize {
			signature = []byte("0")
		}
		require.NoError(t, encoder.Encode(jsonlOutputRecord{Type: recordTypeFileVersion, Data: &FileVersion{ID: int64(index + 1), FileID: 1, Signature: signature}}))
	}
	require.NoError(t, encoder.Encode(jsonlOutputRecord{Type: recordTypeEnd}))
	require.Error(t, lib.Import(ctx, &snapshot))
	kept, err := lib.GetFile(ctx, 900)
	require.NoError(t, err)
	require.Equal(t, "keep", kept.Note)
	tags, err := lib.MGetFileTags(ctx, 900)
	require.NoError(t, err)
	require.Equal(t, []string{"kept"}, tags[900])
	var count int64
	require.NoError(t, db.Model(&FileVersion{}).Count(&count).Error)
	require.Zero(t, count)
}
