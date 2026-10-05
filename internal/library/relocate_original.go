package library

import (
	"context"
	"fmt"

	"github.com/samuelncui/yatm/entity"
	"gorm.io/gorm"
)

// RelocateOriginal is an explicit association change, never a filesystem move.
func (l *Library) RelocateOriginal(ctx context.Context, fileID int64, locationID int64, observation *ObservedEntry) error {
	if observation == nil {
		return fmt.Errorf("original observation is missing")
	}
	// Keep the explicit association change and occupied-path check in one metadata mutation.
	return l.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var file fileRow
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
		var location Location
		if err := tx.First(&location, locationID).Error; err != nil {
			return err
		}
		var occupied FileLocation
		if err := tx.Where("location_id = ? AND path = ?", locationID, observation.Path).Limit(1).Find(&occupied).Error; err != nil {
			return err
		}
		if occupied.FileID != 0 && occupied.FileID != fileID {
			return fmt.Errorf("selected path belongs to another File: %w", ErrLocationConflict)
		}

		// Preserve organization/history, replacing only the executable original and tracking evidence.
		if err := retireOriginals(tx, []int64{fileID}); err != nil {
			return err
		}
		observation.FileID = fileID
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
