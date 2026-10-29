package executor

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"math"
	"syscall"
	"testing"
	"time"

	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/internal/media/tape"
	settingspkg "github.com/samuelncui/yatm/internal/settings"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

func tarCapacityPreparedFixture(t *testing.T) (*tape.Device, *tarDeviceFixture) {
	t.Helper()
	// Reuse synthetic SSC/native issue hooks; no operating-system device is opened.
	ctx := context.Background()
	fixture := newTarDeviceFixture()
	transport, err := fixture.Open(tape.Identity{})
	require.NoError(t, err)
	prepared, err := tape.NewDevice(ctx, tape.Identity{}, &fakeNativeTransport{Transport: transport, limit: 4096})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, prepared.Cleanup(context.Background())) })

	// Capacity preparation requires a genuinely configured Device.
	_, err = prepared.Configure(ctx, &entity.TarMediaProfile{
		RecordMode: entity.TapeRecordMode_TAPE_RECORD_MODE_VARIABLE, RecordBytes: 4096,
	}, tape.Read)
	require.NoError(t, err)
	return prepared, fixture
}

func TestTarCapacityPreparationFailureIsNotOptional(t *testing.T) {
	// Cancellation of the real observation cannot become an optional capacity result.
	prepared, _ := tarCapacityPreparedFixture(t)
	backend := (&Executor{}).NewMediaBackend(0, nil, nil).(*mediaBackend)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	options, err := backend.tarCapacity(ctx, prepared, 5*time.Second)
	require.ErrorIs(t, err, context.Canceled)
	require.Nil(t, options)
}

func TestTarCapacityExplicitOffUsesInitialOnly(t *testing.T) {
	// Explicit Off preserves the unknown fallback without a construction policy override.
	ctx := context.Background()
	prepared, fixture := tarCapacityPreparedFixture(t)
	exe := &Executor{}
	backend := exe.NewMediaBackend(0, nil, nil).(*mediaBackend)
	options, err := backend.tarCapacity(ctx, prepared, 0)
	require.NoError(t, err)
	require.Nil(t, options)

	// A later known estimate remains preparation-only while polling is explicitly Off.
	fixture.capacityEOD, fixture.capacityEW = proto.Uint32(1), proto.Uint32(2)
	options, err = backend.tarCapacity(ctx, prepared, 0)
	require.NoError(t, err)
	require.NotNil(t, options)
	require.Equal(t, uint64(1_000_000), *options.Initial.RemainingBytes)
	require.Nil(t, options.Observe)
	require.Zero(t, options.Interval)
	require.Zero(t, options.MaxAge)
	require.False(t, options.CostBounded)
	require.Nil(t, options.Initial.ErrorBytes)
	require.Nil(t, options.Initial.UnreflectedBytes)
}

func TestTarCapacitySupportedProviderUsesConfiguredCadenceAndRecoversUnknown(t *testing.T) {
	for _, config := range []struct {
		name    string
		seconds *uint32
	}{{"default", nil}, {"Off", proto.Uint32(0)}, {"custom", proto.Uint32(17)}} {
		for _, unknown := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/unknown_%t", config.name, unknown), func(t *testing.T) {
				// A real prepared response establishes counter support even with the unknown sentinel.
				prepared, fixture := tarCapacityPreparedFixture(t)
				fixture.capacityEOD, fixture.capacityEW = proto.Uint32(1), proto.Uint32(2)
				if unknown {
					fixture.capacityEOD, fixture.capacityEW = proto.Uint32(math.MaxUint32), proto.Uint32(math.MaxUint32)
				}
				backend := (&Executor{}).NewMediaBackend(0, nil, nil).(*mediaBackend)
				interval := settingspkg.TapeCapacityPollInterval(&entity.TapeDeviceSettings{CapacityPollIntervalSeconds: config.seconds})
				options, err := backend.tarCapacity(context.Background(), prepared, interval)
				require.NoError(t, err)
				if interval == 0 && unknown {
					require.Nil(t, options)
					return
				}
				require.NotNil(t, options)
				require.False(t, options.CostBounded)
				require.Zero(t, options.MaxAge)
				require.Nil(t, options.Initial.ErrorBytes)
				require.Nil(t, options.Initial.UnreflectedBytes)
				if interval == 0 {
					require.Nil(t, options.Observe)
					require.Zero(t, options.Interval)
					return
				}
				require.Equal(t, interval, options.Interval)
				require.NotNil(t, options.Observe)
				require.Equal(t, tape.CapacityEstimate, options.Initial.Quality)
				if unknown {
					require.Nil(t, options.Initial.RemainingBytes)
				}

				// The same native observer can recover a usable estimate; the initial fact stays immutable.
				fixture.capacityEOD, fixture.capacityEW = proto.Uint32(1), proto.Uint32(2)
				sample, issued, err := options.Observe(context.Background(), nil)
				require.NoError(t, err)
				require.True(t, issued)
				require.Equal(t, uint64(1_000_000), *sample.RemainingBytes)
				if unknown {
					require.Nil(t, options.Initial.RemainingBytes)
				}
			})
		}
	}
}

