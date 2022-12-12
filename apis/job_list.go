package apis

import (
	"context"
	"fmt"

	"github.com/abc950309/tapewriter/entity"
)

func (api *API) JobList(ctx context.Context, req *entity.JobListRequest) (*entity.JobListReply, error) {
	switch param := req.Param.(type) {
	case *entity.JobListRequest_Mget:
		jobs, err := api.exe.MGetJob(ctx, param.Mget.Ids...)
		if err != nil {
			return nil, err
		}
		return &entity.JobListReply{Jobs: convertJobs(map2list(jobs)...)}, nil
	case *entity.JobListRequest_List:
		jobs, err := api.exe.ListJob(ctx, param.List)
		if err != nil {
			return nil, err
		}
		return &entity.JobListReply{Jobs: convertJobs(jobs...)}, nil
	default:
		return nil, fmt.Errorf("unexpected param, %T", req.Param)
	}
}
