package executor

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/samuelncui/yatm/internal/media/tape"
	"github.com/stretchr/testify/require"
)

func TestTapeIdentityCaptureReservesAdmissionBeforeDiscoveryRecovers(t *testing.T) {
	// Recover metadata after the first free fallback key is observed, before admission can return.
	exe, provider := deviceFixture()
	require.NoError(t, exe.beginAttempt(1, func(error) {}))
	t.Cleanup(func() { exe.endAttempt(1) })
	provider.identity = tape.Identity{}
	provider.resolveErr = errors.New("sysfs temporarily unavailable")
	captured := make(chan bool, 1)
	var recoverOnce sync.Once
	provider.onResolve = func(device string) {
		if device != exe.devices[len(exe.devices)-1] {
			return
		}
		recoverOnce.Do(func() {
			// The former resolve-then-admit path can be paused here by its unowned attempt lock.
			locked := exe.attemptsLock.TryLock()
			provider.identity = tape.Identity{Key: "/sys/scsi/drive0", Aliases: exe.devices}
			provider.resolveErr = nil
			captured <- locked
		})
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	admitted := make(chan error, 1)
	go func() { admitted <- exe.AcquireTapeDevice(ctx, 1, exe.devices[0], nil) }()
	var locked bool
	select {
	case locked = <-captured:
	case <-ctx.Done():
		t.Fatal("fallback identity was not resolved")
	}

	// Another alias becomes observable while the first Job still owns its captured admission.
	release, acquired, err := exe.TryAcquireTapeDevice(exe.devices[1])
	if release != nil {
		defer release()
	}
	if locked {
		exe.attemptsLock.Unlock()
	}
	require.NoError(t, err)
	require.NoError(t, <-admitted)
	require.False(t, acquired, "metadata recovery must not admit aliases on separate fallback and canonical keys")
	require.True(t, exe.resourceHeld(tapeResource(exe.devices[0])))
	require.False(t, exe.resourceHeld(tapeResource("/sys/scsi/drive0")))
}

func TestMediaQueueCallbacksRunOutsideAdmissionLocks(t *testing.T) {
	for _, kind := range []string{"Tape", "Volume"} {
		t.Run(kind, func(t *testing.T) {
			// Both backend kinds must release admission locks before publishing a queued phase.
			exe, _ := deviceFixture()
			var release func()
			acquire := func(ctx context.Context, onWait func()) error {
				return exe.AcquireTapeDevice(ctx, 1, exe.devices[0], onWait)
			}
			if kind == "Tape" {
				var acquired bool
				var err error
				release, acquired, err = exe.TryAcquireTapeDevice(exe.devices[0])
				require.NoError(t, err)
				require.True(t, acquired)
			} else {
				var acquired bool
				release, acquired = exe.tryAcquire(volumeResource("volume0"))
				require.True(t, acquired)
				acquire = func(ctx context.Context, onWait func()) error {
					return exe.AcquireVolume(ctx, 1, "volume0", onWait)
				}
			}
			defer release()
			require.NoError(t, exe.beginAttempt(1, func(error) {}))
			t.Cleanup(func() { exe.endAttempt(1) })
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			waiting := make(chan struct{})
			result := make(chan error, 1)
			go func() {
				result <- acquire(ctx, func() {
					for _, lock := range []struct {
						name string
						lock *sync.Mutex
					}{
						{name: "attempt", lock: &exe.attemptsLock},
						{name: "device", lock: &exe.deviceLock},
						{name: "queue", lock: &exe.resources.lock},
					} {
						if lock.lock.TryLock() {
							lock.lock.Unlock()
						} else {
							t.Errorf("queued callback still holds %s lock", lock.name)
						}
					}
					close(waiting)
				})
			}()
			select {
			case <-waiting:
			case <-ctx.Done():
				t.Fatal("queued callback did not run")
			}

			// Cancellation removes the queued admission while the original holder keeps ownership.
			cancel()
			require.ErrorIs(t, <-result, context.Canceled)
			require.Empty(t, exe.attempts[1].resources)
		})
	}
}
