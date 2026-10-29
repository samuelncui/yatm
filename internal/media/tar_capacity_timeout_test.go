package media

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"syscall"
	"testing"
	"testing/synctest"
	"time"

	"github.com/samuelncui/yatm/internal/media/tape"
	"github.com/stretchr/testify/require"
)

func TestTarCapacityLocalMissesKeepWriteAndRecover(t *testing.T) {
	for _, failure := range []error{context.DeadlineExceeded, syscall.EBUSY, fmt.Errorf("capacity sample: %w", context.DeadlineExceeded),
		errors.Join(context.DeadlineExceeded, tape.ErrUnsupported), tape.ErrMalformed,
		errors.Join(context.DeadlineExceeded, tape.ErrMalformed), syscall.EMFILE, syscall.ENFILE, syscall.ENOMEM,
		fmt.Errorf("attach: %w", syscall.EMFILE), fmt.Errorf("attach: %w", syscall.ENFILE), fmt.Errorf("attach: %w", syscall.ENOMEM)} {
		t.Run(failure.Error(), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				// Five failed samples cannot replace cached space or withdraw the next observation.
				entered, release := make(chan struct{}), make(chan struct{})
				releaseQuery := sync.OnceFunc(func() { close(release) })
				options := tarTestOptions(t, false, 4096)
				options.Capacity = tarCapacityTestOptions(100000)
				options.Capacity.Interval = 5 * time.Second
				calls := 0
				options.Capacity.Observe = func(ctx context.Context, issue func() error) (*tape.CapacitySample, bool, error) {
					if err := issue(); err != nil {
						return nil, false, err
					}
					calls++
					switch {
					case calls <= 5:
						return tarCapacityTestSample(0, tape.CapacityLowerBound), true, failure
					case calls == 6:
						close(entered)
						select {
						case <-release:
							return tarCapacityTestSample(10000, tape.CapacityLowerBound), true, nil
						case <-ctx.Done():
							return nil, true, ctx.Err()
						}
					default:
						return nil, false, tape.ErrUnsupported
					}
				}
				session, err := newTarTestWriteSession(&tarCapacityTestDevice{tarTestDevice: &tarTestDevice{}}, options)
				require.NoError(t, err)
				budget := session.tarWriteSession.capacity
				t.Cleanup(func() { releaseQuery(); _ = budget.stop() })
				stopped := make(chan struct{})
				go func() { budget.worker.Wait(); close(stopped) }()
				select {
				case <-entered:
				case <-stopped:
					t.Fatal("local sample failures withdrew the observer before recovery")
				}
				require.Equal(t, 6, calls)
				require.NoError(t, budget.err())
				require.Equal(t, uint64(100000-1344), budget.available)

				// A held query cannot block WRITE or erase its concurrent capacity debit.
				writeTarPositionMember(t, session, "member", 3584)
				require.Equal(t, uint64(100000-1344-4160), budget.available)
				require.Equal(t, uint64(4160), budget.returnedCost)
				releaseQuery()
				<-stopped
				require.Equal(t, 7, calls)
				require.Equal(t, uint64(10000-1344-4160), budget.available)

				// Normal physical completion still publishes the independently completed member.
				result, err := session.Finalize(context.Background(), nil)
				require.NoError(t, err)
				require.Len(t, tarTestCompleted(t, session, result), 1)
			})
		})
	}
}

func TestTarCapacityLocalMissCannotHideDeviceFailure(t *testing.T) {
	for _, cause := range []error{tape.ErrUncertain, errors.New("write-behind failure"),
		errors.Join(syscall.EMFILE, syscall.EIO), &tape.UncertainError{Cause: syscall.ENFILE},
		errors.Join(syscall.ENOMEM, errors.New("attachment cleanup failed")),
		&tape.CommandError{Response: tape.Response{Completed: true, Status: 2}, Sense: tape.Sense{Key: 2, ASC: 0x3a}}} {
		t.Run(cause.Error(), func(t *testing.T) {
			// A local timeout does not erase another cause that invalidates the active write attempt.
			failure := errors.Join(context.DeadlineExceeded, cause)
			options := tarCapacityTestOptions(10000)
			options.Interval = time.Hour
			options.Observe = func(context.Context, func() error) (*tape.CapacitySample, bool, error) {
				return nil, true, failure
			}
			budget, err := newTarCapacityBudget(options, tape.RecordMode{MaximumBytes: 4096})
			require.NoError(t, err)
			budget.start()
			budget.worker.Wait()
			require.ErrorIs(t, budget.reserve(1024), cause)
			require.ErrorIs(t, budget.stop(), cause)
		})
	}
}

