package apis

import (
	"context"

	"github.com/samuelncui/yatm/entity"
)

func (api *API) FileListParents(ctx context.Context, req *entity.FileListParentsRequest) (*entity.FileListParentsReply, error) {
	// Keep ID-based lookup and hydration within one catalog maintenance admission.
	release, err := api.lib.UseOnlineRead()
	if err != nil {
		return nil, onlineError(err)
	}
	defer release()

	files, err := api.lib.ListParents(ctx, req.Id)
	if err != nil {
		return nil, err
	}
	if err := api.hydrateFileTags(ctx, files...); err != nil {
		return nil, err
	}

	return &entity.FileListParentsReply{Parents: convertFiles(files...)}, nil
}
