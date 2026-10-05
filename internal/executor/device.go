package executor

import (
	"context"
	"fmt"
	"sort"
)

// ListAvailableDevices reports configured Tape drives that no operation currently holds.
func (e *Executor) ListAvailableDevices() []string {
	devices := make([]string, 0, len(e.devices))
	for _, device := range e.devices {
		if !e.resourceHeld(tapeResource(device)) {
			devices = append(devices, device)
		}
	}
	sort.Strings(devices)
	return devices
}

// OccupyDevice reserves a configured drive for one operator request without waiting.
func (e *Executor) OccupyDevice(dev string) bool {
	if !e.configuredDevice(dev) {
		return false
	}
	_, acquired := e.tryAcquire(tapeResource(dev))
	return acquired
}

// ReleaseDevice returns a drive reserved by OccupyDevice.
func (e *Executor) ReleaseDevice(dev string) {
	e.resources.release(tapeResource(dev))
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
	return e.holdAttemptResource(ctx, jobID, tapeResource(device), onWait)
}

// AcquireVolume waits for one mounted Volume identity and holds it until the Job attempt ends.
func (e *Executor) AcquireVolume(ctx context.Context, jobID int64, uuid string, onWait func()) error {
	return e.holdAttemptResource(ctx, jobID, volumeResource(uuid), onWait)
}

// holdAttemptResource waits for one shared resource and registers it on the Job attempt, so the
// attempt end releases every hold even when the caller never reaches its cleanup.
func (e *Executor) holdAttemptResource(ctx context.Context, jobID int64, key string, onWait func()) error {
	// Reuse this attempt's lease when identity resolution and Session setup need the same resource.
	e.attemptsLock.Lock()
	current := e.attempts[jobID]
	if current == nil {
		e.attemptsLock.Unlock()
		return fmt.Errorf("job attempt is not active, id=%d", jobID)
	}
	for _, resource := range current.resources {
		if resource.key == key {
			e.attemptsLock.Unlock()
			return nil
		}
	}
	e.attemptsLock.Unlock()

	release, err := e.AcquireJobResource(ctx, key, onWait)
	if err != nil {
		return err
	}

	// Register the hold before it is used; an attempt that ended meanwhile gives it back.
	e.attemptsLock.Lock()
	current = e.attempts[jobID]
	if current == nil {
		e.attemptsLock.Unlock()
		release()
		return fmt.Errorf("job attempt ended while acquiring %q", key)
	}
	current.resources = append(current.resources, attemptResource{key: key})
	e.attemptsLock.Unlock()
	return nil
}

// keepTapeDeviceUnavailable retains a drive whose physical cleanup failed, so later attempts
// wait for an explicit investigation instead of reusing unknown device state.
func (e *Executor) keepTapeDeviceUnavailable(jobID int64, device string) {
	e.attemptsLock.Lock()
	defer e.attemptsLock.Unlock()

	current := e.attempts[jobID]
	if current == nil {
		return
	}
	for index := range current.resources {
		resource := &current.resources[index]
		if resource.key == tapeResource(device) {
			resource.retain = true
			return
		}
	}
}
