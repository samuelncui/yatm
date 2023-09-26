package apis

import (
	"context"

	mapset "github.com/deckarep/golang-set/v2"
	"github.com/samuelncui/yatm/entity"
)

func (api *API) FileDelete(ctx context.Context, req *entity.FileDeleteRequest) (*entity.FileDeleteReply, error) {
	ids := mapset.NewThreadUnsafeSet(req.Ids...)
	if err := api.lib.Delete(ctx, ids.ToSlice()); err != nil {
		return nil, err
	}
	return new(entity.FileDeleteReply), nil
}
