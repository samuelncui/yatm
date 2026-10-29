package executor

import (
	"context"
	"errors"
	"fmt"

	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/internal/media/tape"
)

// tapeDeviceProvider owns passive OS resolution, ownership evidence and raw observations.
type tapeDeviceProvider interface {
	Resolve(string) (tape.Identity, error)
	CheckOwnership(context.Context, tape.Identity) error
	Inspect(context.Context, tape.Identity) (*entity.InspectDeviceResponse, error)
	Open(tape.Identity) (tape.Transport, error)
}

type rawTapeProvider struct{}

func (rawTapeProvider) Resolve(device string) (tape.Identity, error) { return tape.Resolve(device) }

func (rawTapeProvider) CheckOwnership(ctx context.Context, identity tape.Identity) error {
	return tape.CheckOwnership(ctx, identity)
}

func (rawTapeProvider) Inspect(ctx context.Context, identity tape.Identity) (*entity.InspectDeviceResponse, error) {
	return tape.Inspect(ctx, identity)
}

func (rawTapeProvider) Open(identity tape.Identity) (tape.Transport, error) {
	return tape.OpenTransport(identity.SG, false)
}

type tapeDeviceResolution struct {
	identity tape.Identity
	key      string
	err      error
}

func (e *Executor) resolveTapeDevices() map[string]*tapeDeviceResolution {
	e.deviceLock.Lock()
	defer e.deviceLock.Unlock()
	return e.resolveTapeDevicesLocked()
}

// tryAcquireTapeDevice keeps identity capture and non-waiting admission under the same device lock.
func (e *Executor) tryAcquireTapeDevice(device string) (*tapeDeviceResolution, func(), bool) {
	e.deviceLock.Lock()
	defer e.deviceLock.Unlock()
	resolved := e.resolveTapeDevicesLocked()[device]
	release, acquired := e.tryAcquire(resolved.key)
	return resolved, release, acquired
}

// resolveTapeDevicesLocked requires deviceLock; callers retain it through lease admission.
func (e *Executor) resolveTapeDevicesLocked() map[string]*tapeDeviceResolution {
	// Resolve only configured names, including udev symlinks, before any lease or raw command.
	resolved := make(map[string]*tapeDeviceResolution, len(e.devices))
	observed := make(map[string]string, len(e.devices))
	for _, device := range e.devices {
		identity, err := e.tapeProvider.Resolve(device)
		resolved[device] = &tapeDeviceResolution{identity: identity, err: err}
		observed[device] = identity.Key
		if e.deviceKeys[device] == "" {
			e.deviceKeys[device] = tapeResource(device)
		}
	}

	// Kernel-reported aliases can supply exclusion identity when optional raw facts are incomplete.
	for _, device := range e.devices {
		identity := resolved[device].identity
		if identity.Key == "" {
			continue
		}
		for _, alias := range identity.Aliases {
			if _, configured := resolved[alias]; configured && observed[alias] == "" {
				observed[alias] = identity.Key
			}
		}
	}

	// Held, queued and retained resources keep their captured identity across metadata changes or loss.
	e.resources.lock.Lock()
	defer e.resources.lock.Unlock()
	protected := func(key string) bool {
		_, held := e.resources.held[key]
		return held || len(e.resources.waiters[key]) > 0 || e.deviceUnavailable[key] != ""
	}
	pinned := make(map[string]string)
	for _, device := range e.devices {
		identity := observed[device]
		if identity != "" && protected(e.deviceKeys[device]) && pinned[identity] == "" {
			pinned[identity] = e.deviceKeys[device]
		}
	}

	// Free names follow current authoritative identity, sharing any already-held alias in their group.
	for _, device := range e.devices {
		key := e.deviceKeys[device]
		if identity := observed[device]; identity != "" && !protected(key) {
			key = pinned[identity]
			if key == "" {
				key = tapeResource(identity)
			}
			e.deviceKeys[device] = key
		}
		resolved[device].key = key
	}
	return resolved
}

