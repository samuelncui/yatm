package executor

import (
	"context"
	"testing"

	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/internal/library"
	"github.com/samuelncui/yatm/internal/media/tape"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

func calibrationFixtureMedia(barcode string) *library.Media {
	return &library.Media{ID: 7, Kind: entity.MediaKind_MEDIA_KIND_TAPE, Identity: barcode,
		Profile: (&entity.TapeMediaProfile{Format: library.TapeFormatTarPAXV1, Tar: &entity.TarMediaProfile{
			InitializationId: "11111111-1111-4111-8111-111111111111", RecordMode: entity.TapeRecordMode_TAPE_RECORD_MODE_VARIABLE,
			RecordBytes: 4096,
		}}).Pack()}
}

func TestCalibrationDriveOpensAnyCartridgeWithoutTheLibrary(t *testing.T) {
	ctx := context.Background()
	exe, _, fixture, jobID := tarFactoryFixture(t)
	require.NoError(t, exe.AcquireTapeDevice(ctx, jobID, "/dev/nst0", nil))
	fixture.ejectOnUnload = true

	// A cartridge the Library does not know opens on the native path; Locate and Position use partition zero.
	drive, err := exe.OpenCalibrationDrive(ctx, jobID, "/dev/nst0")
	require.NoError(t, err)
	require.Equal(t, "load", fixture.securityEvents[0])
	require.Zero(t, drive.WrapBytes, "without a wrap map there is no end of data to size wraps by")
	require.Empty(t, drive.Endpoints)
	require.NoError(t, drive.Locate(ctx, 0))
	position, err := drive.Position(ctx)
	require.NoError(t, err)
	require.Zero(t, position)
	require.Len(t, fixture.locateCommands, 1)
	require.NoError(t, drive.Finish(ctx))
	require.Equal(t, 1, fixture.unloads)
	require.Equal(t, fixture.opens, fixture.closes)
	require.True(t, fixture.ejected)

	// The next Calibration loads the ejected cartridge itself; its preparation failure unloads it.
	fixture.positionUnsupported = true
	_, err = exe.OpenCalibrationDrive(ctx, jobID, "/dev/nst0")
	require.ErrorContains(t, err, "READ POSITION/LOCATE")
	require.Equal(t, 2, fixture.loads)
	require.Equal(t, 2, fixture.unloads)
	require.Equal(t, fixture.opens, fixture.closes)
}

func TestCalibrationTestDataOverwritesFromBeginningOfTape(t *testing.T) {
	ctx := context.Background()
	exe, _, fixture, jobID := tarFactoryFixture(t)
	require.NoError(t, exe.AcquireTapeDevice(ctx, jobID, "/dev/nst0", nil))
	fixture.records = []tarFixtureRecord{{data: []byte("old label")}, {mark: true}, {data: []byte("old member")}, {mark: true}}
	fixture.cursor = 3
	profile := &entity.TarMediaProfile{RecordMode: entity.TapeRecordMode_TAPE_RECORD_MODE_VARIABLE, RecordBytes: 4096}

	// Whatever the cartridge held is replaced from the beginning by zero records; this drive then
	// reports no wrap map, so the Job cannot calibrate, and the failure unloads the cartridge.
	_, err := exe.writeCalibrationTestData(ctx, jobID, "/dev/nst0", profile, 3*4096, logrus.New())
	require.ErrorContains(t, err, "wrap endpoint map, which is unavailable")
	require.Len(t, fixture.records, 3)
	for _, record := range fixture.records {
		require.Equal(t, make([]byte, 4096), record.data)
		require.False(t, record.mark)
	}
	require.NotContains(t, fixture.writeCompression, true, "test data is written with drive compression off")
	require.Equal(t, 1, fixture.synchronizations)
	require.Zero(t, fixture.filemarks)
	require.Equal(t, 1, fixture.unloads)
	require.Equal(t, 1, fixture.loads, "only the first descriptor loads")
	require.Equal(t, "load", fixture.securityEvents[0])
	require.Zero(t, fixture.barcodeWrites)
	require.Equal(t, fixture.opens, fixture.closes)
}

func TestCalibrationTestDataRefusesRegisteredBarcode(t *testing.T) {
	ctx := context.Background()
	exe, _, fixture, jobID := tarFactoryFixture(t)
	require.NoError(t, exe.AcquireTapeDevice(ctx, jobID, "/dev/nst0", nil))
	_, err := exe.Lib().CreateMedia(ctx, calibrationFixtureMedia("ABC001"))
	require.NoError(t, err)
	fixture.records = []tarFixtureRecord{{data: []byte("label")}, {mark: true}}

	_, err = exe.writeCalibrationTestData(ctx, jobID, "/dev/nst0",
		&entity.TarMediaProfile{RecordMode: entity.TapeRecordMode_TAPE_RECORD_MODE_VARIABLE, RecordBytes: 4096}, 4096, logrus.New())
	require.ErrorContains(t, err, "Media in the Library and test data would overwrite it")
	require.Zero(t, fixture.writes)
	require.Zero(t, fixture.unloads, "a refused registered cartridge stays loaded")
	require.Len(t, fixture.records, 2)
	require.Equal(t, fixture.opens, fixture.closes)
}

func TestCalibrationPartitionAndNominalWrapBytes(t *testing.T) {
	// A tar cartridge has one partition; an LTFS one calibrates in its data partition, which begins at wrap 4.
	tar := []tape.WrapEndpoint{{Wrap: 0, Object: 36752}, {Wrap: 1, Object: 55768}}
	endpoints, err := calibrationPartition(tar)
	require.NoError(t, err)
	require.Equal(t, tar, endpoints)
	ltfs := []tape.WrapEndpoint{{Wrap: 0, Object: 12},
		{Partition: 1, Wrap: 4, Object: 1000}, {Partition: 1, Wrap: 5, Object: 2000}, {Partition: 1, Wrap: 6, Object: 2500}}
	endpoints, err = calibrationPartition(ltfs)
	require.NoError(t, err)
	require.Equal(t, ltfs[1:], endpoints)

	// One wrap's bytes are the recorded density's native capacity over its wraps; unknown stays zero.
	require.InDelta(t, 18.75e9, nominalWrapBytes(proto.Uint32(0x58)), 1)
	require.InDelta(t, 40000e9/472, nominalWrapBytes(proto.Uint32(0x63)), 1)
	require.Zero(t, nominalWrapBytes(nil))
	require.Zero(t, nominalWrapBytes(proto.Uint32(0x99)))

	// The sequence assumes a forward first wrap and contiguous, growing boundaries.
	_, err = calibrationPartition([]tape.WrapEndpoint{{Wrap: 1, Object: 10}, {Wrap: 2, Object: 20}})
	require.ErrorContains(t, err, "forward wrap")
	_, err = calibrationPartition([]tape.WrapEndpoint{{Wrap: 0, Object: 10}, {Wrap: 2, Object: 20}})
	require.ErrorContains(t, err, "not contiguous")
	_, err = calibrationPartition([]tape.WrapEndpoint{{Wrap: 0, Object: 10}, {Wrap: 1, Object: 10}})
	require.ErrorContains(t, err, "empty or reversed")
}

func TestCalibrationCancelledWhileLoadingUnloadsTheCartridge(t *testing.T) {
	exe, _, fixture, jobID := tarFactoryFixture(t)
	require.NoError(t, exe.AcquireTapeDevice(context.Background(), jobID, "/dev/nst0", nil))

	// The LOAD may have completed before the cancellation was seen; the cartridge is unloaded anyway.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := exe.OpenCalibrationDrive(ctx, jobID, "/dev/nst0")
	require.ErrorIs(t, err, context.Canceled)
	require.Equal(t, 1, fixture.unloads)
	require.Equal(t, fixture.opens, fixture.closes)
}
