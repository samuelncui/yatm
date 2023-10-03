package apis

import (
	"context"

	"github.com/samuelncui/yatm/entity"
)

func (api *API) LibraryTrim(ctx context.Context, req *entity.LibraryTrimRequest) (*entity.LibraryTrimReply, error) {
	if err := api.lib.Trim(ctx, req.TrimPosition, req.TrimFile); err != nil {
		return nil, err
	}
	return &entity.LibraryTrimReply{}, nil
}
