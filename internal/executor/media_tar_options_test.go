package executor

import (
	"context"
	"testing"
	"time"

	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/internal/library"
	settingspkg "github.com/samuelncui/yatm/internal/settings"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

func TestTapeCapacityDefaultsWithoutSettingsUseFiveSeconds(t *testing.T) {
	// Direct bare backends retain the same defaults without a Settings module or admitted context.
	exe := &Executor{}
	backend := exe.NewMediaBackend(0, nil, nil).(*mediaBackend)
	defaults, err := backend.tapeDefaults(context.Background(), "/dev/nst0")
	require.NoError(t, err)
	require.Equal(t, 5*time.Second, settingspkg.TapeCapacityPollInterval(defaults))
}

func TestTarAppendCapacityPollSettingUsesAdmittedSnapshot(t *testing.T) {
	// A real completed FORMAT provides the immutable APPEND profile; device cadence stays a preference.
	ctx := context.Background()
	exe, backend, fixture, _ := tarFactoryFixture(t)
	fixture.capacityEOD, fixture.capacityEW = proto.Uint32(1), proto.Uint32(2)
	prefs := &entity.DeviceSettings{Devices: []*entity.DeviceConfig{{
		Device: "/dev/nst0", Options: &entity.DeviceConfig_Tape{Tape: &entity.TapeDeviceSettings{CapacityPollIntervalSeconds: proto.Uint32(0)}},
	}}}
	_, err := exe.settings.Device.Save(ctx, prefs)
	require.NoError(t, err)
	write, err := backend.NewWriteSession(ctx, tarFormatTarget(entity.TapeRecordMode_TAPE_RECORD_MODE_VARIABLE))
	require.NoError(t, err)
	result, err := write.Finalize(ctx, nil)
	require.NoError(t, err)
	_, err = exe.Lib().CreateMedia(ctx, &library.Media{Kind: result.Media.Kind, Identity: result.Media.Identity, Profile: result.Media.Profile})
	require.NoError(t, err)
	admitted, err := exe.withDeviceSettings(ctx)
	require.NoError(t, err)

	// A later saved custom cadence cannot change the admitted APPEND's explicit Off choice.
	prefs.Devices[0].GetTape().CapacityPollIntervalSeconds = proto.Uint32(13)
	_, err = exe.settings.Device.Save(ctx, prefs)
	require.NoError(t, err)
	before := len(fixture.capacityQueries)
	appendSession, err := backend.NewWriteSession(admitted, (&entity.ArchiveTapeTarget{
		Device: "/dev/nst0", Barcode: "ABC001", Mode: entity.ArchiveTapeWriteMode_ARCHIVE_TAPE_WRITE_MODE_APPEND,
	}).Pack())
	require.NoError(t, err)
	tarWriteFixtureMember(t, appendSession, "appended", []byte("content"))
	_, err = appendSession.Finalize(admitted, nil)
	require.NoError(t, err)
	require.Len(t, fixture.capacityQueries, before+3, "only pre-script inspection, post-script inspection and prepared capacity are queried while Off")

	// Direct backend contexts see current Device settings, and returned settings cannot mutate the snapshot.
	direct, err := backend.tapeDefaults(ctx, "/dev/nst0")
	require.NoError(t, err)
	require.Equal(t, 13*time.Second, settingspkg.TapeCapacityPollInterval(direct))
	frozen, err := backend.tapeDefaults(admitted, "/dev/nst0")
	require.NoError(t, err)
	require.Zero(t, settingspkg.TapeCapacityPollInterval(frozen))
	frozen.CapacityPollIntervalSeconds = proto.Uint32(29)
	again, err := backend.tapeDefaults(admitted, "/dev/nst0")
	require.NoError(t, err)
	require.Zero(t, settingspkg.TapeCapacityPollInterval(again))
}
