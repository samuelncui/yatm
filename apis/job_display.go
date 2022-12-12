package apis

import (
	"context"

	"github.com/abc950309/tapewriter/entity"
)

func (api *API) JobDisplay(ctx context.Context, req *entity.JobDisplayRequest) (*entity.JobDisplayReply, error) {
	job, err := api.exe.GetJob(ctx, req.Id)
	if err != nil {
		return &entity.JobDisplayReply{}, nil
	}

	result, err := api.exe.Display(ctx, job)
	if err != nil {
		return &entity.JobDisplayReply{}, nil
	}

	return &entity.JobDisplayReply{Display: result}, nil
}
