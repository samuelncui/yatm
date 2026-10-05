package apis

import (
	"context"
	"fmt"
	"strings"

	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/internal/executor"
	mediapkg "github.com/samuelncui/yatm/internal/media"
	"github.com/samuelncui/yatm/internal/tools"
)

func (s *mediaService) Inspect(ctx context.Context, req *entity.InspectMediaRequest) (*entity.InspectMediaResponse, error) {
	if req == nil {
		return nil, fmt.Errorf("inspect Media request is missing")
	}

	// Inspect the physical target while holding only the backend-specific short-lived resource.
	var identity string
	var inspectedVolume *mediapkg.Volume
	route := tools.NewActionRouter[*entity.InspectMediaRequest, entity.OneofInspectMediaRequest](
		tools.ActionMethod(func(ctx context.Context, target *entity.InspectMediaTapeTarget) error {
			device := strings.TrimSpace(target.Device)
			if device == "" {
				return fmt.Errorf("inspect Tape device is empty")
			}
			if !s.api.exe.OccupyDevice(device) {
				return fmt.Errorf("inspect Tape device is busy, device=%q", device)
			}
			defer s.api.exe.ReleaseDevice(device)

			barcode, err := s.api.exe.ReadTapeBarcode(ctx, device)
			if err != nil {
				return fmt.Errorf("inspect Tape identity failed, %w", err)
			}
			if barcode == "" && req.Identity != nil {
				barcode, err = executor.NormalizeTapeBarcode(*req.Identity)
				if err != nil {
					return fmt.Errorf("invalid inspected Tape identity, %w", err)
				}
			}
			identity = barcode
			return nil
		}),
		tools.ActionMethod(func(_ context.Context, target *entity.InspectMediaVolumeTarget) error {
			volume, err := mediapkg.DiscoverVolume(s.api.exe.Paths().Volumes, strings.TrimSpace(target.Uuid))
			if err != nil {
				return err
			}
			inspectedVolume = volume
			identity = volume.Marker.UUID
			return nil
		}),
	)
	if err := route(ctx, req); err != nil {
		return nil, err
	}

	// Return physical identity even when the Media has not been registered in the Library.
	reply := &entity.InspectMediaResponse{Identity: identity}
	if inspectedVolume != nil {
		mountPoint := inspectedVolume.Root
		reply.MountPoint = &mountPoint
	}
	if identity == "" {
		return reply, nil
	}
	kind := entity.MediaKind_MEDIA_KIND_TAPE
	if req.GetVolume() != nil {
		kind = entity.MediaKind_MEDIA_KIND_VOLUME
	}
	stored, err := s.api.lib.GetMediaByIdentity(ctx, kind, identity)
	if err != nil {
		return nil, fmt.Errorf("inspect Library Media failed, identity=%q, %w", identity, err)
	}
	if stored == nil {
		return reply, nil
	}
	if inspectedVolume != nil {
		profile := stored.Profile.GetVolume()
		if profile == nil || !mediapkg.SameVolumeProfile(profile, inspectedVolume.Marker.Profile) {
			return nil, fmt.Errorf("Volume marker conflicts with Library, uuid=%q", identity)
		}
	}

	// Derive Library statistics only after physical identity and immutable profile validation.
	stats, err := s.api.lib.GetMediaStats(ctx, stored.ID)
	if err != nil {
		return nil, fmt.Errorf("inspect Library Media statistics failed, identity=%q, %w", identity, err)
	}
	reply.Media, err = convertMedia(stored)
	if err != nil {
		return nil, err
	}
	if err := applyVolumeRuntime(stored, reply.Media, inspectedVolume); err != nil {
		return nil, err
	}
	reply.FileCount = stats.FileCount
	reply.LastWrittenAtNs = stats.LastWrittenAtNS
	return reply, nil
}
