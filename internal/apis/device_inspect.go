package apis

import (
	"context"
	"fmt"

	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/internal/executor"
)

func (s *mediaService) InspectDevice(ctx context.Context, req *entity.InspectDeviceRequest) (*entity.InspectDeviceResponse, error) {
	if req == nil {
		return nil, fmt.Errorf("%w: inspect device request is missing", executor.ErrInvalidRequest)
	}
	if req.WriteTarget != nil && req.WriteTarget.Device != req.Device {
		return nil, fmt.Errorf("%w: write target device differs from the inspected device", executor.ErrInvalidRequest)
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
