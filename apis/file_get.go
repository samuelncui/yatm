package apis

import (
	"context"
	"errors"

	"github.com/abc950309/tapewriter/entity"
	"github.com/abc950309/tapewriter/library"
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

	children, err := api.lib.List(ctx, req.Id)
	if err != nil {
		return nil, err
	}

	return &entity.FileGetReply{
		File:      file,
		Positions: convertPositions(positions...),
		Children:  convertFiles(children...),
	}, nil
}
