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
	backend := exe.newMediaBackend(0, nil, nil).(*mediaBackend)
	defaults, err := backend.executor.tapeDefaults(context.Background(), "/dev/nst0")
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
	write, err := openWrite(ctx, backend, tarFormatTarget(entity.TapeRecordMode_TAPE_RECORD_MODE_VARIABLE))
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
	appendSession, err := openWrite(admitted, backend, (&entity.ArchiveTapeTarget{
		Device: "/dev/nst0", Barcode: "ABC001", Mode: entity.ArchiveTapeWriteMode_ARCHIVE_TAPE_WRITE_MODE_APPEND,
	}).Pack())
	require.NoError(t, err)
	tarWriteFixtureMember(t, appendSession, "appended", []byte("content"))
	_, err = appendSession.Finalize(admitted, nil)
	require.NoError(t, err)
	require.Len(t, fixture.capacityQueries, before+3, "only pre-script inspection, post-script inspection and prepared capacity are queried while Off")

	// Direct backend contexts see current Device settings, and returned settings cannot mutate the snapshot.
	direct, err := backend.executor.tapeDefaults(ctx, "/dev/nst0")
	require.NoError(t, err)
	require.Equal(t, 13*time.Second, settingspkg.TapeCapacityPollInterval(direct))
	frozen, err := backend.executor.tapeDefaults(admitted, "/dev/nst0")
	require.NoError(t, err)
	require.Zero(t, settingspkg.TapeCapacityPollInterval(frozen))
	frozen.CapacityPollIntervalSeconds = proto.Uint32(29)
	again, err := backend.executor.tapeDefaults(admitted, "/dev/nst0")
	require.NoError(t, err)
	require.Zero(t, settingspkg.TapeCapacityPollInterval(again))
}

