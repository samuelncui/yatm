package executor

import (
	"context"
	"fmt"
	"sync"

	mapset "github.com/deckarep/golang-set/v2"
	"github.com/modern-go/reflect2"
	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/library"
	"github.com/samuelncui/yatm/tools"
	"gorm.io/gorm"
)

type Executor struct {
	db  *gorm.DB
	lib *library.Library

	devicesLock      sync.Mutex
	devices          []string
	availableDevices mapset.Set[string]

	paths   Paths
	scripts Scripts

	jobExecutors *tools.CacheOnce[int64, JobExecutor]
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
	e := &Executor{
		db:               db,
		lib:              lib,
		devices:          devices,
		availableDevices: mapset.NewThreadUnsafeSet(devices...),
		paths:            paths,
		scripts:          scripts,
	}
	e.jobExecutors = tools.NewCacheOnce(e.newJobExecutor)

	return e
}

func (e *Executor) AutoMigrate() error {
	return e.db.AutoMigrate(ModelJob)
}

func (e *Executor) CreateJob(ctx context.Context, job *Job, param *entity.JobParam) (*Job, error) {
	job, err := e.SaveJob(ctx, job)
	if err != nil {
		return nil, fmt.Errorf("save job fail, err= %w", err)
	}

	typ, found := jobTypes[jobParamToTypes[reflect2.RTypeOf(param.GetParam())]]
	if !found || typ == nil {
		return nil, fmt.Errorf("job type unexpected, state_type= %T", param.GetParam())
	}

	executor, err := typ.GetExecutor(ctx, e, job)
	if err != nil {
		return nil, fmt.Errorf("get job executor fail, job_id= %d, %w", job.ID, err)
	}
	if err := executor.Initialize(ctx, param); err != nil {
		executor.Logger().WithContext(ctx).WithError(err).Errorf("initialize failed, param= %s", param)
		return nil, fmt.Errorf("executor initialize fail, job_id= %d param= %s, %w", job.ID, param, err)
	}
	if err := executor.Close(ctx); err != nil {
		executor.Logger().WithContext(ctx).WithError(err).Errorf("close executor failed, param= %s", param)
		return nil, fmt.Errorf("close executor failed, job_id= %d param= %s, %w", job.ID, param, err)
	}

	return job, nil
}

func (e *Executor) GetJobExecutor(ctx context.Context, id int64) (JobExecutor, error) {
	return e.jobExecutors.Get(ctx, id)
}

func (e *Executor) RemoveJobExecutor(ctx context.Context, id int64) {
	e.jobExecutors.Remove(id)
}

func (e *Executor) newJobExecutor(ctx context.Context, id int64) (JobExecutor, error) {
	job, err := e.GetJob(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("get job fail, id= %d, %w", id, err)
	}

	factory, has := jobTypes[reflect2.RTypeOf(job.State.GetState())]
	if !has {
		return nil, fmt.Errorf("job type unexpected, state_type= %T", job.State.GetState())
	}

	return factory.GetExecutor(ctx, e, job)
}

func (e *Executor) Dispatch(ctx context.Context, jobID int64, param *entity.JobDispatchParam) error {
	executor, err := e.GetJobExecutor(ctx, jobID)
	if err != nil {
		return fmt.Errorf("get job executor fail, job_id= %d, %w", jobID, err)
	}

	if err := executor.Dispatch(ctx, param); err != nil {
		executor.Logger().WithContext(ctx).WithError(err).Errorf("dispatch request fail, req= %s", param)
		return fmt.Errorf("dispatch request fail, job_id= %d, req= %s, %w", jobID, param, err)
	}

	return nil
}

func (e *Executor) Display(ctx context.Context, job *Job) (*entity.JobDisplay, error) {
	if job.Status != entity.JobStatus_PROCESSING {
		return nil, fmt.Errorf("target job is not on processing, status= %s", job.Status)
	}

	executor, err := e.GetJobExecutor(ctx, job.ID)
	if err != nil {
		return nil, fmt.Errorf("get job executor fail, job_id= %d, %w", job.ID, err)
	}

	result, err := executor.Display(ctx)
	if err != nil {
		executor.Logger().WithContext(ctx).WithError(err).Errorf("get display failed")
		return nil, err
	}

	return result, nil
}
