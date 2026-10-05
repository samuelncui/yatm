package restore

import (
	"context"
	"github.com/samuelncui/yatm/internal/library"
	"gorm.io/gorm"
)

func freezeFileSelections(tx *gorm.DB) error {
	// Choose one reconnecting version per logical File before any output is published.
	var after int64
	for {
		var ids []int64
		if err := tx.Model(&File{}).Where("file_id > ?", after).Distinct("file_id").Order("file_id").Limit(batchSize).Pluck("file_id", &ids).Error; err != nil {
			return err
		}
		if len(ids) == 0 {
			return nil
		}
		for _, id := range ids {
			var latest File
			if err := tx.Where("file_id = ?", id).Order("last_archived_at_ns DESC, file_version_id DESC").First(&latest).Error; err != nil {
				return err
			}
			if err := tx.Model(&File{}).Where("item_id = ?", latest.ItemID).Update("reconnect", true).Error; err != nil {
				return err
			}
		}
		after = ids[len(ids)-1]
	}
}

func (a *jobRestoreRunner) prepareOutputs(ctx context.Context, names *outputNames) error {
	// Freeze one collision-safe output path for each selected version.
	var after int64
	for {
		var files []File
		if err := a.db.WithContext(ctx).Where("item_id > ?", after).Order("item_id").Limit(batchSize).Find(&files).Error; err != nil {
			return err
		}
		if len(files) == 0 {
			return nil
		}
		for _, file := range files {
			after = file.ItemID
			if file.Completed {
				continue
			}
			version := &library.FileVersion{ID: file.ItemID, FileID: file.FileID, Hash: file.Hash, Size: file.Size}
			target, satisfied, err := a.reserveOutput(ctx, names, version, file.DesiredPath)
			if err != nil {
				return err
			}
			if !satisfied {
				continue
			}
			var copy Copy
			if err := a.db.WithContext(ctx).Where("item_id = ?", file.ItemID).First(&copy).Error; err != nil {
				return err
			}
			file.Path = target
			candidate := newCopyCandidate(&copy, &file)
			if err := a.completeOutput(ctx, candidate, candidate.Hash, candidate.Size, false); err != nil {
				return err
			}
		}
	}
}
