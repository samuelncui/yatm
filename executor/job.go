package executor

import (
	"context"
	"fmt"
	"time"

	"github.com/samuelncui/yatm/entity"
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
	UpdateTime time.Time `gorm:"index:idx_update_time"`
}

func (j *Job) BeforeUpdate(tx *gorm.DB) error {
	j.UpdateTime = time.Now()
	if j.CreateTime.IsZero() {
		j.CreateTime = j.UpdateTime
	}
	return nil
}

func (e *Executor) DeleteJobs(ctx context.Context, ids ...int64) error {
	jobs, err := e.MGetJob(ctx, ids...)
	if err != nil {
		return fmt.Errorf("mget jobs fail")
	}

	for _, job := range jobs {
		job.Status = entity.JobStatus_DELETED
		if r := e.db.WithContext(ctx).Save(job); r.Error != nil {
			return fmt.Errorf("delete job write db fail, id= %d err= %w", job.ID, r.Error)
		}
	}

	return nil
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
	} else {
		db = db.Where("status < ?", entity.JobStatusVisible)
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

func (e *Executor) ListRecentlyUpdateJob(ctx context.Context, filter *entity.JobRecentlyUpdateFilter) ([]*Job, error) {
	db := e.db.WithContext(ctx)
	if filter.UpdateSinceNs != nil {
		db = db.Where("update_time > ?", time.Unix(0, *filter.UpdateSinceNs))
	}

	if filter.Limit != nil {
		db = db.Limit(int(*filter.Limit))
	} else {
		db = db.Limit(20)
	}

	db = db.Order("update_time ASC")

	jobs := make([]*Job, 0, 20)
	if r := db.Find(&jobs); r.Error != nil {
		return nil, fmt.Errorf("list jobs fail, err= %w", r.Error)
	}

	return jobs, nil
}
