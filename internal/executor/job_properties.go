package executor

import (
	"context"
	"fmt"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const maxJobPropertyBatch = 256

const (
	JobPropertyLocation = "location_id"
	JobPropertyMedia    = "media_id"
)

// JobProperty indexes the Locations and Media associated with a Job for catalog searches.
type JobProperty struct {
	JobID int64  `gorm:"primaryKey;autoIncrement:false;index:idx_job_properties_lookup,priority:3"`
	Key   string `gorm:"primaryKey;index:idx_job_properties_lookup,priority:1"`
	Value int64  `gorm:"primaryKey;autoIncrement:false;index:idx_job_properties_lookup,priority:2"`
}

// AddJobProperties publishes new search values without advancing revisions for duplicate values.
func (e *Executor) AddJobProperties(ctx context.Context, jobID int64, properties ...JobProperty) error {
	// Validate the complete bounded batch before making catalog changes.
	if len(properties) == 0 {
		return nil
	}
	if len(properties) > maxJobPropertyBatch {
		return fmt.Errorf("too many Job properties in one batch")
	}
	for i := range properties {
		property := &properties[i]
		if property.Value <= 0 || jobID <= 0 {
			return fmt.Errorf("Job property identifiers must be positive")
		}
		if property.Key != JobPropertyLocation && property.Key != JobPropertyMedia {
			return fmt.Errorf("invalid Job property key %q", property.Key)
		}
		property.JobID = jobID
	}

	// Keep associations and their visible change revision in the same GORM transaction.
	return e.withNextJobRevision(func(revision int64) (bool, error) {
		changed := false
		err := e.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			var owner jobCatalogRow
			if err := tx.Select("id").First(&owner, jobID).Error; err != nil {
				return fmt.Errorf("resolve Job property owner failed, %w", err)
			}
			result := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&properties)
			if result.Error != nil {
				return fmt.Errorf("publish Job properties failed, %w", result.Error)
			}
			if result.RowsAffected == 0 {
				return nil
			}
			publication := tx.Model(&jobCatalogRow{}).Where("id = ?", jobID).Update("revision", revision)
			changed = publication.RowsAffected > 0
			return publication.Error
		})
		return changed, err
	})
}

func (e *Executor) publishJobProjection(ctx context.Context, jobID int64) error {
	// Read under the publication lock so a late publisher cannot overwrite a newer checkpoint.
	return e.withNextJobRevision(func(revision int64) (bool, error) {
		db, closeDB, err := e.openStateDB(jobID)
		if err != nil {
			return false, err
		}
		defer closeDB()
		var record JobRecord
		if err := db.WithContext(ctx).First(&record, singletonJobID).Error; err != nil {
			return false, fmt.Errorf("read Job checkpoint for catalog failed, id=%d, %w", jobID, err)
		}

		// The revision and the filterable checkpoint become visible together.
		result := e.db.WithContext(ctx).Model(&jobCatalogRow{}).Where("id = ?", jobID).
			Updates(map[string]any{"revision": revision, "catalog_kind": record.Kind})
		return result.RowsAffected > 0, result.Error
	})
}
