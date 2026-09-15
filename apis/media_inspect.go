package apis

import (
	"context"
	"fmt"
	"strings"

	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/executor"
	mediapkg "github.com/samuelncui/yatm/media"
	"github.com/samuelncui/yatm/tools"
)

func (api *API) MediaInspect(ctx context.Context, req *entity.MediaInspectRequest) (*entity.MediaInspectReply, error) {
	if req == nil {
		return nil, fmt.Errorf("inspect Media request is missing")
	}

	// Inspect the physical target while holding only the backend-specific short-lived resource.
	var identity string
	var inspectedVolume *mediapkg.Volume
	route := tools.NewActionRouter[*entity.MediaInspectRequest, entity.OneofMediaInspectRequest](
		tools.ActionMethod(func(ctx context.Context, target *entity.MediaInspectTapeTarget) error {
			device := strings.TrimSpace(target.Device)
			if device == "" {
				return fmt.Errorf("inspect Tape device is empty")
			}
			if !api.exe.OccupyDevice(device) {
				return fmt.Errorf("inspect Tape device is busy, device=%q", device)
			}
			defer api.exe.ReleaseDevice(device)

			barcode, err := api.exe.ReadTapeBarcode(ctx, device)
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
		tools.ActionMethod(func(_ context.Context, target *entity.MediaInspectVolumeTarget) error {
			volume, err := mediapkg.DiscoverVolume(api.exe.Paths().Volumes, strings.TrimSpace(target.Uuid))
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
	reply := &entity.MediaInspectReply{Identity: identity}
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
	stored, err := api.lib.GetMediaByIdentity(ctx, kind, identity)
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
	stats, err := api.lib.GetMediaStats(ctx, stored.ID)
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
	if stats.LastWriteTime != nil {
		value := stats.LastWriteTime.Unix()
		reply.LastWriteTime = &value
	}
	return reply, nil
}
