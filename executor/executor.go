package executor

import (
	"context"
	"fmt"
	"sort"
	"sync"

	mapset "github.com/deckarep/golang-set/v2"
	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/library"
	"gorm.io/gorm"
)

type Executor struct {
	db  *gorm.DB
	lib *library.Library

	devices []string

	devicesLock      sync.Mutex
	availableDevices mapset.Set[string]

	paths   Paths
	scripts Scripts
}

type Paths struct {
	Work   string `yaml:"work"`
	Source string `yaml:"source"`
	Target string `yaml:"target"`
}

type Scripts struct {
	Encrypt  string `yaml:"encrypt"`
	Mkfs     string `yaml:"mkfs"`
	Mount    string `yaml:"mount"`
	Umount   string `yaml:"umount"`
	ReadInfo string `yaml:"read_info"`
}

func New(
	db *gorm.DB, lib *library.Library,
	devices []string, paths Paths, scripts Scripts,
) *Executor {
	return &Executor{
		db:               db,
		lib:              lib,
		devices:          devices,
		availableDevices: mapset.NewThreadUnsafeSet(devices...),
		paths:            paths,
		scripts:          scripts,
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
	job.Status = entity.JobStatus_PROCESSING
	if _, err := e.SaveJob(ctx, job); err != nil {
		return err
	}

	if state := job.State.GetArchive(); state != nil {
		if err := e.startArchive(ctx, job); err != nil {
			return err
		}
		return nil
	}
	if state := job.State.GetRestore(); state != nil {
		if err := e.startRestore(ctx, job); err != nil {
			return err
		}
		return nil
	}

	return fmt.Errorf("unexpected state type, %T", job.State.State)
}

func (e *Executor) Submit(ctx context.Context, job *Job, param *entity.JobNextParam) error {
	if job.Status != entity.JobStatus_PROCESSING {
		return fmt.Errorf("target job is not on processing, status= %s", job.Status)
	}

	if state := job.State.GetArchive(); state != nil {
		exe, err := e.newArchiveExecutor(ctx, job)
		if err != nil {
			return err
		}

		exe.submit(ctx, param.GetArchive())
		return nil
	}
	if state := job.State.GetRestore(); state != nil {
		exe, err := e.newRestoreExecutor(ctx, job)
		if err != nil {
			return err
		}

		exe.submit(ctx, param.GetRestore())
		return nil
	}

	return fmt.Errorf("unexpected state type, %T", job.State.State)
}

func (e *Executor) Display(ctx context.Context, job *Job) (*entity.JobDisplay, error) {
	if job.Status != entity.JobStatus_PROCESSING {
		return nil, fmt.Errorf("target job is not on processing, status= %s", job.Status)
	}

	if state := job.State.GetArchive(); state != nil {
		display, err := e.getArchiveDisplay(ctx, job)
		if err != nil {
			return nil, err
		}

		return &entity.JobDisplay{Display: &entity.JobDisplay_Archive{Archive: display}}, nil
	}
	if state := job.State.GetRestore(); state != nil {
		display, err := e.getRestoreDisplay(ctx, job)
		if err != nil {
			return nil, err
		}

		return &entity.JobDisplay{Display: &entity.JobDisplay_Restore{Restore: display}}, nil
	}

	return nil, fmt.Errorf("unexpected state type, %T", job.State.State)
}
