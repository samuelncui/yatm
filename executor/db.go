package executor

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/samuelncui/yatm/internal/dataformat"
	"github.com/samuelncui/yatm/resource"
	"gorm.io/gorm"
)

func (e *Executor) createBundle(
	ctx context.Context,
	job *Job,
	record *JobRecord,
	initialize func(*gorm.DB) error,
) error {
	// Create the complete directory layout before exposing runner resources.
	dir, err := e.EnsureJobWorkPath(ctx, job.ID)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Join(dir, "tapes"), 0o755); err != nil {
		return fmt.Errorf("create job Tape directory failed, %w", err)
	}

	// Persist immutable bundle identity as a small JSON file.
	metadata, err := json.Marshal(dataformat.NewBundle(job.ID, time.UnixMilli(job.CreatedAt)))
	if err != nil {
		return fmt.Errorf("encode job metadata failed, id=%d, %w", job.ID, err)
	}
	if err := os.WriteFile(filepath.Join(dir, "job.json"), metadata, 0o644); err != nil {
		return fmt.Errorf("write job metadata failed, id=%d, %w", job.ID, err)
	}

	// Initialize the singleton common record before kind-specific tables.
	db, err := resource.OpenSQLite(e.stateDBPath(job.ID))
	if err != nil {
		return fmt.Errorf("create Job DB failed, id=%d, %w", job.ID, err)
	}
	defer closeGORMDB(db)
	if err := db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.AutoMigrate(&JobRecord{}); err != nil {
			return fmt.Errorf("create common schema failed, %w", err)
		}
		if err := tx.Create(record).Error; err != nil {
			return fmt.Errorf("create common record failed, %w", err)
		}
		if err := initialize(tx); err != nil {
			return fmt.Errorf("initialize typed schema failed, %w", err)
		}
		return nil
	}); err != nil {
		return fmt.Errorf("create Job DB failed, id=%d, %w", job.ID, err)
	}
	return nil
}

func (e *Executor) NewStateDB(_ context.Context, jobID int64) (*gorm.DB, error) {
	// Opening existing state must not turn an unsupported bundle into a current one.
	if err := dataformat.CheckBundle(e.jobWorkPath(jobID), jobID); err != nil {
		return nil, err
	}
	if _, err := os.Stat(e.stateDBPath(jobID)); err != nil {
		return nil, fmt.Errorf("open Job DB failed, id=%d, %w", jobID, err)
	}
	db, err := resource.OpenSQLite(e.stateDBPath(jobID))
	if err != nil {
		return nil, fmt.Errorf("open Job DB failed, id=%d, %w", jobID, err)
	}
	return db, nil
}

func (e *Executor) openStateDB(jobID int64) (*gorm.DB, func(), error) {
	db, err := e.NewStateDB(context.Background(), jobID)
	if err != nil {
		return nil, nil, err
	}
	return db, func() { _ = closeGORMDB(db) }, nil
}

func (e *Executor) stateDBPath(jobID int64) string {
	return filepath.Join(e.jobWorkPath(jobID), "state.db")
}

func closeGORMDB(db *gorm.DB) error {
	sqlDB, err := db.DB()
	if err != nil {
		return fmt.Errorf("get SQL DB failed, %w", err)
	}
	if err := sqlDB.Close(); err != nil {
		return fmt.Errorf("close SQL DB failed, %w", err)
	}
	return nil
}
