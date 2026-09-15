package executor

import (
	"context"
	"errors"
	"fmt"
	"os"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type JobResourceKind uint8
type JobResourceRole uint8

const maxJobResourceBatch = 256

const (
	JobResourceLocation JobResourceKind = iota + 1
	JobResourceMedia
)

const (
	JobResourceSource JobResourceRole = iota + 1
	JobResourceDestination
)

// JobResource is an append-only navigation fact captured from a frozen input or an actual operation.
type JobResource struct {
	JobID      int64           `gorm:"primaryKey;autoIncrement:false"`
	Kind       JobResourceKind `gorm:"primaryKey;autoIncrement:false;index:idx_job_resources_lookup,priority:1"`
	ResourceID int64           `gorm:"primaryKey;autoIncrement:false;index:idx_job_resources_lookup,priority:2"`
	Role       JobResourceRole `gorm:"primaryKey;autoIncrement:false"`
}

// AddJobResources publishes only new associations; retries cannot create duplicate history entries.
func (e *Executor) AddJobResources(ctx context.Context, jobID int64, resources ...JobResource) error {
	// Validate the complete bounded batch before making catalog changes.
	if len(resources) == 0 {
		return nil
	}
	if len(resources) > maxJobResourceBatch {
		return fmt.Errorf("too many Job resources in one batch")
	}
	for i := range resources {
		resource := &resources[i]
		if resource.ResourceID <= 0 || jobID <= 0 {
			return fmt.Errorf("Job resource identifiers must be positive")
		}
		if resource.Kind != JobResourceLocation && resource.Kind != JobResourceMedia {
			return fmt.Errorf("invalid Job resource kind %d", resource.Kind)
		}
		if resource.Role != JobResourceSource && resource.Role != JobResourceDestination {
			return fmt.Errorf("invalid Job resource role %d", resource.Role)
		}
		resource.JobID = jobID
	}

	// Keep associations and their visible change revision in the same GORM transaction.
	return e.withNextJobRevision(func(revision int64) (bool, error) {
		changed := false
		err := e.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			var owner Job
			if err := tx.Select("id").First(&owner, jobID).Error; err != nil {
				return fmt.Errorf("resolve Job resource owner failed, %w", err)
			}
			result := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&resources)
			if result.Error != nil {
				return fmt.Errorf("publish Job resources failed, %w", result.Error)
			}
			if result.RowsAffected == 0 {
				return nil
			}
			publication := tx.Model(&Job{}).Where("id = ?", jobID).Update("revision", revision)
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
		result := e.db.WithContext(ctx).Model(&Job{}).Where("id = ?", jobID).
			Updates(map[string]any{"revision": revision, "catalog_kind": record.Kind, "catalog_status": record.Status})
		return result.RowsAffected > 0, result.Error
	})
}

// refreshJobProjections repairs ordinary cross-database checkpoint publication on startup.
func (e *Executor) refreshJobProjections(ctx context.Context) error {
	// Visit bounded catalog pages without loading complete manifests or rewriting valid projections.
	var after int64
	for {
		var rows []*Job
		if err := e.db.WithContext(ctx).Where("id > ?", after).Order("id").Limit(maxJobPageSize).Find(&rows).Error; err != nil {
			return fmt.Errorf("page Job query projections failed, %w", err)
		}
		if len(rows) == 0 {
			return nil
		}
		for _, row := range rows {
			after = row.ID
			if err := e.hydrateJob(ctx, row); err != nil {
				// Missing bundles remain available to the existing recovery diagnostics.
				if errors.Is(err, os.ErrNotExist) {
					continue
				}
				return err
			}
			if row.Kind == row.CatalogKind && row.Status == row.CatalogStatus {
				continue
			}
			if err := e.publishJobProjection(ctx, row.ID); err != nil {
				return err
			}
		}
	}
}
