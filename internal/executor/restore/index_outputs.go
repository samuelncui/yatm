package restore

import (
	"context"
	"errors"

	"github.com/samuelncui/yatm/internal/executor"
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

func (a *jobRestoreRunner) prepareOutputs(ctx context.Context) error {
	a.tracker = executor.NewTracker()
	// Assign each unclaimed version a logical output path; the filesystem decides other collisions
	// when the output is created. A claimed path belongs to its item and is never chosen again.
	var after int64
	for {
		var files []File
		if err := a.db.WithContext(ctx).Where("item_id > ? AND completed = ? AND read_media_id = 0", after, false).
			Order("item_id").Limit(batchSize).Find(&files).Error; err != nil {
			return err
		}
		if len(files) == 0 {
			return nil
		}
		for _, file := range files {
			after = file.ItemID
			target, err := a.chooseOutput(ctx, file.ItemID, file.DesiredPath, nil)
			if err != nil {
				return err
			}
			if target != file.Path {
				if err := a.db.WithContext(ctx).Model(&File{}).Where("item_id = ?", file.ItemID).Update("path", target).Error; err != nil {
					return err
				}
			}

			// Adopt equal existing content only after reading it; other content is left in place, and
			// an item-local inspection failure is reported by the Media attempt.
			matched, _, err := a.outputMatches(ctx, target, file.Hash, file.Size)
			var local *restoreItemError
			if errors.As(err, &local) || (err == nil && !matched) {
				continue
			}
			if err != nil {
				return err
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
