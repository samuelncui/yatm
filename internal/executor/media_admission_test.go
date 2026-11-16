package executor

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/internal/library"
	mediapkg "github.com/samuelncui/yatm/internal/media"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

// openRead opens a read Session through the Job admission bound to backend's attempt.
func openRead(ctx context.Context, backend mediapkg.Backend, target *entity.ReadMediaTarget) (mediapkg.ReadSession, error) {
	b := backend.(*mediaBackend)
	return b.executor.OpenReadSession(ctx, b.jobID, b.logger, b.mediaWait, target)
}

// openWrite opens a write Session through the Job admission bound to backend's attempt.
func openWrite(ctx context.Context, backend mediapkg.Backend, target *entity.ArchiveMediaTarget) (mediapkg.WriteSession, error) {
	b := backend.(*mediaBackend)
	return b.executor.OpenWriteSession(ctx, b.jobID, b.logger, b.mediaWait, target)
}

func TestLTFSFormatRefusesDriveWithoutLTO5Density(t *testing.T) {
	for _, test := range []struct {
		name      string
		densities []byte
		refused   bool
	}{
		{name: "LTO-4 drive", densities: []byte{0x44, 0x46}, refused: true},
		{name: "LTO-5 drive", densities: []byte{0x44, 0x46, 0x58}},
		{name: "unknown densities"},
	} {
		t.Run(test.name, func(t *testing.T) {
			// The leased passive report decides before any LTFS script; unknown support stays allowed.
			exe := setupTestExecutor(t)
			require.NoError(t, exe.lib.AutoMigrate())
			fixture := newTarDeviceFixture()
			fixture.supportedDensities = test.densities
			exe.tapeProvider = fixture
			marker := filepath.Join(t.TempDir(), "script")
			exe.scripts.ReadInfo = writeExecutorTestScript(t, "identity", `: > "`+marker+`"; exit 1`)
			require.NoError(t, exe.beginAttempt(1, func(error) {}))
			t.Cleanup(func() { exe.endAttempt(1) })
			backend := exe.newMediaBackend(1, nil, nil)
			_, err := openWrite(t.Context(), backend, (&entity.ArchiveTapeTarget{
				Device: "/dev/nst0", Barcode: "ABC001", Name: "Test Tape",
				Mode: entity.ArchiveTapeWriteMode_ARCHIVE_TAPE_WRITE_MODE_FORMAT, Format: proto.String(library.TapeFormatLTFSV1),
			}).Pack())
			require.Error(t, err)
			require.Equal(t, fixture.opens, fixture.closes)
			require.Zero(t, fixture.writes)
			require.Zero(t, fixture.unloads, "the passive density check never unloads")
			if test.refused {
				require.ErrorContains(t, err, "does not support LTFS (needs LTO-5 or later)")
				require.NoFileExists(t, marker)
				return
			}
			require.ErrorContains(t, err, "read tape info failed")
			require.FileExists(t, marker)
		})
	}
}
