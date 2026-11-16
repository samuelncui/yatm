package executor

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/samuelncui/yatm/entity"
	mediapkg "github.com/samuelncui/yatm/internal/media"
)

const volumeMinimumReserve int64 = 1 << 30

type volumeReadSession struct {
	volume       *mediapkg.Volume
	media        *mediapkg.Descriptor
	capabilities mediapkg.Capabilities
}

func (b *mediaBackend) newVolumeReadSession(
	ctx context.Context,
	target *entity.ReadVolumeTarget,
	media *mediapkg.Descriptor,
) (mediapkg.ReadSession, error) {
	// Reserve and validate the mounted identity before exposing paths to the runner.
	uuid, err := mediapkg.NormalizeVolumeUUID(target.GetUuid())
	if err != nil {
		return nil, err
	}
	if err := b.acquireDevice(ctx, func(ctx context.Context, onWait func()) error {
		return b.executor.AcquireVolume(ctx, b.jobID, uuid, onWait)
	}); err != nil {
		return nil, err
	}
	volume, capabilities, err := b.openVolume(uuid, media)
	if err != nil {
		return nil, err
	}
	return &volumeReadSession{volume: volume, media: media, capabilities: capabilities}, nil
}

func (s *volumeReadSession) Capabilities() mediapkg.Capabilities { return s.capabilities }

func (s *volumeReadSession) Media() *mediapkg.Descriptor { return s.media }

func (s *volumeReadSession) Source(request mediapkg.ReadRequest) (mediapkg.SourceRef, error) {
	filename, err := mediapkg.ResolveSourcePath(s.volume.Root, request.Path)
	return mediapkg.SourceRef{Path: filename}, err
}

func (s *volumeReadSession) Finalize(context.Context, error) error { return nil }

func (s *volumeReadSession) WalkInventory(ctx context.Context, yield func(*mediapkg.InventoryEntry) error) error {
	// Enumerate bounded directory pages without following links or interpreting an unreadable directory as empty.
	var walk func(string, int) error
	walk = func(relative string, depth int) (returnErr error) {
		// Reject canceled or over-depth traversal before opening another directory.
		if err := ctx.Err(); err != nil {
			return err
		}
		if depth > 256 {
			return fmt.Errorf("Volume inventory exceeds 256 directory levels")
		}

		// Each recursive frame releases its own handle when a child or callback fails.
		dir, err := os.Open(filepath.Join(s.volume.Root, filepath.FromSlash(relative)))
		if err != nil {
			return err
		}
		defer func() {
			if err := dir.Close(); err != nil {
				returnErr = errors.Join(returnErr, fmt.Errorf(
					"close Volume inventory directory failed, path=%q, %w", relative, err,
				))
			}
		}()

		// Cancellation also stops the current page, keeping entries already accepted by yield.
		for {
			if err := ctx.Err(); err != nil {
				return err
			}
			children, err := dir.ReadDir(256)
			if err != nil && !errors.Is(err, io.EOF) {
				return err
			}
			for _, child := range children {
				if err := ctx.Err(); err != nil {
					return err
				}
				name := child.Name()
				if relative != "" {
					name = relative + "/" + name
				}
				if name == ".yatm.json" {
					continue
				}
				info, err := child.Info()
				if err != nil {
					return err
				}
				if info.IsDir() {
					if err := walk(name, depth+1); err != nil {
						return err
					}
					continue
				}
				if !info.Mode().IsRegular() {
					continue
				}
				if err := yield(&mediapkg.InventoryEntry{Path: name, Info: info}); err != nil {
					return err
				}
			}
			if errors.Is(err, io.EOF) {
				return nil
			}
		}
	}
	return walk("", 0)
}

type volumeWriteSession struct {
	volume       *mediapkg.Volume
	media        *mediapkg.Descriptor
	capabilities mediapkg.Capabilities
	pathPrefix   string
	available    int64
	planned      int64
}

func (b *mediaBackend) newVolumeWriteSession(
	ctx context.Context,
	target *entity.ArchiveVolumeTarget,
	media *mediapkg.Descriptor,
) (mediapkg.WriteSession, error) {
	uuid, err := mediapkg.NormalizeVolumeUUID(target.GetUuid())
	if err != nil {
		return nil, err
	}
	if err := b.acquireDevice(ctx, func(ctx context.Context, onWait func()) error {
		return b.executor.AcquireVolume(ctx, b.jobID, uuid, onWait)
	}); err != nil {
		return nil, err
	}
	volume, capabilities, err := b.openVolume(uuid, media)
	if err != nil {
		return nil, err
	}
	pathPrefix, err := allocateMediaPathPrefix(volume.Root, uint64(time.Now().UnixNano()))
	if err != nil {
		return nil, err
	}
	total, available, err := mediapkg.VolumeCapacity(volume.Root)
	if err != nil {
		return nil, err
	}
	reserve := total / 100
	if reserve < volumeMinimumReserve {
		reserve = volumeMinimumReserve
	}
	available -= reserve
	if available < 0 {
		available = 0
	}
	return &volumeWriteSession{
		volume: volume, media: media, capabilities: capabilities,
		pathPrefix: pathPrefix, available: available,
	}, nil
}

func (s *volumeWriteSession) Capabilities() mediapkg.Capabilities { return s.capabilities }

func (s *volumeWriteSession) Media() *mediapkg.Descriptor { return s.media }

func (s *volumeWriteSession) Target(relative string, size int64) (mediapkg.TargetRef, error) {
	// Admit only a valid frozen item that fits the current planning allowance.
	if err := entity.ValidateRelativePath(relative); err != nil {
		return mediapkg.TargetRef{}, err
	}
	if size < 0 {
		return mediapkg.TargetRef{}, fmt.Errorf("Archive item size is negative, path=%q size=%d", relative, size)
	}
	if size > s.available-s.planned {
		return mediapkg.TargetRef{}, mediapkg.ErrCapacityBoundary
	}
	// Reserve the admitted bytes before exposing the contained filesystem target.
	s.planned += size
	mediaPath := filepath.ToSlash(filepath.Join(s.pathPrefix, relative))
	filename, err := mediapkg.ResolveTargetPath(s.volume.Root, mediaPath)
	return mediapkg.TargetRef{Path: filename}, err
}

func (s *volumeWriteSession) Finalize(_ context.Context, _ error) (*mediapkg.WriteResult, error) {
	return &mediapkg.WriteResult{Media: s.media, PathPrefix: s.pathPrefix}, nil
}

// openVolume discovers the mounted Volume and checks its marker against the selected Media.
func (b *mediaBackend) openVolume(uuid string, media *mediapkg.Descriptor) (*mediapkg.Volume, mediapkg.Capabilities, error) {
	volume, err := mediapkg.DiscoverVolume(b.executor.Paths().Volumes, uuid)
	if err != nil {
		return nil, mediapkg.Capabilities{}, err
	}
	if media == nil || media.Kind != entity.MediaKind_MEDIA_KIND_VOLUME || media.Identity != volume.Marker.UUID {
		return nil, mediapkg.Capabilities{}, fmt.Errorf("Volume differs from the selected Media, uuid=%q", volume.Marker.UUID)
	}
	if !mediapkg.SameVolumeProfile(media.Profile, volume.Marker) {
		return nil, mediapkg.Capabilities{}, fmt.Errorf(
			"Volume marker conflicts with Library, uuid=%q", volume.Marker.UUID,
		)
	}
	capabilities, err := mediapkg.VolumeCapabilities(media.Profile.GetVolume().Type)
	if err != nil {
		return nil, mediapkg.Capabilities{}, err
	}
	return volume, capabilities, nil
}
