package apis

import (
	"context"
	"fmt"
	"io"

	"github.com/abc950309/tapewriter/entity"
)

func (api *API) JobGetLog(ctx context.Context, req *entity.JobGetLogRequest) (*entity.JobGetLogReply, error) {
	reader, err := api.exe.NewLogReader(req.JobId)
	if err != nil {
		return nil, fmt.Errorf("open log fail, %w", err)
	}
	if reader == nil {
		return &entity.JobGetLogReply{Logs: []byte{}}, nil
	}

	if req.Offset != nil {
		if _, err := reader.Seek(*req.Offset, 0); err != nil {
			return nil, fmt.Errorf("seek log file fail, offset= %d, %w", req.Offset, err)
		}
	}

	buf, err := io.ReadAll(reader)
	if err != nil {
		return nil, fmt.Errorf("read log fail, %w", err)
	}

	return &entity.JobGetLogReply{Logs: buf}, nil
}
