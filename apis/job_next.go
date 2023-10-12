package apis

import (
	"context"

	"github.com/samuelncui/yatm/entity"
)

func (api *API) JobDispatch(ctx context.Context, req *entity.JobDispatchRequest) (*entity.JobDispatchReply, error) {
	if err := api.exe.Dispatch(ctx, req.Id, req.Param); err != nil {
		return nil, err
	}

	return &entity.JobDispatchReply{}, nil
}
