package library

import (
	"context"
	"fmt"
	"strings"

	"github.com/samuelncui/yatm/entity"
	"gorm.io/gorm"
)

// FileOperation describes a completed physical mutation whose original references need updating.
type FileOperation struct {
	LocationID int64
	Kind       entity.FileOperationKind
	SourcePath string
	TargetPath string
}

// PublishFileOperation changes original references without changing logical organization.
// The caller holds the Location gate and has completed and checked the physical mutation.
func (l *Library) PublishFileOperation(ctx context.Context, result *FileOperation) error {
	if result == nil || result.LocationID <= 0 {
		return fmt.Errorf("file operation publication is incomplete")
	}
	switch result.Kind {
	case entity.FileOperationKind_FILE_OPERATION_KIND_MOVE:
		if result.SourcePath == "" || result.TargetPath == "" || result.SourcePath == result.TargetPath {
			return fmt.Errorf("file operation source and target must be distinct non-root paths")
		}
	case entity.FileOperationKind_FILE_OPERATION_KIND_REMOVE:
		if result.SourcePath == "" {
			return fmt.Errorf("removal requires a non-root source")
		}
		if result.TargetPath != "" && !strings.HasPrefix(result.TargetPath, ".trash/") {
			return fmt.Errorf("removal output must be inside Trash")
		}
	case entity.FileOperationKind_FILE_OPERATION_KIND_MKDIR:
		if result.SourcePath != "" || result.TargetPath == "" {
			return fmt.Errorf("mkdir requires a non-root target only")
		}
	default:
		return fmt.Errorf("unsupported file operation publication")
	}
	if result.SourcePath != "" {
		if err := entity.ValidateRelativePath(result.SourcePath); err != nil {
			return err
		}
	}
	if result.TargetPath != "" {
		if err := entity.ValidateRelativePath(result.TargetPath); err != nil {
			return err
		}
	}

	// Reference edits and the Location revision publish in one metadata-only transaction.
	return l.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var location Location
		if err := tx.First(&location, result.LocationID).Error; err != nil {
			return err
		}
		// Only move and delete alter existing originals. A copied object never inherits organization.
		switch result.Kind {
		case entity.FileOperationKind_FILE_OPERATION_KIND_MOVE:
			// Only the primitive's successfully moved source is remapped. In particular,
			// a directory merge never discards pre-existing target associations.
			if err := moveOperationOriginals(tx, result); err != nil {
				return err
			}
		case entity.FileOperationKind_FILE_OPERATION_KIND_REMOVE:
			if err := removeOperationOriginals(tx, result); err != nil {
				return err
			}
		case entity.FileOperationKind_FILE_OPERATION_KIND_MKDIR:
		default:
			return fmt.Errorf("unsupported file operation publication")
		}
		return tx.Save(&location).Error
	})
}

func removeOperationOriginals(tx *gorm.DB, result *FileOperation) error {
	// Whole-directory recycling relinquishes every known descendant, retaining logical identities and history.
	var after int64
	for {
		var originals []*FileLocation
		if err := tx.Where("location_id = ? AND file_id > ?", result.LocationID, after).
			Where("path = ? OR substr(path, 1, length(?)) = ?", result.SourcePath, result.SourcePath+"/", result.SourcePath+"/").
			Order("file_id").Limit(batchSize).Find(&originals).Error; err != nil {
			return err
		}
		if len(originals) == 0 {
			return nil
		}
		ids := make([]int64, 0, len(originals))
		for _, original := range originals {
			ids = append(ids, original.FileID)
			after = original.FileID
		}
		if err := retireOriginals(tx, ids); err != nil {
			return err
		}
	}
}

func moveOperationOriginals(tx *gorm.DB, result *FileOperation) error {
	// Page by File identity while paths change; SQL LIKE escaping cannot broaden the selected subtree.
	var after int64
	for {
		var originals []*FileLocation
		if err := tx.Where("location_id = ? AND file_id > ?", result.LocationID, after).
			Where("path = ? OR substr(path, 1, length(?)) = ?", result.SourcePath, result.SourcePath+"/", result.SourcePath+"/").
			Order("file_id").Limit(batchSize).Find(&originals).Error; err != nil {
			return err
		}
		if len(originals) == 0 {
			return nil
		}
		for _, original := range originals {
			after = original.FileID
			original.Path = result.TargetPath + strings.TrimPrefix(original.Path, result.SourcePath)
			// Retire only the observed destination slot actually replaced by this moved
			// entry; neighboring entries in a merged directory keep their associations.
			if err := tx.Where("location_id = ? AND path = ? AND file_id <> ?", result.LocationID, original.Path, original.FileID).Delete(&FileLocation{}).Error; err != nil {
				return err
			}
			if err := tx.Save(original).Error; err != nil {
				return fmt.Errorf("move original reference failed, %w", err)
			}
		}
	}
}

// retireOriginals relinquishes both executable associations and their continuity evidence.
// Scan absence intentionally retains tracking and therefore does not use this primitive.
func retireOriginals(tx *gorm.DB, ids []int64) error {
	for start := 0; start < len(ids); start += batchSize {
		batch := ids[start:min(start+batchSize, len(ids))]
		for _, model := range []any{&FileTrackingKey{}, &FileLocation{}} {
			if err := tx.Where("file_id IN ?", batch).Delete(model).Error; err != nil {
				return err
			}
		}
	}
	return nil
}
