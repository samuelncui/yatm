package library

import (
	"bytes"
	"context"
	"fmt"
	"testing"

	"github.com/google/uuid"
	"github.com/samuelncui/yatm/entity"
	"github.com/stretchr/testify/require"
)

func TestFileOperationResultsRoundTripAsHistoricalProvenance(t *testing.T) {
	// More than one export page preserves each item's business key without authorizing replay after import.
	ctx := context.Background()
	_, lib := newTestLibrary(t)
	location := onlineTestSource(t, lib)
	operation := uuid.NewString()
	for item := int64(1); item <= batchSize+2; item++ {
		require.NoError(t, lib.PublishFileOperation(ctx, &FileOperationResult{
			OperationID: operation, ItemID: item, LocationID: location.ID, BindingToken: location.BindingToken,
			Kind: entity.FileOperationKind_MAKE_DIRECTORY, TargetPath: fmt.Sprintf("folder-%03d", item),
		}))
	}
	var backup bytes.Buffer
	require.NoError(t, lib.Export(ctx, &backup, []entity.LibraryEntityType{
		entity.LibraryEntityType_FILE, entity.LibraryEntityType_LOCATION, entity.LibraryEntityType_FILE_LOCATION,
	}))
	db, imported := newTestLibrary(t)
	require.NoError(t, imported.Import(ctx, bytes.NewReader(backup.Bytes())))
	var results []*FileOperationResult
	require.NoError(t, db.Order("operation_id, item_id").Find(&results).Error)
	require.Len(t, results, batchSize+2)
	for index, result := range results {
		require.Equal(t, operation, result.OperationID)
		require.EqualValues(t, index+1, result.ItemID)
		require.Equal(t, location.ID, result.LocationID)
		require.Empty(t, result.BindingToken)
		require.Positive(t, result.CompletedAt)
	}
	confirmed, err := imported.GetOnlineSource(ctx, location.ID)
	require.NoError(t, err)
	require.Equal(t, entity.OnlineBinding_UNCONFIRMED, confirmed.Binding)
	require.NotEqual(t, location.BindingToken, confirmed.BindingToken)
	require.Error(t, imported.PublishFileOperation(ctx, &FileOperationResult{
		OperationID: operation, ItemID: 1, LocationID: location.ID, BindingToken: location.BindingToken,
		Kind: entity.FileOperationKind_MAKE_DIRECTORY, TargetPath: "folder-001",
	}))

	// Unregistration removes access, not historical operation provenance.
	current, err := lib.GetOnlineSource(ctx, location.ID)
	require.NoError(t, err)
	require.NoError(t, lib.DeleteOnlineSource(ctx, location.ID, current.Revision))
	backup.Reset()
	require.NoError(t, lib.Export(ctx, &backup, []entity.LibraryEntityType{
		entity.LibraryEntityType_FILE, entity.LibraryEntityType_LOCATION, entity.LibraryEntityType_FILE_LOCATION,
	}))
	historyDB, history := newTestLibrary(t)
	require.NoError(t, history.Import(ctx, bytes.NewReader(backup.Bytes())))
	results = nil
	require.NoError(t, historyDB.Order("item_id").Find(&results).Error)
	require.Len(t, results, batchSize+2)
	require.Empty(t, results[0].BindingToken)
	_, err = history.GetOnlineSource(ctx, location.ID)
	require.Error(t, err)
	require.Error(t, history.PublishFileOperation(ctx, &FileOperationResult{
		OperationID: operation, ItemID: 1, LocationID: location.ID, BindingToken: location.BindingToken,
		Kind: entity.FileOperationKind_MAKE_DIRECTORY, TargetPath: "folder-001",
	}))
}
