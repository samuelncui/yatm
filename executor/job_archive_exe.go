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

type jobArchiveExecutor struct {
	lock sync.Mutex
	exe  *Executor
	job  *Job

	progress *progress
	logFile  *os.File
	logger   *logrus.Logger
}

func (*jobTypeArchive) GetExecutor(ctx context.Context, exe *Executor, job *Job) (JobExecutor, error) {
	logFile, err := exe.newLogWriter(job.ID)
	if err != nil {
		return nil, fmt.Errorf("get log writer fail, %w", err)
	}

	logger := logrus.New()
	logger.SetOutput(io.MultiWriter(os.Stderr, logFile))

	e := &jobArchiveExecutor{
		exe: exe,
		job: job,

		logFile: logFile,
		logger:  logger,
	}

	return e, nil
}

func (a *jobArchiveExecutor) Dispatch(ctx context.Context, next *entity.JobDispatchParam) error {
	param := next.GetArchive()
	if param == nil {
		return fmt.Errorf("unexpected next param type, unexpected= JobArchiveDispatchParam, has= %s", next)
	}

	return a.dispatch(ctx, param)
}

func (a *jobArchiveExecutor) dispatch(ctx context.Context, param *entity.JobArchiveDispatchParam) error {
	if p := param.GetCopying(); p != nil {
		if err := a.switchStep(
			ctx, entity.JobArchiveStep_COPYING, entity.JobStatus_PROCESSING,
			mapset.NewThreadUnsafeSet(entity.JobArchiveStep_WAIT_FOR_TAPE),
		); err != nil {
			return err
		}

		tools.Working()
		go tools.WrapWithLogger(ctx, a.logger, func() {
			defer tools.Done()

			if err := a.makeTape(tools.ShutdownContext, p.Device, p.Barcode, p.Name); err != nil {
				a.logger.WithContext(ctx).WithError(err).Errorf("make tape has error, barcode= '%s' name= '%s'", p.Barcode, p.Name)
			}
		})

		return nil
	}

	if p := param.GetWaitForTape(); p != nil {
		return a.switchStep(
			ctx, entity.JobArchiveStep_WAIT_FOR_TAPE, entity.JobStatus_PENDING,
			mapset.NewThreadUnsafeSet(entity.JobArchiveStep_PENDING, entity.JobArchiveStep_COPYING),
		)
	}

	if p := param.GetFinished(); p != nil {
		if err := a.switchStep(
			ctx, entity.JobArchiveStep_FINISHED, entity.JobStatus_COMPLETED,
			mapset.NewThreadUnsafeSet(entity.JobArchiveStep_COPYING),
		); err != nil {
			return err
		}

		return a.Close(ctx)
	}

	return nil
}

func (a *jobArchiveExecutor) Display(ctx context.Context) (*entity.JobDisplay, error) {
	p := a.progress
	if p == nil {
		return nil, nil
	}

	display := new(entity.JobArchiveDisplay)
	display.CopiedBytes = atomic.LoadInt64(&p.bytes)
	display.CopiedFiles = atomic.LoadInt64(&p.files)
	display.TotalBytes = atomic.LoadInt64(&p.totalBytes)
	display.TotalFiles = atomic.LoadInt64(&p.totalFiles)
	display.StartTime = p.startTime.Unix()

	speed := atomic.LoadInt64(&p.speed)
	display.Speed = &speed

	return &entity.JobDisplay{Display: &entity.JobDisplay_Archive{Archive: display}}, nil
}

func (a *jobArchiveExecutor) Close(ctx context.Context) error {
	a.logFile.Close()
	a.exe.RemoveJobExecutor(ctx, a.job.ID)
	return nil
}

func (a *jobArchiveExecutor) Logger() *logrus.Logger {
	return a.logger
}

func (a *jobArchiveExecutor) getState() *entity.JobArchiveState {
	a.lock.Lock()
	defer a.lock.Unlock()

	if a.job.State == nil || a.job.State.GetArchive() == nil {
		a.job.State = &entity.JobState{State: &entity.JobState_Archive{Archive: &entity.JobArchiveState{}}}
	}

	return a.job.State.GetArchive()
}

func (a *jobArchiveExecutor) updateJob(ctx context.Context, change func(*Job, *entity.JobArchiveState) error) error {
	a.lock.Lock()
	defer a.lock.Unlock()

	if a.job.State == nil || a.job.State.GetArchive() == nil {
		a.job.State = &entity.JobState{State: &entity.JobState_Archive{Archive: &entity.JobArchiveState{}}}
	}

	if err := change(a.job, a.job.State.GetArchive()); err != nil {
		a.logger.WithContext(ctx).WithError(err).Warnf("update state failed while exec change callback")
		return fmt.Errorf("update state failed while exec change callback, %w", err)
	}
	if _, err := a.exe.SaveJob(ctx, a.job); err != nil {
		a.logger.WithContext(ctx).WithError(err).Warnf("update state failed while save job")
		return fmt.Errorf("update state failed while save job, %w", err)
	}

	return nil
}

func (a *jobArchiveExecutor) switchStep(ctx context.Context, target entity.JobArchiveStep, status entity.JobStatus, expect mapset.Set[entity.JobArchiveStep]) error {
	return a.updateJob(ctx, func(job *Job, state *entity.JobArchiveState) error {
		if !expect.Contains(state.Step) {
			return fmt.Errorf("unexpected current step, target= '%s' expect= '%s' has= '%s'", target, expect, state.Step)
		}

		state.Step = target
		job.Status = status
		return nil
	})
}
