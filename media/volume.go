package media

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/google/uuid"
	"github.com/samuelncui/yatm/entity"
	"google.golang.org/protobuf/proto"
)

const (
	VolumeMarkerName    = ".yatm.json"
	volumeMarkerVersion = 1
	maxVolumeMarkerSize = 64 << 10
)

// VolumeMarker is the durable identity stored at a managed Volume root.
type VolumeMarker struct {
	Version   int                        `json:"version"`
	UUID      string                     `json:"uuid"`
	CreatedAt time.Time                  `json:"created_at"`
	Profile   *entity.VolumeMediaProfile `json:"profile"`
}

// Volume describes a currently mounted marker and its canonical root.
type Volume struct {
	Root   string
	Marker VolumeMarker
}

// InitializeVolume creates a marker without mounting or ejecting the filesystem.
func InitializeVolume(root string, profile *entity.VolumeMediaProfile) (*Volume, error) {
	// Validate the immutable profile before writing an identity marker.
	if profile == nil {
		return nil, fmt.Errorf("initialize Volume failed, profile is nil")
	}
	if _, err := VolumeCapabilities(profile.Type); err != nil {
		return nil, err
	}
	canonical, err := canonicalDirectory(root)
	if err != nil {
		return nil, err
	}
	markerPath := filepath.Join(canonical, VolumeMarkerName)
	if _, err := os.Lstat(markerPath); err == nil {
		return nil, fmt.Errorf("initialize Volume failed, marker already exists, path=%q", markerPath)
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("inspect Volume marker failed, path=%q, %w", markerPath, err)
	}

	// Publish the complete marker with exclusive creation.
	marker := VolumeMarker{
		Version: volumeMarkerVersion, UUID: uuid.NewString(), CreatedAt: time.Now().UTC(),
		Profile: proto.Clone(profile).(*entity.VolumeMediaProfile),
	}
	data, err := json.MarshalIndent(marker, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("encode Volume marker failed, %w", err)
	}
	file, err := os.CreateTemp(canonical, ".yatm-*.tmp")
	if err != nil {
		return nil, fmt.Errorf("create temporary Volume marker failed, root=%q, %w", canonical, err)
	}
	temporaryPath := file.Name()
	defer os.Remove(temporaryPath)
	if err := file.Chmod(0o644); err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("set Volume marker mode failed, path=%q, %w", temporaryPath, err)
	}
	if _, err := file.Write(append(data, '\n')); err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("write Volume marker failed, path=%q, %w", temporaryPath, err)
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("sync Volume marker failed, path=%q, %w", temporaryPath, err)
	}
	if err := file.Close(); err != nil {
		return nil, fmt.Errorf("close Volume marker failed, path=%q, %w", temporaryPath, err)
	}
	if err := os.Link(temporaryPath, markerPath); err != nil {
		return nil, fmt.Errorf("publish Volume marker failed, path=%q, %w", markerPath, err)
	}
	return &Volume{Root: canonical, Marker: marker}, nil
}

