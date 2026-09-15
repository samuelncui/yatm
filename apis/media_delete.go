package apis

import (
	"context"
	"fmt"

	"github.com/samuelncui/yatm/entity"
)

func (api *API) MediaDelete(ctx context.Context, req *entity.MediaDeleteRequest) (*entity.MediaDeleteReply, error) {
	if req == nil {
		return nil, fmt.Errorf("Media delete request is missing")
	}
	if err := api.lib.DeleteMedia(ctx, req.Ids...); err != nil {
		return nil, err
	}

	return &entity.MediaDeleteReply{}, nil
}
