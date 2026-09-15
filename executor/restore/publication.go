package restore

import (
	"context"
	"fmt"

	"github.com/samuelncui/yatm/entity"
	"gorm.io/gorm/clause"
)

func (a *jobRestoreRunner) beginOutput(ctx context.Context, copy *Copy) error {
	// Record the source before ACP writes bytes, including attempts whose completion checkpoint fails.
	value := &Output{ItemID: copy.ItemID, Path: copy.TargetPath, ReadMediaID: copy.MediaID}
	return a.db.WithContext(ctx).Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "item_id"}},
		DoUpdates: clause.AssignmentColumns([]string{"read_media_id"}),
	}).Create(value).Error
}

// stageOutput records complete bytes, not a verified Media operation or an original association.
func (a *jobRestoreRunner) stageOutput(ctx context.Context, copy *Copy, hash []byte, size int64, damaged bool) error {
	value := &Output{ItemID: copy.ItemID, Path: copy.TargetPath, Ready: true, ReadMediaID: copy.MediaID,
		ActualHash: hash, ActualSize: size, Damaged: damaged}
	return a.db.WithContext(ctx).Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "item_id"}},
		DoUpdates: clause.AssignmentColumns([]string{"ready", "read_media_id", "actual_hash", "actual_size", "damaged"}),
	}).Create(value).Error
}

func (a *jobRestoreRunner) finalizeOutputs(ctx context.Context, mediaID int64) error {
	// Only a successful final identity check authorizes these complete outputs for Library publication.
	if err := a.db.WithContext(ctx).Model(&Output{}).Where("ready = ? AND read_media_id = ?", true, mediaID).
		Update("finalized", true).Error; err != nil {
		return err
	}
	return a.publishReadyOutputs(ctx, false)
}

func (a *jobRestoreRunner) publishReadyOutputs(ctx context.Context, recheck bool) error {
	var after int64
	for {
		items := a.db.Model(&Copy{}).Select("item_id").Where("status = ?", entity.CopyStatus_PENDING).Group("item_id")
		var outputs []Output
		if err := a.db.WithContext(ctx).Where("ready = ? AND finalized = ? AND completed = ? AND item_id > ? AND item_id IN (?)", true, true, false, after, items).
			Order("item_id").Limit(batchSize).Find(&outputs).Error; err != nil {
			return err
		}
		if len(outputs) == 0 {
			return nil
		}
		for _, output := range outputs {
			var copy Copy
			if err := a.db.WithContext(ctx).Where("item_id = ? AND media_id = ?", output.ItemID, output.ReadMediaID).First(&copy).Error; err != nil {
				return err
			}
			if recheck {
				matched, _, err := a.outputMatches(ctx, output.Path, output.ActualHash, output.ActualSize)
				if err != nil {
					return err
				}
				if !matched {
					return fmt.Errorf("pending Restore output changed: %q", output.Path)
				}
			}
			if err := a.completeOutput(ctx, &copy, output.ActualHash, output.ActualSize, output.Damaged); err != nil {
				return err
			}
			after = output.ItemID
		}
	}
}
