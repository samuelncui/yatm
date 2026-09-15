package apis

import (
	"context"
	"fmt"

	"github.com/samuelncui/yatm/entity"
)

const maxMediaPositionPageSize = 1000

func (api *API) MediaGetPositions(ctx context.Context, req *entity.MediaGetPositionsRequest) (*entity.MediaGetPositionsReply, error) {
	if req == nil {
		return nil, fmt.Errorf("Media positions request is missing")
	}
	limit := int(req.GetLimit())
	if limit == 0 {
		limit = 200
	}
	if limit < 0 || limit > maxMediaPositionPageSize {
		return nil, fmt.Errorf("Media position page limit is invalid, limit=%d", limit)
	}

	// Page immediate children by the stable physical path cursor.
	positions, hasMore, err := api.lib.ListPositionsPage(
		ctx, req.Id, req.Directory, req.GetAfterPath(), limit,
	)
	if err != nil {
		return nil, fmt.Errorf("list Media positions failed, %w", err)
	}
	return &entity.MediaGetPositionsReply{Positions: convertPositions(positions...), HasMore: hasMore}, nil
}
