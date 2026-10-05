package apis

import (
	"context"

	"github.com/samuelncui/yatm/entity"
)

func (s *mediaService) ListDevices(ctx context.Context, req *entity.ListDevicesRequest) (*entity.ListDevicesResponse, error) {
	return &entity.ListDevicesResponse{Devices: s.api.exe.ListAvailableDevices()}, nil
}
