package executor

import (
	"context"
	"testing"
	"time"

	"github.com/samuelncui/yatm/entity"
	"github.com/stretchr/testify/require"
)

func TestDevicePreparedReadIsHistoricalAndRequestOwned(t *testing.T) {
	exe, _, _, _ := tarFactoryFixture(t)
	device := exe.devices[0]
	value := &entity.TapePreparedRead{PreparedAtNs: time.Now().UnixNano(), JobId: 1,
		Identity: "ABC123", ParametersSource: "builtin", CoverageDetail: "No position coverage"}
	exe.recordPreparedRead(device, value)
	value.Identity = "caller mutation"
	retained := exe.lastPreparedRead(device)
	require.Equal(t, "ABC123", retained.Identity)
	retained.CoverageDetail = "response mutation"
	require.Equal(t, "No position coverage", exe.lastPreparedRead(device).CoverageDetail)
	exe.recordPreparedRead("unconfigured", value)
	require.Nil(t, exe.lastPreparedRead("unconfigured"))

	// Busy inspection still reports the historical observation without attributing it to current hardware.
	release, acquired, err := exe.TryAcquireTapeDevice(device)
	require.NoError(t, err)
	require.True(t, acquired)
	defer release()
	reply, err := exe.InspectDevice(context.Background(), device)
	require.NoError(t, err)
	require.Nil(t, reply.ObservedAtNs)
	require.Equal(t, "ABC123", reply.PreparedRead.Identity)
	reply.PreparedRead.Identity = "inspector mutation"
	require.Equal(t, "ABC123", exe.lastPreparedRead(device).Identity)
}