func TestCheckTarWriteReportsAttemptResolution(t *testing.T) {
	// A completed real FORMAT registers ABC001; the requested writes are answered without opening hardware.
	ctx := context.Background()
	exe, backend, fixture, _ := tarFactoryFixture(t)
	write, err := openWrite(ctx, backend, tarFormatTarget(entity.TapeRecordMode_TAPE_RECORD_MODE_VARIABLE))
	require.NoError(t, err)
	result, err := write.Finalize(ctx, nil)
	require.NoError(t, err)
	_, err = exe.Lib().CreateMedia(ctx, &library.Media{Kind: result.Media.Kind, Identity: result.Media.Identity, Profile: result.Media.Profile})
	require.NoError(t, err)
	opens := fixture.opens

	saveDefaults := func(options *entity.TarWriteOptions) {
		_, err := exe.settings.Device.Save(ctx, &entity.DeviceSettings{Devices: []*entity.DeviceConfig{{
			Device: "/dev/nst0", Options: &entity.DeviceConfig_Tape{Tape: &entity.TapeDeviceSettings{TarWriteOptions: options}},
		}}})
		require.NoError(t, err)
	}
	target := func(barcode string, mode entity.ArchiveTapeWriteMode, options *entity.TarWriteOptions) *entity.ArchiveTapeTarget {
		return &entity.ArchiveTapeTarget{Device: "/dev/nst0", Barcode: barcode, Name: "Checked", Mode: mode,
			Format: proto.String(library.TapeFormatTarPAXV1), TarOptions: options}
	}
	variable, fixed := entity.TapeRecordMode_TAPE_RECORD_MODE_VARIABLE.Enum(), entity.TapeRecordMode_TAPE_RECORD_MODE_FIXED.Enum()
	format, appendMode := entity.ArchiveTapeWriteMode_ARCHIVE_TAPE_WRITE_MODE_FORMAT, entity.ArchiveTapeWriteMode_ARCHIVE_TAPE_WRITE_MODE_APPEND
	for _, tc := range []struct {
		name     string
		saved    *entity.TarWriteOptions
		report   *entity.InspectDeviceResponse
		target   *entity.ArchiveTapeTarget
		options  *entity.TarWriteOptions
		rejected string
	}{
		{name: "default options", report: &entity.InspectDeviceResponse{}, target: target("ABC002", format, nil),
			options: &entity.TarWriteOptions{RecordMode: variable, RecordBytes: proto.Uint32(524288)}},
		{name: "unset format resolves tar", report: &entity.InspectDeviceResponse{},
			target:  &entity.ArchiveTapeTarget{Device: "/dev/nst0", Barcode: "ABC002", Name: "Checked", Mode: format},
			options: &entity.TarWriteOptions{RecordMode: variable, RecordBytes: proto.Uint32(524288)}},
		{name: "device defaults", saved: &entity.TarWriteOptions{RecordMode: fixed, RecordBytes: proto.Uint32(262144)},
			report: &entity.InspectDeviceResponse{}, target: target("ABC002", format, nil),
			options: &entity.TarWriteOptions{RecordMode: fixed, RecordBytes: proto.Uint32(262144)}},
		{name: "explicit choice wins", report: &entity.InspectDeviceResponse{},
			target:  target("ABC002", format, &entity.TarWriteOptions{RecordBytes: proto.Uint32(1048576)}),
			options: &entity.TarWriteOptions{RecordMode: variable, RecordBytes: proto.Uint32(1048576)}},
		{name: "FORMAT on existing Media", report: &entity.InspectDeviceResponse{}, target: target("ABC001", format, nil),
			options:  &entity.TarWriteOptions{RecordMode: variable, RecordBytes: proto.Uint32(524288)},
			rejected: "Tape already exists in the Library; delete its Media metadata explicitly before formatting"},
		{name: "APPEND uses the immutable profile", report: &entity.InspectDeviceResponse{}, target: target("ABC001", appendMode, nil),
			options: &entity.TarWriteOptions{RecordMode: variable, RecordBytes: proto.Uint32(4096)}},
		{name: "APPEND identity mismatch", report: &entity.InspectDeviceResponse{}, target: target("ABC002", appendMode, nil),
			rejected: "APPEND requires available registered Tape Media"},
		{name: "APPEND options conflict", report: &entity.InspectDeviceResponse{}, target: target("ABC001", appendMode, &entity.TarWriteOptions{RecordBytes: proto.Uint32(8192)}),
			rejected: "APPEND options differ from the immutable Tape profile"},
		{name: "optional diagnostic failure", report: &entity.InspectDeviceResponse{Reason: entity.DeviceReason_DEVICE_REASON_UNSUPPORTED},
			target: target("ABC001", appendMode, nil), options: &entity.TarWriteOptions{RecordMode: variable, RecordBytes: proto.Uint32(4096)}},
		{name: "unloaded cartridge", report: &entity.InspectDeviceResponse{Reason: entity.DeviceReason_DEVICE_REASON_NO_MEDIUM, Detail: "Tape command failed",
			Report: &entity.InspectDeviceResponse_Tape{Tape: &entity.TapeDeviceReport{MediumPresent: proto.Bool(false)}}},
			target: target("ABC002", format, nil), options: &entity.TarWriteOptions{RecordMode: variable, RecordBytes: proto.Uint32(524288)}},
		{name: "busy device", report: &entity.InspectDeviceResponse{Device: &entity.DeviceSummary{Busy: true}, Reason: entity.DeviceReason_DEVICE_REASON_BUSY, Detail: "Tape device is in use"},
			target: target("ABC002", format, nil), options: &entity.TarWriteOptions{RecordMode: variable, RecordBytes: proto.Uint32(524288)},
			rejected: "Tape device is unavailable: Tape device is in use"},
		{name: "write protected", report: &entity.InspectDeviceResponse{Report: &entity.InspectDeviceResponse_Tape{Tape: &entity.TapeDeviceReport{WriteProtected: proto.Bool(true)}}},
			target: target("ABC001", appendMode, nil), options: &entity.TarWriteOptions{RecordMode: variable, RecordBytes: proto.Uint32(4096)},
			rejected: "Tape device is unavailable"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			saveDefaults(tc.saved)
			request := proto.Clone(tc.target).(*entity.ArchiveTapeTarget)
			require.NoError(t, exe.CheckTarWrite(ctx, tc.report, request))
			require.True(t, proto.Equal(tc.target, request), "the request is not modified")
			require.Equal(t, tc.rejected == "", tc.report.Writable)
			if tc.rejected != "" {
				require.Contains(t, tc.report.WriteDetail, tc.rejected)
			} else {
				require.Empty(t, tc.report.WriteDetail)
			}
			if tc.options == nil {
				require.Nil(t, tc.report.ResolvedTarOptions)
			} else {
				require.True(t, proto.Equal(tc.options, tc.report.ResolvedTarOptions), "resolved=%v", tc.report.ResolvedTarOptions)
			}
			require.Equal(t, opens, fixture.opens)
		})
	}
}
