package apis

import (
	"context"

	"github.com/samuelncui/yatm/entity"
)

func (api *API) RetryIndex(ctx context.Context, req *entity.RetryJobIndexRequest) (*entity.RetryJobIndexReply, error) {
	if err := api.exe.RetryIndex(ctx, req.Id); err != nil {
		return nil, err
	}
	return &entity.RetryJobIndexReply{}, nil
}
