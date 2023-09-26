package apis

import (
	"context"

	"github.com/samuelncui/yatm/entity"
)

func (api *API) LibraryExport(ctx context.Context, req *entity.LibraryExportRequest) (*entity.LibraryExportReply, error) {
	buf, err := api.lib.Export(ctx, req.Types)
	if err != nil {
		return nil, err
	}

	return &entity.LibraryExportReply{Json: buf}, nil
}
