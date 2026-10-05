package executor

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"

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
