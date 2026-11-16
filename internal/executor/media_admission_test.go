package executor

import (
	"context"

	"github.com/samuelncui/yatm/entity"
	mediapkg "github.com/samuelncui/yatm/internal/media"
)

// openRead opens a read Session through the Job admission bound to backend's attempt.
func openRead(ctx context.Context, backend mediapkg.Backend, target *entity.ReadMediaTarget) (mediapkg.ReadSession, error) {
	b := backend.(*mediaBackend)
	return b.executor.OpenReadSession(ctx, b.jobID, b.logger, b.mediaWait, target)
}

// openWrite opens a write Session through the Job admission bound to backend's attempt.
func openWrite(ctx context.Context, backend mediapkg.Backend, target *entity.ArchiveMediaTarget) (mediapkg.WriteSession, error) {
	b := backend.(*mediaBackend)
	return b.executor.OpenWriteSession(ctx, b.jobID, b.logger, b.mediaWait, target)
}
