package library

import (
	"context"
	"fmt"

	"github.com/samuelncui/yatm/internal/resource"
	"gorm.io/gorm"
)

// NativeCandidates pages the Files whose retained native evidence names an observed object. A File
// whose original belongs to another Location is never eligible; one without an original is.
func (l *Library) NativeCandidates(ctx context.Context, locationID int64, observed []*FileTrackingKey, after int64, limit int) ([]*FileTrackingKey, error) {
	if len(observed) > 1000 || limit <= 0 || limit > 1000 {
		return nil, fmt.Errorf("invalid native candidate bounds")
	}
	groups := map[string][][]byte{}
	for _, key := range observed {
		if key.Kind == TrackingNative && key.Scope != "" && len(key.KeyValue) != 0 {
			groups[key.Scope] = append(groups[key.Scope], key.KeyValue)
		}
	}
	if len(groups) == 0 {
		return nil, nil
	}
	evidence := l.readDB().Session(&gorm.Session{NewDB: true})
	for scope, values := range groups {
		evidence = evidence.Or("file_tracking_keys.scope = ? AND file_tracking_keys.key_value IN ?", scope, values)
	}
	var keys []*FileTrackingKey
	err := l.readDB().WithContext(ctx).Model(&FileTrackingKey{}).
		Joins("LEFT JOIN file_locations ON file_locations.file_id = file_tracking_keys.file_id").
		Where("file_tracking_keys.kind = ? AND file_tracking_keys.file_id > ?", TrackingNative, after).
		Where("file_locations.file_id IS NULL OR file_locations.location_id = ?", locationID).
		Where(evidence).Select("file_tracking_keys.*").Order("file_tracking_keys.file_id").Limit(limit).Find(&keys).Error
	return keys, err
}

// SignatureCandidates pages this Location's originals with an observed signature; historical versions
// never lend their organization to new content.
func (l *Library) SignatureCandidates(ctx context.Context, locationID int64, signatures [][]byte, after int64, limit int) ([]*FileLocation, error) {
	if len(signatures) == 0 {
		return nil, nil
	}
	if len(signatures) > 1000 || limit <= 0 || limit > 1000 {
		return nil, fmt.Errorf("invalid signature candidate bounds")
	}
	var rows []*FileLocation
	err := l.readDB().WithContext(ctx).Where("location_id = ? AND file_id > ? AND signature IN ?",
		locationID, after, signatures).Order("file_id").Limit(limit).Find(&rows).Error
	return rows, err
}

// AdmitObservations records fresh observations of known originals at their recorded paths in one
// transaction, writing only those whose facts changed.
func (l *Library) AdmitObservations(ctx context.Context, locationID int64, observations []*ObservedEntry) ([]*FileLocation, error) {
	if len(observations) == 0 || len(observations) > 1000 {
		return nil, fmt.Errorf("admit between 1 and 1000 observations")
	}
	results := make([]*FileLocation, 0, len(observations))
	err := resource.WriteTransaction(l.db.WithContext(ctx), func(tx *gorm.DB) error {
		results = results[:0]
		var location Location
		if err := tx.First(&location, locationID).Error; err != nil {
			return err
		}
		// Unchanged observations need only their path associations and evidence; changed ones share
		// the full transaction-local facts, read once.
		paths := make([]string, 0, len(observations))
		for _, p := range observations {
			paths = append(paths, p.Path)
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
		var facts *observationBatch
		var admitted []int64
		for _, p := range observations {
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
				if facts, err = readObservationBatch(tx, locationID, observations); err != nil {
					return err
				}
			}
			original, err := facts.admit(tx, &location, p)
			if err != nil {
				return err
			}
			results = append(results, original)
			admitted = append(admitted, original.FileID)
		}
		if len(admitted) == 0 {
			return nil
		}
		if err := reconcileCoveredVersions(tx, tx.Where("file_id IN ?", admitted)); err != nil {
			return err
		}
		return tx.Save(&location).Error
	})
	if err != nil {
		return nil, err
	}
	return results, nil
}
