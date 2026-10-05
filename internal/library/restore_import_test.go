package library

import (
	"bytes"
	"context"
	"fmt"
	"testing"

	"github.com/samuelncui/yatm/entity"
	"github.com/stretchr/testify/require"
)

func restoreImportFixture(t *testing.T) (*Library, *RestoreResult, *RestoreResult, *File) {
	t.Helper()
	// Publish both linked outcomes through the same boundary used by successful Restore Jobs.
	ctx := context.Background()
	db, lib, _, _, publication := restorePublicationFixture(t)
	reconnected, err := lib.PublishRestore(ctx, publication)
	require.NoError(t, err)
	publication.Result.Path = "copies/photo.jpg"
	publication.Reconnect = false
	independent, err := lib.PublishRestore(ctx, publication)
	require.NoError(t, err)
	unversioned := &File{Name: "unversioned.txt", Kind: entity.FileKind_FILE_KIND_REGULAR}
	createFileRows(t, db, unversioned)

	// Keep every copy-health state in the snapshot, with installation-local Job links to discard.
	media := &Media{
		Kind: entity.MediaKind_MEDIA_KIND_TAPE, Identity: "IMPORT1",
		Profile: (&entity.TapeMediaProfile{Format: TapeFormatLTFSV1}).Pack(),
	}
	require.NoError(t, db.Create(media).Error)
	for index, health := range []entity.PositionHealth{
		entity.PositionHealth_POSITION_HEALTH_UNKNOWN, entity.PositionHealth_POSITION_HEALTH_HEALTHY,
		entity.PositionHealth_POSITION_HEALTH_DAMAGED, entity.PositionHealth_POSITION_HEALTH_MISSING, entity.PositionHealth_POSITION_HEALTH_UNREADABLE,
	} {
		require.NoError(t, db.Create(&Position{
			MediaID: media.ID, Path: fmt.Sprintf("copy-%d.jpg", index), Mode: 0640,
			Signature: reconnected.Signature, Hash: reconnected.ExpectedHash, Size: reconnected.ExpectedSize,
			Health: health, CheckedAtNS: int64(1000 + index), HealthJobID: int64(70 + index),
		}).Error)
	}
	return lib, reconnected, independent, unversioned
}

func exportRestoreImportSnapshot(t *testing.T, lib *Library) []byte {
	t.Helper()
	var snapshot bytes.Buffer
	require.NoError(t, lib.Export(context.Background(), &snapshot, []entity.LibraryEntityType{
		entity.LibraryEntityType_LIBRARY_ENTITY_TYPE_FILE, entity.LibraryEntityType_LIBRARY_ENTITY_TYPE_MEDIA, entity.LibraryEntityType_LIBRARY_ENTITY_TYPE_POSITION,
		entity.LibraryEntityType_LIBRARY_ENTITY_TYPE_LOCATION, entity.LibraryEntityType_LIBRARY_ENTITY_TYPE_FILE_LOCATION,
	}))
	return snapshot.Bytes()
}

func TestRestoreContentAndOriginalsRoundTripAcrossPages(t *testing.T) {
	// Seed enough restored content versions to cross export pages.
	ctx := context.Background()
	source, reconnected, _, _ := restoreImportFixture(t)
	versions := make([]*FileVersion, 0, 2*batchSize+1)
	for index := 0; index < 2*batchSize+1; index++ {
		versions = append(versions, &FileVersion{
			FileID: reconnected.SourceFileID, Signature: []byte(fmt.Sprintf("opaque-%03d\x00", index)),
			Hash: reconnected.ExpectedHash, Size: reconnected.ExpectedSize, Mode: 0640,
		})
	}
	require.NoError(t, source.db.CreateInBatches(versions, batchSize).Error)
	// Import every content version and current original association.
	snapshot := exportRestoreImportSnapshot(t, source)
	db, target := newTestLibrary(t)
	require.NoError(t, target.Import(ctx, bytes.NewReader(snapshot), false))
	require.False(t, db.Migrator().HasTable("restore_results"))
	var versionCount int64
	require.NoError(t, db.Model(&FileVersion{}).Count(&versionCount).Error)
	require.EqualValues(t, len(versions)+2, versionCount)

	// Imported originals keep their associations and are usable immediately.
	location, err := target.GetLocation(ctx, reconnected.LocationID)
	require.NoError(t, err)
	require.Equal(t, reconnected.LocationID, location.ID)
	var originals []*FileLocation
	require.NoError(t, db.Find(&originals).Error)
	require.Len(t, originals, 2)
	// Copy health remains historical evidence, while numeric Job links are installation-local.
	var previous, imported []*Position
	require.NoError(t, source.db.Where("is_dir = ?", false).Order("id").Find(&previous).Error)
	require.NoError(t, db.Where("is_dir = ?", false).Order("id").Find(&imported).Error)
	require.Len(t, imported, len(previous))
	for index, position := range imported {
		require.Equal(t, previous[index].Health, position.Health)
		require.Equal(t, previous[index].CheckedAtNS, position.CheckedAtNS)
		require.Equal(t, previous[index].Signature, position.Signature)
		require.Equal(t, previous[index].Hash, position.Hash)
		require.Zero(t, position.HealthJobID)
	}

	// A second export/import preserves every version without dropping later pages.
	secondDB, second := newTestLibrary(t)
	require.NoError(t, second.Import(ctx, bytes.NewReader(exportRestoreImportSnapshot(t, target)), false))
	var secondVersionCount int64
	require.NoError(t, secondDB.Model(&FileVersion{}).Count(&secondVersionCount).Error)
	require.Equal(t, versionCount, secondVersionCount)
}
