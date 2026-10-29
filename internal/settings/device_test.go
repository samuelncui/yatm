package settings

import (
	"context"
	"testing"
	"time"

	"github.com/samuelncui/yatm/entity"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

func deviceTestValue() *entity.DeviceSettings {
	return &entity.DeviceSettings{Devices: []*entity.DeviceConfig{{Device: "/dev/inactive",
		Options: &entity.DeviceConfig_Tape{Tape: &entity.TapeDeviceSettings{DefaultFormat: proto.String("tar_pax_v1"),
			TarWriteOptions: &entity.TarWriteOptions{RecordMode: entity.TapeRecordMode_TAPE_RECORD_MODE_VARIABLE.Enum(),
				RecordBytes: proto.Uint32(262144), HardwareCompression: proto.Bool(false)}}}}}}
}

func TestDeviceSettingsRetainOptionalFalseAndInactiveEntries(t *testing.T) {
	// Missing Settings remain an LTFS chooser preference without creating a registry.
	module := testModule(t)
	ctx := context.Background()
	absent, err := module.Device.Read(ctx)
	require.NoError(t, err)
	require.False(t, absent.Present)
	require.Equal(t, "ltfs_v1", TapeDefaultFormat(TapeDefaults(absent.Value, "/dev/inactive")))
	value := deviceTestValue()
	saved, err := module.Device.Save(ctx, value)
	require.NoError(t, err)
	saved.Devices[0].GetTape().TarWriteOptions.HardwareCompression = nil

	// Saving inactive entries needs no hardware and preserves explicit false through protobuf JSON.
	stored, err := module.Device.Read(ctx)
	require.NoError(t, err)
	require.True(t, stored.Present)
	require.True(t, proto.Equal(value, stored.Value))
	defaults := TapeDefaults(stored.Value, "/dev/inactive")
	require.NotNil(t, defaults.TarWriteOptions.HardwareCompression)
	require.False(t, defaults.TarWriteOptions.GetHardwareCompression())
	defaults.TarWriteOptions.RecordBytes = proto.Uint32(512)
	require.EqualValues(t, 262144, stored.Value.Devices[0].GetTape().TarWriteOptions.GetRecordBytes())
}

func TestDeviceSettingsRejectInvalidStructure(t *testing.T) {
	for name, mutate := range map[string]func(*entity.DeviceSettings){
		"blank key": func(v *entity.DeviceSettings) { v.Devices[0].Device = " " },
		"duplicate key": func(v *entity.DeviceSettings) {
			v.Devices = append(v.Devices, proto.Clone(v.Devices[0]).(*entity.DeviceConfig))
		},
		"missing branch":     func(v *entity.DeviceSettings) { v.Devices[0].Options = nil },
		"unknown format":     func(v *entity.DeviceSettings) { v.Devices[0].GetTape().DefaultFormat = proto.String("unknown") },
		"unknown read order": func(v *entity.DeviceSettings) { v.Devices[0].GetTape().ReadOrder = entity.TapeReadOrder(99) },
		"unspecified mode": func(v *entity.DeviceSettings) {
			v.Devices[0].GetTape().TarWriteOptions.RecordMode = entity.TapeRecordMode_TAPE_RECORD_MODE_UNSPECIFIED.Enum()
		},
		"unaligned size": func(v *entity.DeviceSettings) { v.Devices[0].GetTape().TarWriteOptions.RecordBytes = proto.Uint32(513) },
	} {
		t.Run(name, func(t *testing.T) {
			value := deviceTestValue()
			mutate(value)
			require.Error(t, ValidateDevice(value))
		})
	}
}

func TestDeviceReadOrderDefaultsAndOfflinePersistence(t *testing.T) {
	// Read settings remain deployment Settings, including inactive devices, rather than cartridge metadata.
	module := testModule(t)
	ctx := context.Background()
	require.Equal(t, entity.TapeReadOrder_TAPE_READ_ORDER_LOGICAL, TapeDefaultReadOrder(nil))
	for _, order := range []entity.TapeReadOrder{entity.TapeReadOrder_TAPE_READ_ORDER_UNSPECIFIED, entity.TapeReadOrder_TAPE_READ_ORDER_LOGICAL, entity.TapeReadOrder_TAPE_READ_ORDER_AUTO} {
		value := deviceTestValue()
		value.Devices[0].GetTape().ReadOrder = order
		_, err := module.Device.Save(ctx, value)
		require.NoError(t, err)
		stored, err := module.Device.Read(ctx)
		require.NoError(t, err)
		require.Equal(t, order, stored.Value.Devices[0].GetTape().ReadOrder)
		defaults := TapeDefaults(stored.Value, "/dev/inactive")
		if order == entity.TapeReadOrder_TAPE_READ_ORDER_UNSPECIFIED {
			require.Equal(t, entity.TapeReadOrder_TAPE_READ_ORDER_LOGICAL, TapeDefaultReadOrder(defaults))
		} else {
			require.Equal(t, order, TapeDefaultReadOrder(defaults))
		}
		defaults.ReadOrder = entity.TapeReadOrder_TAPE_READ_ORDER_LOGICAL
		require.Equal(t, order, stored.Value.Devices[0].GetTape().ReadOrder)
	}
}

func TestTapeCapacityPollIntervalDefaultsAndValidBounds(t *testing.T) {
	// The default applies without a Device setting, while explicit zero stays Off.
	require.Equal(t, 5*time.Second, TapeCapacityPollInterval(nil))
	for _, test := range []struct {
		name    string
		seconds *uint32
		want    time.Duration
	}{
		{name: "absent preference", want: 5 * time.Second},
		{name: "explicit Off", seconds: proto.Uint32(0)},
		{name: "minimum", seconds: proto.Uint32(5), want: 5 * time.Second},
		{name: "custom", seconds: proto.Uint32(17), want: 17 * time.Second},
		{name: "above one hour", seconds: proto.Uint32(3601), want: 3601 * time.Second},
		{name: "uint32 maximum", seconds: proto.Uint32(^uint32(0)), want: time.Duration(^uint32(0)) * time.Second},
	} {
		t.Run(test.name, func(t *testing.T) {
			// Valid Device settings resolve without mutating or opening the inactive device.
			value := deviceTestValue()
			value.Devices[0].GetTape().CapacityPollIntervalSeconds = test.seconds
			require.Equal(t, test.want, TapeCapacityPollInterval(value.Devices[0].GetTape()))
			require.NoError(t, ValidateDevice(value))
		})
	}
}

func TestDeviceCapacityPollIntervalOfflinePersistenceAndIsolation(t *testing.T) {
	// Existing chooser defaults stay intact while absent capacity policy resolves without persistence.
	module := testModule(t)
	ctx := context.Background()
	absent, err := module.Device.Read(ctx)
	require.NoError(t, err)
	require.False(t, absent.Present)
	require.Equal(t, "ltfs_v1", TapeDefaultFormat(TapeDefaults(absent.Value, "/dev/inactive")))
	require.Equal(t, 5*time.Second, TapeCapacityPollInterval(TapeDefaults(absent.Value, "/dev/inactive")))

	// Saving an inactive device preserves an explicit zero in the existing protobuf JSON group.
	value := deviceTestValue()
	value.Devices[0].GetTape().CapacityPollIntervalSeconds = proto.Uint32(0)
	saved, err := module.Device.Save(ctx, value)
	require.NoError(t, err)
	*saved.Devices[0].GetTape().CapacityPollIntervalSeconds = 17
	stored, err := module.Device.Read(ctx)
	require.NoError(t, err)
	require.True(t, proto.Equal(value, stored.Value))
	var document row
	require.NoError(t, module.db.First(&document, "key = ?", "devices").Error)
	require.Contains(t, string(document.Value), `"capacityPollIntervalSeconds":0`)
	defaults := TapeDefaults(stored.Value, "/dev/inactive")
	require.NotNil(t, defaults.CapacityPollIntervalSeconds)
	require.Zero(t, TapeCapacityPollInterval(defaults))
	*defaults.CapacityPollIntervalSeconds = 5
	require.Zero(t, stored.Value.Devices[0].GetTape().GetCapacityPollIntervalSeconds())

	// Invalid short cadences cannot replace the saved Off, including for inactive devices.
	for seconds := uint32(1); seconds < 5; seconds++ {
		invalid := deviceTestValue()
		invalid.Devices[0].GetTape().CapacityPollIntervalSeconds = proto.Uint32(seconds)
		_, err := module.Device.Save(ctx, invalid)
		require.ErrorIs(t, err, ErrInvalid)
		current, err := module.Device.Current(ctx)
		require.NoError(t, err)
		require.True(t, proto.Equal(value, current))
	}

	// Replacing explicit Off with an omitted preference restores the default without storing five.
	value.Devices[0].GetTape().CapacityPollIntervalSeconds = nil
	_, err = module.Device.Save(ctx, value)
	require.NoError(t, err)
	current, err := module.Device.Current(ctx)
	require.NoError(t, err)
	require.Nil(t, current.Devices[0].GetTape().CapacityPollIntervalSeconds)
	require.Equal(t, 5*time.Second, TapeCapacityPollInterval(current.Devices[0].GetTape()))
}
