package executor

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/internal/media/tape"
	"github.com/stretchr/testify/require"
	"golang.org/x/sync/errgroup"
	"google.golang.org/protobuf/proto"
)

type deviceProviderFixture struct {
	lock                                   sync.Mutex
	identity                               tape.Identity
	identities                             map[string]tape.Identity
	resolveErr                             error
	onResolve                              func(string)
	ownershipErr                           error
	report                                 *entity.InspectDeviceResponse
	inspectErr                             error
	cancelInspection                       context.CancelFunc
	resolves, ownershipChecks, inspections int
}

func (p *deviceProviderFixture) Resolve(device string) (tape.Identity, error) {
	p.lock.Lock()
	defer p.lock.Unlock()
	p.resolves++
	identity := p.identity
	if p.identities != nil {
		identity = p.identities[device]
	}
	identity.Configured = device
	err := p.resolveErr
	if p.onResolve != nil {
		p.onResolve(device)
	}
	return identity, err
}

func (p *deviceProviderFixture) CheckOwnership(context.Context, tape.Identity) error {
	p.lock.Lock()
	defer p.lock.Unlock()
	p.ownershipChecks++
	return p.ownershipErr
}

func (p *deviceProviderFixture) Inspect(context.Context, tape.Identity) (*entity.InspectDeviceResponse, error) {
	p.lock.Lock()
	defer p.lock.Unlock()
	p.inspections++
	if p.cancelInspection != nil {
		p.cancelInspection()
	}
	if p.report == nil {
		return nil, p.inspectErr
	}
	return proto.Clone(p.report).(*entity.InspectDeviceResponse), p.inspectErr
}

func (p *deviceProviderFixture) Open(tape.Identity) (tape.Transport, error) {
	return nil, tape.ErrUnsupported
}

func deviceFixture() (*Executor, *deviceProviderFixture) {
	devices := []string{"/dev/st0", "/dev/sg0", "/dev/nst0"}
	exe := New(nil, nil, devices, Paths{}, Scripts{}, nil)
	provider := &deviceProviderFixture{identity: tape.Identity{Key: "/sys/scsi/drive0", SG: "/dev/sg0", Aliases: devices}, report: &entity.InspectDeviceResponse{}}
	exe.tapeProvider = provider
	return exe, provider
}

func TestDeviceAliasesShareQueuedAndPassiveLeases(t *testing.T) {
	// All configured aliases identify one physical resource while preserving their display names.
	exe, provider := deviceFixture()
	summaries := exe.ListDevices()
	require.Len(t, summaries, 3)
	require.Equal(t, "/dev/nst0", summaries[0].Device)
	require.Equal(t, "/dev/sg0", summaries[1].Device)
	require.Equal(t, "/dev/st0", summaries[2].Device)
	require.Zero(t, provider.inspections)
	require.Zero(t, provider.ownershipChecks)
	require.NoError(t, exe.beginAttempt(1, func(error) {}))
	require.NoError(t, exe.beginAttempt(2, func(error) {}))
	t.Cleanup(func() { exe.endAttempt(1); exe.endAttempt(2) })
	require.NoError(t, exe.AcquireTapeDevice(context.Background(), 1, "/dev/nst0", nil))
	require.NoError(t, exe.AcquireTapeDevice(context.Background(), 1, "/dev/sg0", func() { t.Error("aliases must reuse one attempt lease") }))
	require.Len(t, exe.attempts[1].resources, 1)

	// A queued Job waits; passive inspection reports contention without entering raw discovery.
	waiting := make(chan struct{})
	waitingCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	var group errgroup.Group
	defer func() { cancel(); _ = group.Wait() }()
	group.Go(func() error {
		return exe.AcquireTapeDevice(waitingCtx, 2, "/dev/st0", func() { close(waiting) })
	})
	select {
	case <-waiting:
	case <-waitingCtx.Done():
		t.Fatal("Job did not queue behind its alias lease")
	}
	reply, err := exe.InspectDevice(context.Background(), "/dev/sg0")
	require.NoError(t, err)
	require.Equal(t, entity.DeviceReason_DEVICE_REASON_BUSY, reply.Reason)
	require.True(t, reply.Device.Busy)
	require.Nil(t, reply.ObservedAtNs)
	require.Zero(t, provider.inspections)
	require.Zero(t, provider.ownershipChecks)
	for _, summary := range exe.ListDevices() {
		require.True(t, summary.Busy)
	}

	// Settlement hands the same resource to the waiting alias before later requests can take it.
	exe.endAttempt(1)
	require.NoError(t, group.Wait())
	require.Empty(t, exe.ListAvailableDevices())
	exe.endAttempt(2)
	require.Len(t, exe.ListAvailableDevices(), 3)
}

