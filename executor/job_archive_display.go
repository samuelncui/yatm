package executor

import (
	"context"
	"sync/atomic"

	"github.com/samuelncui/yatm/entity"
)

func (e *Executor) getArchiveDisplay(ctx context.Context, job *Job) (*entity.JobArchiveDisplay, error) {
	display := new(entity.JobArchiveDisplay)

	if exe := e.getArchiveExecutor(ctx, job); exe != nil && exe.progress != nil {
		display.CopiedBytes = atomic.LoadInt64(&exe.progress.bytes)
		display.CopiedFiles = atomic.LoadInt64(&exe.progress.files)
		display.TotalBytes = atomic.LoadInt64(&exe.progress.totalBytes)
		display.TotalFiles = atomic.LoadInt64(&exe.progress.totalFiles)
		display.StartTime = exe.progress.startTime.Unix()

		speed := atomic.LoadInt64(&exe.progress.speed)
		display.Speed = &speed
	}

	return display, nil
}
