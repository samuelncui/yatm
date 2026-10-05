package library

import (
	"context"
	"fmt"
	"io/fs"
	"math"
	"strings"

	"github.com/samuelncui/yatm/entity"
	"gorm.io/gorm"
)

func invalidateLocationImport(tx *gorm.DB) error {
	for _, model := range []any{&FileTrackingKey{}, &FileLocation{}} {
		if err := clearImportedModel(tx, model, "online associations"); err != nil {
			return err
		}
	}
	var after int64
	for {
		var sources []*Location
		if err := tx.Where("id > ?", after).Order("id").Limit(batchSize).Find(&sources).Error; err != nil {
			return err
		}
		if len(sources) == 0 {
			return nil
		}
		for _, source := range sources {
			source.LastJobID, source.LastSyncJobID = 0, 0
			if err := tx.Save(source).Error; err != nil {
				return err
			}
		}
		after = sources[len(sources)-1].ID
	}
}

// validateCatalogImport checks references after all record groups, including arbitrarily ordered input.
func validateCatalogImport(ctx context.Context, tx *gorm.DB) error {
	// Validate references after arbitrarily ordered records have all been inserted.
	checks := []struct {
		model     any
		joins     string
		condition string
		label     string
		args      []any
	}{
		{&FileVersion{}, "LEFT JOIN files ON files.id = file_versions.file_id", "files.id IS NULL OR files.kind != ?", "FileVersion owner", []any{entity.FileKind_FILE_KIND_REGULAR}},
		{&FileVersionArchive{}, "LEFT JOIN file_versions ON file_versions.id = file_version_archives.version_id", "file_versions.id IS NULL", "archive observation version", nil},
		{&FileLocation{}, "LEFT JOIN files ON files.id = file_locations.file_id LEFT JOIN locations ON locations.id = file_locations.location_id", "files.id IS NULL OR files.kind != ? OR locations.id IS NULL", "original reference", []any{entity.FileKind_FILE_KIND_REGULAR}},
		{&FileTrackingKey{}, "LEFT JOIN files ON files.id = file_tracking_keys.file_id LEFT JOIN locations ON locations.id = file_tracking_keys.location_id", "files.id IS NULL OR files.kind != ? OR locations.id IS NULL", "tracking reference", []any{entity.FileKind_FILE_KIND_REGULAR}},
		{&Position{}, "LEFT JOIN media ON media.id = positions.media_id", "media.id IS NULL", "Position Media reference", nil},
	}
	for _, check := range checks {
		var count int64
		if err := tx.WithContext(ctx).Model(check.model).Joins(check.joins).Where(check.condition, check.args...).Count(&count).Error; err != nil {
			return err
		}
		if count != 0 {
			return fmt.Errorf("invalid imported %s (%d rows)", check.label, count)
		}
	}

	// Original facts and physical ancestor relationships must remain internally consistent.
	var after int64
	for {
		var originals []*FileLocation
		if err := tx.WithContext(ctx).Where("file_id > ?", after).Order("file_id").Limit(batchSize).Find(&originals).Error; err != nil {
			return err
		}
		if len(originals) == 0 {
			break
		}
		for _, original := range originals {
			if original.Size < 0 || !fs.FileMode(original.Mode).IsRegular() || (len(original.Hash) != 0 && len(original.Hash) != 32) {
				return fmt.Errorf("invalid original facts for File %d", original.FileID)
			}
			// A file cannot also be a physical ancestor, even in a malformed imported index.
			parts := strings.Split(original.Path, "/")
			if len(parts) > 256 {
				return fmt.Errorf("original path is too deep")
			}
			for depth := 1; depth < len(parts); depth++ {
				var count int64
				if err := tx.Model(&FileLocation{}).Where("location_id = ? AND path = ?", original.LocationID, strings.Join(parts[:depth], "/")).Count(&count).Error; err != nil {
					return err
				}
				if count != 0 {
					return fmt.Errorf("original parent is another file")
				}
			}
		}
		after = originals[len(originals)-1].FileID
	}

	// Logical tree validity is separate from physical original-path validity.
	return validateImportedTree(ctx, tx)
}

func validateImportedTree(ctx context.Context, tx *gorm.DB) error {
	after := int64(math.MinInt64)
	for {
		var rows []*fileRow
		if err := tx.WithContext(ctx).Where("id > ?", after).Order("id").Limit(batchSize).Find(&rows).Error; err != nil {
			return err
		}
		if len(rows) == 0 {
			return nil
		}
		files := fileViews(rows)
		for _, file := range files {
			if err := entity.ValidateRelativePath(file.Name); err != nil {
				return err
			}
			if strings.Contains(file.Name, "/") {
				return fmt.Errorf("File name contains a path separator")
			}
			parentID := file.ParentID
			for depth := 0; parentID != 0; depth++ {
				if parentID == file.ID || depth >= 256 {
					return fmt.Errorf("imported File tree contains a cycle or excessive depth")
				}
				var parent fileRow
				if err := tx.First(&parent, parentID).Error; err != nil {
					return err
				}
				if parent.Kind != entity.FileKind_FILE_KIND_DIRECTORY {
					return fmt.Errorf("File parent is not a directory")
				}
				parentID = parent.ParentID
			}
		}
		after = files[len(files)-1].ID
	}
}
