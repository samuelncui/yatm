package executor

import (
	"context"
	"fmt"

	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/library"
	mediapkg "github.com/samuelncui/yatm/media"
	"github.com/samuelncui/yatm/tools"
	"github.com/sirupsen/logrus"
	"google.golang.org/protobuf/proto"
	"gorm.io/gorm"
)

type mediaBackend struct {
	executor *Executor
	jobID    int64
	logger   *logrus.Logger
}

// NewMediaBackend creates the factory bound to one active Job attempt.
func (e *Executor) NewMediaBackend(jobID int64, logger *logrus.Logger) mediapkg.Backend {
	if logger == nil {
		logger = logrus.New()
	}
	return &mediaBackend{executor: e, jobID: jobID, logger: logger}
}

func (b *mediaBackend) NewReadSession(
	ctx context.Context,
	db *gorm.DB,
	target *entity.ReadMediaTarget,
) (mediapkg.ReadSession, error) {
	if db == nil || target == nil {
		return nil, fmt.Errorf("create Media read session failed, request is incomplete")
	}
	var session mediapkg.ReadSession
	route := tools.NewActionRouter[*entity.ReadMediaTarget, entity.OneofReadMediaTarget](
		tools.ActionMethod(func(ctx context.Context, access *entity.ReadTapeTarget) error {
			var err error
			session, err = b.newTapeReadSession(ctx, db, access, target)
			return err
		}),
		tools.ActionMethod(func(ctx context.Context, access *entity.ReadVolumeTarget) error {
			var err error
			session, err = b.newVolumeReadSession(ctx, db, access, target)
			return err
		}),
	)
	if err := route(ctx, target); err != nil {
		return nil, err
	}
	return session, nil
}

func validateReadExpectation(stored *library.Media, target *entity.ReadMediaTarget) error {
	// Legacy restored Jobs may have no frozen Media expectation; new readers supply all fields together.
	if target.GetExpectedMediaId() == 0 && target.GetExpectedIdentity() == "" && target.GetExpectedProfile() == nil {
		return nil
	}
	if stored.ID != target.ExpectedMediaId || stored.Identity != target.ExpectedIdentity ||
		!proto.Equal(stored.Profile, target.ExpectedProfile) {
		return fmt.Errorf("Media read identity differs from the frozen expectation, media_id=%d identity=%q", stored.ID, stored.Identity)
	}
	return nil
}

func (b *mediaBackend) NewWriteSession(
	ctx context.Context,
	db *gorm.DB,
	target *entity.ArchiveMediaTarget,
) (mediapkg.WriteSession, error) {
	if db == nil || target == nil {
		return nil, fmt.Errorf("create Media write session failed, request is incomplete")
	}
	var session mediapkg.WriteSession
	route := tools.NewActionRouter[*entity.ArchiveMediaTarget, entity.OneofArchiveMediaTarget](
		tools.ActionMethod(func(ctx context.Context, target *entity.ArchiveTapeTarget) error {
			var err error
			session, err = b.newTapeWriteSession(ctx, db, target)
			return err
		}),
		tools.ActionMethod(func(ctx context.Context, target *entity.ArchiveVolumeTarget) error {
			var err error
			session, err = b.newVolumeWriteSession(ctx, db, target)
			return err
		}),
	)
	if err := route(ctx, target); err != nil {
		return nil, err
	}
	return session, nil
}
