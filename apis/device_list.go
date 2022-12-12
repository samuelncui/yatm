package apis

import (
	"context"

	"github.com/abc950309/tapewriter/entity"
)

func (api *API) DeviceList(ctx context.Context, req *entity.DeviceListRequest) (*entity.DeviceListReply, error) {
	return &entity.DeviceListReply{Devices: api.exe.ListAvailableDevices()}, nil
}
