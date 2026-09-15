package dataformat

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"gorm.io/gorm"
)

const (
	BundleFormat   = "yatm-job-bundle"
	BundleRevision = 1
)

var ErrBundleMissing = errors.New("job bundle is missing")

type Bundle struct {
	Format        string    `json:"format"`
	FormatVersion int       `json:"format_version"`
	ID            int64     `json:"id"`
	CreatedAt     time.Time `json:"created_at"`
}

func NewBundle(id int64, createdAt time.Time) Bundle {
	return Bundle{Format: BundleFormat, FormatVersion: BundleRevision, ID: id, CreatedAt: createdAt}
}

// CheckBundle validates the published family before any Job database writes.
func CheckBundle(dir string, id int64) error {
	// Metadata must be a real file, not an indirect reference to another bundle.
	filename := filepath.Join(dir, "job.json")
	info, err := os.Lstat(filename)
	if err != nil {
		return fmt.Errorf("inspect Job bundle metadata failed, id=%d, %w", id, err)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("Job bundle metadata is not a regular file, id=%d", id)
	}
	data, err := os.ReadFile(filename)
	if err != nil {
		return fmt.Errorf("read Job bundle metadata failed, id=%d, %w", id, err)
	}

	// The revision alone cannot identify an unpublished Draft artifact.
	var metadata Bundle
	if err := json.Unmarshal(data, &metadata); err != nil {
		return fmt.Errorf("decode Job bundle metadata failed, id=%d, %w", id, err)
	}
	if metadata.Format != BundleFormat || metadata.FormatVersion != BundleRevision || metadata.ID != id {
		return fmt.Errorf("unsupported Job bundle format, id=%d format=%q revision=%d", id, metadata.Format, metadata.FormatVersion)
	}
	return nil
}

// CheckBundles checks catalog-owned bundle formats without changing incomplete creations.
func CheckBundles(db *gorm.DB, workRoot string) error {
	if !db.Migrator().HasTable("jobs") {
		return nil
	}
	var after int64
	for {
		// Bound startup/preflight reads without loading any execution manifests.
		var ids []int64
		if err := db.Table("jobs").Where("id > ? AND deleted_at = 0", after).Order("id").Limit(100).Pluck("id", &ids).Error; err != nil {
			return fmt.Errorf("list Job bundle identities failed, %w", err)
		}
		if len(ids) == 0 {
			return nil
		}
		for _, id := range ids {
			dir := filepath.Join(workRoot, "jobs", strconv.FormatInt(id, 10))
			info, err := os.Lstat(dir)
			if err != nil {
				if errors.Is(err, os.ErrNotExist) {
					return fmt.Errorf("job_id=%d, %w: %w", id, ErrBundleMissing, err)
				}
				return fmt.Errorf("inspect Job bundle directory failed, id=%d, %w", id, err)
			}
			if !info.IsDir() {
				return fmt.Errorf("Job bundle path is not a directory, id=%d", id)
			}
			entries, err := os.ReadDir(dir)
			if err != nil {
				return fmt.Errorf("inspect Job bundle directory failed, id=%d, %w", id, err)
			}
			// Existing recovery owns literally empty interrupted creations.
			if len(entries) != 0 {
				if err := CheckBundle(dir, id); err != nil {
					return err
				}
			}
			after = id
		}
	}
}
