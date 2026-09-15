package fileops

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/google/uuid"
	"github.com/samuelncui/yatm/executor"
	"github.com/samuelncui/yatm/resource"
	"gorm.io/gorm"
)

type operation struct {
	exe              *executor.Executor
	db               *gorm.DB
	directory        string
	releaseTemporary func()
}

func newOperation(ctx context.Context, exe *executor.Executor) (_ *operation, returnErr error) {
	// Protect the random namespace before creating a request-scoped disk-backed bounded manifest.
	prefix := ".yatm-fileops-" + uuid.NewString() + "-"
	release, err := exe.ProtectTemporaryNames(prefix)
	if err != nil {
		return nil, err
	}
	op := &operation{exe: exe, releaseTemporary: release}
	defer func() {
		if returnErr != nil {
			returnErr = errors.Join(returnErr, op.Close())
		}
	}()
	op.directory, err = os.MkdirTemp("", prefix)
	if err != nil {
		return nil, fmt.Errorf("create temporary operation directory failed, %w", err)
	}
	op.db, err = resource.OpenSQLite(filepath.Join(op.directory, "manifest.db"))
	if err != nil {
		return nil, err
	}
	return op, nil
}

func (r *operation) Close() error {
	// Remove only this request's allocated directory after releasing its SQLite handles.
	var failures []error
	if r.db != nil {
		sqlDB, err := r.db.DB()
		if err != nil {
			failures = append(failures, err)
		} else {
			failures = append(failures, sqlDB.Close())
		}
		r.db = nil
	}
	if r.directory != "" {
		failures = append(failures, os.RemoveAll(r.directory))
		r.directory = ""
	}
	if r.releaseTemporary != nil {
		r.releaseTemporary()
		r.releaseTemporary = nil
	}
	return errors.Join(failures...)
}
