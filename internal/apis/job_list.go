package apis

import (
	"context"
	"errors"

	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/internal/executor"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func (api *API) Get(ctx context.Context, req *entity.GetJobRequest) (*entity.GetJobResponse, error) {
	job, err := api.exe.GetJob(ctx, req.Id)
	if errors.Is(err, executor.ErrJobNotFound) {
		return nil, status.Error(codes.NotFound, err.Error())
	}
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
