package media

import (
	"context"
	"testing"

	"github.com/samuelncui/acp"
	"github.com/samuelncui/yatm/entity"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

func TestTarStorageSnapshotsAndFinalValidation(t *testing.T) {
	// One real target owns its snapshot; callers cannot modify the receipt before or after finalization.
	device := newTarWritePositionDevice()
	options := tarTestOptions(t, false, 4096)
	options.Navigator, options.TapeFile = device, 3
	device.start.File = 3
	session, err := NewTarWriteSession(device, options)
	require.NoError(t, err)
	target, err := session.Target("member", 1)
	require.NoError(t, err)
	require.Nil(t, target.Storage())
	writer, err := target.Content.Open(context.Background(), acp.ContentMetadata{
		Info: tarWritePositionInfo{tarTestInfo{name: "member", size: 1}},
	})
	require.NoError(t, err)
	_, err = writer.Write([]byte{1})
	require.NoError(t, err)
	require.NoError(t, writer.Finish(nil))
	staged := target.Storage()
	require.EqualValues(t, 3, staged.Metadata.GetTar().TapeFile)
	require.Zero(t, staged.Metadata.GetTar().HeaderOffsetBytes)
	changed := target.Storage()
	changed.Metadata.GetTar().TapeFile = 99
	changed.Order[0] = 0xff
	require.True(t, proto.Equal(staged, target.Storage()))
	result, err := session.Finalize(context.Background(), nil)
	require.NoError(t, err)
	require.Nil(t, result.Inventory)
	final, err := result.FinalizeStorage(staged)
	require.NoError(t, err)
	require.NotNil(t, final)
	require.NotSame(t, staged, final)
	final.Metadata.GetTar().Header.LogicalObject++
	require.True(t, proto.Equal(staged, target.Storage()))

	// Missing or contradictory input is distinct from a valid but unaccepted tail.
	for _, test := range []struct {
		name   string
		change func(*entity.StoragePosition) *entity.StoragePosition
	}{
		{"absent", func(*entity.StoragePosition) *entity.StoragePosition { return nil }},
		{"native", func(value *entity.StoragePosition) *entity.StoragePosition { value.Metadata = nil; return value }},
		{"foreign archive", func(value *entity.StoragePosition) *entity.StoragePosition {
			value.Metadata.GetTar().TapeFile++
			value.Order = entity.TarStorageOrder(value.Metadata.GetTar())
			return value
		}},
		{"wrong order", func(value *entity.StoragePosition) *entity.StoragePosition { value.Order = nil; return value }},
		{"unpaired anchor", func(value *entity.StoragePosition) *entity.StoragePosition {
			value.Metadata.GetTar().End = nil
			return value
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := result.FinalizeStorage(test.change(proto.Clone(staged).(*entity.StoragePosition)))
			require.Error(t, err)
		})
	}
}

func TestNativeStorageStillRequiresExistingInventory(t *testing.T) {
	// Native targets supply no early receipt and a native result cannot turn absence into confirmation.
	require.Nil(t, (TargetRef{Path: "native"}).Storage())
	result := &WriteResult{}
	storage, err := result.FinalizeStorage(nil)
	require.NoError(t, err)
	require.Nil(t, storage)
	native := &entity.StoragePosition{Metadata: (&entity.LtfsMetadata{}).Pack()}
	storage, err = result.FinalizeStorage(native)
	require.NoError(t, err)
	require.Same(t, native, storage)
}
