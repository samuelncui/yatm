package apis

import (
	"context"

	"github.com/abc950309/tapewriter/entity"
)

func (api *API) JobNext(ctx context.Context, req *entity.JobNextRequest) (*entity.JobNextReply, error) {
	job, err := api.exe.GetJob(ctx, req.Id)
	if err != nil {
		return nil, err
	}

	if err := api.exe.Submit(ctx, job, req.Param); err != nil {
		return nil, err
	}

	return &entity.JobNextReply{Job: convertJobs(job)[0]}, nil
}
