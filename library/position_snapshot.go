package library

import (
	"context"
	"fmt"

	"gorm.io/gorm"
)

// SnapshotMediaPositions reads a consistent, bounded regular-file inventory without physical I/O.
func (l *Library) SnapshotMediaPositions(ctx context.Context, mediaID int64, yield func(*Position) error) error {
	if yield == nil {
		return fmt.Errorf("Media inventory consumer is missing")
	}
	return l.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// Keep the snapshot open while only bounded metadata pages cross the Job boundary.
		var after int64
		for {
			var positions []*Position
			if err := tx.Where("media_id = ? AND is_dir = ? AND id > ?", mediaID, false, after).
				Order("id").Limit(batchSize).Find(&positions).Error; err != nil {
				return fmt.Errorf("read Media inventory snapshot failed, after=%d, %w", after, err)
			}
			if len(positions) == 0 {
				return nil
			}
			for _, position := range positions {
				if err := yield(position); err != nil {
					return err
				}
				after = position.ID
			}
		}
	})
}
