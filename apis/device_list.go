package apis

import (
	"context"

	"github.com/samuelncui/tapemanager/entity"
)

func (api *API) DeviceList(ctx context.Context, req *entity.DeviceListRequest) (*entity.DeviceListReply, error) {
	return &entity.DeviceListReply{Devices: api.exe.ListAvailableDevices()}, nil
}
