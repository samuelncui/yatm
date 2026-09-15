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
// The caller owns the Location gate and has validated the complete ranges outside this transaction.
func (l *Library) PublishAnalyzed(ctx context.Context, locationID, revision, jobID int64, manifest, absent OnlineManifest) (*Location, error) {
	return l.publishObservations(ctx, locationID, revision, &jobID, manifest, absent)
}

// PublishSelectedObservations admits a complete live selection without recording a Location analysis.
// The caller holds the Location gate and validates files and directory membership before publication.
func (l *Library) PublishSelectedObservations(ctx context.Context, locationID, revision int64, manifest OnlineManifest) (*Location, error) {
	return l.publishObservations(ctx, locationID, revision, nil, manifest, nil)
}

func (l *Library) publishObservations(ctx context.Context, locationID, revision int64, analysisJobID *int64, manifest, absent OnlineManifest) (*Location, error) {
	// Bind the metadata publication to the configuration used for actual filesystem observations.
	if manifest == nil {
		return nil, fmt.Errorf("analysis manifest is missing")
	}
	var location Location
	err := l.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.First(&location, locationID).Error; err != nil {
			return err
		}
		if location.Revision != revision {
			return ErrOnlineConflict
		}
		if location.Binding != entity.OnlineBinding_CONFIRMED {
			return ErrOnlineUnverified
		}

		// Only a completely observed range can establish absence; organization and tracking survive.
		if absent != nil {
			if err := absent(ctx, func(p *OnlinePosition) error {
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
		if err := manifest(ctx, func(p *OnlinePosition) error {
			if p == nil || p.FileID <= 0 {
				return nil
			}
			var old FileLocation
			if err := tx.Where("file_id = ?", p.FileID).Limit(1).Find(&old).Error; err != nil {
				return err
			}
			if old.FileID > 0 && old.LocationID != locationID {
				return fmt.Errorf("File %d is associated with another Location", p.FileID)
			}
			if old.Path == p.Path {
				return nil
			}
			return tx.Where("file_id = ? AND location_id = ?", p.FileID, locationID).Delete(&FileLocation{}).Error
		}); err != nil {
			return err
		}

		// Validated explicit files may bypass user Ignore; authorization belongs to the observing Executor.
		previous := ""
		if err := manifest(ctx, func(p *OnlinePosition) error {
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
			_, err := admitObservation(tx, &location, p)
			return err
		}); err != nil {
			return err
		}

		// Coverage creates saved versions only from evidenced copies; analysis itself is not a backup.
		if err := reconcileCoveredVersions(tx, tx.Where("location_id = ?", locationID)); err != nil {
			return err
		}
		if analysisJobID != nil {
			location.LastSyncAt, location.LastSyncJobID = time.Now().UnixMilli(), *analysisJobID
		}
		return tx.Save(&location).Error
	})
	if err != nil {
		return nil, fmt.Errorf("publish analysis failed, %w", err)
	}
	return &location, nil
}
