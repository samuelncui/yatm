package executor

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"

	"github.com/sirupsen/logrus"
	"gorm.io/gorm"
)

func (e *Executor) logPath(jobID int64) string {
	return filepath.Join(e.jobWorkPath(jobID), "job.log")
}

// JobLogWriter keeps a descriptor only during an active attempt. Idle diagnostic
// writes append through short-lived files while the cached runner keeps its counters.
type JobLogWriter struct {
	lock   sync.Mutex
	path   string
	file   *os.File
	active bool
	closed bool
}

func (e *Executor) NewLogWriter(ctx context.Context, jobID int64) (*JobLogWriter, error) {
	if _, err := e.EnsureJobWorkPath(ctx, jobID); err != nil {
		return nil, fmt.Errorf("ensure job work path failed, job_id=%d, %w", jobID, err)
	}
	path := e.logPath(jobID)
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o644)
	if err != nil {
		return nil, fmt.Errorf("create job log failed, path=%q, %w", path, err)
	}

	if err := file.Close(); err != nil {
		return nil, err
	}
	return &JobLogWriter{path: path}, nil
}

func (w *JobLogWriter) Write(data []byte) (int, error) {
	w.lock.Lock()
	defer w.lock.Unlock()
	if w.closed {
		return 0, os.ErrClosed
	}

	// Opening is lazy, so progress-only reads never retain a log descriptor.
	if w.file == nil {
		file, err := os.OpenFile(w.path, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o644)
		if err != nil {
			return 0, err
		}
		w.file = file
	}
	n, err := w.file.Write(data)
	if !w.active {
		err = errors.Join(err, w.file.Close())
		w.file = nil
	}
	return n, err
}

func (w *JobLogWriter) setActive(active bool) error {
	w.lock.Lock()
	defer w.lock.Unlock()
	if w.closed {
		return os.ErrClosed
	}
	w.active = active
	if active || w.file == nil {
		return nil
	}
	err := w.file.Close()
	w.file = nil
	return err
}

func (w *JobLogWriter) Close() error {
	w.lock.Lock()
	defer w.lock.Unlock()
	w.closed = true
	if w.file == nil {
		return nil
	}
	err := w.file.Close()
	w.file = nil
	return err
}

// RunnerFiles are the Job bundle handles a runner owns from creation until its Close.
type RunnerFiles struct {
	DB  *gorm.DB
	Log *JobLogWriter
	// Logger writes the runner's diagnostics to both stderr and the Job log.
	Logger *logrus.Logger
}

// OpenRunnerFiles opens the Job's append log and its existing state database for a new runner.
// A failure releases whatever it already opened, so the caller owns handles only on success.
func (e *Executor) OpenRunnerFiles(ctx context.Context, jobID int64) (*RunnerFiles, error) {
	log, err := e.NewLogWriter(ctx, jobID)
	if err != nil {
		return nil, err
	}

	// Opening existing state never migrates the bundle's schema.
	db, err := e.NewStateDB(ctx, jobID)
	if err != nil {
		_ = log.Close()
		return nil, err
	}

	logger := logrus.New()
	logger.SetOutput(io.MultiWriter(os.Stderr, log))
	return &RunnerFiles{DB: db, Log: log, Logger: logger}, nil
}

// CloseRunnerFiles releases a runner's state database and log independently, reporting each failure.
func CloseRunnerFiles(db *gorm.DB, log *JobLogWriter) error {
	dbErr := closeGORMDB(db)
	if err := log.Close(); err != nil {
		return errors.Join(dbErr, fmt.Errorf("close job log failed, %w", err))
	}
	return dbErr
}

// SetRunnerFilesActive changes descriptor retention without replacing the runner
// or its database handle. database/sql closes borrowed connections when they return.
func SetRunnerFilesActive(db *gorm.DB, log *JobLogWriter, active bool) error {
	connections, err := db.DB()
	if err != nil {
		return err
	}
	idle := 0
	if active {
		idle = 1
	}
	connections.SetMaxIdleConns(idle)
	return log.setActive(active)
}

func (e *Executor) NewLogReader(_ context.Context, jobID int64) (*os.File, error) {
	path := e.logPath(jobID)
	file, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("open job log failed, path=%q, %w", path, err)
	}

	return file, nil
}
