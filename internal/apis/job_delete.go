package apis

import (
	"context"

	"github.com/samuelncui/yatm/entity"
)

func (api *API) Delete(ctx context.Context, req *entity.DeleteJobsRequest) (*entity.DeleteJobsResponse, error) {
	deleted, err := api.exe.DeleteJobs(ctx, req.Dryrun, req.Ids...)
	if err != nil {
		return nil, err
	}

	return &entity.DeleteJobsResponse{Deleted: deleted}, nil
}
