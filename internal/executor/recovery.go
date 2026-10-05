package executor

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"

	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/internal/dataformat"
)

var (
	ErrBundleMissing = dataformat.ErrBundleMissing
	ErrOrphanBundle  = errors.New("job bundle has no catalog entry")
)

func (e *Executor) ReconcileStorage(ctx context.Context) error {
	// Reject unsupported formats before cleanup or reconciliation can write anything.
	if _, err := dataformat.CheckCatalog(e.db); err != nil {
		return err
	}
	if err := dataformat.CheckBundles(e.db, e.paths.Work); err != nil {
		return err
	}

	// Recovery owns incomplete creation cleanup only after format validation succeeds.
	jobsRoot := filepath.Join(e.paths.Work, "jobs")
	if err := os.MkdirAll(jobsRoot, 0o755); err != nil {
		return fmt.Errorf("create Job storage root failed, %w", err)
	}

	// Validate every catalog entry and clean only clearly incomplete creations.
	var catalog []*jobCatalogRow
	if err := e.db.WithContext(ctx).Find(&catalog).Error; err != nil {
		return fmt.Errorf("read job catalog during recovery failed, %w", err)
	}
	known := make(map[int64]struct{}, len(catalog))
	for _, job := range catalog {
		known[job.ID] = struct{}{}
		complete, err := e.validateBundle(job.ID)
		if err != nil {
			return err
		}
		if complete {
			continue
		}
		if err := os.Remove(e.jobWorkPath(job.ID)); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("remove incomplete Job bundle failed, id=%d, %w", job.ID, err)
		}
		if err := e.db.WithContext(ctx).Unscoped().Delete(&jobCatalogRow{}, job.ID).Error; err != nil {
			return fmt.Errorf("remove incomplete Job catalog entry failed, id=%d, %w", job.ID, err)
		}
		delete(known, job.ID)
	}

	// An unreferenced complete bundle may contain user data, so report it instead of deleting it.
	entries, err := os.ReadDir(jobsRoot)
	if err != nil {
		return fmt.Errorf("scan Job storage root failed, %w", err)
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		id, err := strconv.ParseInt(entry.Name(), 10, 64)
		if err != nil {
			continue
		}
		if _, exists := known[id]; exists {
			continue
		}
		dir := filepath.Join(jobsRoot, entry.Name())
		empty, err := bundleDirectoryEmpty(dir)
		if err != nil {
			return err
		}
		if empty {
			if err := os.Remove(dir); err != nil && !errors.Is(err, os.ErrNotExist) {
				return fmt.Errorf("remove incomplete orphan Job directory failed, path=%q, %w", dir, err)
			}
			continue
		}
		return fmt.Errorf("job_id=%d, %w", id, ErrOrphanBundle)
	}

	return nil
}

func (e *Executor) validateBundle(jobID int64) (complete bool, returnErr error) {
	// Only a literally empty creation directory is eligible for automatic cleanup.
	empty, err := bundleDirectoryEmpty(e.jobWorkPath(jobID))
	if err != nil {
		return false, err
	}
	if empty {
		return false, nil
	}

	// Validate bundle identity before reading its kind-specific durable manifest.
	metadataPath := filepath.Join(e.jobWorkPath(jobID), "job.json")
	statePath := e.stateDBPath(jobID)
	for _, filename := range []string{metadataPath, statePath} {
		info, err := os.Lstat(filename)
		if err != nil {
			return false, fmt.Errorf("inspect Job bundle file failed, path=%q, %w", filename, err)
		}
		if !info.Mode().IsRegular() {
			return false, fmt.Errorf("Job bundle file is not regular, path=%q", filename)
		}
	}
	if err := dataformat.CheckBundle(e.jobWorkPath(jobID), jobID); err != nil {
		return false, err
	}

	// This validation owns its state connection and reports cleanup failures with the original error.
	db, err := e.NewStateDB(context.Background(), jobID)
	if err != nil {
		return false, fmt.Errorf("job_id=%d, %w: %v", jobID, ErrBundleMissing, err)
	}
	defer func() { returnErr = errors.Join(returnErr, closeGORMDB(db)) }()
	record := new(JobRecord)
	if err := db.First(record, singletonJobID).Error; err != nil {
		return false, fmt.Errorf("validate Job DB failed, id=%d, %w", jobID, err)
	}

	// Check only the manifest/configuration required by the existing durable checkpoint.
	manifestTable := ""
	switch record.Kind {
	case entity.JobKind_JOB_KIND_ARCHIVE:
		manifestTable = "items"
	case entity.JobKind_JOB_KIND_RESTORE:
		manifestTable = "copies"
	case entity.JobKind_JOB_KIND_SCAN:
		manifestTable = "entries"
	default:
		return false, fmt.Errorf("invalid Job kind in bundle, id=%d kind=%d", jobID, record.Kind)
	}
	if !db.Migrator().HasTable(manifestTable) {
		return false, fmt.Errorf("committed Job bundle is missing manifest table, id=%d table=%q", jobID, manifestTable)
	}
	if record.Kind == entity.JobKind_JOB_KIND_SCAN ||
		manifestPosition(record.Checkpoint) == entity.JobStatus_JOB_STATUS_PREPARING {
		if !db.Migrator().HasTable("config") {
			return false, fmt.Errorf("committed Job bundle is missing config table, id=%d", jobID)
		}
		var count int64
		if err := db.Table("config").Count(&count).Error; err != nil {
			return false, fmt.Errorf("validate Job config failed, id=%d, %w", jobID, err)
		}
		if count != 1 {
			return false, fmt.Errorf("committed Job bundle has invalid config count, id=%d count=%d", jobID, count)
		}
		return true, nil
	}
	var count int64
	if err := db.Table(manifestTable).Count(&count).Error; err != nil {
		return false, fmt.Errorf("validate Job manifest failed, id=%d, %w", jobID, err)
	}
	if count == 0 && record.Status != entity.JobStatus_JOB_STATUS_COMPLETED {
		return false, fmt.Errorf("committed Job bundle has empty unfinished manifest, id=%d", jobID)
	}
	return true, nil
}

func bundleDirectoryEmpty(dir string) (empty bool, returnErr error) {
	// Inspect the path itself so a symlink cannot authorize removal of another directory.
	info, err := os.Lstat(dir)
	if errors.Is(err, os.ErrNotExist) {
		return false, fmt.Errorf("path=%q, %w", dir, ErrBundleMissing)
	}
	if err != nil {
		return false, fmt.Errorf("inspect Job bundle directory failed, path=%q, %w", dir, err)
	}
	if !info.IsDir() {
		return false, fmt.Errorf("Job bundle path is not a directory, path=%q", dir)
	}

	// One entry is enough to preserve a nonempty bundle; release the directory on every return.
	handle, err := os.Open(dir)
	if err != nil {
		return false, fmt.Errorf("open Job bundle directory failed, path=%q, %w", dir, err)
	}
	defer func() {
		if err := handle.Close(); err != nil {
			returnErr = errors.Join(returnErr, fmt.Errorf("close Job bundle directory failed, path=%q, %w", dir, err))
		}
	}()
	_, err = handle.Readdirnames(1)
	if errors.Is(err, io.EOF) {
		return true, nil
	}
	if err != nil {
		return false, fmt.Errorf("read Job bundle directory failed, path=%q, %w", dir, err)
	}
	return false, nil
}
