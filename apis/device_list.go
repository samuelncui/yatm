package apis

import (
	"context"

	"github.com/samuelncui/tapewriter/entity"
)

func (api *API) DeviceList(ctx context.Context, req *entity.DeviceListRequest) (*entity.DeviceListReply, error) {
	return &entity.DeviceListReply{Devices: api.exe.ListAvailableDevices()}, nil
}
