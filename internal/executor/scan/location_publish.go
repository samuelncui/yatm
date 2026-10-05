package scan

import (
	"context"
	"fmt"

	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/internal/library"
)

func (s *locationStage) publish(ctx context.Context, scope *Scope) error {
	// Resolve continuity before publishing the completed observed ranges.
	if err := s.matchRelocations(ctx); err != nil {
		return err
	}

	// An explicit root selection establishes whole-Location coverage for this source only.
	var completeJobID int64
	for _, selected := range s.scopes {
		if selected.LocationID == s.source.ID && selected.Path == "" {
			completeJobID = s.job.ID
			break
		}
	}

	// Partial ranges still publish their observed and absent originals in the same transaction.
	_, err := s.exe.Lib().PublishAnalyzed(ctx, s.source.ID, completeJobID, s.manifest(s.source.ID), s.absent(s.source.ID))
	if err != nil {
		return err
	}
	return s.recordPublished(ctx, s.source.ID)
}

func (r *locationStage) absent(sourceID int64) library.ObservationManifest {
	return func(ctx context.Context, yield func(*library.ObservedEntry) error) error {
		// Only the successfully observed ranges retain REMOVED observations in this manifest.
		var after string
		for {
			var rows []*Item
			if err := r.db.WithContext(ctx).Where("location_id = ? AND path > ? AND change = ?", sourceID, after, entity.ScanChange_SCAN_CHANGE_REMOVED).
				Order("path").Limit(batchSize).Find(&rows).Error; err != nil {
				return err
			}
			if len(rows) == 0 {
				return nil
			}
			for _, row := range rows {
				if err := yield(&library.ObservedEntry{FileID: row.Before.GetFileId(), Path: row.Path}); err != nil {
					return err
				}
			}
			after = rows[len(rows)-1].Path
		}
	}
}

func (r *locationStage) manifest(sourceID int64) library.ObservationManifest {
	return func(ctx context.Context, yield func(*library.ObservedEntry) error) error {
		// Page the observed Job manifest while the independent Library transaction publishes it.
		var after string
		for {
			var items []*Item
			if err := r.db.WithContext(ctx).Where("location_id = ? AND path > ? AND change != ?", sourceID, after, entity.ScanChange_SCAN_CHANGE_REMOVED).Order("path").Limit(batchSize).Find(&items).Error; err != nil {
				return err
			}
			if len(items) == 0 {
				return nil
			}
			for _, item := range items {
				if err := yield(item.Position()); err != nil {
					return err
				}
			}
			after = items[len(items)-1].Path
		}
	}
}

func (r *locationStage) recordPublished(ctx context.Context, sourceID int64) error {
	// Resolve only this manifest's published paths from the committed Library in bounded batches.
	var after string
	for {
		var rows []*Entry
		if err := r.db.WithContext(ctx).Where("location_id = ? AND path > ? AND change != ?", sourceID, after, entity.ScanChange_SCAN_CHANGE_REMOVED).
			Order("path").Limit(batchSize).Find(&rows).Error; err != nil {
			return err
		}
		if len(rows) == 0 {
			return nil
		}
		paths := make([]string, 0, len(rows))
		for _, row := range rows {
			paths = append(paths, row.Path)
		}
		originals, err := r.exe.Lib().ReadFileOriginalsAt(ctx, sourceID, paths)
		if err != nil {
			return err
		}
		for _, row := range rows {
			old := originals[row.Path]
			if old == nil {
				return fmt.Errorf("published original is missing: Location %d path %q", sourceID, row.Path)
			}
			row.FileID, row.PositionID, row.Signature = old.FileID, old.FileID, old.Signature
			row.After, row.Published = old.Observation(), true
		}
		if err := r.saveEntries(ctx, rows); err != nil {
			return err
		}
		after = rows[len(rows)-1].Path
	}
}
