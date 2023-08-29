package executor

import (
	"context"
	"sync/atomic"

	"github.com/abc950309/tapewriter/entity"
)

func (e *Executor) getRestoreDisplay(ctx context.Context, job *Job) (*entity.JobRestoreDisplay, error) {
	display := new(entity.JobRestoreDisplay)

	if exe := e.getRestoreExecutor(ctx, job); exe != nil && exe.progress != nil {
		display.CopyedBytes = atomic.LoadInt64(&exe.progress.bytes)
		display.CopyedFiles = atomic.LoadInt64(&exe.progress.files)
		display.TotalBytes = atomic.LoadInt64(&exe.progress.totalBytes)
		display.TotalFiles = atomic.LoadInt64(&exe.progress.totalFiles)
		display.StartTime = exe.progress.startTime.Unix()

		speed := atomic.LoadInt64(&exe.progress.speed)
		display.Speed = &speed
	}

	return display, nil
}
