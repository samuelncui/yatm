package library

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/samuelncui/yatm/entity"
	"gorm.io/gorm"
)

// FileOperationResult records completed physical work and its metadata publication.
// It is business provenance, not a filesystem transaction or physical deletion cascade.
type FileOperationResult struct {
	OperationID    string                   `gorm:"primaryKey;type:varchar(36)" json:"operation_id"`
	ItemID         int64                    `gorm:"primaryKey;autoIncrement:false" json:"item_id"`
	LocationID     int64                    `gorm:"index;index:idx_file_operation_output,priority:1" json:"location_id"`
	BindingToken   string                   `gorm:"index:idx_file_operation_output,priority:2" json:"binding_token"`
	Kind           entity.FileOperationKind `gorm:"index:idx_file_operation_output,priority:3" json:"kind"`
	OutputIdentity string                   `gorm:"index:idx_file_operation_output,priority:4" json:"output_identity"`
	AdmittedFileID *int64                   `gorm:"index" json:"admitted_file_id,omitempty"`
	SourcePath     string                   `json:"source_path"`
	TargetPath     string                   `json:"target_path"`
	CompletedAt    int64                    `gorm:"autoCreateTime:milli" json:"completed_at_ms"`
}

// PublishFileOperation changes original references without changing logical organization.
// The caller holds the Location gate and has completed and checked the physical mutation.
func (l *Library) PublishFileOperation(ctx context.Context, result *FileOperationResult) error {
	// A binding generation and stable operation/item key prevent numeric-ID and retry confusion.
	if result == nil || result.ItemID <= 0 || result.LocationID <= 0 || result.BindingToken == "" {
		return fmt.Errorf("file operation publication is incomplete")
	}
	if _, err := uuid.Parse(result.OperationID); err != nil {
		return fmt.Errorf("invalid file operation identity, %w", err)
	}
	switch result.Kind {
	case entity.FileOperationKind_MOVE, entity.FileOperationKind_COPY:
		if result.SourcePath == "" || result.TargetPath == "" || result.SourcePath == result.TargetPath {
			return fmt.Errorf("file operation source and target must be distinct non-root paths")
		}
	case entity.FileOperationKind_DELETE:
		if result.SourcePath == "" || result.TargetPath != "" {
			return fmt.Errorf("deletion requires a non-root source only")
		}
	case entity.FileOperationKind_MAKE_DIRECTORY:
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

	// Reference edits and their idempotency receipt share one metadata-only transaction.
	return l.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var stored FileOperationResult
		err := tx.Where("operation_id = ? AND item_id = ?", result.OperationID, result.ItemID).First(&stored).Error
		if err == nil {
			if stored.LocationID != result.LocationID || stored.BindingToken != result.BindingToken || stored.Kind != result.Kind || stored.SourcePath != result.SourcePath || stored.TargetPath != result.TargetPath || stored.OutputIdentity != result.OutputIdentity {
				return fmt.Errorf("file operation result identity changed")
			}
			return nil
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		var location Location
		if err := tx.First(&location, result.LocationID).Error; err != nil {
			return err
		}
		if location.Binding != entity.OnlineBinding_CONFIRMED || location.BindingToken != result.BindingToken {
			return ErrOnlineConflict
		}

		// Only move and delete alter existing originals. A copied object never inherits organization.
		switch result.Kind {
		case entity.FileOperationKind_MOVE:
			// Only the primitive's successfully moved source is remapped. In particular,
			// a directory merge never discards pre-existing target associations.
			if err := moveOperationOriginals(tx, result); err != nil {
				return err
			}
		case entity.FileOperationKind_DELETE:
			var original FileLocation
			if err := tx.Where("location_id = ? AND path = ?", result.LocationID, result.SourcePath).Limit(1).Find(&original).Error; err != nil {
				return err
			}
			if original.FileID != 0 {
				// Explicit deletion relinquishes copy-continuity claims; provenance and other hardlinks remain.
				if err := tx.Model(&FileOperationResult{}).Where("admitted_file_id = ?", original.FileID).
					Update("admitted_file_id", nil).Error; err != nil {
					return err
				}
				if err := tx.Where("file_id = ?", original.FileID).Delete(&FileTrackingKey{}).Error; err != nil {
					return err
				}
				if err := tx.Delete(&original).Error; err != nil {
					return err
				}
			}
		case entity.FileOperationKind_COPY:
			// Settle only this actually created item; failed/unseen descendants retain their old observations.
			if err := tx.Where("location_id = ? AND path = ?", result.LocationID, result.TargetPath).Delete(&FileLocation{}).Error; err != nil {
				return err
			}
		case entity.FileOperationKind_MAKE_DIRECTORY:
		default:
			return fmt.Errorf("unsupported file operation publication")
		}
		if err := tx.Save(&location).Error; err != nil {
			return err
		}
		return tx.Create(result).Error
	})
}

func moveOperationOriginals(tx *gorm.DB, result *FileOperationResult) error {
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
