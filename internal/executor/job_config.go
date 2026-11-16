package executor

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"

	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/internal/dataformat"
	"github.com/samuelncui/yatm/internal/resource"
	"gorm.io/gorm"
)

// ReadJobConfig reads the retained typed config and priority without constructing a runner.
// The caller owns config and interprets absent creation inputs, including a missing config row.
func (e *Executor) ReadJobConfig(ctx context.Context, jobID int64, kind entity.JobKind, config any) (int64, error) {
	// Keep the catalog admission and bundle reads inside the existing deletion guard.
	if jobID <= 0 {
		return 0, fmt.Errorf("Job ID must be positive")
	}
	unlockJob := e.lockJob(jobID)
	defer unlockJob()
	var row jobCatalogRow
	if err := e.readDB().WithContext(ctx).Select("id").First(&row, jobID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return 0, fmt.Errorf("read Job config failed, id=%d, %w", jobID, ErrJobNotFound)
		}
		return 0, fmt.Errorf("read Job catalog failed, id=%d, %w", jobID, err)
	}

	// Open only an existing supported bundle, with SQLite itself enforcing read-only access.
	if err := dataformat.CheckBundle(e.jobWorkPath(jobID), jobID); err != nil {
		return 0, err
	}
	filename, err := filepath.Abs(e.stateDBPath(jobID))
	if err != nil {
		return 0, fmt.Errorf("resolve Job DB path failed, id=%d, %w", jobID, err)
	}
	info, err := os.Lstat(filename)
	if err != nil {
		return 0, fmt.Errorf("inspect Job DB failed, id=%d, %w", jobID, err)
	}
	if !info.Mode().IsRegular() {
		return 0, fmt.Errorf("Job DB is not a regular file, id=%d", jobID)
	}
	dsn := (&url.URL{Scheme: "file", Path: filename, RawQuery: "mode=ro"}).String()
	db, err := resource.OpenSQLite(dsn)
	if err != nil {
		return 0, fmt.Errorf("open read-only Job DB failed, id=%d, %w", jobID, err)
	}
	defer resource.CloseDB(db)

	// The common record owns kind and priority; the typed caller owns the config shape.
	var record JobRecord
	if err := db.WithContext(ctx).First(&record, singletonJobID).Error; err != nil {
		return 0, fmt.Errorf("read Job record failed, id=%d, %w", jobID, err)
	}
	if record.Kind != kind {
		return 0, fmt.Errorf("unexpected job kind, id=%d got=%s want=%s", jobID, record.Kind, kind)
	}
	if err := db.WithContext(ctx).Find(config, 1).Error; err != nil {
		return 0, fmt.Errorf("read Job config failed, id=%d, %w", jobID, err)
	}
	return record.Priority, nil
}

// LatestJobID returns the most recently created Job of kind with an ID below before, or zero when
// there is none; a zero before considers every Job. Deleted Jobs are not considered.
func (e *Executor) LatestJobID(ctx context.Context, kind entity.JobKind, before int64) (int64, error) {
	query := e.readDB().WithContext(ctx).Model(&jobCatalogRow{}).Where("catalog_kind = ?", kind)
	if before > 0 {
		query = query.Where("id < ?", before)
	}
	var ids []int64
	if err := query.Order("id DESC").Limit(1).Pluck("id", &ids).Error; err != nil {
		return 0, fmt.Errorf("read latest Job failed, kind=%s, %w", kind, err)
	}
	if len(ids) == 0 {
		return 0, nil
	}
	return ids[0], nil
}

// CreationJobID resolves a creation read to its Job: the given id, or with latest the most recent
// Job of kind, which is zero when there is none.
func (e *Executor) CreationJobID(ctx context.Context, kind entity.JobKind, id int64, latest bool) (int64, error) {
	if !latest {
		return id, nil
	}
	if id != 0 {
		return 0, fmt.Errorf("latest Job creation takes no Job ID, id=%d", id)
	}
	return e.LatestJobID(ctx, kind, 0)
}