func (e *Executor) deviceSummary(device, key string) *entity.DeviceSummary {
	summary := &entity.DeviceSummary{Device: device, Kind: entity.DeviceKind_DEVICE_KIND_TAPE, Busy: e.resourceHeld(key)}
	if summary.Busy {
		e.deviceLock.Lock()
		summary.Detail = e.deviceUnavailable[key]
		e.deviceLock.Unlock()
		if summary.Detail == "" {
			summary.Detail = "Tape device is in use"
		}
	}
	return summary
}

// InspectDevice performs bounded passive discovery under a non-waiting configured-device lease.
// A leased Media session uses the same provider directly without reacquiring its resource.
func (e *Executor) InspectDevice(ctx context.Context, device string) (result *entity.InspectDeviceResponse, returnErr error) {
	// An arbitrary path never reaches a provider, and already-canceled requests take no lease.
	if !e.configuredDevice(device) {
		return nil, fmt.Errorf("Tape device is not configured, device=%q", device)
	}
	defer func() {
		if result != nil {
			result.PreparedRead = e.lastPreparedRead(device)
		}
	}()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	resolved, release, acquired := e.tryAcquireTapeDevice(device)
	identity, key, resolveErr := resolved.identity, resolved.key, resolved.err
	reply := &entity.InspectDeviceResponse{Device: &entity.DeviceSummary{Device: device, Kind: entity.DeviceKind_DEVICE_KIND_TAPE}}
	if !acquired {
		reply.Device = e.deviceSummary(device, key)
		reply.Device.Busy = true
		if reply.Device.Detail == "" {
			reply.Device.Detail = "Tape device is in use"
		}
		reply.Reason, reply.Detail = entity.DeviceReason_DEVICE_REASON_BUSY, reply.Device.Detail
		return reply, nil
	}
	retain := false
	defer func() {
		if !retain {
			release()
		}
	}()

	// Lack of raw access facts blocks inspection without changing existing LTFS admission.
	if resolveErr != nil {
		reply.Reason, reply.Detail = tape.Reason(resolveErr), resolveErr.Error()
		if reply.Reason == entity.DeviceReason_DEVICE_REASON_TRANSPORT_ERROR {
			reply.Reason = entity.DeviceReason_DEVICE_REASON_TRANSPORT_UNAVAILABLE
		}
		return reply, nil
	}
	if key != tapeResource(identity.Key) {
		reply.Reason, reply.Detail = entity.DeviceReason_DEVICE_REASON_OWNERSHIP_UNCERTAIN, "Tape access identity changed"
		return reply, nil
	}
	if err := e.tapeProvider.CheckOwnership(ctx, identity); err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		reply.Reason, reply.Detail = tape.Reason(err), err.Error()
		if errors.Is(err, tape.ErrBusy) {
			reply.Device.Busy = true
			reply.Device.Detail = err.Error()
			reply.Reason = entity.DeviceReason_DEVICE_REASON_BUSY
		}
		return reply, nil
	}

	// The provider reports observations; only unknown issued-command outcomes retain exclusion.
	observed, err := e.tapeProvider.Inspect(ctx, identity)
	retain = errors.Is(err, tape.ErrUncertain)
	if retain {
		e.deviceLock.Lock()
		e.deviceUnavailable[key] = "Tape command outcome is unknown; device retained for investigation"
		e.deviceLock.Unlock()
	}
	if observed == nil {
		if err != nil {
			return nil, fmt.Errorf("inspect Tape device failed, %w", err)
		}
		return nil, fmt.Errorf("inspect Tape device returned no report")
	}
	observed.Device = reply.Device
	if retain {
		observed.Device.Busy = true
		observed.Device.Detail = "Tape command outcome is unknown; device retained for investigation"
	}
	if err != nil && !retain && ctx.Err() != nil {
		return nil, ctx.Err()
	}
	return observed, nil
}
