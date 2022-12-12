package executor

import (
	"context"
	"fmt"
	"sort"
	"sync"

	"github.com/abc950309/tapewriter/entity"
	"github.com/abc950309/tapewriter/library"
	mapset "github.com/deckarep/golang-set/v2"
	"gorm.io/gorm"
)

type Executor struct {
	db  *gorm.DB
	lib *library.Library

	devices []string

	devicesLock      sync.Mutex
	availableDevices mapset.Set[string]

	workDirectory string
	encryptScript string
	mkfsScript    string
	mountScript   string
	umountScript  string
}

func New(
	db *gorm.DB, lib *library.Library,
	devices []string, workDirectory string,
	encryptScript, mkfsScript, mountScript, umountScript string,
) *Executor {
	return &Executor{
		db:               db,
		lib:              lib,
		devices:          devices,
		availableDevices: mapset.NewThreadUnsafeSet(devices...),
		encryptScript:    encryptScript,
		mkfsScript:       mkfsScript,
		mountScript:      mountScript,
		umountScript:     umountScript,
	}
}

func (e *Executor) AutoMigrate() error {
	return e.db.AutoMigrate(ModelJob)
}

func (e *Executor) ListAvailableDevices() []string {
	e.devicesLock.Lock()
	defer e.devicesLock.Unlock()

	devices := e.availableDevices.ToSlice()
	sort.Slice(devices, func(i, j int) bool {
		return devices[i] < devices[j]
	})

	return devices
}

func (e *Executor) occupyDevice(dev string) bool {
	e.devicesLock.Lock()
	defer e.devicesLock.Unlock()

	if !e.availableDevices.Contains(dev) {
		return false
	}

	e.availableDevices.Remove(dev)
	return true
}

func (e *Executor) releaseDevice(dev string) {
	e.devicesLock.Lock()
	defer e.devicesLock.Unlock()
	e.availableDevices.Add(dev)
}

func (e *Executor) Start(ctx context.Context, job *Job) error {
	job.Status = entity.JobStatus_Processing
	if _, err := e.SaveJob(ctx, job); err != nil {
		return err
	}

	if state := job.State.GetArchive(); state != nil {
		if err := e.startArchive(ctx, job); err != nil {
			return err
		}
		return nil
	}

	return fmt.Errorf("unexpected state type, %T", job.State.State)
}

func (e *Executor) Submit(ctx context.Context, job *Job, param *entity.JobNextParam) error {
	if job.Status != entity.JobStatus_Processing {
		return fmt.Errorf("target job is not on processing, status= %s", job.Status)
	}

	if state := job.State.GetArchive(); state != nil {
		exe, err := e.newArchiveExecutor(ctx, job)
		if err != nil {
			return err
		}

		exe.submit(param.GetArchive())
		return nil
	}

	return fmt.Errorf("unexpected state type, %T", job.State.State)
}

func (e *Executor) Display(ctx context.Context, job *Job) (*entity.JobDisplay, error) {
	if job.Status != entity.JobStatus_Processing {
		return nil, fmt.Errorf("target job is not on processing, status= %s", job.Status)
	}

	if state := job.State.GetArchive(); state != nil {
		display, err := e.getArchiveDisplay(ctx, job)
		if err != nil {
			return nil, err
		}

		return &entity.JobDisplay{Display: &entity.JobDisplay_Archive{Archive: display}}, nil
	}

	return nil, fmt.Errorf("unexpected state type, %T", job.State.State)
}
