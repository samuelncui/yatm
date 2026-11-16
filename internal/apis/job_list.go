package apis

import (
	"context"

	"github.com/samuelncui/yatm/entity"
)

func (api *API) Get(ctx context.Context, req *entity.GetJobRequest) (*entity.GetJobResponse, error) {
	job, err := api.exe.GetJob(ctx, req.Id)
	if err != nil {
		return nil, err
	}
	return &entity.GetJobResponse{Job: job.ToEntity()}, nil
}

func (api *API) List(ctx context.Context, req *entity.ListJobsRequest) (*entity.ListJobsResponse, error) {
	page, err := api.exe.ListJob(ctx, req.Filter)
	if err != nil {
		return nil, err
	}
	return &entity.ListJobsResponse{
		Jobs: convertJobs(page.Jobs...), Revision: page.Revision, HasMore: page.HasMore,
	}, nil
}
