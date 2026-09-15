package library

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/google/uuid"
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
	publication.Result.OperationID = uuid.NewString()
	publication.Result.Path = "copies/photo.jpg"
	publication.Reconnect = false
	independent, err := lib.PublishRestore(ctx, publication)
	require.NoError(t, err)
	unversioned := &File{Name: "unversioned.txt", Kind: entity.FileKind_FILE_KIND_REGULAR}
	require.NoError(t, db.Create(unversioned).Error)

	// Keep every copy-health state in the snapshot, with installation-local Job links to discard.
	media := &Media{
		Kind: entity.MediaKind_MEDIA_KIND_TAPE, Identity: "IMPORT1",
		Profile: (&entity.TapeMediaProfile{Format: TapeFormatLTFSV1}).Pack(),
	}
	require.NoError(t, db.Create(media).Error)
	for index, health := range []entity.PositionHealth{
		entity.PositionHealth_POSITION_HEALTH_UNKNOWN, entity.PositionHealth_HEALTHY,
		entity.PositionHealth_DAMAGED, entity.PositionHealth_MISSING, entity.PositionHealth_UNREADABLE,
	} {
		require.NoError(t, db.Create(&Position{
			MediaID: media.ID, Path: fmt.Sprintf("copy-%d.jpg", index), Mode: 0640,
			Signature: reconnected.Signature, Hash: reconnected.ExpectedHash, Size: reconnected.ExpectedSize,
			Health: health, CheckedAt: int64(1000 + index), HealthJobID: int64(70 + index),
		}).Error)
	}
	return lib, reconnected, independent, unversioned
}

func exportRestoreImportSnapshot(t *testing.T, lib *Library) []byte {
	t.Helper()
	var snapshot bytes.Buffer
	require.NoError(t, lib.Export(context.Background(), &snapshot, []entity.LibraryEntityType{
		entity.LibraryEntityType_FILE, entity.LibraryEntityType_MEDIA, entity.LibraryEntityType_POSITION,
		entity.LibraryEntityType_LOCATION, entity.LibraryEntityType_FILE_LOCATION,
	}))
	return snapshot.Bytes()
}

