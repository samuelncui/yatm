package library

import (
	"context"
	"fmt"

	"github.com/samuelncui/yatm/entity"
	"gorm.io/gorm"
)

// ObservationAdmission carries a resolved observation and the association it may replace.
// The Executor proves physical absence outside the metadata transaction.
type ObservationAdmission struct {
	Observation *OnlinePosition
	Previous    *FileLocation
}

// AdmissionCandidates pages only indexed evidence relevant to the bounded observed batch.
// An original still associated with another Location is never eligible for reassignment.
func (l *Library) AdmissionCandidates(ctx context.Context, location *Location, kind TrackingKind, observations []*OnlinePosition, after int64, limit int) ([]*FileTrackingKey, error) {
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
	query := l.db.WithContext(ctx).Model(&FileTrackingKey{}).
		Joins("JOIN locations ON locations.id = file_tracking_keys.location_id").
		Joins("LEFT JOIN file_locations ON file_locations.file_id = file_tracking_keys.file_id").
		Where("file_tracking_keys.kind = ? AND file_tracking_keys.file_id > ?", kind, after).
		Where("locations.executor_id = ? AND locations.binding = ?", location.ExecutorID, entity.OnlineBinding_CONFIRMED).
		Where("file_locations.file_id IS NULL OR file_locations.location_id = ?", location.ID)
	evidence := l.db.Session(&gorm.Session{NewDB: true})
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
	err := l.db.WithContext(ctx).Where("location_id = ? AND observed_binding_token = ? AND file_id > ? AND signature IN ?",
		location.ID, location.BindingToken, after, signatures).Order("file_id").Limit(limit).Find(&rows).Error
	return rows, err
}

// AdmitObservations publishes a resolved bounded batch atomically without a directory cache.
// The caller holds the Location gate and has revalidated every observation and relinquished path.
func (l *Library) AdmitObservations(ctx context.Context, locationID int64, token string, matches []*ObservationAdmission) ([]*FileLocation, error) {
	if len(matches) == 0 || len(matches) > 1000 {
		return nil, fmt.Errorf("admit between 1 and 1000 observations")
	}
	results := make([]*FileLocation, 0, len(matches))
	err := l.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var location Location
		if err := tx.First(&location, locationID).Error; err != nil {
			return err
		}
		if location.BindingToken != token || location.Binding != entity.OnlineBinding_CONFIRMED {
			return ErrOnlineConflict
		}
		changed := false
		for _, match := range matches {
			if match == nil || match.Observation == nil {
				return fmt.Errorf("missing resolved observation")
			}
			p := match.Observation
			if old := match.Previous; old != nil && old.Path != p.Path {
				// Release exactly the previously observed relation, never a later reassignment.
				deleted := tx.Where("file_id = ? AND location_id = ? AND path = ? AND observed_binding_token = ?",
					old.FileID, locationID, old.Path, token).Delete(&FileLocation{})
				if deleted.Error != nil {
					return deleted.Error
				}
				if deleted.RowsAffected != 1 || p.FileID != old.FileID {
					return ErrOnlineConflict
				}
				changed = true
			}
			previous, err := matchingObservation(tx, &location, p)
			if err != nil {
				return err
			}
			if previous != nil {
				results = append(results, previous)
				continue
			}
			original, err := admitObservation(tx, &location, p)
			if err != nil {
				return err
			}
			results = append(results, original)
			changed = true
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
