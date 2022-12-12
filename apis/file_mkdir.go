package apis

import (
	"context"
	"fmt"
	"io/fs"

	"github.com/abc950309/tapewriter/entity"
)

func (api *API) FileMkdir(ctx context.Context, req *entity.FileMkdirRequest) (*entity.FileMkdirReply, error) {
	if req.ParentId != 0 {
		parent, err := api.lib.GetFile(ctx, req.ParentId)
		if err != nil || parent == nil {
			return nil, err
		}
		if parent == nil {
			return nil, fmt.Errorf("file not found, id= %d", req.ParentId)
		}
	}

	dir, err := api.lib.MkdirAll(ctx, req.ParentId, req.Path, fs.ModePerm)
	if err != nil {
		return nil, err
	}

	return &entity.FileMkdirReply{File: convertFiles(dir)[0]}, nil
}