// OpenVolume validates one marker at an already mounted root.
func OpenVolume(root string) (*Volume, error) {
	// Resolve the mounted root before opening its authoritative marker.
	canonical, err := canonicalDirectory(root)
	if err != nil {
		return nil, err
	}
	markerPath := filepath.Join(canonical, VolumeMarkerName)
	markerInfo, err := os.Lstat(markerPath)
	if err != nil {
		return nil, fmt.Errorf("inspect Volume marker failed, root=%q, %w", canonical, err)
	}
	if !markerInfo.Mode().IsRegular() {
		return nil, fmt.Errorf("Volume marker is not a regular file, root=%q", canonical)
	}
	if markerInfo.Size() > maxVolumeMarkerSize {
		return nil, fmt.Errorf("Volume marker exceeds size limit, root=%q", canonical)
	}
	markerFile, err := os.Open(markerPath)
	if err != nil {
		return nil, fmt.Errorf("open Volume marker failed, root=%q, %w", canonical, err)
	}
	openedInfo, err := markerFile.Stat()
	if err != nil {
		closeErr := markerFile.Close()
		return nil, fmt.Errorf("stat opened Volume marker failed, root=%q, %w", canonical, errors.Join(err, closeErr))
	}
	if !os.SameFile(markerInfo, openedInfo) {
		closeErr := markerFile.Close()
		return nil, errors.Join(fmt.Errorf("Volume marker changed while opening, root=%q", canonical), closeErr)
	}
	data, readErr := io.ReadAll(io.LimitReader(markerFile, maxVolumeMarkerSize+1))
	closeErr := markerFile.Close()
	if readErr != nil || closeErr != nil {
		return nil, fmt.Errorf("read Volume marker failed, root=%q, %w", canonical, errors.Join(readErr, closeErr))
	}
	if len(data) > maxVolumeMarkerSize {
		return nil, fmt.Errorf("Volume marker exceeds size limit, root=%q", canonical)
	}

	// Decode and validate every immutable identity field before exposing the Volume.
	marker := VolumeMarker{}
	if err := json.Unmarshal(data, &marker); err != nil {
		return nil, fmt.Errorf("decode Volume marker failed, root=%q, %w", canonical, err)
	}
	if marker.Version != volumeMarkerVersion {
		return nil, fmt.Errorf("unsupported Volume marker version, root=%q version=%d", canonical, marker.Version)
	}
	if _, err := uuid.Parse(marker.UUID); err != nil {
		return nil, fmt.Errorf("invalid Volume UUID, root=%q uuid=%q, %w", canonical, marker.UUID, err)
	}
	if marker.Profile == nil {
		return nil, fmt.Errorf("invalid Volume marker, root=%q profile is nil", canonical)
	}
	if _, err := VolumeCapabilities(marker.Profile.Type); err != nil {
		return nil, fmt.Errorf("invalid Volume marker, root=%q, %w", canonical, err)
	}
	return &Volume{Root: canonical, Marker: marker}, nil
}

// DiscoverVolume resolves a UUID below one of the configured discovery roots.
func DiscoverVolume(roots []string, want string) (*Volume, error) {
	canonicalUUID, err := NormalizeVolumeUUID(want)
	if err != nil {
		return nil, err
	}
	volumes, err := ListVolumes(roots)
	if err != nil {
		return nil, err
	}
	var found *Volume
	for _, volume := range volumes {
		if volume.Marker.UUID != canonicalUUID {
			continue
		}
		if found != nil {
			return nil, fmt.Errorf(
				"Volume identity is mounted more than once, uuid=%q roots=%q,%q",
				canonicalUUID, found.Root, volume.Root,
			)
		}
		found = volume
	}
	if found != nil {
		return found, nil
	}
	return nil, fmt.Errorf("Volume is not mounted, uuid=%q", canonicalUUID)
}

// ListVolumes returns every valid mounted marker below the configured discovery roots.
func ListVolumes(roots []string) ([]*Volume, error) {
	// Enumerate each canonical candidate once even when configured roots overlap.
	found := make(map[string]struct{})
	var candidates []string
	for _, root := range roots {
		values, err := volumeCandidates(root)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		for _, candidate := range values {
			if _, ok := found[candidate]; ok {
				continue
			}
			found[candidate] = struct{}{}
			candidates = append(candidates, candidate)
		}
	}

	// Only an absent marker denotes an ordinary directory; invalid archive metadata must remain visible.
	volumes := make([]*Volume, 0, len(candidates))
	for _, candidate := range candidates {
		volume, err := OpenVolume(candidate)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("inspect discovered Volume failed, root=%q, %w", candidate, err)
		}
		volumes = append(volumes, volume)
	}
	return volumes, nil
}

