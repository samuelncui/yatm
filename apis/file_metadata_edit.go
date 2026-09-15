package apis

import (
	"context"
	"fmt"

	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/library"
)

func (api *API) FileMetadataEdit(
	ctx context.Context,
	req *entity.FileMetadataEditRequest,
) (*entity.FileMetadataEditReply, error) {
	edit := library.FileMetadataEdit{
		AddTags:    req.AddTags,
		RemoveTags: req.RemoveTags,
		Note:       req.Note,
	}
	if err := api.lib.EditFileMetadata(ctx, req.Ids, edit); err != nil {
		return nil, fmt.Errorf("edit File metadata failed, %w", err)
	}
	return &entity.FileMetadataEditReply{}, nil
}