func TestRestoreImportRoundTripAcrossCompositeCursorPages(t *testing.T) {
	// Seed enough versions and result items to cross pages within and between operation IDs.
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
	results := make([]*RestoreResult, 0, len(versions))
	for index, version := range versions {
		operation := "00000000-0000-4000-8000-000000000001"
		if index >= batchSize+1 {
			operation = "ffffffff-ffff-4fff-8fff-ffffffffffff"
		}
		results = append(results, &RestoreResult{
			OperationID: operation, ItemID: version.ID, SourceFileID: version.FileID, SourceVersionID: version.ID,
			LocationID: reconnected.LocationID, BindingToken: reconnected.BindingToken,
			Path: fmt.Sprintf("ignored/output-%03d.jpg", index), Signature: version.Signature,
			ExpectedHash: version.Hash, ExpectedSize: version.Size, ActualHash: version.Hash, ActualSize: version.Size,
			Outcome: RestoreIgnored, RestoredAt: int64(2000 + index),
		})
	}
	require.NoError(t, source.db.CreateInBatches(results, batchSize).Error)
	var expected []*RestoreResult
	require.NoError(t, source.db.Order("operation_id, item_id").Find(&expected).Error)
	require.Len(t, expected, 2*batchSize+3)

	// Import all entity groups together and retain every historical content and outcome fact.
	snapshot := exportRestoreImportSnapshot(t, source)
	db, target := newTestLibrary(t)
	require.NoError(t, target.Import(ctx, bytes.NewReader(snapshot)))
	var actual []*RestoreResult
	require.NoError(t, db.Order("operation_id, item_id").Find(&actual).Error)
	for _, result := range expected {
		result.BindingToken = ""
	}
	require.Equal(t, expected, actual)
	var versionCount int64
	require.NoError(t, db.Model(&FileVersion{}).Count(&versionCount).Error)
	require.EqualValues(t, len(versions)+2, versionCount)

	// Imported paths and history cannot authorize local reads or a previous installation's retry.
	location, err := target.GetOnlineSource(ctx, reconnected.LocationID)
	require.NoError(t, err)
	require.Equal(t, entity.OnlineBinding_UNCONFIRMED, location.Binding)
	require.NotEmpty(t, location.BindingToken)
	require.NotEqual(t, reconnected.BindingToken, location.BindingToken)
	var originals []*FileLocation
	require.NoError(t, db.Find(&originals).Error)
	require.Len(t, originals, 2)
	for _, original := range originals {
		require.Empty(t, original.ObservedBindingToken)
		require.False(t, original.CurrentBinding(location))
	}
	for _, result := range []*RestoreResult{actual[0], actual[len(actual)-1]} {
		_, err := target.GetRestoreResult(ctx, result.OperationID, result.ItemID)
		require.ErrorContains(t, err, "cannot authorize a local retry")
	}

	// Copy health remains historical evidence, while numeric Job links are installation-local.
	var previous, imported []*Position
	require.NoError(t, source.db.Where("is_dir = ?", false).Order("id").Find(&previous).Error)
	require.NoError(t, db.Where("is_dir = ?", false).Order("id").Find(&imported).Error)
	require.Len(t, imported, len(previous))
	for index, position := range imported {
		require.Equal(t, previous[index].Health, position.Health)
		require.Equal(t, previous[index].CheckedAt, position.CheckedAt)
		require.Equal(t, previous[index].Signature, position.Signature)
		require.Equal(t, previous[index].Hash, position.Hash)
		require.Zero(t, position.HealthJobID)
	}

	// A second full export/import preserves the normalized provenance without dropping later pages.
	secondDB, second := newTestLibrary(t)
	require.NoError(t, second.Import(ctx, bytes.NewReader(exportRestoreImportSnapshot(t, target))))
	var repeated []*RestoreResult
	require.NoError(t, secondDB.Order("operation_id, item_id").Find(&repeated).Error)
	require.Equal(t, actual, repeated)
}

func TestRestoreImportRejectsContradictoryFileAssociationAtomically(t *testing.T) {
	// Corrupt only one late provenance row while keeping its referenced Files and source version valid.
	source, reconnected, independent, unversioned := restoreImportFixture(t)
	snapshot := exportRestoreImportSnapshot(t, source)
	for _, test := range []struct {
		name, operation, errorText string
		resultFileID               int64
	}{
		{"reconnected to another File", reconnected.OperationID, "outcome disagrees", independent.ResultFileID},
		{"new File is the source", independent.OperationID, "outcome disagrees", independent.SourceFileID},
		{"result File lacks saved content", independent.OperationID, "invalid imported Restore result references", unversioned.ID},
	} {
		t.Run(test.name, func(t *testing.T) {
			// Rewrite the selected JSON record without changing earlier entity groups or framing.
			var corrupt bytes.Buffer
			encoder := json.NewEncoder(&corrupt)
			changed := 0
			for _, line := range bytes.Split(bytes.TrimSpace(snapshot), []byte("\n")) {
				var record jsonlInputRecord
				require.NoError(t, json.Unmarshal(line, &record))
				if record.Type == recordTypeRestoreResult {
					var result RestoreResult
					require.NoError(t, json.Unmarshal(record.Data, &result))
					if result.OperationID == test.operation {
						result.ResultFileID = test.resultFileID
						var err error
						record.Data, err = json.Marshal(result)
						require.NoError(t, err)
						changed++
					}
				}
				require.NoError(t, encoder.Encode(&record))
			}
			require.Equal(t, 1, changed)

			// Compare complete snapshots so rollback includes organization, originals, versions and health.
			target, _, _, _ := restoreImportFixture(t)
			before := exportRestoreImportSnapshot(t, target)
			require.ErrorContains(t, target.Import(context.Background(), &corrupt), test.errorText)
			require.Equal(t, before, exportRestoreImportSnapshot(t, target))
		})
	}
}
