package apis_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/internal/apis"
	"github.com/samuelncui/yatm/internal/executor"
	"github.com/stretchr/testify/require"
)

func TestDeviceDiscoveryListsBusyConfigurationWithoutCartridgeProbe(t *testing.T) {
	// A trap cartridge adapter must remain unused by list polling and passive device discovery.
	root := t.TempDir()
	trap := filepath.Join(root, "probe-was-run")
	readInfo := filepath.Join(root, "readinfo")
	require.NoError(t, os.WriteFile(readInfo, []byte("#!/bin/sh\ntouch '"+trap+"'\nexit 1\n"), 0755))
	devices := []string{filepath.Join(root, "z-device"), filepath.Join(root, "a-device")}
	exe := executor.New(nil, nil, devices, executor.Paths{}, executor.Scripts{ReadInfo: readInfo}, nil)
	release, acquired, err := exe.TryAcquireTapeDevice(devices[0])
	require.NoError(t, err)
	require.True(t, acquired)
	defer release()
	client := entity.NewMediaServiceClient(domainConnection(t, apis.New(nil, exe)))

	// The wire response retains every configured name and presents occupied devices instead of omitting them.
	listed, err := client.ListDevices(context.Background(), &entity.ListDevicesRequest{})
	require.NoError(t, err)
	require.Len(t, listed.Devices, 2)
	require.Equal(t, devices[1], listed.Devices[0].Device)
	require.False(t, listed.Devices[0].Busy)
	require.Equal(t, devices[0], listed.Devices[1].Device)
	require.True(t, listed.Devices[1].Busy)
	require.NotEmpty(t, listed.Devices[1].Detail)
	require.Equal(t, entity.DeviceKind_DEVICE_KIND_TAPE, listed.Devices[1].Kind)

	// Passive busy and missing-raw-facts reports never execute the cartridge loading adapter.
	busy, err := client.InspectDevice(context.Background(), &entity.InspectDeviceRequest{Device: devices[0]})
	require.NoError(t, err)
	require.Equal(t, entity.DeviceReason_DEVICE_REASON_BUSY, busy.Reason)
	require.Nil(t, busy.ObservedAtNs)
	missing, err := client.InspectDevice(context.Background(), &entity.InspectDeviceRequest{Device: devices[1]})
	require.NoError(t, err)
	require.Equal(t, entity.DeviceReason_DEVICE_REASON_TRANSPORT_UNAVAILABLE, missing.Reason)
	_, err = client.InspectDevice(context.Background(), &entity.InspectDeviceRequest{Device: filepath.Join(root, "unconfigured")})
	require.ErrorContains(t, err, "not configured")
	require.NoFileExists(t, trap)
}