func TestDeviceReleaseUsesCapturedCanonicalKey(t *testing.T) {
	// The short request owns the resolved resource even if subsequent passive resolution fails.
	exe, provider := deviceFixture()
	release, acquired, err := exe.TryAcquireTapeDevice("/dev/nst0")
	require.NoError(t, err)
	require.True(t, acquired)
	provider.identity = tape.Identity{}
	provider.resolveErr = errors.New("sysfs unavailable")
	before := provider.resolves
	release()
	require.Equal(t, before, provider.resolves, "release must not re-resolve the device")
	require.False(t, exe.resourceHeld(tapeResource("/sys/scsi/drive0")))
	require.Len(t, exe.ListAvailableDevices(), 3)
}

func TestUnusableTapeCleanupRetainsAliasesAfterSysfsLoss(t *testing.T) {
	// Capture every known configured alias with the physical lease before cleanup becomes uncertain.
	exe, provider := deviceFixture()
	require.NoError(t, exe.beginAttempt(1, func(error) {}))
	require.NoError(t, exe.AcquireTapeDevice(context.Background(), 1, "/dev/nst0", nil))
	provider.identity = tape.Identity{}
	provider.resolveErr = errors.New("device disappeared")
	exe.keepTapeDeviceUnavailable(1, "/dev/nst0")
	exe.endAttempt(1)

	// Lifetime-retained exclusion stays visible under aliases, and inspection sends no commands.
	for _, summary := range exe.ListDevices() {
		require.True(t, summary.Busy)
		require.Contains(t, summary.Detail, "cleanup failed")
	}
	reply, err := exe.InspectDevice(context.Background(), "/dev/sg0")
	require.NoError(t, err)
	require.Equal(t, entity.DeviceReason_DEVICE_REASON_BUSY, reply.Reason)
	require.Zero(t, provider.inspections)
	require.Zero(t, provider.ownershipChecks)
}

func TestDeviceInspectionAllowlistAndNativeFallback(t *testing.T) {
	// Unconfigured and canceled requests do not resolve, lease or inspect a device.
	exe, provider := deviceFixture()
	_, err := exe.InspectDevice(context.Background(), "/dev/not-configured")
	require.ErrorContains(t, err, "not configured")
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = exe.InspectDevice(canceled, "/dev/nst0")
	require.ErrorIs(t, err, context.Canceled)
	require.Zero(t, provider.resolves)

	// Missing raw facts leave a typed unavailable report and permit the existing LTFS request path.
	provider.identity = tape.Identity{}
	provider.resolveErr = errors.New("raw sysfs information unavailable")
	reply, err := exe.InspectDevice(context.Background(), "/dev/nst0")
	require.NoError(t, err)
	require.Equal(t, entity.DeviceReason_DEVICE_REASON_TRANSPORT_UNAVAILABLE, reply.Reason)
	require.Nil(t, reply.ObservedAtNs)
	require.Zero(t, provider.inspections)
	release, acquired, err := exe.TryAcquireTapeDevice("/dev/nst0")
	require.NoError(t, err)
	require.True(t, acquired)
	release()
}

func TestDeviceInspectionOwnershipFailuresDoNotIssueCommands(t *testing.T) {
	for _, test := range []struct {
		name   string
		err    error
		reason entity.DeviceReason
		busy   bool
	}{
		{name: "external holder", err: tape.ErrBusy, reason: entity.DeviceReason_DEVICE_REASON_BUSY, busy: true},
		{name: "missing tool", err: tape.ErrMissingTool, reason: entity.DeviceReason_DEVICE_REASON_MISSING_TOOL},
		{name: "permission", err: os.ErrPermission, reason: entity.DeviceReason_DEVICE_REASON_PERMISSION},
		{name: "unknown owner", err: tape.ErrOwnershipUnknown, reason: entity.DeviceReason_DEVICE_REASON_OWNERSHIP_UNCERTAIN},
	} {
		t.Run(test.name, func(t *testing.T) {
			// Ownership failures prevent SCSI discovery while leaving ordinary native lease acquisition available.
			exe, provider := deviceFixture()
			provider.ownershipErr = fmt.Errorf("ownership check: %w", test.err)
			reply, err := exe.InspectDevice(context.Background(), "/dev/nst0")
			require.NoError(t, err)
			require.Equal(t, test.reason, reply.Reason)
			require.Equal(t, test.busy, reply.Device.Busy)
			require.Zero(t, provider.inspections)
			require.Equal(t, 1, provider.ownershipChecks)
			require.Len(t, exe.ListAvailableDevices(), 3)
		})
	}
}

