package executor

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

func (e *Executor) logPath(jobID int64) string {
	return filepath.Join(e.jobWorkPath(jobID), "job.log")
}

func (e *Executor) NewLogWriter(ctx context.Context, jobID int64) (*os.File, error) {
	if _, err := e.EnsureJobWorkPath(ctx, jobID); err != nil {
		return nil, fmt.Errorf("ensure job work path failed, job_id=%d, %w", jobID, err)
	}
	path := e.logPath(jobID)
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o644)
	if err != nil {
		return nil, fmt.Errorf("create job log failed, path=%q, %w", path, err)
	}

	return file, nil
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
