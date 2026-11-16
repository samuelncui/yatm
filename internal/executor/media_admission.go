package executor

import (
	"context"
	"fmt"
	"strings"

	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/internal/library"
	mediapkg "github.com/samuelncui/yatm/internal/media"
	"github.com/sirupsen/logrus"
	"google.golang.org/protobuf/proto"
)

// OpenReadSession resolves the frozen expectation's Media record once for a Job attempt, opens the
// Backend Session with it.
func (e *Executor) OpenReadSession(
	ctx context.Context, jobID int64, logger *logrus.Logger, mediaWait func(bool), target *entity.ReadMediaTarget,
) (mediapkg.ReadSession, error) {
	if target == nil || target.ExpectedMediaId <= 0 {
		return nil, fmt.Errorf("Media read requires frozen Media expectations")
	}
	stored, err := e.Lib().GetMedia(ctx, target.ExpectedMediaId)
	if err != nil {
		return nil, err
	}
	if err := checkReadMedia(stored, target); err != nil {
		return nil, err
	}
	backend := e.newMediaBackend(jobID, logger, mediaWait).(*mediaBackend)
	return backend.NewReadSession(ctx, target, describeMedia(stored))
}

// checkReadMedia admits the resolved record only as the identity and profile frozen for this attempt.
func checkReadMedia(stored *library.Media, target *entity.ReadMediaTarget) error {
	if stored.ID != target.ExpectedMediaId || stored.Identity != target.ExpectedIdentity ||
		!proto.Equal(stored.Profile, target.ExpectedProfile) {
		return fmt.Errorf("Media read identity differs from the frozen expectation, media_id=%d identity=%q", stored.ID, stored.Identity)
	}
	if stored.Profile.GetTape().GetFormat() != library.TapeFormatTarPAXV1 {
		return nil
	}
	if err := entity.ValidateTarMediaProfile(stored.Profile.GetTape()); err != nil {
		return err
	}
	key, err := decodeTarKey(stored.Profile.GetTape().Encryption)
	clear(key)
	return err
}

// OpenWriteSession admits one Archive Media choice for a Job attempt. It holds the physical resource
// before resolving the Media record once, so FORMAT and APPEND eligibility stays valid for the Session.
func (e *Executor) OpenWriteSession(
	ctx context.Context, jobID int64, logger *logrus.Logger, mediaWait func(bool), target *entity.ArchiveMediaTarget,
) (mediapkg.WriteSession, error) {
	backend := e.newMediaBackend(jobID, logger, mediaWait).(*mediaBackend)
	switch {
	case target.GetTape() != nil:
		device := strings.TrimSpace(target.GetTape().Device)
		if err := backend.acquireDevice(ctx, func(ctx context.Context, onWait func()) error {
			return e.AcquireTapeDevice(ctx, jobID, device, onWait)
		}); err != nil {
			return nil, err
		}
		resolved, stored, err := e.resolveTapeWrite(ctx, target.GetTape())
		if err != nil {
			return nil, err
		}
		if err := checkTapeWriteMedia(stored, resolved); err != nil {
			return nil, err
		}
		return backend.NewWriteSession(ctx, resolved.Pack(), describeMedia(stored))
	case target.GetVolume() != nil:
		uuid, err := mediapkg.NormalizeVolumeUUID(target.GetVolume().GetUuid())
		if err != nil {
			return nil, err
		}
		if err := backend.acquireDevice(ctx, func(ctx context.Context, onWait func()) error {
			return e.AcquireVolume(ctx, jobID, uuid, onWait)
		}); err != nil {
			return nil, err
		}
		stored, err := e.Lib().GetMediaByIdentity(ctx, entity.MediaKind_MEDIA_KIND_VOLUME, uuid)
		if err != nil {
			return nil, err
		}
		if stored == nil {
			return nil, fmt.Errorf("Volume is not in Library, uuid=%q", uuid)
		}
		return backend.NewWriteSession(ctx, target, describeMedia(stored))
	default:
		return nil, fmt.Errorf("create Media write session failed, request is incomplete")
	}
}

// checkTapeWriteMedia admits FORMAT only for an unregistered identity and APPEND only for usable registered Media.
// resolveTapeWrite has already bound an APPEND target to its registered format.
func checkTapeWriteMedia(stored *library.Media, target *entity.ArchiveTapeTarget) error {
	if target.Mode == entity.ArchiveTapeWriteMode_ARCHIVE_TAPE_WRITE_MODE_FORMAT {
		if stored != nil {
			return fmt.Errorf("Tape already exists in the Library; delete its Media metadata explicitly before formatting, barcode=%q", target.Barcode)
		}
		return nil
	}
	if target.GetFormat() != library.TapeFormatTarPAXV1 {
		return nil
	}
	key, err := decodeTarKey(stored.Profile.GetTape().Encryption)
	clear(key)
	return err
}
