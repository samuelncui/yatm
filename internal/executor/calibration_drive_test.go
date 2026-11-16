package executor

import (
	"context"
	"testing"

	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/internal/library"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/require"
)

func calibrationFixtureMedia(barcode string, compressed bool) *library.Media {
	return &library.Media{ID: 7, Kind: entity.MediaKind_MEDIA_KIND_TAPE, Identity: barcode,
		Profile: (&entity.TapeMediaProfile{Format: library.TapeFormatTarPAXV1, Tar: &entity.TarMediaProfile{
			InitializationId: "11111111-1111-4111-8111-111111111111", RecordMode: entity.TapeRecordMode_TAPE_RECORD_MODE_VARIABLE,
			RecordBytes: 4096, HardwareCompression: compressed,
		}}).Pack()}
}

func TestCalibrationDriveChecksMediaBeforeMotion(t *testing.T) {
	ctx := context.Background()
	exe, _, fixture, jobID := tarFactoryFixture(t)
	require.NoError(t, exe.AcquireTapeDevice(ctx, jobID, "/dev/nst0", nil))

	// Compressed records are refused before the device is opened.
	_, err := exe.OpenCalibrationDrive(ctx, jobID, "/dev/nst0", calibrationFixtureMedia("ABC001", true))
	require.ErrorContains(t, err, "hardware compression off")
	require.Zero(t, fixture.opens)

	// Another cartridge fails the tar read's barcode check without motion or unload.
	_, err = exe.OpenCalibrationDrive(ctx, jobID, "/dev/nst0", calibrationFixtureMedia("XYZ999", false))
	require.ErrorContains(t, err, "barcode differs")
	require.Equal(t, fixture.opens, fixture.closes)
	require.Empty(t, fixture.locateCommands)
	require.Zero(t, fixture.unloads)

	// The matching cartridge opens on the native path; Locate and Position use partition zero.
	drive, err := exe.OpenCalibrationDrive(ctx, jobID, "/dev/nst0", calibrationFixtureMedia("ABC001", false))
	require.NoError(t, err)
	require.Equal(t, uint32(4096), drive.RecordBytes)
	require.Empty(t, drive.Endpoints)
	require.NoError(t, drive.Locate(ctx, 0))
	position, err := drive.Position(ctx)
	require.NoError(t, err)
	require.Zero(t, position)
	require.Len(t, fixture.locateCommands, 1)
	require.NoError(t, drive.Finish(ctx))
	require.Equal(t, 1, fixture.unloads)
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
	// reports no wrap map, so the Job cannot calibrate and stops without unloading.
	_, err := exe.writeCalibrationTestData(ctx, jobID, "/dev/nst0", profile, 3*4096, logrus.New())
	require.ErrorContains(t, err, "wrap endpoint map, which is unavailable")
	require.Len(t, fixture.records, 3)
	for _, record := range fixture.records {
		require.Equal(t, make([]byte, 4096), record.data)
		require.False(t, record.mark || record.compressed)
	}
	require.Equal(t, 1, fixture.synchronizations)
	require.Zero(t, fixture.filemarks)
	require.Zero(t, fixture.unloads)
	require.Zero(t, fixture.barcodeWrites)
	require.Equal(t, fixture.opens, fixture.closes)
}

func TestCalibrationTestDataRefusesRegisteredBarcode(t *testing.T) {
	ctx := context.Background()
	exe, _, fixture, jobID := tarFactoryFixture(t)
	require.NoError(t, exe.AcquireTapeDevice(ctx, jobID, "/dev/nst0", nil))
	_, err := exe.Lib().CreateMedia(ctx, calibrationFixtureMedia("ABC001", false))
	require.NoError(t, err)
	fixture.records = []tarFixtureRecord{{data: []byte("label")}, {mark: true}}

	_, err = exe.writeCalibrationTestData(ctx, jobID, "/dev/nst0",
		&entity.TarMediaProfile{RecordMode: entity.TapeRecordMode_TAPE_RECORD_MODE_VARIABLE, RecordBytes: 4096}, 4096, logrus.New())
	require.ErrorContains(t, err, "Tape already exists in the Library")
	require.Zero(t, fixture.writes)
	require.Len(t, fixture.records, 2)
	require.Equal(t, fixture.opens, fixture.closes)
}
