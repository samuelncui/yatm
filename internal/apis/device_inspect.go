package apis

import (
	"context"
	"fmt"

	"github.com/samuelncui/yatm/entity"
)

func (s *mediaService) InspectDevice(ctx context.Context, req *entity.InspectDeviceRequest) (*entity.InspectDeviceResponse, error) {
	if req == nil {
		return nil, fmt.Errorf("inspect device request is missing")
	}
	return s.api.exe.InspectDevice(ctx, req.Device)
}
