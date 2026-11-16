package apis

import (
	"context"
	"fmt"

	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/internal/executor"
)

func (s *mediaService) Delete(ctx context.Context, req *entity.DeleteMediaRequest) (*entity.DeleteMediaResponse, error) {
	if req == nil {
		return nil, fmt.Errorf("%w: Media delete request is missing", executor.ErrInvalidRequest)
	}
	result, err := s.api.lib.DeleteMedia(ctx, req.Dryrun, req.Ids...)
	if err != nil {
		return nil, err
	}

	return &entity.DeleteMediaResponse{MediaCount: result.Media, PositionCount: result.Positions}, nil
}
