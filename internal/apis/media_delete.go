package apis

import (
	"context"
	"fmt"

	"github.com/samuelncui/yatm/entity"
)

func (s *mediaService) Delete(ctx context.Context, req *entity.DeleteMediaRequest) (*entity.DeleteMediaResponse, error) {
	if req == nil {
		return nil, fmt.Errorf("Media delete request is missing")
	}
	result, err := s.api.lib.DeleteMedia(ctx, req.Dryrun, req.Ids...)
	if err != nil {
		return nil, err
	}

	return &entity.DeleteMediaResponse{MediaCount: result.Media, PositionCount: result.Positions}, nil
}
