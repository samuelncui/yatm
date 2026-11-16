package restore

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/internal/executor"
	"github.com/sirupsen/logrus"
	"gorm.io/gorm"
)

func init() {
	executor.RegisterJobType(entity.JobKind_JOB_KIND_RESTORE, newRunner, registerService)
}

type jobRestoreRunner struct {
	lock         sync.Mutex
	state        restoreState
	exe          *executor.Executor
	job          *executor.Job
	db           *gorm.DB
	destination  *entity.RestoreDestination
	allowDamaged bool

	progress *executor.Progress
	// summary is the result summary the work path maintains; summaryKnown is false until an
	// attempt or the first request counted it.
	summary      restoreSummary
	summaryKnown bool
	// stageKey is the attempt identity of the sampling window this Media attempt opened.
	stageKey string
	logFile  *executor.JobLogWriter
	logger   *logrus.Logger
	tracker  *executor.Tracker
}

func newRunner(ctx context.Context, exe *executor.Executor, job *executor.Job) (executor.Runner, error) {
	files, err := exe.OpenRunnerFiles(ctx, job.ID)
	if err != nil {
		return nil, err
	}
	db := files.DB

	// Recover the frozen creation options only after admitting the current candidate schema.
	var config Config
	if err := db.WithContext(ctx).First(&config, 1).Error; err != nil {
		_ = executor.CloseRunnerFiles(db, files.Log)
		return nil, err
	}

	// Build the typed runner from the complete Job schema created with its bundle.
	runner := &jobRestoreRunner{
		state: newRestoreState(job.CheckpointState()), exe: exe, job: job, db: db, logFile: files.Log, logger: files.Logger,
		destination: config.Spec.GetDestination(), allowDamaged: config.Spec.GetAllowDamagedCopies(),
	}
	if config.LegacyRoot != "" && runner.destination == nil {
		runner.destination = &entity.RestoreDestination{RootPath: config.LegacyRoot, ExecutorId: "local"}
	}
	return runner, nil
}

func (a *jobRestoreRunner) Index(ctx context.Context) (returnErr error) {
	// Enter the type-specific indexing attempt before touching the manifest.
	if err := a.transition(restoreStateIndexing); err != nil {
		return err
	}
	defer func() {
		if returnErr == nil {
			return
		}
		if err := a.transition(restoreStateUnspecified); err != nil {
			returnErr = errors.Join(returnErr, err)
		}
	}()

	// Rebuild the manifest from the durable specification.
	config := new(Config)
	if err := a.db.WithContext(ctx).First(config, 1).Error; err != nil {
		return fmt.Errorf("read restore job config failed, %w", err)
	}
	if err := a.applySpec(ctx, config.Spec); err != nil {
		return err
	}
	var pending int64
	if err := a.db.WithContext(ctx).Model(&Copy{}).Where("status = ?", entity.CopyStatus_COPY_STATUS_PENDING).Count(&pending).Error; err != nil {
		return err
	}
	if pending == 0 {
		if err := a.exe.UpdateJobStatus(ctx, a.job.ID, entity.JobStatus_JOB_STATUS_PREPARING, entity.JobStatus_JOB_STATUS_COMPLETED); err != nil {
			return err
		}
		return a.transition(restoreStateCompleted)
	}

	// Publish the durable checkpoint before exposing the Media-ready phase.
	if err := a.exe.UpdateJobStatus(ctx, a.job.ID, entity.JobStatus_JOB_STATUS_PREPARING, entity.JobStatus_JOB_STATUS_READY); err != nil {
		return err
	}
	return a.transition(restoreStateWaitingForMedia)
}

func (a *jobRestoreRunner) restore(ctx context.Context, param *entity.RestoreMediaRequest) error {
	// Enter the Restore Media flow.
	if err := a.transition(restoreStatePreparingMedia); err != nil {
		return err
	}

	// The summary is counted before this attempt's first checkpoint; its checkpoints advance it.
	// The runner owns the copy flow while the Backend owns physical Media preparation and finalization.
	err := a.recountSummary(ctx)
	if err == nil {
		err = a.restoreMedia(ctx, param.Target)
	}
	if err == nil {
		a.dropProgress()
	}

	// Return to the stable phase represented by the committed Job record.
	next := restoreStateWaitingForMedia
	record := new(executor.JobRecord)
	if readErr := a.db.WithContext(context.WithoutCancel(ctx)).First(record, 1).Error; readErr != nil {
		err = errors.Join(err, fmt.Errorf("read restore job state failed, %w", readErr))
	} else if record.Status == entity.JobStatus_JOB_STATUS_COMPLETED {
		next = restoreStateCompleted
	}
	return errors.Join(err, a.transition(next))
}

func (a *jobRestoreRunner) Close() error {
	return executor.CloseRunnerFiles(a.db, a.logFile)
}

func (a *jobRestoreRunner) Logger() *logrus.Logger {
	return a.logger
}

func (a *jobRestoreRunner) SetActive(active bool) error {
	return executor.SetRunnerFilesActive(a.db, a.logFile, active)
}
