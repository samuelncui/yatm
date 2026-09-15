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
	"github.com/samuelncui/yatm/executor/jobstorage"
	"github.com/samuelncui/yatm/library"
	mediapkg "github.com/samuelncui/yatm/media"
	"gorm.io/gorm"
)

const volumeMinimumReserve int64 = 1 << 30

type volumeReadSession struct {
	volume       *mediapkg.Volume
	media        *library.Media
	capabilities mediapkg.Capabilities
}

func (b *mediaBackend) newVolumeReadSession(
	ctx context.Context,
	db *gorm.DB,
	target *entity.ReadVolumeTarget,
	expectation *entity.ReadMediaTarget,
) (mediapkg.ReadSession, error) {
	// Reserve and validate the mounted identity before exposing paths to the runner.
	uuid, err := mediapkg.NormalizeVolumeUUID(target.GetUuid())
	if err != nil {
		return nil, err
	}
	if !b.executor.LeaseVolume(b.jobID, uuid) {
		return nil, fmt.Errorf("restore Volume is busy, uuid=%q", uuid)
	}
	volume, stored, capabilities, err := b.openVolume(ctx, uuid)
	if err != nil {
		return nil, err
	}
	if err := validateReadExpectation(stored, expectation); err != nil {
		return nil, err
	}
	return &volumeReadSession{volume: volume, media: stored, capabilities: capabilities}, nil
}

func (s *volumeReadSession) Capabilities() mediapkg.Capabilities { return s.capabilities }

func (s *volumeReadSession) Inspect() *library.Media { return s.media }

func (s *volumeReadSession) SourcePath(relative string) (string, error) {
	return mediapkg.ResolveSourcePath(s.volume.Root, relative)
}

func (s *volumeReadSession) Finalize(context.Context) error { return validateSameVolume(s.volume) }

func (s *volumeReadSession) WalkInventory(ctx context.Context, yield func(*mediapkg.InventoryEntry) error) error {
	// Enumerate bounded directory pages without following links or interpreting an unreadable directory as empty.
	var walk func(string, int) error
	walk = func(relative string, depth int) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if depth > 256 {
			return fmt.Errorf("Volume inventory exceeds 256 directory levels")
		}
		dir, err := os.Open(filepath.Join(s.volume.Root, filepath.FromSlash(relative)))
		if err != nil {
			return err
		}
		defer dir.Close()
		for {
			children, err := dir.ReadDir(256)
			if err != nil && !errors.Is(err, io.EOF) {
				return err
			}
			for _, child := range children {
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
	db           *gorm.DB
	volume       *mediapkg.Volume
	media        *library.Media
	capabilities mediapkg.Capabilities
	pathPrefix   string
	available    int64
	planned      int64
}

func (b *mediaBackend) newVolumeWriteSession(
	ctx context.Context,
	db *gorm.DB,
	target *entity.ArchiveVolumeTarget,
) (mediapkg.WriteSession, error) {
	uuid, err := mediapkg.NormalizeVolumeUUID(target.GetUuid())
	if err != nil {
		return nil, err
	}
	if !b.executor.LeaseVolume(b.jobID, uuid) {
		return nil, fmt.Errorf("archive Volume is busy, uuid=%q", uuid)
	}
	volume, stored, capabilities, err := b.openVolume(ctx, uuid)
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
		db: db, volume: volume, media: stored, capabilities: capabilities,
		pathPrefix: pathPrefix, available: available,
	}, nil
}

func (s *volumeWriteSession) Capabilities() mediapkg.Capabilities { return s.capabilities }

func (s *volumeWriteSession) Inspect() *library.Media { return s.media }

func (s *volumeWriteSession) TargetPath(relative string) (string, error) {
	if err := entity.ValidateRelativePath(relative); err != nil {
		return "", err
	}
	item := new(jobstorage.ArchiveItem)
	if err := s.db.Select("size").Where(
		"status = ? AND target_path = ?", entity.CopyStatus_PENDING, relative,
	).First(item).Error; err != nil {
		return "", fmt.Errorf("read Archive item size failed, path=%q, %w", relative, err)
	}
	if item.Size > s.available-s.planned {
		return "", mediapkg.ErrCapacityBoundary
	}
	s.planned += item.Size
	mediaPath := filepath.ToSlash(filepath.Join(s.pathPrefix, relative))
	return mediapkg.ResolveTargetPath(s.volume.Root, mediaPath)
}

func (s *volumeWriteSession) Finalize(ctx context.Context, copyErr error) error {
	// A changed marker invalidates every candidate from this physical session.
	markerErr := validateSameVolume(s.volume)
	if markerErr != nil {
		resetErr := resetArchiveStaged(ctx, s.db)
		return errors.Join(mediapkg.ErrFinalizeUnusable, markerErr, resetErr, targetNoSpaceError(copyErr))
	}

	// Publish the allocated namespace only after the same Volume identity has been revalidated.
	prefixErr := prefixArchiveStagedPaths(ctx, s.db, s.pathPrefix)
	if prefixErr != nil {
		resetErr := resetArchiveStaged(ctx, s.db)
		return errors.Join(mediapkg.ErrFinalizeUnusable, prefixErr, resetErr, targetNoSpaceError(copyErr))
	}
	return targetNoSpaceError(copyErr)
}

func (b *mediaBackend) openVolume(
	ctx context.Context,
	uuid string,
) (*mediapkg.Volume, *library.Media, mediapkg.Capabilities, error) {
	volume, err := mediapkg.DiscoverVolume(b.executor.Paths().Volumes, uuid)
	if err != nil {
		return nil, nil, mediapkg.Capabilities{}, err
	}
	stored, err := b.executor.Lib().GetMediaByIdentity(ctx, entity.MediaKind_MEDIA_KIND_VOLUME, volume.Marker.UUID)
	if err != nil {
		return nil, nil, mediapkg.Capabilities{}, err
	}
	if stored == nil {
		return nil, nil, mediapkg.Capabilities{}, fmt.Errorf("Volume is not in Library, uuid=%q", volume.Marker.UUID)
	}
	profile := stored.Profile.GetVolume()
	if profile == nil || !mediapkg.SameVolumeProfile(profile, volume.Marker.Profile) {
		return nil, nil, mediapkg.Capabilities{}, fmt.Errorf(
			"Volume marker conflicts with Library, uuid=%q", volume.Marker.UUID,
		)
	}
	capabilities, err := mediapkg.VolumeCapabilities(profile.Type)
	if err != nil {
		return nil, nil, mediapkg.Capabilities{}, err
	}
	return volume, stored, capabilities, nil
}

func validateSameVolume(expected *mediapkg.Volume) error {
	actual, err := mediapkg.OpenVolume(expected.Root)
	if err != nil {
		return err
	}
	if !mediapkg.SameVolume(expected, actual) {
		return fmt.Errorf("Volume marker changed, uuid=%q", expected.Marker.UUID)
	}
	return nil
}
