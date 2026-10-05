package library

import (
	"context"
	"fmt"
	"io/fs"
	"time"

	"github.com/samuelncui/yatm/entity"
	"gorm.io/gorm"
)

// PublishAnalyzed publishes successful observed ranges without replacing unobserved originals.
// The caller validates the complete observed ranges and owns the whole-Location coverage decision.
// jobID records a whole-Location publication; zero leaves the last complete-scan markers unchanged.
func (l *Library) PublishAnalyzed(ctx context.Context, locationID, jobID int64, manifest, absent ObservationManifest) (*Location, error) {
	// Only caller-confirmed whole-Location coverage supplies a complete-scan Job marker.
	var completeJobID *int64
	if jobID != 0 {
		completeJobID = &jobID
	}
	return l.publishObservations(ctx, locationID, completeJobID, manifest, absent)
}

// PublishSelectedObservations admits a complete live selection without recording a Location analysis.
// The caller holds the Location gate and validates files and directory membership before publication.
func (l *Library) PublishSelectedObservations(ctx context.Context, locationID int64, manifest ObservationManifest) (*Location, error) {
	return l.publishObservations(ctx, locationID, nil, manifest, nil)
}

func (l *Library) publishObservations(ctx context.Context, locationID int64, analysisJobID *int64, manifest, absent ObservationManifest) (*Location, error) {
	if manifest == nil {
		return nil, fmt.Errorf("analysis manifest is missing")
	}
	var location Location
	err := l.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.First(&location, locationID).Error; err != nil {
			return err
		}

		// Only a completely observed range can establish absence; organization and tracking survive.
		if absent != nil {
			if err := absent(ctx, func(p *ObservedEntry) error {
				if p == nil || p.FileID <= 0 {
					return fmt.Errorf("absent original identity is missing")
				}
				return tx.Where("file_id = ? AND location_id = ? AND path = ?", p.FileID, locationID, p.Path).
					Delete(&FileLocation{}).Error
			}); err != nil {
				return err
			}
		}

		// Release only resolved old associations, allowing atomic relocation without deleting absent records.
		if err := eachObservationBatch(ctx, manifest, func(rows []*ObservedEntry) error {
			ids := make([]int64, 0, len(rows))
			for _, row := range rows {
				if row.FileID > 0 {
					ids = append(ids, row.FileID)
				}
			}
			local := &Library{db: tx}
			facts, err := local.ReadFileFacts(ctx, ids, false, false)
			if err != nil {
				return err
			}
			for _, row := range rows {
				if row.FileID <= 0 {
					continue
				}
				old := facts[row.FileID].Original
				if old != nil && old.LocationID != locationID {
					return fmt.Errorf("File %d is associated with another Location", row.FileID)
				}
				if old == nil || old.Path == row.Path {
					continue
				}
				if err := tx.Where("file_id = ? AND location_id = ?", row.FileID, locationID).Delete(&FileLocation{}).Error; err != nil {
					return err
				}
			}
			return nil
		}); err != nil {
			return err
		}

		// The runner applies Location Ignore before publishing; authorization belongs to the observing Executor.
		previous := ""
		if err := eachObservationBatch(ctx, manifest, func(rows []*ObservedEntry) error {
			facts, err := readObservationBatch(tx, locationID, rows)
			if err != nil {
				return err
			}
			ids := make([]int64, 0, len(rows))
			for _, p := range rows {
				if p == nil || p.IsDir || !fs.FileMode(p.Mode).IsRegular() || p.Size < 0 {
					return fmt.Errorf("invalid analysis file facts")
				}
				if err := entity.ValidateRelativePath(p.Path); err != nil {
					return err
				}
				if p.Path <= previous {
					return fmt.Errorf("analysis manifest is not ordered at %q", p.Path)
				}
				previous = p.Path
				if len(p.Hash) != 0 && len(p.Hash) != 32 {
					return fmt.Errorf("invalid analysis SHA-256")
				}
				if len(p.Signature) == 0 && len(p.Hash) != 0 {
					signature, err := NewFileSignature(p.Hash, p.Size)
					if err != nil {
						return err
					}
					p.Signature = signature
				}
				if _, err := facts.admit(tx, &location, p); err != nil {
					return err
				}
				ids = append(ids, p.FileID)
			}
			// Only published originals can gain coverage from this observation batch.
			return reconcileCoveredVersions(tx, tx.Where("file_id IN ?", ids))
		}); err != nil {
			return err
		}

		if analysisJobID != nil {
			location.LastSyncAtNS, location.LastSyncJobID = time.Now().UnixNano(), *analysisJobID
		}
		return tx.Save(&location).Error
	})
	if err != nil {
		return nil, fmt.Errorf("publish analysis failed, %w", err)
	}
	return &location, nil
}

// eachObservationBatch keeps publication reads bounded while preserving manifest order.
func eachObservationBatch(ctx context.Context, manifest ObservationManifest, use func([]*ObservedEntry) error) error {
	rows := make([]*ObservedEntry, 0, batchSize)
	inputs := make([]*ObservedEntry, 0, batchSize)
	flush := func() error {
		if len(rows) == 0 {
			return nil
		}
		err := use(rows)
		// Publication has always returned allocated File IDs and derived content through its inputs.
		for i, row := range rows {
			*inputs[i] = *row
		}
		rows, inputs = rows[:0], inputs[:0]
		return err
	}
	if err := manifest(ctx, func(row *ObservedEntry) error {
		if row == nil {
			return fmt.Errorf("invalid analysis file facts")
		}
		copy := *row
		copy.Hash, copy.Signature = append([]byte(nil), row.Hash...), append([]byte(nil), row.Signature...)
		if row.TrackingKeys != nil {
			copy.TrackingKeys = make([]*FileTrackingKey, 0, len(row.TrackingKeys))
			for _, key := range row.TrackingKeys {
				value := *key
				value.KeyValue = append([]byte(nil), key.KeyValue...)
				copy.TrackingKeys = append(copy.TrackingKeys, &value)
			}
		}
		rows, inputs = append(rows, &copy), append(inputs, row)
		if len(rows) == batchSize {
			return flush()
		}
		return nil
	}); err != nil {
		return err
	}
	return flush()
}
