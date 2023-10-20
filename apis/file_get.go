package apis

import (
	"context"
	"errors"

	"github.com/samber/lo"
	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/library"
)

func (api *API) FileGet(ctx context.Context, req *entity.FileGetRequest) (*entity.FileGetReply, error) {
	libFile, err := api.lib.GetFile(ctx, req.Id)
	if err != nil && !errors.Is(err, library.ErrFileNotFound) {
		return nil, err
	}

	var file *entity.File
	if libFile != nil {
		file = convertFiles(libFile)[0]
	}

	positions, err := api.lib.GetPositionByFileID(ctx, req.Id)
	if err != nil {
		return nil, err
	}

	reply := &entity.FileGetReply{
		File:      file,
		Positions: convertPositions(positions...),
	}

	if req.GetNeedSize() {
		children, err := api.lib.ListWithSize(ctx, req.Id)
		if err != nil {
			return nil, err
		}
		reply.Children = convertFiles(children...)

		if reply.File != nil {
			reply.File.Size += lo.Sum(lo.Map(children, func(file *library.File, _ int) int64 { return file.Size }))
		}
	} else {
		children, err := api.lib.List(ctx, req.Id)
		if err != nil {
			return nil, err
		}
		reply.Children = convertFiles(children...)
	}

	return reply, nil
}
