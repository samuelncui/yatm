package apis

import (
	"context"
	"fmt"

	"github.com/samuelncui/yatm/entity"
)

func (s *libraryService) ListTags(ctx context.Context, req *entity.ListTagsRequest) (*entity.ListTagsResponse, error) {
	// Read one Tag projection page and preserve its canonical order.
	page, err := s.api.lib.ListTags(ctx, req.GetPrefix(), req.GetCursor(), req.GetLimit())
	if err != nil {
		return nil, fmt.Errorf("list Tags failed, %w", err)
	}
	reply := &entity.ListTagsResponse{Tags: make([]*entity.Tag, 0, len(page.Tags)), NextCursor: page.NextCursor}
	for _, tag := range page.Tags {
		reply.Tags = append(reply.Tags, &entity.Tag{Name: tag.Name, FileCount: tag.FileCount})
	}
	return reply, nil
}
