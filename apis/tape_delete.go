package apis

import (
	"context"

	"github.com/samuelncui/yatm/entity"
)

func (api *API) TapeDelete(ctx context.Context, req *entity.TapeDeleteRequest) (*entity.TapeDeleteReply, error) {
	if err := api.lib.DeleteTapes(ctx, req.Ids...); err != nil {
		return nil, err
	}

	return &entity.TapeDeleteReply{}, nil
}
