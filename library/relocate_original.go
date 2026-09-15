package library

import (
	"context"
	"fmt"

	"github.com/samuelncui/yatm/entity"
	"google.golang.org/protobuf/proto"
	"gorm.io/gorm"
)

// RelocateOriginal is an explicit association change, never a filesystem move.
func (l *Library) RelocateOriginal(ctx context.Context, fileID int64, expected *entity.FileLocation, locationID int64, binding string, observation *OnlinePosition) error {
	if observation == nil {
		return fmt.Errorf("original observation is missing")
	}
	// Optimistic original comparison prevents concurrent relinks from replacing one another.
	return l.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var file File
		if err := tx.First(&file, fileID).Error; err != nil {
			return err
		}
		if file.Kind != entity.FileKind_FILE_KIND_REGULAR {
			return fmt.Errorf("only ordinary Files have originals")
		}
		var old FileLocation
		if err := tx.Where("file_id = ?", fileID).Limit(1).Find(&old).Error; err != nil {
			return err
		}
		if old.FileID == 0 && expected != nil {
			return ErrOnlineConflict
		}
		if old.FileID != 0 && !proto.Equal(old.ToEntity(), expected) {
			return ErrOnlineConflict
		}
		var location Location
		if err := tx.First(&location, locationID).Error; err != nil {
			return err
		}
		if location.BindingToken != binding || location.Binding != entity.OnlineBinding_CONFIRMED {
			return ErrOnlineConflict
		}
		var occupied FileLocation
		if err := tx.Where("location_id = ? AND path = ?", locationID, observation.Path).Limit(1).Find(&occupied).Error; err != nil {
			return err
		}
		if occupied.FileID != 0 && occupied.FileID != fileID {
			return fmt.Errorf("selected path belongs to another File: %w", ErrOnlineConflict)
		}

		// Explicit user association supersedes inherited copy evidence without stealing any occupied path.
		if err := tx.Model(&FileOperationResult{}).Where("admitted_file_id = ?", fileID).Update("admitted_file_id", nil).Error; err != nil {
			return err
		}
		if observation.CopyResult != nil {
			copy, err := checkedCopyResult(tx, &location, observation.CopyResult)
			if err != nil {
				return err
			}
			if err := tx.Model(copy).Update("admitted_file_id", fileID).Error; err != nil {
				return err
			}
		}

		// Preserve organization/history, replacing only the executable original and tracking evidence.
		if err := tx.Where("file_id = ?", fileID).Delete(&FileLocation{}).Error; err != nil {
			return err
		}
		if err := tx.Where("file_id = ?", fileID).Delete(&FileTrackingKey{}).Error; err != nil {
			return err
		}
		observation.FileID, observation.Independent = fileID, false
		if _, err := admitObservation(tx, &location, observation); err != nil {
			return err
		}
		if old.FileID != 0 && old.LocationID != location.ID {
			var previousLocation Location
			if err := tx.First(&previousLocation, old.LocationID).Error; err != nil {
				return err
			}
			if err := tx.Save(&previousLocation).Error; err != nil {
				return err
			}
		}
		return tx.Save(&location).Error
	})
}