// ValidateVolumeCandidate restricts initialization and registration to discoverable roots.
func ValidateVolumeCandidate(roots []string, value string) (string, error) {
	want, err := canonicalDirectory(value)
	if err != nil {
		return "", err
	}
	if len(roots) == 0 {
		return "", fmt.Errorf("no Volume discovery roots are configured")
	}
	for _, root := range roots {
		candidates, err := volumeCandidates(root)
		if err != nil {
			continue
		}
		for _, candidate := range candidates {
			if candidate == want {
				return want, nil
			}
		}
	}
	return "", fmt.Errorf("Volume mount point is not a configured discovery candidate, path=%q", value)
}

// NormalizeVolumeUUID returns the canonical identity used for leases and Library lookup.
func NormalizeVolumeUUID(value string) (string, error) {
	parsed, err := uuid.Parse(strings.TrimSpace(value))
	if err != nil {
		return "", fmt.Errorf("invalid Volume UUID, uuid=%q, %w", value, err)
	}
	return parsed.String(), nil
}

// VolumeCapabilities maps the immutable profile onto runtime access behavior.
func VolumeCapabilities(volumeType entity.VolumeType) (Capabilities, error) {
	switch volumeType {
	case entity.VolumeType_VOLUME_TYPE_HDD:
		return Capabilities{Read: AccessConcurrentRandom, Write: AccessConcurrentRandom}, nil
	case entity.VolumeType_VOLUME_TYPE_HM_SMR:
		return Capabilities{Read: AccessConcurrentRandom, Write: AccessSequential}, nil
	default:
		return Capabilities{}, fmt.Errorf("unsupported Volume type, type=%s", volumeType)
	}
}

// SameVolume reports whether a mounted marker still identifies the same immutable Volume.
func SameVolume(first, second *Volume) bool {
	if first == nil || second == nil {
		return false
	}
	return first.Root == second.Root && first.Marker.Version == second.Marker.Version &&
		first.Marker.UUID == second.Marker.UUID && first.Marker.CreatedAt.Equal(second.Marker.CreatedAt) &&
		proto.Equal(first.Marker.Profile, second.Marker.Profile)
}

// SameVolumeProfile compares the immutable profile stored in a marker and the Library.
func SameVolumeProfile(first, second *entity.VolumeMediaProfile) bool {
	return proto.Equal(first, second)
}

// VolumeCapacity returns total and currently available bytes.
func VolumeCapacity(root string) (int64, int64, error) {
	stat := new(syscall.Statfs_t)
	if err := syscall.Statfs(root, stat); err != nil {
		return 0, 0, fmt.Errorf("read Volume capacity failed, root=%q, %w", root, err)
	}
	blockSize := int64(stat.Bsize)
	return int64(stat.Blocks) * blockSize, int64(stat.Bavail) * blockSize, nil
}

func canonicalDirectory(value string) (string, error) {
	cleaned := filepath.Clean(strings.TrimSpace(value))
	if cleaned == "." || !filepath.IsAbs(cleaned) {
		return "", fmt.Errorf("Volume root must be absolute, root=%q", value)
	}
	canonical, err := filepath.EvalSymlinks(cleaned)
	if err != nil {
		return "", fmt.Errorf("resolve Volume root failed, root=%q, %w", cleaned, err)
	}
	info, err := os.Stat(canonical)
	if err != nil {
		return "", fmt.Errorf("stat Volume root failed, root=%q, %w", canonical, err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("Volume root is not a directory, root=%q", canonical)
	}
	return canonical, nil
}

func volumeCandidates(root string) ([]string, error) {
	canonical, err := canonicalDirectory(root)
	if err != nil {
		return nil, err
	}
	candidates := []string{canonical}
	entries, err := os.ReadDir(canonical)
	if err != nil {
		return nil, fmt.Errorf("list Volume discovery root failed, root=%q, %w", canonical, err)
	}
	for _, entry := range entries {
		if entry.IsDir() {
			candidates = append(candidates, filepath.Join(canonical, entry.Name()))
		}
	}
	return candidates, nil
}