func TestTarCapacityFactoryLogsMissesAndRecovery(t *testing.T) {
	for _, initial := range []string{"known", "malformed", "deadline", "unknown"} {
		t.Run(initial, func(t *testing.T) {
			// Use the real Device/factory/observer path; no construction override supplies samples.
			ctx := context.Background()
			fixture := newTarDeviceFixture()
			fixture.capacityEOD, fixture.capacityEW = proto.Uint32(1), proto.Uint32(2)
			transport, err := fixture.Open(tape.Identity{})
			require.NoError(t, err)
			// Fail only optional capacity payloads after a known-complete native command.
			failure := ""
			faults := &fakeTransport{Transport: transport, execute: func(ctx context.Context, command tape.Command) (tape.Response, error) {
				if command.CDB[0] == 0x4d && failure == "malformed" {
					command.Data[0] = 0
					return tape.Response{Completed: true, Residual: int32(len(command.Data) - 1)}, nil
				}
				if command.CDB[0] == 0x4d && failure == "deadline" {
					return tape.Response{Completed: true}, context.DeadlineExceeded
				}
				return transport.Execute(ctx, command)
			}}
			native := &fakeNativeTransport{Transport: faults, limit: 4096}
			prepared, err := tape.NewDevice(ctx, tape.Identity{}, native)
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, prepared.Cleanup(context.Background())) })
			_, err = prepared.Configure(ctx, &entity.TarMediaProfile{
				RecordMode: entity.TapeRecordMode_TAPE_RECORD_MODE_VARIABLE, RecordBytes: 4096,
			}, tape.Read)
			require.NoError(t, err)
			failure = initial
			if initial == "unknown" {
				fixture.capacityEOD, fixture.capacityEW = proto.Uint32(math.MaxUint32), proto.Uint32(math.MaxUint32)
			}
			var diagnostics bytes.Buffer
			logger := logrus.New()
			logger.SetFormatter(&logrus.TextFormatter{DisableTimestamp: true, DisableColors: true})
			logger.SetOutput(&diagnostics)
			backend := (&Executor{}).NewMediaBackend(0, logger, nil).(*mediaBackend)
			options, err := backend.tarCapacity(ctx, prepared, 5*time.Second)
			require.NoError(t, err)
			require.NotNil(t, options)
			require.NotNil(t, options.Observe)
			require.Equal(t, 5*time.Second, options.Interval)
			initialMisses := 0
			if initial != "known" {
				initialMisses = 1
				require.Contains(t, diagnostics.String(), "level=warning")
				require.Contains(t, diagnostics.String(), "consecutive_failures=1")
			}
			if initial == "malformed" || initial == "deadline" {
				require.Nil(t, options.Initial)
			}

			// Busy admission and ordinary cancellation neither add failures nor emit recovery.
			diagnostics.Reset()
			native.busy = true
			before := len(fixture.capacityQueries)
			_, issued, err := options.Observe(ctx, nil)
			require.NoError(t, err)
			require.False(t, issued)
			require.Len(t, fixture.capacityQueries, before)
			canceled, cancel := context.WithCancel(ctx)
			cancel()
			_, issued, err = options.Observe(canceled, nil)
			require.ErrorIs(t, err, context.Canceled)
			require.False(t, issued)
			require.Empty(t, diagnostics.String())
			native.busy = false

			// An unissued local resource failure is still diagnosable and cannot be mistaken for a skipped poll.
			_, issued, err = options.Observe(ctx, func() error { return syscall.EMFILE })
			require.ErrorIs(t, err, syscall.EMFILE)
			require.False(t, issued)
			require.Len(t, fixture.capacityQueries, before)
			require.Contains(t, diagnostics.String(), syscall.EMFILE.Error())

			// Each settled failure and unknown non-value is logged, without inventing zero space.
			for _, next := range []string{"malformed", "deadline", "unknown"} {
				failure = next
				fixture.capacityEOD, fixture.capacityEW = proto.Uint32(math.MaxUint32), proto.Uint32(math.MaxUint32)
				sample, issued, err := options.Observe(ctx, nil)
				require.True(t, issued)
				if failure == "unknown" {
					require.NoError(t, err)
					require.NotNil(t, sample)
					require.Nil(t, sample.RemainingBytes)
				} else {
					require.Error(t, err)
				}
			}
			require.Equal(t, 4, bytes.Count(diagnostics.Bytes(), []byte("level=warning")))
			require.Contains(t, diagnostics.String(), fmt.Sprintf("consecutive_failures=%d", initialMisses+4))
			require.Contains(t, diagnostics.String(), "error=")
			require.NotContains(t, diagnostics.String(), "remaining_native_bytes=0")

			// A subsequent real value logs recovery once; later successful observations stay quiet.
			diagnostics.Reset()
			failure = ""
			fixture.capacityEOD, fixture.capacityEW = proto.Uint32(1), proto.Uint32(2)
			for range 2 {
				sample, issued, err := options.Observe(ctx, nil)
				require.NoError(t, err)
				require.True(t, issued)
				require.Equal(t, uint64(1_000_000), *sample.RemainingBytes)
			}
			require.Equal(t, 1, bytes.Count(diagnostics.Bytes(), []byte("tar capacity sample recovered")))
			require.Contains(t, diagnostics.String(), "level=info")
		})
	}
}

func TestTarCapacityCancellationCannotHideJoinedFailure(t *testing.T) {
	// Quiet shutdown applies only when every error cause is cancellation.
	require.True(t, tarCapacityCanceled(errors.Join(fmt.Errorf("observe: %w", context.Canceled), context.Canceled)))
	require.False(t, tarCapacityCanceled(errors.Join(context.Canceled, tape.ErrUncertain)))
	require.False(t, tarCapacityCanceled(context.DeadlineExceeded))
	require.False(t, tarCapacityCanceled(nil))
}
