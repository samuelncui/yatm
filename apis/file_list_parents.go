package apis

import (
	"context"

	"github.com/abc950309/tapewriter/entity"
)

func (api *API) FileListParents(ctx context.Context, req *entity.FileListParentsRequest) (*entity.FileListParentsReply, error) {
	files, err := api.lib.ListParents(ctx, req.Id)
	if err != nil {
		return nil, err
	}

	return &entity.FileListParentsReply{Parents: convertFiles(files...)}, nil
}
