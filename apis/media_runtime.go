package apis

import (
	"fmt"

	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/library"
	mediapkg "github.com/samuelncui/yatm/media"
)

func discoverVolumeRuntime(roots []string) (map[string]*mediapkg.Volume, error) {
	// Collapse the physical discovery into one identity map and mark duplicate UUIDs unavailable.
	volumes, err := mediapkg.ListVolumes(roots)
	if err != nil {
		return nil, err
	}
	result := make(map[string]*mediapkg.Volume, len(volumes))
	for _, volume := range volumes {
		if _, found := result[volume.Marker.UUID]; found {
			result[volume.Marker.UUID] = nil
			continue
		}
		result[volume.Marker.UUID] = volume
	}
	return result, nil
}

func applyVolumeRuntime(
	stored *library.Media,
	converted *entity.Media,
	volume *mediapkg.Volume,
) error {
	// Represent Volume availability as response-only state; Tape responses leave these fields absent.
	if stored.Kind != entity.MediaKind_MEDIA_KIND_VOLUME {
		return nil
	}
	mounted := false
	converted.Mounted = &mounted
	if volume == nil {
		return nil
	}
	profile := stored.Profile.GetVolume()
	if profile == nil || !mediapkg.SameVolumeProfile(profile, volume.Marker.Profile) {
		return nil
	}

	// Publish live filesystem capacity only after the immutable marker matches the Library.
	_, available, err := mediapkg.VolumeCapacity(volume.Root)
	if err != nil {
		return fmt.Errorf("read mounted Volume capacity failed, uuid=%q, %w", stored.Identity, err)
	}
	mounted = true
	converted.FilesystemAvailableBytes = &available
	return nil
}
