package apis

import (
	"context"
	"fmt"
	"io/fs"
	"path"
	"strings"

	"github.com/abc950309/tapewriter/entity"
)

func (api *API) FileEdit(ctx context.Context, req *entity.FileEditRequest) (*entity.FileEditReply, error) {
	file, err := api.lib.GetFile(ctx, req.Id)
	if err != nil {
		return nil, err
	}
	if file == nil {
		return nil, fmt.Errorf("file not found, id= %d", req.Id)
	}

	if req.File.ParentId != nil {
		file.ParentID = *req.File.ParentId
	}
	if req.File.Name != nil {
		name := strings.TrimSpace(*req.File.Name)
		if name == "" {
			return nil, fmt.Errorf("unexpected target name, not a string")
		}

		if !strings.ContainsAny(name, `\/`) {
			file.Name = name
		} else {
			name = path.Clean(strings.ReplaceAll(name, `\`, `/`))

			dirname, filename := path.Split(name)
			if filename == "" {
				return nil, fmt.Errorf("unexpected target name, end with slash, '%s'", name)
			}

			dir, err := api.lib.MkdirAll(ctx, file.ParentID, dirname, fs.ModePerm)
			if err != nil {
				return nil, err
			}

			file.ParentID = dir.ID
		}
	}

	if err := api.lib.MoveFile(ctx, file); err != nil {
		return nil, err
	}
	return &entity.FileEditReply{File: convertFiles(file)[0]}, nil
}
