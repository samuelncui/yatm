package library

import (
	"context"
	"fmt"

	"gorm.io/gorm"
)

// ObservationAdmission carries a resolved observation and the association it may replace.
type ObservationAdmission struct {
	Observation *ObservedEntry
	Previous    *FileLocation
}

// AdmissionCandidates pages only indexed evidence relevant to the bounded observed batch.
// An original still associated with another Location is never eligible for reassignment.
func (l *Library) AdmissionCandidates(ctx context.Context, location *Location, kind TrackingKind, observations []*ObservedEntry, after int64, limit int) ([]*FileTrackingKey, error) {
	if len(observations) == 0 || len(observations) > 1000 || limit <= 0 || limit > 1000 {
		return nil, fmt.Errorf("invalid admission candidate bounds")
	}
	groups := map[string][][]byte{}
	for _, observation := range observations {
		for _, key := range observation.TrackingKeys {
			if key.Kind == kind && key.Scope != "" && len(key.KeyValue) != 0 {
				groups[key.Scope] = append(groups[key.Scope], key.KeyValue)
			}
		}
	}
	if len(groups) == 0 {
		return nil, nil
	}
	query := l.readDB().WithContext(ctx).Model(&FileTrackingKey{}).
		Joins("JOIN locations ON locations.id = file_tracking_keys.location_id").
		Joins("LEFT JOIN file_locations ON file_locations.file_id = file_tracking_keys.file_id").
		Where("file_tracking_keys.kind = ? AND file_tracking_keys.file_id > ?", kind, after).
		Where("locations.executor_id = ?", location.ExecutorID).
		Where("file_locations.file_id IS NULL OR file_locations.location_id = ?", location.ID)
	evidence := l.readDB().Session(&gorm.Session{NewDB: true})
	for scope, values := range groups {
		evidence = evidence.Or("file_tracking_keys.scope = ? AND file_tracking_keys.key_value IN ?", scope, values)
	}
	var keys []*FileTrackingKey
	err := query.Where(evidence).Select("file_tracking_keys.*").Order("file_tracking_keys.file_id").Limit(limit).Find(&keys).Error
	return keys, err
}

// AdmissionSignatureCandidates never searches historical versions to inherit organization.
func (l *Library) AdmissionSignatureCandidates(ctx context.Context, location *Location, signatures [][]byte, after int64, limit int) ([]*FileLocation, error) {
	if len(signatures) == 0 {
		return nil, nil
	}
	if len(signatures) > 1000 || limit <= 0 || limit > 1000 {
		return nil, fmt.Errorf("invalid signature candidate bounds")
	}
	var rows []*FileLocation
	err := l.readDB().WithContext(ctx).Where("location_id = ? AND file_id > ? AND signature IN ?",
		location.ID, after, signatures).Order("file_id").Limit(limit).Find(&rows).Error
	return rows, err
}

// AdmitObservations publishes a resolved bounded batch atomically without a directory cache.
func (l *Library) AdmitObservations(ctx context.Context, locationID int64, matches []*ObservationAdmission) ([]*FileLocation, error) {
	if len(matches) == 0 || len(matches) > 1000 {
		return nil, fmt.Errorf("admit between 1 and 1000 observations")
	}
	results := make([]*FileLocation, 0, len(matches))
	err := l.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var location Location
		if err := tx.First(&location, locationID).Error; err != nil {
			return err
		}
		// Read unchanged-path facts once for the batch; mutations still use this transaction's view.
		paths := make([]string, 0, len(matches))
		positions := make([]*ObservedEntry, 0, len(matches))
		for _, match := range matches {
			if match == nil || match.Observation == nil {
				return fmt.Errorf("missing resolved observation")
			}
			paths = append(paths, match.Observation.Path)
			positions = append(positions, match.Observation)
		}
		local := &Library{db: tx}
		originals, err := local.ReadFileOriginalsAt(ctx, locationID, paths)
		if err != nil {
			return err
		}
		ids := make([]int64, 0, len(originals))
		for _, original := range originals {
			ids = append(ids, original.FileID)
		}
		tracking, err := local.ReadFileTrackingKeys(ctx, ids)
		if err != nil {
			return err
		}
		changed := false
		var admitted []int64
		var facts *observationBatch
		for _, match := range matches {
			if match == nil || match.Observation == nil {
				return fmt.Errorf("missing resolved observation")
			}
			p := match.Observation
			if old := match.Previous; old != nil && old.Path != p.Path {
				if p.FileID != old.FileID {
					return ErrLocationConflict
				}
				if err := tx.Where("file_id = ?", old.FileID).Delete(&FileLocation{}).Error; err != nil {
					return err
				}
				delete(originals, old.Path)
				if facts != nil {
					delete(facts.paths, old.Path)
					delete(facts.originals, old.FileID)
				}
				changed = true
			}
			previous := originals[p.Path]
			var keys []*FileTrackingKey
			if previous != nil {
				keys = tracking[previous.FileID]
			}
			if MatchesOriginalObservation(previous, p, keys) {
				results = append(results, previous)
				continue
			}
			if facts == nil {
				facts, err = readObservationBatch(tx, locationID, positions)
				if err != nil {
					return err
				}
			}
			original, err := facts.admit(tx, &location, p)
			if err != nil {
				return err
			}
			results = append(results, original)
			admitted = append(admitted, original.FileID)
			originals[p.Path] = original
			if p.TrackingKeys != nil {
				tracking[original.FileID] = p.TrackingKeys
			}
			changed = true
		}
		if len(admitted) > 0 {
			if err := reconcileCoveredVersions(tx, tx.Where("file_id IN ?", admitted)); err != nil {
				return err
			}
		}
		if changed {
			return tx.Save(&location).Error
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return results, nil
}
