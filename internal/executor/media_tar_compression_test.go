package executor

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/samuelncui/acp"
	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/internal/library"
	mediapkg "github.com/samuelncui/yatm/internal/media"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

func TestTarFactoryHardwareCompressionOwnsProfileAndPreparationOrder(t *testing.T) {
	// Hardware compression affects the device, while the existing PAX and content contracts stay unchanged.
	ctx := context.Background()
	exe, backend, fixture, jobID := tarFactoryFixture(t)
	var diagnostics bytes.Buffer
	backend.logger.SetOutput(&diagnostics)
	original := append([]byte(nil), fixture.mode...)
	target := tarFormatTarget(entity.TapeRecordMode_TAPE_RECORD_MODE_VARIABLE)
	target.GetTape().TarOptions.HardwareCompression = proto.Bool(true)
	write, err := newTarFixtureWriteSession(backend.NewWriteSession(ctx, target))
	require.NoError(t, err)
	data := bytes.Repeat([]byte("hardware compressed source\n"), 500)
	tarWriteFixtureMember(t, write, "first.bin", data)
	result, err := write.Finalize(ctx, nil)
	require.NoError(t, err)
	require.True(t, result.Media.Profile.GetTape().Tar.HardwareCompression)
	require.Equal(t, original, fixture.mode)
	for _, compressed := range fixture.writeCompression {
		require.True(t, compressed)
	}
	parameters, err := filepath.Glob(filepath.Join(exe.jobWorkPath(jobID), "tapes", "ABC001", "recovery-*.json"))
	require.NoError(t, err)
	require.Empty(t, parameters)
	var member *mediapkg.CompletedFile
	require.NoError(t, tarFixtureInventory(write, result, ctx, func(value *mediapkg.CompletedFile) error { member = value; return nil }))
	require.NotNil(t, member)
	stored, err := exe.Lib().CreateMedia(ctx, &library.Media{
		Kind: result.Media.Kind, Identity: result.Media.Identity, Profile: result.Media.Profile,
	})
	require.NoError(t, err)

	// APPEND ignores new FORMAT defaults and enables write compression only after its label/tail reads.
	_, err = exe.settings.Device.Save(ctx, &entity.DeviceSettings{Devices: []*entity.DeviceConfig{{Device: "/dev/nst0",
		Options: &entity.DeviceConfig_Tape{Tape: &entity.TapeDeviceSettings{TarWriteOptions: &entity.TarWriteOptions{HardwareCompression: proto.Bool(false)}}},
	}}})
	require.NoError(t, err)
	exe.endAttempt(jobID)
	require.NoError(t, exe.beginAttempt(jobID, func(error) {}))
	fixture.readCompression, fixture.writeCompression = nil, nil
	fixture.decodeAlgorithmOnRead = proto.Uint32(0xff)
	appendSession, err := newTarFixtureWriteSession(backend.NewWriteSession(ctx, (&entity.ArchiveTapeTarget{
		Device: "/dev/nst0", Barcode: "ABC001", Mode: entity.ArchiveTapeWriteMode_ARCHIVE_TAPE_WRITE_MODE_APPEND,
	}).Pack()))
	require.NoError(t, err)
	require.NotEmpty(t, fixture.readCompression)
	for _, compressed := range fixture.readCompression {
		require.False(t, compressed, "identity/tail reads must not reproduce write DCE")
	}
	tarWriteFixtureMember(t, appendSession, "later.txt", []byte("another file"))
	_, err = appendSession.Finalize(ctx, nil)
	require.NoError(t, err)
	for _, compressed := range fixture.writeCompression {
		require.True(t, compressed)
	}
	// The decoder's last-read algorithm is an observation, not a captured control to overwrite.
	original[27] = 0xff
	require.Equal(t, original, fixture.mode)

	// A write-protected reader with no write compressor can independently enable hardware decoding.
	exe.endAttempt(jobID)
	require.NoError(t, exe.beginAttempt(jobID, func(error) {}))
	fixture.mode[3] |= 0x80
	fixture.mode[18] &^= 0x40
	readOriginal := append([]byte(nil), fixture.mode...)
	fixture.readCompression = nil
	readTarget := (&entity.ReadTapeTarget{Device: "/dev/nst0", MediaId: proto.Int64(stored.ID)}).Pack()
	readTarget.ExpectedMediaId, readTarget.ExpectedIdentity, readTarget.ExpectedProfile = stored.ID, stored.Identity, stored.Profile
	read, err := backend.NewReadSession(ctx, readTarget)
	require.NoError(t, err)
	source, err := read.Source(mediapkg.ReadRequest{Path: member.Path, Storage: member.Storage})
	require.NoError(t, err)
	destination := filepath.Join(t.TempDir(), "restored")
	runTarFixtureStream(t, &tarFixtureItem{SimpleJob: &acp.SimpleJob{Path: member.Path, Dsts: []string{destination}}, source: source.Content})
	actual, err := os.ReadFile(destination)
	require.NoError(t, err)
	require.Equal(t, data, actual)
	require.NoError(t, read.Finalize(ctx, nil))
	require.Contains(t, diagnostics.String(), `action=\"APPEND READ\" write_compression=false read_decompression=true`)
	require.Contains(t, diagnostics.String(), `action=\"APPEND WRITE\" write_compression=true read_decompression=true observed_write_algorithm=1 observed_read_algorithm=255`)
	require.Contains(t, diagnostics.String(), `action=\"READ\" write_compression=false read_decompression=true`)
	for _, compressed := range fixture.readCompression {
		require.False(t, compressed)
	}
	require.Equal(t, readOriginal, fixture.mode)
	require.Equal(t, fixture.opens, fixture.closes)
}

