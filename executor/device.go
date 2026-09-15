package executor

import (
	"fmt"
	"sort"
)

func (e *Executor) ListAvailableDevices() []string {
	e.devicesLock.Lock()
	defer e.devicesLock.Unlock()

	devices := e.availableDevices.ToSlice()
	sort.Slice(devices, func(i, j int) bool {
		return devices[i] < devices[j]
	})

	return devices
}

func (e *Executor) OccupyDevice(dev string) bool {
	e.devicesLock.Lock()
	defer e.devicesLock.Unlock()

	if !e.availableDevices.Contains(dev) {
		return false
	}

	e.availableDevices.Remove(dev)
	return true
}

func (e *Executor) ReleaseDevice(dev string) {
	e.devicesLock.Lock()
	defer e.devicesLock.Unlock()
	e.availableDevices.Add(dev)
}

// LeaseTapeDevice holds a configured Tape device until the Job attempt ends.
func (e *Executor) LeaseTapeDevice(jobID int64, device string) bool {
	e.attemptsLock.Lock()
	defer e.attemptsLock.Unlock()

	current := e.attempts[jobID]
	if current == nil {
		return false
	}
	key := fmt.Sprintf("tape:%s", device)

	e.resourcesLock.Lock()
	defer e.resourcesLock.Unlock()
	e.devicesLock.Lock()
	defer e.devicesLock.Unlock()
	if e.leasedResources.Contains(key) || !e.availableDevices.Contains(device) {
		return false
	}

	e.leasedResources.Add(key)
	e.availableDevices.Remove(device)
	current.resources = append(current.resources, attemptResource{
		key: key, device: device, releaseDevice: true,
	})
	return true
}

func (e *Executor) keepTapeDeviceUnavailable(jobID int64, device string) {
	e.attemptsLock.Lock()
	defer e.attemptsLock.Unlock()

	current := e.attempts[jobID]
	if current == nil {
		return
	}
	for index := range current.resources {
		resource := &current.resources[index]
		if resource.device == device {
			resource.releaseDevice = false
			return
		}
	}
}

// LeaseVolume holds one mounted Volume identity until the Job attempt ends.
func (e *Executor) LeaseVolume(jobID int64, uuid string) bool {
	e.attemptsLock.Lock()
	defer e.attemptsLock.Unlock()

	current := e.attempts[jobID]
	if current == nil {
		return false
	}
	key := fmt.Sprintf("volume:%s", uuid)

	e.resourcesLock.Lock()
	defer e.resourcesLock.Unlock()
	if e.leasedResources.Contains(key) {
		return false
	}
	e.leasedResources.Add(key)
	current.resources = append(current.resources, attemptResource{key: key})
	return true
}
