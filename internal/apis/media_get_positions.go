package apis

import (
	"context"
	"fmt"

	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/internal/executor"
)

const maxMediaPositionPageSize = 1000

func (s *mediaService) ListPositions(ctx context.Context, req *entity.ListMediaPositionsRequest) (*entity.ListMediaPositionsResponse, error) {
	if req == nil {
		return nil, fmt.Errorf("%w: Media positions request is missing", executor.ErrInvalidRequest)
	}
	limit := int(req.GetLimit())
	if limit == 0 {
		limit = 200
	}
	if limit < 0 || limit > maxMediaPositionPageSize {
		return nil, fmt.Errorf("%w: Media position page limit is invalid, limit=%d", executor.ErrInvalidRequest, limit)
	}

	// Page immediate children by the stable physical path cursor.
	positions, hasMore, err := s.api.lib.ListPositionsPage(
		ctx, req.Id, req.Directory, req.GetAfterPath(), limit,
	)
	if err != nil {
		return nil, fmt.Errorf("list Media positions failed, %w", err)
	}
	return &entity.ListMediaPositionsResponse{Positions: convertPositions(positions...), HasMore: hasMore}, nil
}
