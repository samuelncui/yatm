package library

import (
	"bytes"
	"context"
	"time"

	"gorm.io/gorm"
)

// AdmitObservation publishes a checked ordinary file, never a physical directory cache.
// The caller holds the Location gate and performs all filesystem I/O before this transaction.
func (l *Library) AdmitObservation(ctx context.Context, locationID int64, p *ObservedEntry) (*FileLocation, error) {
	// Single-file admission shares unchanged-observation and Location revision semantics with batches.
	originals, err := l.AdmitObservations(ctx, locationID, []*ObservationAdmission{{Observation: p}})
	if err != nil {
		return nil, err
	}
	return originals[0], nil
}

// MatchesOriginalObservation applies the same content-basis rule to already batched facts.
func MatchesOriginalObservation(previous *FileLocation, p *ObservedEntry, keys []*FileTrackingKey) bool {
	if !matchesObservationContent(previous, p) {
		return false
	}
	if p.TrackingKeys == nil {
		return true
	}
	if len(keys) != len(p.TrackingKeys) {
		return false
	}
	for _, observed := range p.TrackingKeys {
		matches := false
		for _, stored := range keys {
			if observed.Kind == stored.Kind && observed.Scope == stored.Scope && bytes.Equal(observed.KeyValue, stored.KeyValue) && observed.Details == stored.Details {
				matches = true
				break
			}
		}
		if !matches {
			return false
		}
	}
	return true
}

func matchesObservationContent(previous *FileLocation, p *ObservedEntry) bool {
	if previous == nil || p == nil || previous.FileID == 0 || previous.Size != p.Size || previous.Mode != p.Mode || previous.MtimeNS != p.MtimeNS {
		return false
	}
	if p.FileID != 0 && previous.FileID != p.FileID {
		return false
	}
	if len(p.Hash) > 0 && !bytes.Equal(p.Hash, previous.Hash) {
		return false
	}
	if len(p.Signature) > 0 && !bytes.Equal(p.Signature, previous.Signature) {
		return false
	}
	return true
}

func admitObservation(tx *gorm.DB, location *Location, p *ObservedEntry) (*FileLocation, error) {
	facts, err := readObservationBatch(tx, location.ID, []*ObservedEntry{p})
	if err != nil {
		return nil, err
	}
	original, err := facts.admit(tx, location, p)
	if err != nil {
		return nil, err
	}
	if err := reconcileCoveredVersions(tx, tx.Where("file_id = ?", p.FileID)); err != nil {
		return nil, err
	}
	return original, nil
}

// replaceTrackingKeys publishes explicitly supplied evidence; callers decide whether nil means retain.
func replaceTrackingKeys(tx *gorm.DB, fileID, locationID int64, keys []*FileTrackingKey) error {
	// Replacement is atomic with its original association in the caller's metadata transaction.
	if err := tx.Where("file_id = ?", fileID).Delete(&FileTrackingKey{}).Error; err != nil {
		return err
	}
	observedAt := time.Now().UnixNano()
	for _, key := range keys {
		stored := *key
		stored.FileID, stored.LocationID, stored.ObservedAtNS = fileID, locationID, observedAt
		if err := tx.Create(&stored).Error; err != nil {
			return err
		}
	}
	return nil
}
