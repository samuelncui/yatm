package executor

import (
	"context"
	"fmt"
	"time"

	"github.com/abc950309/tapewriter/entity"
	"gorm.io/gorm"
)

var (
	ModelJob = &Job{}

	ErrJobNotFound = fmt.Errorf("get job: job not found")
)

type Job struct {
	ID       int64 `gorm:"primaryKey;autoIncrement"`
	Status   entity.JobStatus
	Priority int64
	State    *entity.JobState

	CreateTime time.Time
	UpdateTime time.Time
}

func (j *Job) BeforeUpdate(tx *gorm.DB) error {
	j.UpdateTime = time.Now()
	if j.CreateTime.IsZero() {
		j.CreateTime = j.UpdateTime
	}
	return nil
}

func (e *Executor) initJob(ctx context.Context, job *Job, param *entity.JobParam) error {
	if p := param.GetArchive(); p != nil {
		return e.initArchive(ctx, job, p)
	}
	return fmt.Errorf("unexpected param type, %T", param.Param)
}

func (e *Executor) CreateJob(ctx context.Context, job *Job, param *entity.JobParam) (*Job, error) {
	if err := e.initJob(ctx, job, param); err != nil {
		return nil, err
	}

	if r := e.db.WithContext(ctx).Create(job); r.Error != nil {
		return nil, fmt.Errorf("save job fail, err= %w", r.Error)
	}

	return job, nil
}

func (e *Executor) SaveJob(ctx context.Context, job *Job) (*Job, error) {
	if r := e.db.WithContext(ctx).Save(job); r.Error != nil {
		return nil, fmt.Errorf("save job fail, err= %w", r.Error)
	}
	return job, nil
}

func (e *Executor) MGetJob(ctx context.Context, ids ...int64) (map[int64]*Job, error) {
	if len(ids) == 0 {
		return map[int64]*Job{}, nil
	}

	jobs := make([]*Job, 0, len(ids))
	if r := e.db.WithContext(ctx).Where("id IN (?)", ids).Find(&jobs); r.Error != nil {
		return nil, fmt.Errorf("list jobs fail, err= %w", r.Error)
	}

	result := make(map[int64]*Job, len(jobs))
	for _, job := range jobs {
		result[job.ID] = job
	}

	return result, nil
}

func (e *Executor) GetJob(ctx context.Context, id int64) (*Job, error) {
	jobs, err := e.MGetJob(ctx, id)
	if err != nil {
		return nil, err
	}

	job, ok := jobs[id]
	if !ok || job == nil {
		return nil, ErrJobNotFound
	}

	return job, nil
}

// func (e *Executor) getNextJob(ctx context.Context) (*Job, error) {
// 	job := new(Job)
// 	if r := e.db.WithContext(ctx).
// 		Where("status = ?", entity.JobStatus_Pending).
// 		Order("priority DESC, create_time ASC").
// 		Limit(1).First(job); r.Error != nil {
// 		if errors.Is(r.Error, gorm.ErrRecordNotFound) {
// 			return nil, nil
// 		}
// 		return nil, r.Error
// 	}

// 	return job, nil
// }

func (e *Executor) ListJob(ctx context.Context, filter *entity.JobFilter) ([]*Job, error) {
	db := e.db.WithContext(ctx)
	if filter.Status != nil {
		db = db.Where("status = ?", *filter.Status)
	}

	if filter.Limit != nil {
		db = db.Limit(int(*filter.Limit))
	} else {
		db = db.Limit(20)
	}
	if filter.Offset != nil {
		db = db.Offset(int(*filter.Offset))
	}

	db = db.Order("create_time DESC")

	jobs := make([]*Job, 0, 20)
	if r := db.Find(&jobs); r.Error != nil {
		return nil, fmt.Errorf("list jobs fail, err= %w", r.Error)
	}

	return jobs, nil
}
