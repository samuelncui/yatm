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
	if req.WriteTarget != nil && req.WriteTarget.Device != req.Device {
		return nil, fmt.Errorf("write target device differs from the inspected device")
	}
	reply, err := s.api.exe.InspectDevice(ctx, req.Device)
	if err != nil || req.WriteTarget == nil {
		return reply, err
	}
	if err := s.api.exe.CheckTarWrite(ctx, reply, req.WriteTarget); err != nil {
		return nil, err
	}
	return reply, nil
}
