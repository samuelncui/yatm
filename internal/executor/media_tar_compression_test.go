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

func TestTarWritesForceDriveCompressionOffAndRestore(t *testing.T) {
	// A drive left with compression enabled writes every tar record uncompressed, then gets its controls back.
	ctx := context.Background()
	exe, backend, fixture, jobID := tarFactoryFixture(t)
	fixture.mode[18], fixture.mode[46] = 0xc0, 1
	original := append([]byte(nil), fixture.mode...)
	write, err := newTarFixtureWriteSession(openWrite(ctx, backend, tarFormatTarget(entity.TapeRecordMode_TAPE_RECORD_MODE_VARIABLE)))
	require.NoError(t, err)
	data := bytes.Repeat([]byte("uncompressed source\n"), 500)
	tarWriteFixtureMember(t, write, "first.bin", data)
	result, err := write.Finalize(ctx, nil)
	require.NoError(t, err)
	require.NotEmpty(t, fixture.writeCompression)
	require.NotContains(t, fixture.writeCompression, true, "FORMAT writes with drive compression off")
	require.Equal(t, original, fixture.mode)
	var member *mediapkg.CompletedFile
	require.NoError(t, tarFixtureInventory(write, result, ctx, func(value *mediapkg.CompletedFile) error { member = value; return nil }))
	require.NotNil(t, member)
	stored, err := exe.Lib().CreateMedia(ctx, &library.Media{
		Kind: result.Media.Kind, Identity: result.Media.Identity, Profile: result.Media.Profile,
	})
	require.NoError(t, err)

	// APPEND turns compression off again only for its writes and restores it afterwards.
	exe.endAttempt(jobID)
	require.NoError(t, exe.beginAttempt(jobID, func(error) {}))
	fixture.writeCompression = nil
	appendSession, err := newTarFixtureWriteSession(openWrite(ctx, backend, (&entity.ArchiveTapeTarget{
		Device: "/dev/nst0", Barcode: "ABC001", Mode: entity.ArchiveTapeWriteMode_ARCHIVE_TAPE_WRITE_MODE_APPEND,
	}).Pack()))
	require.NoError(t, err)
	tarWriteFixtureMember(t, appendSession, "later.txt", []byte("another file"))
	_, err = appendSession.Finalize(ctx, nil)
	require.NoError(t, err)
	require.NotEmpty(t, fixture.writeCompression)
	require.NotContains(t, fixture.writeCompression, true, "APPEND writes with drive compression off")
	require.Equal(t, original, fixture.mode)

	// READ leaves the drive's compression controls untouched.
	exe.endAttempt(jobID)
	require.NoError(t, exe.beginAttempt(jobID, func(error) {}))
	fixture.selectedCompression = nil
	readTarget := (&entity.ReadTapeTarget{Device: "/dev/nst0", MediaId: proto.Int64(stored.ID)}).Pack()
	readTarget.ExpectedMediaId, readTarget.ExpectedIdentity, readTarget.ExpectedProfile = stored.ID, stored.Identity, stored.Profile
	read, err := openRead(ctx, backend, readTarget)
	require.NoError(t, err)
	source, err := read.Source(mediapkg.ReadRequest{Path: member.Path, Storage: member.Storage})
	require.NoError(t, err)
	destination := filepath.Join(t.TempDir(), "restored")
	runTarFixtureStream(t, &tarFixtureItem{SimpleJob: &acp.SimpleJob{Path: member.Path, Dsts: []string{destination}}, source: source.Content})
	actual, err := os.ReadFile(destination)
	require.NoError(t, err)
	require.Equal(t, data, actual)
	require.NoError(t, read.Finalize(ctx, nil))
	require.Empty(t, fixture.selectedCompression)
	require.Equal(t, original, fixture.mode)
	require.Equal(t, fixture.opens, fixture.closes)
}

func TestTarInvalidAppendNeverAppliesWritePolicy(t *testing.T) {
	for _, failure := range []string{"label identity", "terminating filemark"} {
		t.Run(failure, func(t *testing.T) {
			// Neither an invalid identity nor an unterminated tail authorizes write-mode controls.
			ctx := context.Background()
			exe, backend, fixture, _ := tarFactoryFixture(t)
			fixture.mode[18], fixture.mode[46] = 0xc0, 1
			write, err := newTarFixtureWriteSession(openWrite(ctx, backend, tarFormatTarget(entity.TapeRecordMode_TAPE_RECORD_MODE_VARIABLE)))
			require.NoError(t, err)
			result, err := write.Finalize(ctx, nil)
			require.NoError(t, err)
			profile := proto.Clone(result.Media.Profile).(*entity.MediaProfile)
			if failure == "label identity" {
				profile.GetTape().Tar.InitializationId = "00000000-0000-4000-8000-000000000001"
			} else {
				fixture.records = append(fixture.records, tarFixtureRecord{data: []byte("unterminated tail")})
			}
			_, err = exe.Lib().CreateMedia(ctx, &library.Media{Kind: result.Media.Kind, Identity: result.Media.Identity, Profile: profile})
			require.NoError(t, err)
			fixture.selectedCompression = nil
			original, writes := append([]byte(nil), fixture.mode...), fixture.writes
			_, err = newTarFixtureWriteSession(openWrite(ctx, backend, (&entity.ArchiveTapeTarget{
				Device: "/dev/nst0", Barcode: "ABC001", Mode: entity.ArchiveTapeWriteMode_ARCHIVE_TAPE_WRITE_MODE_APPEND,
			}).Pack()))
			require.ErrorContains(t, err, failure)
			require.Empty(t, fixture.selectedCompression)
			require.Equal(t, writes, fixture.writes)
			require.Equal(t, original, fixture.mode)
			require.Equal(t, fixture.opens, fixture.closes)
		})
	}
}
