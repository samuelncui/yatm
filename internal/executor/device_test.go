package executor

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestAttemptTapeLeaseReuseRetainsSingleOwnership(t *testing.T) {
	// Identity resolution and physical Session setup share one attempt-owned drive lease.
	exe := New(nil, nil, []string{"/dev/nst0"}, Paths{}, Scripts{}, nil)
	require.NoError(t, exe.beginAttempt(1, func(error) {}))
	require.NoError(t, exe.AcquireTapeDevice(context.Background(), 1, "/dev/nst0", nil))
	require.NoError(t, exe.AcquireTapeDevice(context.Background(), 1, "/dev/nst0", func() {
		t.Error("the same attempt must not queue behind its own drive lease")
	}))
	require.Len(t, exe.attempts[1].resources, 1)
	require.Empty(t, exe.ListAvailableDevices())

	// One settlement returns the drive exactly once, after all work using it has ended.
	exe.endAttempt(1)
	require.Equal(t, []string{"/dev/nst0"}, exe.ListAvailableDevices())
	exe.endAttempt(1)
	require.Equal(t, []string{"/dev/nst0"}, exe.ListAvailableDevices())
}
