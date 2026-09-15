package apis

import (
	"context"
	"errors"

	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/executor"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func (api *API) Get(ctx context.Context, req *entity.GetJobRequest) (*entity.GetJobReply, error) {
	job, err := api.exe.GetJob(ctx, req.Id)
	if errors.Is(err, executor.ErrJobNotFound) {
		return nil, status.Error(codes.NotFound, err.Error())
	}
	if err != nil {
		return nil, err
	}
	return &entity.GetJobReply{Job: job.ToEntity()}, nil
}

func (api *API) List(ctx context.Context, req *entity.ListJobsRequest) (*entity.ListJobsReply, error) {
	page, err := api.exe.ListJob(ctx, req.Filter)
	if err != nil {
		return nil, err
	}
	return &entity.ListJobsReply{
		Jobs: convertJobs(page.Jobs...), Revision: page.Revision, HasMore: page.HasMore,
	}, nil
}
