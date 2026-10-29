package tape

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func performanceQueryFixture() Command {
	return Command{CDB: []byte{0x4d, 0, 0x74, 0, 0, 0, 0, 0x10, 0, 0}, Direction: FromDevice,
		Data: make([]byte, MaxQueryBytes), Timeout: 2 * time.Second}
}

func TestOptionalPerformanceQueryKeepsExactPassiveForm(t *testing.T) {
	// The extra current-value page retains the existing bounded, data-in-only optional admission.
	calls := 0
	transport := newSerializedTransport(func(command Command) (Response, error) {
		calls++
		require.Equal(t, byte(0x74), command.CDB[2])
		return Response{Completed: true}, nil
	}, func() error { return nil })
	t.Cleanup(func() { require.NoError(t, transport.Close()) })
	_, issued, err := transport.TryExecute(context.Background(), performanceQueryFixture(), nil)
	require.NoError(t, err)
	require.True(t, issued)
	require.Equal(t, 1, calls)

	// Threshold/default values, read-clearing pages and every changed reserved/allocation field remain blocked.
	for _, change := range []struct {
		index int
		value byte
	}{{0, 0x4c}, {1, 1}, {2, 0x34}, {2, 0xb4}, {2, 0x6e}, {3, 1}, {4, 1}, {5, 1}, {6, 1}, {7, 1}, {8, 1}, {9, 1}} {
		command := performanceQueryFixture()
		command.CDB[change.index] = change.value
		_, issued, err := transport.TryExecute(context.Background(), command, nil)
		require.Error(t, err)
		require.False(t, issued)
	}
	for _, direction := range []Direction{ToDevice, NoData} {
		command := performanceQueryFixture()
		command.Direction = direction
		_, issued, err := transport.TryExecute(context.Background(), command, nil)
		require.Error(t, err)
		require.False(t, issued)
	}
	for _, size := range []int{0, MaxQueryBytes + 1} {
		command := performanceQueryFixture()
		command.Data = make([]byte, size)
		_, issued, err := transport.TryExecute(context.Background(), command, nil)
		require.Error(t, err)
		require.False(t, issued)
	}
	require.Equal(t, 1, calls)
}

func TestOptionalPerformanceUncertaintyBlocksLaterPhysicalWork(t *testing.T) {
	for _, mode := range []string{"deferred sense", "host failure", "invalid residual", "missing completion"} {
		t.Run(mode, func(t *testing.T) {
			// Observe the actual serialized transport, including the terminal barrier before lane release.
			calls, closes := 0, 0
			transport := newSerializedTransport(func(Command) (Response, error) {
				calls++
				response := Response{Completed: true}
				switch mode {
				case "deferred sense":
					response.Status = 2
					response.Sense = []byte{0x71, 0, 5, 0, 0, 0, 0, 10, 0, 0, 0, 0, 0x24, 0, 0, 0, 0, 0}
				case "host failure":
					response.HostStatus = 1
				case "invalid residual":
					response.Residual = MaxQueryBytes + 1
				case "missing completion":
					response.Completed = false
				}
				return response, nil
			}, func() error { closes++; return nil })
			_, issued, err := transport.TryExecute(context.Background(), performanceQueryFixture(), nil)
			require.ErrorIs(t, err, ErrUncertain)
			require.True(t, issued)

			// Neither data, closure nor another optional query can reach the native executor afterward.
			for _, opcode := range []byte{0x0a, 0x10} {
				_, err := transport.Execute(context.Background(), Command{CDB: []byte{opcode, 0, 0, 0, 1, 0}, Timeout: time.Second, Direction: NoData})
				require.ErrorIs(t, err, ErrUncertain)
			}
			_, issued, err = transport.TryExecute(context.Background(), capacityCommand(), nil)
			require.ErrorIs(t, err, ErrUncertain)
			require.False(t, issued)
			require.Equal(t, 1, calls)
			require.True(t, errors.Is(transport.Close(), ErrUncertain))
			require.ErrorIs(t, transport.Close(), ErrUncertain)
			require.Equal(t, 1, closes)
		})
	}
}
