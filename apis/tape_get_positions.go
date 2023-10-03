package apis

import (
	"context"
	"fmt"

	"github.com/samuelncui/yatm/entity"
)

func (api *API) TapeGetPositions(ctx context.Context, req *entity.TapeGetPositionsRequest) (*entity.TapeGetPositionsReply, error) {
	positions, err := api.lib.ListPositions(ctx, req.Id, req.Directory)
	if err != nil {
		return nil, fmt.Errorf("list position has error, %w", err)
	}

	return &entity.TapeGetPositionsReply{Positions: convertPositions(positions...)}, nil
}
