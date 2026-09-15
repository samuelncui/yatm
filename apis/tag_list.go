package apis

import (
	"context"
	"fmt"

	"github.com/samuelncui/yatm/entity"
)

func (api *API) TagList(ctx context.Context, req *entity.TagListRequest) (*entity.TagListReply, error) {
	// Read one Tag projection page and preserve its canonical order.
	page, err := api.lib.ListTags(ctx, req.GetPrefix(), req.GetCursor(), req.GetLimit())
	if err != nil {
		return nil, fmt.Errorf("list Tags failed, %w", err)
	}
	reply := &entity.TagListReply{Tags: make([]*entity.Tag, 0, len(page.Tags)), NextCursor: page.NextCursor}
	for _, tag := range page.Tags {
		reply.Tags = append(reply.Tags, &entity.Tag{Name: tag.Name, FileCount: tag.FileCount})
	}
	return reply, nil
}
