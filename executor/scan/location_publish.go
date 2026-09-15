package scan

import (
	"context"

	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/library"
	"gorm.io/gorm"
)

func (s *locationStage) publish(ctx context.Context, scope *Scope) error {
	// Matching sees the full successfully observed range before publication creates new identities.
	var successful int64
	if err := s.db.WithContext(ctx).Model(&Scope{}).Where("location_id = ? AND error = ''", s.source.ID).Count(&successful).Error; err != nil {
		return err
	}
	if successful == 0 {
		return nil
	}
	if err := s.pruneUnobservedOriginals(ctx); err != nil {
		return err
	}
	if err := s.exe.ConstrainCopiedObservations(ctx, s.db, s.source); err != nil {
		return err
	}
	if err := s.matchRelocations(ctx); err != nil {
		return err
	}
	location, err := s.exe.Lib().PublishAnalyzed(ctx, s.source.ID, s.source.Revision, s.job.ID, s.manifest(s.source.ID), s.absent(s.source.ID))
	if err != nil {
		return err
	}
	if err := s.recordPublished(ctx, location.ID); err != nil {
		return err
	}

	// Resolve only committed identities into result rows; Job failure never rolls back the Library commit.
	scope.PublishedAt = location.LastSyncAt
	return s.eachEntry(ctx, scope, func(entry *Entry) error {
		if entry.Change == entity.ScanChange_SCAN_CHANGE_REMOVED {
			return nil
		}
		var item Item
		if err := s.db.WithContext(ctx).Where("path = ?", entry.Path).First(&item).Error; err != nil {
			return err
		}
		entry.After = item.Position(location.ID).ToEntity()
		entry.Signature = item.Signature
		entry.Published = true
		return s.db.WithContext(ctx).Save(entry).Error
	})
}

func (r *locationStage) absent(sourceID int64) library.OnlineManifest {
	return func(ctx context.Context, yield func(*library.OnlinePosition) error) error {
		// Only the successful validated ranges retain REMOVED observations in this manifest.
		var after string
		for {
			var rows []*Item
			if err := r.db.WithContext(ctx).Where("path > ? AND change = ?", after, entity.ScanChange_SCAN_CHANGE_REMOVED).
				Order("path").Limit(batchSize).Find(&rows).Error; err != nil {
				return err
			}
			if len(rows) == 0 {
				return nil
			}
			for _, row := range rows {
				if err := yield(&library.OnlinePosition{FileID: row.Before.GetFileId(), SourceID: sourceID, Path: row.Path}); err != nil {
					return err
				}
			}
			after = rows[len(rows)-1].Path
		}
	}
}

func (r *locationStage) manifest(sourceID int64) library.OnlineManifest {
	return func(ctx context.Context, yield func(*library.OnlinePosition) error) error {
		// Page the validated Job manifest while the independent Library transaction publishes it.
		var after string
		for {
			var items []*Item
			if err := r.db.WithContext(ctx).Where("path > ? AND change != ?", after, entity.ScanChange_SCAN_CHANGE_REMOVED).Order("path").Limit(batchSize).Find(&items).Error; err != nil {
				return err
			}
			if len(items) == 0 {
				return nil
			}
			for _, item := range items {
				if err := yield(item.Position(sourceID)); err != nil {
					return err
				}
			}
			after = items[len(items)-1].Path
		}
	}
}

func (r *locationStage) recordPublished(ctx context.Context, sourceID int64) error {
	// Resolve newly created File and position IDs from the committed index, not precommit guesses.
	var after string
	for {
		rows, err := r.exe.Lib().OnlineFilesPage(ctx, sourceID, after, batchSize)
		if err != nil {
			return err
		}
		if len(rows) == 0 {
			return nil
		}
		if err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			for _, row := range rows {
				if err := tx.Model(&Item{}).Where("path = ?", row.Path).Updates(map[string]any{"file_id": row.FileID, "position_id": row.ID, "signature": row.Signature}).Error; err != nil {
					return err
				}
			}
			return nil
		}); err != nil {
			return err
		}
		after = rows[len(rows)-1].Path
	}
}
