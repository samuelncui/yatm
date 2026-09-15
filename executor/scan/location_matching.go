package scan

import (
	"context"

	"github.com/samuelncui/yatm/executor/observation"
	"github.com/samuelncui/yatm/library"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func (r *locationStage) snapshotTracking(ctx context.Context, source *library.Location) error {
	var after int64
	for {
		keys, err := r.exe.Lib().TrackingCandidatesPage(ctx, source, after, batchSize)
		if err != nil {
			return err
		}
		if len(keys) == 0 {
			return nil
		}
		if err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			for _, key := range keys {
				// A partial observation cannot steal a still-bound file outside its successful ranges.
				bound, err := r.exe.Lib().GetFileLocation(ctx, key.FileID)
				if err != nil {
					return err
				}
				if bound != nil {
					if bound.LocationID != source.ID {
						continue
					}
					covered, _, err := r.covered(ctx, tx, source, bound.Path)
					if err != nil {
						return err
					}
					if !covered {
						continue
					}
				}
				if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&Original{FileID: key.FileID}).Error; err != nil {
					return err
				}
				var values map[string]any
				switch key.Kind {
				case library.TrackingNative:
					values = map[string]any{"native_scope": key.Scope, "native_key": key.KeyValue, "birth_ns": key.Details.BirthNS, "generation": key.Details.Generation}
				case library.TrackingUUID:
					values = map[string]any{"uuid_scope": key.Scope, "uuid": key.KeyValue}
				default:
					continue
				}
				if err := tx.Model(&Original{}).Where("file_id = ?", key.FileID).Updates(values).Error; err != nil {
					return err
				}
			}
			return nil
		}); err != nil {
			return err
		}
		after = keys[len(keys)-1].FileID
	}
}

func (r *locationStage) pruneUnobservedOriginals(ctx context.Context) error {
	// Hashing or final validation can invalidate a range after its prior identities were captured.
	var after int64
	for {
		var rows []*Original
		if err := r.db.WithContext(ctx).Where("file_id > ?", after).Order("file_id").Limit(batchSize).Find(&rows).Error; err != nil {
			return err
		}
		if len(rows) == 0 {
			return nil
		}
		var rejected []int64
		for _, row := range rows {
			if row.Path != "" {
				covered, _, err := r.covered(ctx, r.db, r.source, row.Path)
				if err != nil {
					return err
				}
				if !covered {
					rejected = append(rejected, row.FileID)
				}
			}
		}
		if len(rejected) > 0 {
			if err := r.db.WithContext(ctx).Where("file_id IN ?", rejected).Delete(&Original{}).Error; err != nil {
				return err
			}
		}
		after = rows[len(rows)-1].FileID
	}
}

func (r *locationStage) matchRelocations(ctx context.Context) error {
	return observation.Match(ctx, r.db, r.logger)
}
