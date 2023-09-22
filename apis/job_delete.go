package apis

import (
	"context"

	"github.com/samuelncui/tapewriter/entity"
)

func (api *API) JobDelete(ctx context.Context, req *entity.JobDeleteRequest) (*entity.JobDeleteReply, error) {
	if err := api.exe.DeleteJobs(ctx, req.Ids...); err != nil {
		return nil, err
	}

	return &entity.JobDeleteReply{}, nil
}
