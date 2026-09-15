package library

import (
	"context"
	"fmt"

	"github.com/samuelncui/yatm/entity"
	"gorm.io/gorm"
)

// CopyAdmission identifies YATM-created objects independently of their current names.
// Imported receipts retain history but cannot match a current confirmed binding.
func (l *Library) CopyAdmission(ctx context.Context, locationID int64, token, identity string) (*FileOperationResult, error) {
	if token == "" || identity == "" {
		return nil, nil
	}
	var result FileOperationResult
	err := l.db.WithContext(ctx).Where("location_id = ? AND binding_token = ? AND kind = ? AND output_identity = ?",
		locationID, token, entity.FileOperationKind_COPY, identity).
		Order("completed_at DESC, operation_id, item_id").Limit(1).Find(&result).Error
	if err != nil || result.OperationID == "" {
		return nil, err
	}
	return &result, nil
}

func validateCopyAdmission(tx *gorm.DB, location *Location, p *OnlinePosition) error {
	if p.CopyResult == nil {
		if p.Independent {
			return fmt.Errorf("independent copy admission requires its operation result")
		}
		return nil
	}
	stored, err := checkedCopyResult(tx, location, p.CopyResult)
	if err != nil {
		return err
	}
	if p.Independent && p.FileID != 0 {
		return ErrOnlineConflict
	}
	if !p.Independent && p.FileID <= 0 {
		return ErrOnlineConflict
	}
	if !p.Independent && stored.AdmittedFileID == nil {
		var atPath FileLocation
		if err := tx.Where("file_id = ? AND location_id = ? AND path = ?", p.FileID, location.ID, p.Path).Limit(1).Find(&atPath).Error; err != nil {
			return err
		}
		if atPath.FileID == 0 {
			return ErrOnlineConflict
		}
	}
	return nil
}

func checkedCopyResult(tx *gorm.DB, location *Location, expected *FileOperationResult) (*FileOperationResult, error) {
	var stored FileOperationResult
	if err := tx.Where("operation_id = ? AND item_id = ?", expected.OperationID, expected.ItemID).First(&stored).Error; err != nil {
		return nil, err
	}
	if stored.Kind != entity.FileOperationKind_COPY || stored.LocationID != location.ID || stored.BindingToken != location.BindingToken ||
		stored.OutputIdentity == "" || stored.OutputIdentity != expected.OutputIdentity {
		return nil, ErrOnlineConflict
	}
	return &stored, nil
}

func consumeCopyAdmission(tx *gorm.DB, p *OnlinePosition) error {
	if p.CopyResult == nil {
		return nil
	}
	// Existing regular owners survive hardlink admission; deleted owners cannot break future continuity.
	owners := tx.Model(&File{}).Select("id").Where("kind = ?", entity.FileKind_FILE_KIND_REGULAR)
	return tx.Model(&FileOperationResult{}).
		Where("operation_id = ? AND item_id = ?", p.CopyResult.OperationID, p.CopyResult.ItemID).
		Where("admitted_file_id IS NULL OR admitted_file_id NOT IN (?)", owners).
		Update("admitted_file_id", p.FileID).Error
}
