package archive

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"sync"

	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/executor"
	"github.com/samuelncui/yatm/tools"
	"github.com/sirupsen/logrus"
	"gorm.io/gorm"
)

func init() {
	executor.RegisterJobType(entity.JobKind_ARCHIVE, newRunner, registerService)
}

type jobArchiveRunner struct {
	lock  sync.Mutex
	state archiveState
	exe   *executor.Executor
	job   *executor.Job
	db    *gorm.DB

	progress *executor.Progress
	logFile  *os.File
	logger   *logrus.Logger
}

func newRunner(ctx context.Context, exe *executor.Executor, job *executor.Job) (executor.Runner, error) {
	logFile, err := exe.NewLogWriter(ctx, job.ID)
	if err != nil {
		return nil, fmt.Errorf("open archive log failed, %w", err)
	}

	logger := logrus.New()
	logger.SetOutput(io.MultiWriter(os.Stderr, logFile))
	db, err := exe.NewStateDB(ctx, job.ID)
	if err != nil {
		_ = logFile.Close()
		return nil, fmt.Errorf("open archive Job DB failed, %w", err)
	}
	if err := ensureSchema(ctx, db); err != nil {
		if sqlDB, dbErr := db.DB(); dbErr == nil {
			_ = sqlDB.Close()
		}
		_ = logFile.Close()
		return nil, err
	}
	// Build the typed runner after the complete Job schema has been created.
	runner := &jobArchiveRunner{
		state: newArchiveState(job.Status), exe: exe, job: job, db: db, logFile: logFile, logger: logger,
	}

	// Discard any uncommitted per-file observations from a prior process.
	if err := runner.resetStagedItems(ctx); err != nil {
		if sqlDB, dbErr := db.DB(); dbErr == nil {
			_ = sqlDB.Close()
		}
		_ = logFile.Close()
		return nil, fmt.Errorf("reset archive staged items failed, %w", err)
	}
	return runner, nil
}

func (a *jobArchiveRunner) Index(ctx context.Context) (returnErr error) {
	// Enter the type-specific indexing attempt before touching the manifest.
	if err := a.transition(archiveStateIndexing); err != nil {
		return err
	}
	defer func() {
		if returnErr == nil {
			return
		}
		if err := a.transition(archiveStateWaitingForIndexRetry); err != nil {
			returnErr = errors.Join(returnErr, err)
		}
	}()

	// Rebuild the manifest from the durable specification.
	config := new(Config)
	if err := a.db.WithContext(ctx).First(config, 1).Error; err != nil {
		return fmt.Errorf("read archive job config failed, %w", err)
	}
	if config.Spec == nil {
		return fmt.Errorf("archive job spec is missing")
	}
	if err := a.applySpec(ctx, config.Spec); err != nil {
		return err
	}
	a.createCompanionPreview(ctx, config)

	// Publish the durable checkpoint before exposing the Media-ready phase.
	if err := a.exe.UpdateJobStatus(ctx, a.job.ID, entity.JobStatus_INDEXING, entity.JobStatus_PENDING); err != nil {
		return err
	}
	return a.transition(archiveStateWaitingForMedia)
}

func validateWriteMedia(param *entity.WriteArchiveMediaRequest) error {
	if param == nil || param.Target == nil {
		return fmt.Errorf("archive Media target is missing")
	}
	return nil
}

func (a *jobArchiveRunner) writeMedia(ctx context.Context, param *entity.WriteArchiveMediaRequest) error {
	// Validate and enter the Archive Media flow.
	if err := validateWriteMedia(param); err != nil {
		return err
	}
	if err := a.transition(archiveStatePreparingMedia); err != nil {
		return err
	}

	// The runner owns the copy flow while the Backend owns physical Media preparation and finalization.
	archiveErr := a.archiveMedia(ctx, param.Target)

	// Return to the stable phase represented by the committed Job record.
	record := new(executor.JobRecord)
	if err := a.db.WithContext(tools.WithoutTimeout(ctx)).First(record, 1).Error; err != nil {
		return errors.Join(archiveErr, fmt.Errorf("read archive job state failed, %w", err))
	}
	if record.Status == entity.JobStatus_COMPLETED {
		return errors.Join(archiveErr, a.transition(archiveStateCompleted))
	}
	return errors.Join(archiveErr, a.transition(archiveStateWaitingForMedia))
}

func (a *jobArchiveRunner) Close() error {
	var closeErr error
	if sqlDB, err := a.db.DB(); err != nil {
		closeErr = fmt.Errorf("get archive SQL DB failed, %w", err)
	} else if err := sqlDB.Close(); err != nil {
		closeErr = fmt.Errorf("close archive SQL DB failed, %w", err)
	}
	if err := a.logFile.Close(); err != nil && closeErr == nil {
		closeErr = fmt.Errorf("close archive log failed, %w", err)
	}
	return closeErr
}

func (a *jobArchiveRunner) Logger() *logrus.Logger {
	return a.logger
}