func TestDeviceInspectionKeepsObservationsAndRetainsUnknownCommandOutcome(t *testing.T) {
	for _, uncertain := range []bool{false, true} {
		t.Run(fmt.Sprintf("uncertain=%t", uncertain), func(t *testing.T) {
			// The provider's drive facts survive a no-medium report; issued-command uncertainty retains exclusion.
			exe, provider := deviceFixture()
			vendor := "observed vendor"
			present := false
			provider.report = &entity.InspectDeviceResponse{Vendor: &vendor, Reason: entity.DeviceReason_DEVICE_REASON_NO_MEDIUM,
				Report: &entity.InspectDeviceResponse_Tape{Tape: &entity.TapeDeviceReport{MediumPresent: &present}}}
			if uncertain {
				provider.report.Reason = entity.DeviceReason_DEVICE_REASON_OWNERSHIP_UNCERTAIN
				provider.inspectErr = &tape.UncertainError{Cause: errors.New("command timeout")}
			}
			reply, err := exe.InspectDevice(context.Background(), "/dev/nst0")
			require.NoError(t, err)
			require.Equal(t, vendor, reply.GetVendor())
			require.False(t, reply.GetTape().GetMediumPresent())
			require.Equal(t, "/dev/nst0", reply.Device.Device)
			require.Equal(t, uncertain, reply.Device.Busy)
			require.Equal(t, 1, provider.inspections)

			// A retained key blocks every configured alias without another raw observation.
			if uncertain {
				again, err := exe.InspectDevice(context.Background(), "/dev/sg0")
				require.NoError(t, err)
				require.Equal(t, entity.DeviceReason_DEVICE_REASON_BUSY, again.Reason)
				require.Equal(t, 1, provider.inspections)
				for _, summary := range exe.ListDevices() {
					require.Contains(t, summary.Detail, "command outcome is unknown")
				}
			} else {
				require.Len(t, exe.ListAvailableDevices(), 3)
			}
		})
	}
}

func TestPartialRawResolutionStillUnifiesNativeTapeAliases(t *testing.T) {
	// A known SCSI identity remains usable for exclusion even when raw SG facts are incomplete.
	exe, provider := deviceFixture()
	provider.resolveErr = errors.New("SG aliases unavailable")
	release, acquired, err := exe.TryAcquireTapeDevice("/dev/nst0")
	require.NoError(t, err)
	require.True(t, acquired)
	_, acquired, err = exe.TryAcquireTapeDevice("/dev/sg0")
	require.NoError(t, err)
	require.False(t, acquired)

	// Busy inspection never treats optional raw failure as permission to bypass the native lease.
	reply, err := exe.InspectDevice(context.Background(), "/dev/st0")
	require.NoError(t, err)
	require.Equal(t, entity.DeviceReason_DEVICE_REASON_BUSY, reply.Reason)
	require.Zero(t, provider.inspections)
	release()
	require.Len(t, exe.ListAvailableDevices(), 3)
}

func TestDeviceIdentityRebindsBetweenFreeOperations(t *testing.T) {
	// Previously distinct configured names can identify the same drive on their next free operation.
	exe, provider := deviceFixture()
	exe.devices = []string{"/dev/tape-a", "/dev/tape-b"}
	provider.identities = map[string]tape.Identity{
		"/dev/tape-a": {Key: "/sys/scsi/drive0"},
		"/dev/tape-b": {Key: "/sys/scsi/drive1"},
	}
	release, acquired, err := exe.TryAcquireTapeDevice("/dev/tape-a")
	require.NoError(t, err)
	require.True(t, acquired)
	release()
	provider.identities["/dev/tape-a"] = tape.Identity{Key: "/sys/scsi/drive1"}

	// Rebinding must exclude a second alias instead of leaving the first name on its stale resource.
	release, acquired, err = exe.TryAcquireTapeDevice("/dev/tape-a")
	require.NoError(t, err)
	require.True(t, acquired)
	defer release()
	otherRelease, acquired, err := exe.TryAcquireTapeDevice("/dev/tape-b")
	if otherRelease != nil {
		defer otherRelease()
	}
	require.NoError(t, err)
	require.False(t, acquired)
}

