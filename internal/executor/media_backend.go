package executor

import (
	"context"
	"fmt"
	"time"

	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/internal/library"
	mediapkg "github.com/samuelncui/yatm/internal/media"
	"github.com/samuelncui/yatm/internal/tools"
	"github.com/sirupsen/logrus"
	"google.golang.org/protobuf/proto"
)

type mediaBackend struct {
	executor *Executor
	jobID    int64
	logger   *logrus.Logger
	// mediaWait reports a shared-device wait so the Job can publish its waiting phase.
	mediaWait func(waiting bool)
}

// newMediaBackend creates the factory bound to one active Job attempt.
func (e *Executor) newMediaBackend(jobID int64, logger *logrus.Logger, mediaWait func(bool)) mediapkg.Backend {
	if logger == nil {
		logger = logrus.New()
	}
	return &mediaBackend{executor: e, jobID: jobID, logger: logger, mediaWait: mediaWait}
}

// acquireDevice waits for one shared Media resource and restores the caller's phase afterwards.
func (b *mediaBackend) acquireDevice(ctx context.Context, acquire func(context.Context, func()) error) error {
	waiting := b.mediaWait
	if waiting == nil {
		return acquire(ctx, nil)
	}
	err := acquire(ctx, func() { waiting(true) })
	waiting(false)
	return err
}

// NewReadSession opens the Media the Job resolved for this attempt behind the selected physical target.
func (b *mediaBackend) NewReadSession(
	ctx context.Context,
	target *entity.ReadMediaTarget,
	media *mediapkg.Descriptor,
) (mediapkg.ReadSession, error) {
	if target == nil || media == nil {
		return nil, fmt.Errorf("create Media read session failed, request is incomplete")
	}
	var session mediapkg.ReadSession
	route := tools.NewActionRouter[*entity.ReadMediaTarget, entity.OneofReadMediaTarget](
		tools.ActionMethod(func(ctx context.Context, access *entity.ReadTapeTarget) error {
			var err error
			switch media.Profile.GetTape().GetFormat() {
			case library.TapeFormatTarPAXV1:
				session, err = b.newTarReadSession(ctx, access, media)
			case library.TapeFormatLTFSV0, library.TapeFormatLTFSV1:
				session, err = b.newTapeReadSession(ctx, access, media)
			default:
				err = fmt.Errorf("Tape read format is unsupported")
			}
			return err
		}),
		tools.ActionMethod(func(ctx context.Context, access *entity.ReadVolumeTarget) error {
			var err error
			session, err = b.newVolumeReadSession(ctx, access, media)
			return err
		}),
	)
	if err := route(ctx, target); err != nil {
		return nil, err
	}
	return session, nil
}

// NewWriteSession opens an admitted write. The target is already resolved; media is nil only for FORMAT.
func (b *mediaBackend) NewWriteSession(
	ctx context.Context,
	target *entity.ArchiveMediaTarget,
	media *mediapkg.Descriptor,
) (mediapkg.WriteSession, error) {
	if target == nil {
		return nil, fmt.Errorf("create Media write session failed, request is incomplete")
	}
	var session mediapkg.WriteSession
	route := tools.NewActionRouter[*entity.ArchiveMediaTarget, entity.OneofArchiveMediaTarget](
		tools.ActionMethod(func(ctx context.Context, target *entity.ArchiveTapeTarget) error {
			var err error
			if target.GetFormat() == library.TapeFormatTarPAXV1 {
				session, err = b.newTarWriteSession(ctx, target, media)
			} else {
				session, err = b.newTapeWriteSession(ctx, target, media)
			}
			return err
		}),
		tools.ActionMethod(func(ctx context.Context, target *entity.ArchiveVolumeTarget) error {
			var err error
			session, err = b.newVolumeWriteSession(ctx, target, media)
			return err
		}),
	)
	if err := route(ctx, target); err != nil {
		return nil, err
	}
	return session, nil
}

// registeredMedia rejects a write that needs the Job's registered Media but received none.
func registeredMedia(media *mediapkg.Descriptor, kind entity.MediaKind, identity string) error {
	if media == nil || media.Kind != kind || media.Identity != identity {
		return fmt.Errorf("Media write requires the registered Media, identity=%q", identity)
	}
	return nil
}

// validatePositionMap applies the Library's structural map rules to a boundary descriptor.
func validatePositionMap(media *mediapkg.Descriptor, value *entity.TapePositionMap) error {
	return library.ValidateTapePositionMap(&library.Media{Kind: media.Kind, Profile: media.Profile}, value)
}

func describeMedia(value *library.Media) *mediapkg.Descriptor {
	if value == nil {
		return nil
	}

	// Convert persisted instants for the runtime boundary without inventing unknown dates.
	var created time.Time
	if value.CreatedAtNS != 0 {
		created = time.Unix(0, value.CreatedAtNS)
	}

	// Mutable geometry belongs to this request even when the Library row was cached.
	var positionMap *entity.TapePositionMap
	if value.TapePositionMap != nil {
		positionMap = proto.Clone(value.TapePositionMap).(*entity.TapePositionMap)
	}
	return &mediapkg.Descriptor{
		ID: value.ID, Kind: value.Kind, Identity: value.Identity, Name: value.Name, Profile: value.Profile,
		TapePositionMap: positionMap,
		CreateTime:      created,
		CapacityBytes:   value.CapacityBytes, WrittenBytes: value.WrittenBytes,
	}
}
