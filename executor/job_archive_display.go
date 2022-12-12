package executor

import (
	"context"
	"sync/atomic"

	"github.com/abc950309/tapewriter/entity"
)

func (e *Executor) getArchiveDisplay(ctx context.Context, job *Job) (*entity.JobDisplayArchive, error) {
	display := new(entity.JobDisplayArchive)

	if exe := e.getArchiveExecutor(ctx, job); exe != nil && exe.progress != nil {
		display.CopyedBytes = atomic.LoadInt64(&exe.progress.bytes)
		display.CopyedFiles = atomic.LoadInt64(&exe.progress.files)
		display.TotalBytes = atomic.LoadInt64(&exe.progress.totalBytes)
		display.TotalFiles = atomic.LoadInt64(&exe.progress.totalFiles)
		display.Speed = atomic.LoadInt64(&exe.progress.speed)
	}

	return display, nil
}
