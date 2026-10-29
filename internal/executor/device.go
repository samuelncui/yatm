package executor

import (
	"context"
	"fmt"
	"sort"

	"github.com/samuelncui/yatm/entity"
)

// ListDevices reports every configured Tape drive without issuing SCSI commands.
func (e *Executor) ListDevices() []*entity.DeviceSummary {
	// The configured spelling remains the operator-facing identifier across aliases.
	resolved := e.resolveTapeDevices()
	devices := make([]*entity.DeviceSummary, 0, len(e.devices))
	for _, device := range e.devices {
		devices = append(devices, e.deviceSummary(device, resolved[device].key))
	}
	sort.Slice(devices, func(i, j int) bool { return devices[i].Device < devices[j].Device })
	return devices
}

// ListAvailableDevices retains the native available-device view over the shared summaries.
func (e *Executor) ListAvailableDevices() []string {
	devices := make([]string, 0, len(e.devices))
	for _, device := range e.ListDevices() {
		if !device.Busy {
			devices = append(devices, device.Device)
		}
	}
	return devices
}

// TryAcquireTapeDevice reserves a configured drive without waiting and captures its release key.
func (e *Executor) TryAcquireTapeDevice(device string) (func(), bool, error) {
	// Reject names outside the configured Tape adapter boundary.
	if !e.configuredDevice(device) {
		return nil, false, fmt.Errorf("Tape device is not configured, device=%q", device)
	}

	// A raw-inspection failure does not prevent configured LTFS adapters from taking their lease.
	_, release, acquired := e.tryAcquireTapeDevice(device)
	return release, acquired, nil
}

func (e *Executor) configuredDevice(dev string) bool {
	for _, device := range e.devices {
		if device == dev {
			return true
		}
	}
	return false
}

// AcquireTapeDevice waits for a configured Tape drive and holds it until the Job attempt ends.
// Contention with another Client or operator request waits instead of failing the Job.
func (e *Executor) AcquireTapeDevice(ctx context.Context, jobID int64, device string, onWait func()) error {
	// An unknown drive is a configuration error, not contention to wait for.
	if !e.configuredDevice(device) {
		return fmt.Errorf("Tape device is not configured, device=%q", device)
	}

	// Capture identity and enqueue ownership before another discovery pass can rebind a free name.
	return e.holdAttemptResource(ctx, jobID, func() string {
		return e.resolveTapeDevicesLocked()[device].key
	}, onWait)
}

// AcquireVolume waits for one mounted Volume identity and holds it until the Job attempt ends.
func (e *Executor) AcquireVolume(ctx context.Context, jobID int64, uuid string, onWait func()) error {
	return e.holdAttemptResource(ctx, jobID, func() string { return volumeResource(uuid) }, onWait)
}

// holdAttemptResource waits for one shared resource and registers it on the Job attempt, so the
// attempt end releases every hold even when the caller never reaches its cleanup.
func (e *Executor) holdAttemptResource(ctx context.Context, jobID int64, resolve func() string, onWait func()) error {
	// Keep attempt reuse, identity capture and queue admission atomic in attempt/device/queue lock order.
	e.attemptsLock.Lock()
	current := e.attempts[jobID]
	if current == nil {
		e.attemptsLock.Unlock()
		return fmt.Errorf("job attempt is not active, id=%d", jobID)
	}
	e.deviceLock.Lock()
	key := resolve()
	for _, resource := range current.resources {
		if resource.key == key {
			e.deviceLock.Unlock()
			e.attemptsLock.Unlock()
			return nil
		}
	}
	ready, err := e.resources.enqueue(ctx, key)
	e.deviceLock.Unlock()
	e.attemptsLock.Unlock()
	if err != nil {
		return err
	}

	// Waiting and phase publication happen without identity or attempt locks, preserving cancellation.
	if err := e.resources.wait(ctx, key, ready, onWait); err != nil {
		return err
	}

	// Register the hold before it is used; an attempt that ended meanwhile gives it back.
	e.attemptsLock.Lock()
	current = e.attempts[jobID]
	if current == nil {
		e.attemptsLock.Unlock()
		e.resources.release(key)
		return fmt.Errorf("job attempt ended while acquiring %q", key)
	}
	current.resources = append(current.resources, attemptResource{key: key})
	e.attemptsLock.Unlock()
	return nil
}

// keepTapeDeviceUnavailable retains a drive whose physical cleanup failed, so later attempts
// wait for an explicit investigation instead of reusing unknown device state.
func (e *Executor) keepTapeDeviceUnavailable(jobID int64, device string) {
	// Reuse the remembered lease identity even when failed I/O makes sysfs unavailable.
	e.deviceLock.Lock()
	key := e.deviceKeys[device]
	e.deviceLock.Unlock()
	if key == "" {
		key = tapeResource(device)
	}

	// Retain the actual attempt resource through settlement.
	e.attemptsLock.Lock()
	defer e.attemptsLock.Unlock()

	current := e.attempts[jobID]
	if current == nil {
		return
	}
	for index := range current.resources {
		resource := &current.resources[index]
		if resource.key == key {
			resource.retain = true
			e.deviceLock.Lock()
			e.deviceUnavailable[key] = "Tape cleanup failed; device retained for investigation"
			e.deviceLock.Unlock()
			return
		}
	}
}
