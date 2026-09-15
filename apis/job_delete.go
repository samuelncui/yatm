package apis

import (
	"context"

	"github.com/samuelncui/yatm/entity"
)

func (api *API) Delete(ctx context.Context, req *entity.DeleteJobsRequest) (*entity.DeleteJobsReply, error) {
	if err := api.exe.DeleteJobs(ctx, req.Ids...); err != nil {
		return nil, err
	}

	return &entity.DeleteJobsReply{}, nil
}
