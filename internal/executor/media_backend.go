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

// NewMediaBackend creates the factory bound to one active Job attempt.
func (e *Executor) NewMediaBackend(jobID int64, logger *logrus.Logger, mediaWait func(bool)) mediapkg.Backend {
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

func (b *mediaBackend) NewReadSession(
	ctx context.Context,
	target *entity.ReadMediaTarget,
) (mediapkg.ReadSession, error) {
	if target == nil {
		return nil, fmt.Errorf("create Media read session failed, request is incomplete")
	}
	var session mediapkg.ReadSession
	route := tools.NewActionRouter[*entity.ReadMediaTarget, entity.OneofReadMediaTarget](
		tools.ActionMethod(func(ctx context.Context, access *entity.ReadTapeTarget) error {
			var err error
			session, err = b.newTapeReadSession(ctx, access, target)
			return err
		}),
		tools.ActionMethod(func(ctx context.Context, access *entity.ReadVolumeTarget) error {
			var err error
			session, err = b.newVolumeReadSession(ctx, access, target)
			return err
		}),
	)
	if err := route(ctx, target); err != nil {
		return nil, err
	}
	return session, nil
}

func validateReadExpectation(stored *library.Media, target *entity.ReadMediaTarget) error {
	// Read only from the Media identity and profile frozen for this attempt.
	if stored.ID != target.ExpectedMediaId || stored.Identity != target.ExpectedIdentity ||
		!proto.Equal(stored.Profile, target.ExpectedProfile) {
		return fmt.Errorf("Media read identity differs from the frozen expectation, media_id=%d identity=%q", stored.ID, stored.Identity)
	}
	return nil
}

func (b *mediaBackend) NewWriteSession(
	ctx context.Context,
	target *entity.ArchiveMediaTarget,
) (mediapkg.WriteSession, error) {
	if target == nil {
		return nil, fmt.Errorf("create Media write session failed, request is incomplete")
	}
	var session mediapkg.WriteSession
	route := tools.NewActionRouter[*entity.ArchiveMediaTarget, entity.OneofArchiveMediaTarget](
		tools.ActionMethod(func(ctx context.Context, target *entity.ArchiveTapeTarget) error {
			var err error
			session, err = b.newTapeWriteSession(ctx, target)
			return err
		}),
		tools.ActionMethod(func(ctx context.Context, target *entity.ArchiveVolumeTarget) error {
			var err error
			session, err = b.newVolumeWriteSession(ctx, target)
			return err
		}),
	)
	if err := route(ctx, target); err != nil {
		return nil, err
	}
	return session, nil
}

func describeMedia(value *library.Media) *mediapkg.Descriptor {
	if value == nil {
		return nil
	}
	var created time.Time
	if value.CreatedAtNS != 0 {
		created = time.Unix(0, value.CreatedAtNS)
	}
	var destroyed *time.Time
	if value.DestroyedAtNS != nil {
		stamp := time.Unix(0, *value.DestroyedAtNS)
		destroyed = &stamp
	}
	return &mediapkg.Descriptor{
		ID: value.ID, Kind: value.Kind, Identity: value.Identity, Name: value.Name, Profile: value.Profile,
		CreateTime: created, DestroyTime: destroyed,
		CapacityBytes: value.CapacityBytes, WrittenBytes: value.WrittenBytes,
	}
}
