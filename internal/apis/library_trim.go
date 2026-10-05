package apis

import (
	"context"

	"github.com/samuelncui/yatm/entity"
)

func (s *libraryService) Trim(ctx context.Context, req *entity.TrimLibraryRequest) (*entity.TrimLibraryResponse, error) {
	result, err := s.api.lib.Trim(ctx, req.TrimPosition, req.TrimFile, req.Dryrun)
	if err != nil {
		return nil, err
	}
	return &entity.TrimLibraryResponse{PositionCount: result.Positions, FileCount: result.Files}, nil
}
