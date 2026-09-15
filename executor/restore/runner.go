package restore

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"sync"

	"github.com/google/uuid"
	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/executor"
	"github.com/samuelncui/yatm/tools"
	"github.com/sirupsen/logrus"
	"gorm.io/gorm"
)

func init() {
	executor.RegisterJobType(entity.JobKind_RESTORE, newRunner, registerService)
}

type jobRestoreRunner struct {
	lock         sync.Mutex
	state        restoreState
	exe          *executor.Executor
	job          *executor.Job
	db           *gorm.DB
	destination  *entity.RestoreDestination
	operationID  string
	allowDamaged bool

	progress *executor.Progress
	logFile  *os.File
	logger   *logrus.Logger
}

func newRunner(ctx context.Context, exe *executor.Executor, job *executor.Job) (executor.Runner, error) {
	logFile, err := exe.NewLogWriter(ctx, job.ID)
	if err != nil {
		return nil, fmt.Errorf("open restore log failed, %w", err)
	}

	logger := logrus.New()
	logger.SetOutput(io.MultiWriter(os.Stderr, logFile))
	db, err := exe.NewStateDB(ctx, job.ID)
	if err != nil {
		_ = logFile.Close()
		return nil, fmt.Errorf("open restore Job DB failed, %w", err)
	}
	if err := ensureSchema(ctx, db); err != nil {
		if sqlDB, dbErr := db.DB(); dbErr == nil {
			_ = sqlDB.Close()
		}
		_ = logFile.Close()
		return nil, err
	}
	var config Config
	if err := db.WithContext(ctx).First(&config, 1).Error; err != nil {
		if sqlDB, dbErr := db.DB(); dbErr == nil {
			_ = sqlDB.Close()
		}
		_ = logFile.Close()
		return nil, err
	}
	if config.Spec.GetDestination().GetLocationId() != 0 {
		if _, err := uuid.Parse(config.OperationID); err != nil {
			if sqlDB, dbErr := db.DB(); dbErr == nil {
				_ = sqlDB.Close()
			}
			_ = logFile.Close()
			return nil, fmt.Errorf("Restore operation identity is invalid, %w", err)
		}
	}

	// Build the typed runner after the complete Job schema has been created.
	runner := &jobRestoreRunner{
		state: newRestoreState(job.Status), exe: exe, job: job, db: db, logFile: logFile, logger: logger,
		destination: config.Spec.GetDestination(), operationID: config.OperationID, allowDamaged: config.Spec.GetAllowDamagedCopies(),
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
		if err := a.transition(restoreStateWaitingForIndexRetry); err != nil {
			returnErr = errors.Join(returnErr, err)
		}
	}()

	// Rebuild the manifest from the durable specification.
	config := new(Config)
	if err := a.db.WithContext(ctx).First(config, 1).Error; err != nil {
		return fmt.Errorf("read restore job config failed, %w", err)
	}
	if config.Spec == nil {
		return fmt.Errorf("restore job spec is missing")
	}
	if err := a.applySpec(ctx, config.Spec); err != nil {
		return err
	}
	var pending int64
	if err := a.db.WithContext(ctx).Model(&Copy{}).Where("status = ?", entity.CopyStatus_PENDING).Count(&pending).Error; err != nil {
		return err
	}
	if pending == 0 {
		if err := a.exe.UpdateJobStatus(ctx, a.job.ID, entity.JobStatus_INDEXING, entity.JobStatus_COMPLETED); err != nil {
			return err
		}
		return a.transition(restoreStateCompleted)
	}

	// Publish the durable checkpoint before exposing the Media-ready phase.
	if err := a.exe.UpdateJobStatus(ctx, a.job.ID, entity.JobStatus_INDEXING, entity.JobStatus_PENDING); err != nil {
		return err
	}
	return a.transition(restoreStateWaitingForMedia)
}

func validateRestoreMedia(param *entity.RestoreMediaRequest) error {
	if param == nil || param.Target == nil {
		return fmt.Errorf("restore Media target is missing")
	}
	return nil
}

func (a *jobRestoreRunner) restore(ctx context.Context, param *entity.RestoreMediaRequest) (returnErr error) {
	// Validate and enter the Restore Media flow.
	if err := validateRestoreMedia(param); err != nil {
		return err
	}
	if err := a.transition(restoreStatePreparingMedia); err != nil {
		return err
	}
	defer func() {
		if returnErr == nil {
			return
		}
		next := restoreStateWaitingForMedia
		record := new(executor.JobRecord)
		if err := a.db.WithContext(tools.WithoutTimeout(ctx)).First(record, 1).Error; err == nil && record.Status == entity.JobStatus_COMPLETED {
			next = restoreStateCompleted
		}
		if err := a.transition(next); err != nil {
			returnErr = errors.Join(returnErr, err)
		}
	}()

	// The runner owns the copy flow while the Backend owns physical Media preparation and finalization.
	if err := a.restoreMedia(ctx, param.Target); err != nil {
		return err
	}
	a.dropProgress()

	// Return to the stable phase represented by the committed Job record.
	record := new(executor.JobRecord)
	if err := a.db.WithContext(tools.WithoutTimeout(ctx)).First(record, 1).Error; err != nil {
		return fmt.Errorf("read restore job state failed, %w", err)
	}
	if record.Status == entity.JobStatus_COMPLETED {
		return a.transition(restoreStateCompleted)
	}
	return a.transition(restoreStateWaitingForMedia)
}

func (a *jobRestoreRunner) Close() error {
	var closeErr error
	if sqlDB, err := a.db.DB(); err != nil {
		closeErr = fmt.Errorf("get restore SQL DB failed, %w", err)
	} else if err := sqlDB.Close(); err != nil {
		closeErr = fmt.Errorf("close restore SQL DB failed, %w", err)
	}
	if err := a.logFile.Close(); err != nil && closeErr == nil {
		closeErr = fmt.Errorf("close restore log failed, %w", err)
	}
	return closeErr
}

func (a *jobRestoreRunner) Logger() *logrus.Logger {
	return a.logger
}
