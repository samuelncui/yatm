package executor

import "sort"

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
