package apis

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/library"
	mediapkg "github.com/samuelncui/yatm/media"
)

func (api *API) VolumeInitialize(
	ctx context.Context,
	req *entity.VolumeInitializeRequest,
) (*entity.VolumeInitializeReply, error) {
	// Validate the explicit new-Volume request before creating its durable marker.
	if req == nil {
		return nil, fmt.Errorf("Volume initialize request is missing")
	}
	name, err := validateVolumeName(req.Name)
	if err != nil {
		return nil, err
	}
	root, err := mediapkg.ValidateVolumeCandidate(api.exe.Paths().Volumes, req.MountPoint)
	if err != nil {
		return nil, err
	}

	// Create the marker, then register the matching immutable profile in the Library.
	volume, err := mediapkg.InitializeVolume(root, req.Profile)
	if err != nil {
		return nil, err
	}
	stored, err := api.createVolumeMedia(ctx, volume, name)
	if err != nil {
		removeErr := os.Remove(filepath.Join(volume.Root, mediapkg.VolumeMarkerName))
		return nil, errors.Join(err, removeErr)
	}
	return &entity.VolumeInitializeReply{Media: stored}, nil
}

func (api *API) VolumeRegister(
	ctx context.Context,
	req *entity.VolumeRegisterRequest,
) (*entity.VolumeRegisterReply, error) {
	// Resolve an existing marker at one configured discovery candidate.
	if req == nil {
		return nil, fmt.Errorf("Volume register request is missing")
	}
	name, err := validateVolumeName(req.Name)
	if err != nil {
		return nil, err
	}
	root, err := mediapkg.ValidateVolumeCandidate(api.exe.Paths().Volumes, req.MountPoint)
	if err != nil {
		return nil, err
	}
	volume, err := mediapkg.OpenVolume(root)
	if err != nil {
		return nil, fmt.Errorf("open registered Volume failed, %w", err)
	}
	discovered, err := mediapkg.DiscoverVolume(api.exe.Paths().Volumes, volume.Marker.UUID)
	if err != nil {
		return nil, err
	}
	if discovered.Root != volume.Root {
		return nil, fmt.Errorf(
			"registered Volume identity resolved to another mount point, uuid=%q root=%q",
			volume.Marker.UUID, discovered.Root,
		)
	}

	// Return an existing matching registration unchanged so retries remain metadata-only.
	existing, err := api.lib.GetMediaByIdentity(ctx, entity.MediaKind_MEDIA_KIND_VOLUME, volume.Marker.UUID)
	if err != nil {
		return nil, err
	}
	if existing != nil {
		profile := existing.Profile.GetVolume()
		if profile == nil || !mediapkg.SameVolumeProfile(profile, volume.Marker.Profile) {
			return nil, fmt.Errorf("Volume marker conflicts with Library, uuid=%q", volume.Marker.UUID)
		}
		converted, err := convertMedia(existing)
		if err != nil {
			return nil, err
		}
		if err := applyVolumeRuntime(existing, converted, volume); err != nil {
			return nil, err
		}
		return &entity.VolumeRegisterReply{Media: converted}, nil
	}

	// Recreate only the Library Media row; the existing marker and physical files remain untouched.
	stored, err := api.createVolumeMedia(ctx, volume, name)
	if err != nil {
		return nil, err
	}
	return &entity.VolumeRegisterReply{Media: stored}, nil
}

func (api *API) createVolumeMedia(
	ctx context.Context,
	volume *mediapkg.Volume,
	name string,
) (*entity.Media, error) {
	// Snapshot total capacity while preserving current free space as response-only data.
	total, available, err := mediapkg.VolumeCapacity(volume.Root)
	if err != nil {
		return nil, err
	}
	stored, err := api.lib.CreateMedia(ctx, &library.Media{
		Kind: entity.MediaKind_MEDIA_KIND_VOLUME, Identity: volume.Marker.UUID, Name: name,
		Profile: volume.Marker.Profile.Pack(), CreateTime: volume.Marker.CreatedAt, CapacityBytes: total,
	})
	if err != nil {
		return nil, err
	}

	// Return the mounted runtime projection from the capacity snapshot used for registration.
	converted, err := convertMedia(stored)
	if err != nil {
		return nil, err
	}
	mounted := true
	converted.Mounted = &mounted
	converted.FilesystemAvailableBytes = &available
	return converted, nil
}

func validateVolumeName(value string) (string, error) {
	name := strings.TrimSpace(value)
	if name == "" || name == "." || name == ".." || strings.ContainsAny(name, "/\\") {
		return "", fmt.Errorf("invalid Volume name, name=%q", value)
	}
	return name, nil
}
