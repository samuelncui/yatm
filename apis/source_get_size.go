package apis

import (
	"context"
	"os"
	"path"
	"path/filepath"

	"github.com/samuelncui/yatm/entity"
)

func (api *API) SourceGetSize(ctx context.Context, req *entity.SourceGetSizeRequest) (*entity.SourceGetSizeReply, error) {
	if req.Path == "./" {
		req.Path = ""
	}

	var size int64
	if err := filepath.Walk(path.Join(api.sourceBase, req.Path), func(_ string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if !info.IsDir() {
			size += info.Size()
		}
		return err
	}); err != nil {
		return nil, err
	}

	return &entity.SourceGetSizeReply{Size: size}, nil
}
