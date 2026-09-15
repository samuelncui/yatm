package restore

import (
	"context"

	"github.com/samuelncui/yatm/library"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func freezeFileSelections(tx *gorm.DB) error {
	// Complete this metadata decision for every selected File before any output can be adopted.
	var after int64
	for {
		var ids []int64
		if err := tx.Model(&Copy{}).Where("file_id > ?", after).Distinct("file_id").Order("file_id").Limit(batchSize).Pluck("file_id", &ids).Error; err != nil {
			return err
		}
		if len(ids) == 0 {
			return nil
		}
		for _, id := range ids {
			var latest Copy
			if err := tx.Where("file_id = ?", id).Order("last_archived_at DESC, file_version_id DESC").First(&latest).Error; err != nil {
				return err
			}
			if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&FileSelection{FileID: id, ReconnectVersionID: latest.FileVersionID,
				ParentID: latest.LibraryParentID, Name: latest.LibraryName}).Error; err != nil {
				return err
			}
		}
		after = ids[len(ids)-1]
	}
}

func (a *jobRestoreRunner) prepareOutputs(ctx context.Context, names *outputNames) error {
	// Choose the latest selected version per File independently of request and Media read order.
	var after int64
	for {
		ids := a.db.Model(&Copy{}).Select("MIN(id)").Where("item_id > ?", after).
			Group("item_id").Order("item_id").Limit(batchSize)
		var copies []Copy
		if err := a.db.WithContext(ctx).Where("id IN (?)", ids).Order("item_id").Find(&copies).Error; err != nil {
			return err
		}
		if len(copies) == 0 {
			return nil
		}
		for _, copy := range copies {
			version := &library.FileVersion{ID: copy.FileVersionID, FileID: copy.FileID, Hash: copy.Hash, Size: copy.Size}
			target, satisfied, err := a.reserveOutput(ctx, names, version, copy.TargetPath)
			if err != nil {
				return err
			}

			copy.TargetPath = target
			if err := a.db.WithContext(ctx).Model(&Copy{}).Where("item_id = ?", copy.ItemID).Update("target_path", target).Error; err != nil {
				return err
			}

			// Adopted content and committed results share the Library-first completion boundary.
			result, err := a.restoreResult(ctx, &copy)
			if err != nil {
				return err
			}
			if result != nil {
				if err := a.checkpointResult(ctx, &copy, result); err != nil {
					return err
				}
			} else if satisfied {
				if err := a.completeOutput(ctx, &copy, copy.Hash, copy.Size, false); err != nil {
					return err
				}
			}
			after = copy.ItemID
		}
	}
}