func TestDeviceInspectionRebindsReleasedAccessIdentity(t *testing.T) {
	// A completed operation does not permanently bind the configured name to its old identity.
	exe, provider := deviceFixture()
	release, acquired, err := exe.TryAcquireTapeDevice("/dev/nst0")
	require.NoError(t, err)
	require.True(t, acquired)
	release()
	provider.identity.Key = "/sys/scsi/replacement"

	// A fresh free operation uses the observed replacement identity for inspection and exclusion.
	reply, err := exe.InspectDevice(context.Background(), "/dev/nst0")
	require.NoError(t, err)
	require.Equal(t, entity.DeviceReason_DEVICE_REASON_UNSPECIFIED, reply.Reason)
	require.Equal(t, 1, provider.ownershipChecks)
	require.Equal(t, 1, provider.inspections)
}

func TestConfiguredSymlinksRetainExclusionWithoutDeviceListing(t *testing.T) {
	for _, uncertainInspection := range []bool{false, true} {
		t.Run(fmt.Sprintf("inspection=%t", uncertainInspection), func(t *testing.T) {
			// Udev names are configured aliases even when sysfs lists only the kernel nodes.
			exe, provider := deviceFixture()
			exe.devices = []string{"/dev/tape/by-id/a", "/dev/tape/by-id/b"}
			provider.identity.Aliases = []string{"/dev/sg0", "/dev/nst0"}
			if uncertainInspection {
				provider.inspectErr = &tape.UncertainError{Cause: errors.New("command timeout")}
				_, err := exe.InspectDevice(context.Background(), exe.devices[0])
				require.NoError(t, err)
			} else {
				require.NoError(t, exe.beginAttempt(1, func(error) {}))
				require.NoError(t, exe.AcquireTapeDevice(context.Background(), 1, exe.devices[0], nil))
				exe.keepTapeDeviceUnavailable(1, exe.devices[0])
				exe.endAttempt(1)
			}
			require.Equal(t, len(exe.devices), provider.resolves, "capture configured names in one bounded metadata pass")
			provider.identity = tape.Identity{}
			provider.resolveErr = errors.New("sysfs disappeared")
			inspections := provider.inspections

			// The unlisted configured symlink cannot take a second lease after raw identity discovery fails.
			release, acquired, err := exe.TryAcquireTapeDevice(exe.devices[1])
			if release != nil {
				defer release()
			}
			require.NoError(t, err)
			require.False(t, acquired)
			reply, err := exe.InspectDevice(context.Background(), exe.devices[1])
			require.NoError(t, err)
			require.Equal(t, entity.DeviceReason_DEVICE_REASON_BUSY, reply.Reason)
			require.Equal(t, inspections, provider.inspections)
		})
	}
}

func TestNativeFallbackLeaseSurvivesRawDiscoveryRecovery(t *testing.T) {
	// Native LTFS can acquire a configured name before raw sysfs identity is available.
	exe, provider := deviceFixture()
	provider.identity = tape.Identity{}
	provider.resolveErr = errors.New("sysfs unavailable")
	release, acquired, err := exe.TryAcquireTapeDevice("/dev/nst0")
	require.NoError(t, err)
	require.True(t, acquired)
	defer release()
	provider.identity = tape.Identity{Key: "/sys/scsi/drive0", Aliases: exe.devices}
	provider.resolveErr = nil

	// Recovering metadata cannot move an in-use configured name onto a second canonical resource.
	otherRelease, acquired, err := exe.TryAcquireTapeDevice("/dev/sg0")
	if otherRelease != nil {
		defer otherRelease()
	}
	require.NoError(t, err)
	require.False(t, acquired)
	require.True(t, exe.resourceHeld(tapeResource("/dev/nst0")))
}

