package apis

import (
	"context"
	"fmt"
	"io"

	"github.com/samuelncui/yatm/entity"
)

const maxJobLogReadSize = 4 << 20

func (api *API) GetLog(ctx context.Context, req *entity.GetJobLogRequest) (*entity.GetJobLogReply, error) {
	reader, err := api.exe.NewLogReader(ctx, req.Id)
	if err != nil {
		return nil, fmt.Errorf("open log fail, %w", err)
	}
	if reader == nil {
		return &entity.GetJobLogReply{Logs: []byte{}}, nil
	}
	defer reader.Close()

	if req.Offset != nil {
		if *req.Offset < 0 {
			return nil, fmt.Errorf("log offset must not be negative, offset=%d", *req.Offset)
		}
		if _, err := reader.Seek(*req.Offset, 0); err != nil {
			return nil, fmt.Errorf("seek log file fail, offset=%d, %w", *req.Offset, err)
		}
	}

	buf, err := io.ReadAll(io.LimitReader(reader, maxJobLogReadSize))
	if err != nil {
		return nil, fmt.Errorf("read log fail, %w", err)
	}

	return &entity.GetJobLogReply{Logs: buf, Offset: req.GetOffset() + int64(len(buf))}, nil
}