func TestTarCapacityBusyAndUnknownSamplesKeepObserver(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		// Neither a busy device nor repeated unknown values can replace the debited estimate.
		entered, release := make(chan struct{}), make(chan struct{})
		releaseQuery := sync.OnceFunc(func() { close(release) })
		options := tarCapacityTestOptions(100000)
		options.Interval = 5 * time.Second
		calls := 0
		options.Observe = func(ctx context.Context, issue func() error) (*tape.CapacitySample, bool, error) {
			calls++
			if calls <= 5 {
				return tarCapacityTestSample(0, tape.CapacityLowerBound), false, nil
			}
			if err := issue(); err != nil {
				return nil, false, err
			}
			if calls <= 10 {
				sample := tarCapacityTestSample(0, tape.CapacityUnknown)
				sample.RemainingBytes = nil
				return sample, true, nil
			}
			if calls == 11 {
				close(entered)
				select {
				case <-release:
					return tarCapacityTestSample(10000, tape.CapacityLowerBound), true, nil
				case <-ctx.Done():
					return nil, true, ctx.Err()
				}
			}
			return nil, false, tape.ErrUnsupported
		}
		budget, err := newTarCapacityBudget(options, tape.RecordMode{MaximumBytes: 4096})
		require.NoError(t, err)
		require.NoError(t, budget.reserve(1024))
		budget.start()
		t.Cleanup(func() { releaseQuery(); _ = budget.stop() })
		stopped := make(chan struct{})
		go func() { budget.worker.Wait(); close(stopped) }()
		select {
		case <-entered:
		case <-stopped:
			t.Fatal("busy or unknown samples withdrew the observer before recovery")
		}
		require.Equal(t, uint64(100000-1344-1088), budget.available)

		// The successful replacement must still debit reservations admitted during its query.
		require.NoError(t, budget.reserve(512))
		releaseQuery()
		<-stopped
		require.Equal(t, 12, calls)
		require.Equal(t, uint64(10000-1344-1600), budget.available)
		require.NoError(t, budget.stop())
	})
}

func TestTarCapacityShutdownJoinsNativeObservation(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		// Cancellation stops admission; it cannot release an already issued native observation.
		entered, canceled, release := make(chan bool), make(chan struct{}), make(chan struct{})
		releaseQuery := sync.OnceFunc(func() { close(release) })
		options := tarTestOptions(t, false, 4096)
		options.Capacity = tarCapacityTestOptions(100000)
		options.Capacity.Interval = time.Hour
		options.Capacity.Observe = func(ctx context.Context, issue func() error) (*tape.CapacitySample, bool, error) {
			if err := issue(); err != nil {
				return nil, false, err
			}
			_, deadline := ctx.Deadline()
			entered <- deadline
			<-ctx.Done()
			close(canceled)
			<-release
			return tarCapacityTestSample(0, tape.CapacityLowerBound), true, nil
		}
		session, err := newTarTestWriteSession(&tarCapacityTestDevice{tarTestDevice: &tarTestDevice{}}, options)
		require.NoError(t, err)
		budget := session.tarWriteSession.capacity
		t.Cleanup(func() { releaseQuery(); _ = budget.stop() })
		require.False(t, <-entered, "optional observations must not receive an artificial sample deadline")

		// WRITE proceeds while the native observation is pending, before any cancellation.
		writeTarPositionMember(t, session, "member", 3584)
		require.Equal(t, uint64(100000-1344-4160), budget.available)
		done := make(chan struct{})
		var result *WriteResult
		go func() { result, err = session.Finalize(context.Background(), nil); close(done) }()
		<-canceled
		synctest.Wait()
		select {
		case <-done:
			t.Fatal("Finalize released a native observation before it settled")
		default:
		}

		// A canceled query's nonnil sample cannot replenish or replace the settled write debit.
		releaseQuery()
		<-done
		require.NoError(t, err)
		require.Len(t, tarTestCompleted(t, session, result), 1)
		require.Equal(t, uint64(100000-1344-4160), budget.available)
	})
}
