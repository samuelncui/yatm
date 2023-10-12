package executor

import (
	"context"
	"fmt"
	"io"
	"os"
	"sync"
	"sync/atomic"

	mapset "github.com/deckarep/golang-set/v2"
	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/tools"
	"github.com/sirupsen/logrus"
)

type jobRestoreExecutor struct {
	lock sync.Mutex
	exe  *Executor
	job  *Job

	progress *progress
	logFile  *os.File
	logger   *logrus.Logger
}

func (*jobTypeRestore) GetExecutor(ctx context.Context, exe *Executor, job *Job) (JobExecutor, error) {
	logFile, err := exe.newLogWriter(job.ID)
	if err != nil {
		return nil, fmt.Errorf("get log writer fail, %w", err)
	}

	logger := logrus.New()
	logger.SetOutput(io.MultiWriter(os.Stderr, logFile))

	e := &jobRestoreExecutor{
		exe: exe,
		job: job,

		logFile: logFile,
		logger:  logger,
	}

	return e, nil
}

func (a *jobRestoreExecutor) Dispatch(ctx context.Context, next *entity.JobDispatchParam) error {
	param := next.GetRestore()
	if param == nil {
		return fmt.Errorf("unexpected next param type, unexpected= JobRestoreDispatchParam, has= %s", next)
	}

	return a.dispatch(ctx, param)
}

func (a *jobRestoreExecutor) dispatch(ctx context.Context, param *entity.JobRestoreDispatchParam) error {
	if p := param.GetCopying(); p != nil {
		if err := a.switchStep(
			ctx, entity.JobRestoreStep_COPYING, entity.JobStatus_PROCESSING,
			mapset.NewThreadUnsafeSet(entity.JobRestoreStep_WAIT_FOR_TAPE),
		); err != nil {
			return err
		}

		tools.Working()
		go tools.WrapWithLogger(ctx, a.logger, func() {
			defer tools.Done()

			if err := a.restoreTape(tools.ShutdownContext, p.Device); err != nil {
				a.logger.WithContext(ctx).WithError(err).Errorf("restore tape has error, device= '%s'", p.Device)
			}
		})

		return nil
	}

	if p := param.GetWaitForTape(); p != nil {
		return a.switchStep(
			ctx, entity.JobRestoreStep_WAIT_FOR_TAPE, entity.JobStatus_PENDING,
			mapset.NewThreadUnsafeSet(entity.JobRestoreStep_PENDING, entity.JobRestoreStep_COPYING),
		)
	}

	if p := param.GetFinished(); p != nil {
		if err := a.switchStep(
			ctx, entity.JobRestoreStep_FINISHED, entity.JobStatus_COMPLETED,
			mapset.NewThreadUnsafeSet(entity.JobRestoreStep_COPYING),
		); err != nil {
			return err
		}

		return a.Close(ctx)
	}

	return nil
}

func (a *jobRestoreExecutor) Display(ctx context.Context) (*entity.JobDisplay, error) {
	p := a.progress
	if p == nil {
		return nil, nil
	}

	display := new(entity.JobRestoreDisplay)
	display.CopiedBytes = atomic.LoadInt64(&p.bytes)
	display.CopiedFiles = atomic.LoadInt64(&p.files)
	display.TotalBytes = atomic.LoadInt64(&p.totalBytes)
	display.TotalFiles = atomic.LoadInt64(&p.totalFiles)
	display.StartTime = p.startTime.Unix()

	speed := atomic.LoadInt64(&p.speed)
	display.Speed = &speed

	return &entity.JobDisplay{Display: &entity.JobDisplay_Restore{Restore: display}}, nil
}

func (a *jobRestoreExecutor) Close(ctx context.Context) error {
	a.logFile.Close()
	a.exe.RemoveJobExecutor(ctx, a.job.ID)
	return nil
}

func (a *jobRestoreExecutor) Logger() *logrus.Logger {
	return a.logger
}

func (a *jobRestoreExecutor) getState() *entity.JobRestoreState {
	a.lock.Lock()
	defer a.lock.Unlock()

	if a.job.State == nil || a.job.State.GetRestore() == nil {
		a.job.State = &entity.JobState{State: &entity.JobState_Restore{Restore: &entity.JobRestoreState{}}}
	}

	return a.job.State.GetRestore()
}

func (a *jobRestoreExecutor) updateJob(ctx context.Context, change func(*Job, *entity.JobRestoreState) error) error {
	a.lock.Lock()
	defer a.lock.Unlock()

	if a.job.State == nil || a.job.State.GetRestore() == nil {
		a.job.State = &entity.JobState{State: &entity.JobState_Restore{Restore: &entity.JobRestoreState{}}}
	}

	if err := change(a.job, a.job.State.GetRestore()); err != nil {
		a.logger.WithContext(ctx).WithError(err).Warnf("update state failed while exec change callback")
		return fmt.Errorf("update state failed while exec change callback, %w", err)
	}
	if _, err := a.exe.SaveJob(ctx, a.job); err != nil {
		a.logger.WithContext(ctx).WithError(err).Warnf("update state failed while save job")
		return fmt.Errorf("update state failed while save job, %w", err)
	}

	return nil
}

func (a *jobRestoreExecutor) switchStep(ctx context.Context, target entity.JobRestoreStep, status entity.JobStatus, expect mapset.Set[entity.JobRestoreStep]) error {
	return a.updateJob(ctx, func(job *Job, state *entity.JobRestoreState) error {
		if !expect.Contains(state.Step) {
			return fmt.Errorf("unexpected current step, target= '%s' expect= '%s' has= '%s'", target, expect, state.Step)
		}

		state.Step = target
		job.Status = status
		return nil
	})
}
