package executor

import (
	"context"

	"github.com/samuelncui/tapemanager/entity"
)

func (e *Executor) startRestore(ctx context.Context, job *Job) error {
	return e.Submit(ctx, job, &entity.JobNextParam{Param: &entity.JobNextParam_Restore{
		Restore: &entity.JobRestoreNextParam{Param: &entity.JobRestoreNextParam_WaitForTape{
			WaitForTape: &entity.JobRestoreWaitForTapeParam{},
		}},
	}})
}