func TestChangedIdentityPreservesQueuedLeaseAndCancellation(t *testing.T) {
	// A queued attempt shares its holder's captured key even if metadata later changes.
	exe, provider := deviceFixture()
	release, acquired, err := exe.TryAcquireTapeDevice("/dev/nst0")
	require.NoError(t, err)
	require.True(t, acquired)
	require.NoError(t, exe.beginAttempt(1, func(error) {}))
	t.Cleanup(func() { exe.endAttempt(1) })
	waiting := make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result := make(chan error, 1)
	go func() {
		result <- exe.AcquireTapeDevice(ctx, 1, "/dev/sg0", func() { close(waiting) })
	}()
	select {
	case <-waiting:
	case <-time.After(5 * time.Second):
		cancel()
		t.Fatal("alias did not queue")
	}
	provider.lock.Lock()
	provider.identity.Key = "/sys/scsi/replacement"
	provider.lock.Unlock()

	// Changed metadata cannot replace a captured key while a holder and waiter still use it.
	reply, err := exe.InspectDevice(context.Background(), "/dev/st0")
	require.NoError(t, err)
	require.Equal(t, entity.DeviceReason_DEVICE_REASON_BUSY, reply.Reason)
	require.Zero(t, provider.inspections)

	// Canceling the waiter leaves the original holder in place and sends no raw command.
	cancel()
	require.ErrorIs(t, <-result, context.Canceled)
	reply, err = exe.InspectDevice(context.Background(), "/dev/st0")
	require.NoError(t, err)
	require.Equal(t, entity.DeviceReason_DEVICE_REASON_BUSY, reply.Reason)
	require.Zero(t, provider.inspections)
	require.True(t, exe.resourceHeld(tapeResource("/sys/scsi/drive0")))
	release()

	// Once all holders and waiters settle, a later operation can bind the current identity.
	reply, err = exe.InspectDevice(context.Background(), "/dev/st0")
	require.NoError(t, err)
	require.Equal(t, entity.DeviceReason_DEVICE_REASON_UNSPECIFIED, reply.Reason)
	require.Equal(t, 1, provider.inspections)
	require.False(t, exe.resourceHeld(tapeResource("/sys/scsi/replacement")))
}

func TestDeviceInspectionCancellationReleasesOnlyKnownOutcomes(t *testing.T) {
	for _, uncertain := range []bool{false, true} {
		t.Run(fmt.Sprintf("uncertain=%t", uncertain), func(t *testing.T) {
			// Cancellation after an issued command releases only a known completed outcome.
			exe, provider := deviceFixture()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			provider.cancelInspection = cancel
			provider.inspectErr = context.Canceled
			if uncertain {
				provider.inspectErr = &tape.UncertainError{Cause: context.Canceled}
			}
			reply, err := exe.InspectDevice(ctx, "/dev/nst0")
			if uncertain {
				require.NoError(t, err)
				require.True(t, reply.Device.Busy)
			} else {
				require.ErrorIs(t, err, context.Canceled)
				require.Nil(t, reply)
			}

			// The retained result excludes every alias; a known cancellation leaves LTFS admission free.
			release, acquired, err := exe.TryAcquireTapeDevice("/dev/sg0")
			if release != nil {
				defer release()
			}
			require.NoError(t, err)
			require.Equal(t, !uncertain, acquired)
			require.Equal(t, 1, provider.inspections)
		})
	}
}

func TestDeviceResolutionFailuresPreserveTypedReasonsAndNativeAdmission(t *testing.T) {
	for _, test := range []struct {
		name   string
		err    error
		reason entity.DeviceReason
	}{
		{name: "permission", err: os.ErrPermission, reason: entity.DeviceReason_DEVICE_REASON_PERMISSION},
		{name: "unsupported", err: tape.ErrUnsupported, reason: entity.DeviceReason_DEVICE_REASON_UNSUPPORTED},
		{name: "missing identity", err: os.ErrNotExist, reason: entity.DeviceReason_DEVICE_REASON_TRANSPORT_UNAVAILABLE},
	} {
		t.Run(test.name, func(t *testing.T) {
			// Passive identity failures report their known cause without issuing ownership or raw commands.
			exe, provider := deviceFixture()
			provider.identity = tape.Identity{}
			provider.resolveErr = fmt.Errorf("resolve failed: %w", test.err)
			reply, err := exe.InspectDevice(context.Background(), "/dev/nst0")
			require.NoError(t, err)
			require.Equal(t, test.reason, reply.Reason)
			require.Nil(t, reply.ObservedAtNs)
			require.Zero(t, provider.ownershipChecks)
			require.Zero(t, provider.inspections)

			// Raw discovery failure never changes configured LTFS adapter admission.
			release, acquired, err := exe.TryAcquireTapeDevice("/dev/nst0")
			require.NoError(t, err)
			require.True(t, acquired)
			release()
		})
	}
}