func TestTarInvalidCompressedAppendNeverAppliesWritePolicy(t *testing.T) {
	for _, failure := range []string{"label identity", "terminating filemark"} {
		t.Run(failure, func(t *testing.T) {
			// Neither an invalid identity nor an unterminated tail authorizes write-mode controls.
			ctx := context.Background()
			exe, backend, fixture, _ := tarFactoryFixture(t)
			target := tarFormatTarget(entity.TapeRecordMode_TAPE_RECORD_MODE_VARIABLE)
			target.GetTape().TarOptions.HardwareCompression = proto.Bool(true)
			write, err := newTarFixtureWriteSession(backend.NewWriteSession(ctx, target))
			require.NoError(t, err)
			result, err := write.Finalize(ctx, nil)
			require.NoError(t, err)
			profile := proto.Clone(result.Media.Profile).(*entity.MediaProfile)
			if failure == "label identity" {
				profile.GetTape().Tar.InitializationId = "00000000-0000-4000-8000-000000000001"
			} else {
				fixture.records = append(fixture.records, tarFixtureRecord{data: []byte("unterminated tail"), compressed: true})
			}
			_, err = exe.Lib().CreateMedia(ctx, &library.Media{Kind: result.Media.Kind, Identity: result.Media.Identity, Profile: profile})
			require.NoError(t, err)
			fixture.selectedCompression = nil
			original, writes := append([]byte(nil), fixture.mode...), fixture.writes
			_, err = newTarFixtureWriteSession(backend.NewWriteSession(ctx, (&entity.ArchiveTapeTarget{
				Device: "/dev/nst0", Barcode: "ABC001", Mode: entity.ArchiveTapeWriteMode_ARCHIVE_TAPE_WRITE_MODE_APPEND,
			}).Pack()))
			require.ErrorContains(t, err, failure)
			for _, compressed := range fixture.selectedCompression {
				require.False(t, compressed)
			}
			require.Equal(t, writes, fixture.writes)
			require.Equal(t, original, fixture.mode)
			require.Equal(t, fixture.opens, fixture.closes)
		})
	}
}
