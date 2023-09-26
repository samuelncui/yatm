package apis

import (
	"context"

	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/executor"
)

func (api *API) JobCreate(ctx context.Context, req *entity.JobCreateRequest) (*entity.JobCreateReply, error) {
	job, err := api.exe.CreateJob(ctx, &executor.Job{
		Status:   entity.JobStatus_PENDING,
		Priority: req.Job.Priority,
	}, req.Job.Param)
	if err != nil {
		return nil, err
	}

	if err := api.exe.Start(ctx, job); err != nil {
		return nil, err
	}

	return &entity.JobCreateReply{Job: convertJobs(job)[0]}, nil
}
