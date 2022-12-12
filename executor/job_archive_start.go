package executor

import (
	"context"

	"github.com/abc950309/tapewriter/entity"
)

func (e *Executor) startArchive(ctx context.Context, job *Job) error {
	return e.Submit(ctx, job, &entity.JobNextParam{Param: &entity.JobNextParam_Archive{
		Archive: &entity.JobArchiveNextParam{Param: &entity.JobArchiveNextParam_WaitForTape{
			WaitForTape: &entity.JobArchiveWaitForTapeParam{},
		}},
	}})
}
