package apis

import (
	"context"

	"github.com/samuelncui/yatm/entity"
)

func (api *API) Cancel(_ context.Context, req *entity.CancelJobRequest) (*entity.CancelJobResponse, error) {
	if err := api.exe.Cancel(req.Id); err != nil {
		return nil, err
	}
	return &entity.CancelJobResponse{}, nil
}
