package apis

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/internal/library"
	mediapkg "github.com/samuelncui/yatm/internal/media"
	"google.golang.org/protobuf/proto"
)

func (s *mediaService) InitializeVolume(
	ctx context.Context,
	req *entity.InitializeVolumeRequest,
) (*entity.InitializeVolumeResponse, error) {
	// Validate the explicit new-Volume request before creating its durable marker.
	if req == nil {
		return nil, fmt.Errorf("Volume initialize request is missing")
	}
	name, err := validateVolumeName(req.Name)
	if err != nil {
		return nil, err
	}
	root, err := mediapkg.ValidateVolumeCandidate(s.api.exe.Paths().Volumes, req.MountPoint)
	if err != nil {
		return nil, err
	}

	// Read the physical serial when the caller did not provide one; unknown devices stay empty.
	profile := req.Profile
	if profile == nil {
		return nil, fmt.Errorf("Volume profile is missing")
	}
	if strings.TrimSpace(profile.SerialNumber) == "" {
		if serial := mediapkg.VolumeSerialNumber(root); serial != "" {
			profile = proto.Clone(profile).(*entity.VolumeMediaProfile)
			profile.SerialNumber = serial
		}
	}

	// Create the marker, then register the matching immutable profile in the Library.
	volume, err := mediapkg.InitializeVolume(root, profile)
	if err != nil {
		return nil, err
	}
	stored, err := s.createVolumeMedia(ctx, volume, name)
	if err != nil {
		removeErr := os.Remove(filepath.Join(volume.Root, mediapkg.VolumeMarkerName))
		return nil, errors.Join(err, removeErr)
	}
	return &entity.InitializeVolumeResponse{Media: stored}, nil
}

func (s *mediaService) RegisterVolume(
	ctx context.Context,
	req *entity.RegisterVolumeRequest,
) (*entity.RegisterVolumeResponse, error) {
	// Resolve an existing marker at one configured discovery candidate.
	if req == nil {
		return nil, fmt.Errorf("Volume register request is missing")
	}
	name, err := validateVolumeName(req.Name)
	if err != nil {
		return nil, err
	}
	root, err := mediapkg.ValidateVolumeCandidate(s.api.exe.Paths().Volumes, req.MountPoint)
	if err != nil {
		return nil, err
	}
	volume, err := mediapkg.OpenVolume(root)
	if err != nil {
		return nil, fmt.Errorf("open registered Volume failed, %w", err)
	}
	discovered, err := mediapkg.DiscoverVolume(s.api.exe.Paths().Volumes, volume.Marker.UUID)
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
	existing, err := s.api.lib.GetMediaByIdentity(ctx, entity.MediaKind_MEDIA_KIND_VOLUME, volume.Marker.UUID)
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
		return &entity.RegisterVolumeResponse{Media: converted}, nil
	}

	// Recreate only the Library Media row; the existing marker and physical files remain untouched.
	stored, err := s.createVolumeMedia(ctx, volume, name)
	if err != nil {
		return nil, err
	}
	return &entity.RegisterVolumeResponse{Media: stored}, nil
}

func (s *mediaService) ListVolumeCandidates(
	ctx context.Context,
	req *entity.ListVolumeCandidatesRequest,
) (*entity.ListVolumeCandidatesResponse, error) {
	// Report the configured discovery roots even when no candidate is currently mounted.
	if req == nil {
		return nil, fmt.Errorf("Volume candidates request is missing")
	}
	roots := s.api.exe.Paths().Volumes
	reply := &entity.ListVolumeCandidatesResponse{DiscoveryRoots: roots}
	candidates, err := mediapkg.ListVolumeCandidates(roots)
	if err != nil {
		return nil, fmt.Errorf("list mounted Volume candidates failed, %w", err)
	}

	// One identity mounted twice cannot be registered or initialized unambiguously.
	mounted := make(map[string]int, len(candidates))
	for _, candidate := range candidates {
		if candidate.Marker != nil {
			mounted[candidate.Marker.UUID]++
		}
	}
	for _, candidate := range candidates {
		item := &entity.VolumeCandidate{
			MountPoint: candidate.Root, Name: filepath.Base(candidate.Root),
			SeparateFilesystem: mediapkg.SeparateFilesystem(candidate.Root),
		}
		switch candidate.State {
		case mediapkg.VolumeCandidateUninitialized:
			item.State = entity.VolumeCandidateState_VOLUME_CANDIDATE_STATE_UNINITIALIZED
			if serial := mediapkg.VolumeSerialNumber(candidate.Root); serial != "" {
				item.Profile = &entity.VolumeMediaProfile{SerialNumber: serial}
			}
		case mediapkg.VolumeCandidateInvalid:
			item.State = entity.VolumeCandidateState_VOLUME_CANDIDATE_STATE_INVALID
			item.Detail = candidate.Detail
		default:
			if err := s.classifyInitializedCandidate(ctx, candidate, mounted[candidate.Marker.UUID], item); err != nil {
				return nil, err
			}
		}
		reply.Candidates = append(reply.Candidates, item)
	}
	return reply, nil
}

// classifyInitializedCandidate reports the registration state of one valid marker.
func (s *mediaService) classifyInitializedCandidate(
	ctx context.Context,
	candidate *mediapkg.VolumeCandidate,
	mounts int,
	item *entity.VolumeCandidate,
) error {
	item.Profile = candidate.Marker.Profile
	if mounts > 1 {
		item.State = entity.VolumeCandidateState_VOLUME_CANDIDATE_STATE_CONFLICT
		item.Detail = "Volume identity is mounted more than once"
		return nil
	}
	stored, err := s.api.lib.GetMediaByIdentity(ctx, entity.MediaKind_MEDIA_KIND_VOLUME, candidate.Marker.UUID)
	if err != nil {
		return fmt.Errorf("read candidate Library Media failed, uuid=%q, %w", candidate.Marker.UUID, err)
	}
	if stored == nil {
		item.State = entity.VolumeCandidateState_VOLUME_CANDIDATE_STATE_UNREGISTERED
		return nil
	}
	profile := stored.Profile.GetVolume()
	if profile == nil || !mediapkg.SameVolumeProfile(profile, candidate.Marker.Profile) {
		item.State = entity.VolumeCandidateState_VOLUME_CANDIDATE_STATE_CONFLICT
		item.Detail = "Volume marker conflicts with the Library profile"
		return nil
	}
	converted, err := convertMedia(stored)
	if err != nil {
		return err
	}

	// Publish the same mounted runtime projection the Media list uses for a matching identity.
	if err := applyVolumeRuntime(stored, converted, &mediapkg.Volume{Root: candidate.Root, Marker: *candidate.Marker}); err != nil {
		return err
	}
	item.State = entity.VolumeCandidateState_VOLUME_CANDIDATE_STATE_REGISTERED
	item.Name = stored.Name
	item.Media = converted
	return nil
}

func (s *mediaService) createVolumeMedia(
	ctx context.Context,
	volume *mediapkg.Volume,
	name string,
) (*entity.Media, error) {
	// Snapshot total capacity while preserving current free space as response-only data.
	total, available, err := mediapkg.VolumeCapacity(volume.Root)
	if err != nil {
		return nil, err
	}
	stored, err := s.api.lib.CreateMedia(ctx, &library.Media{
		Kind: entity.MediaKind_MEDIA_KIND_VOLUME, Identity: volume.Marker.UUID, Name: name,
		Profile: volume.Marker.Profile.Pack(), CreatedAtNS: volume.Marker.CreatedAtNS, CapacityBytes: total,
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
