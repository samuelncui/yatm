package legacy

import (
	"context"
	"fmt"

	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/internal/executor"
	"github.com/samuelncui/yatm/internal/executor/archive"
	"github.com/samuelncui/yatm/internal/executor/restore"
	"gorm.io/gorm"
)

type stagedJobProperty executor.JobProperty

func (stagedJobProperty) TableName() string { return "job_properties_staging" }

func migrateJobProperties(ctx context.Context, catalog, state *gorm.DB, jobID int64, kind entity.JobKind) error {
	// Legacy Jobs have Media history but no registered Location identities to infer.
	return walkJobMedia(ctx, state, kind, func(ids []int64) error {
		properties := make([]stagedJobProperty, 0, len(ids))
		for _, id := range ids {
			properties = append(properties, stagedJobProperty{JobID: jobID, Key: executor.JobPropertyMedia, Value: id})
		}
		return catalog.WithContext(ctx).Create(&properties).Error
	})
}

func validateJobProperties(ctx context.Context, catalog, expected *gorm.DB, jobID int64, kind entity.JobKind) error {
	// Compare every indexed Media against the reconstructed legacy manifest in bounded pages.
	var total int64
	if err := walkJobMedia(ctx, expected, kind, func(ids []int64) error {
		var count int64
		if err := catalog.WithContext(ctx).Model(&executor.JobProperty{}).
			Where("job_id = ? AND `key` = ? AND value IN ?", jobID, executor.JobPropertyMedia, ids).Count(&count).Error; err != nil {
			return err
		}
		if count != int64(len(ids)) {
			return fmt.Errorf("historical Job %d Media search properties differ", jobID)
		}
		total += count
		return nil
	}); err != nil {
		return err
	}

	// No invented Location or extra Media association may be introduced by migration.
	var count int64
	if err := catalog.WithContext(ctx).Model(&executor.JobProperty{}).Where("job_id = ?", jobID).Count(&count).Error; err != nil {
		return err
	}
	if count != total {
		return fmt.Errorf("historical Job %d search property count differs", jobID)
	}
	return nil
}

func walkJobMedia(ctx context.Context, db *gorm.DB, kind entity.JobKind, yield func([]int64) error) error {
	// Only actually submitted Archive targets and frozen Restore candidates support historical search.
	query := db.WithContext(ctx).Model(&restore.Copy{})
	if kind == entity.JobKind_JOB_KIND_ARCHIVE {
		query = db.WithContext(ctx).Model(&archive.Item{}).Where("status = ?", entity.CopyStatus_COPY_STATUS_SUBMITTED)
	}
	var after int64
	for {
		var ids []int64
		if err := query.Where("media_id > ?", after).Distinct("media_id").Order("media_id").Limit(256).Pluck("media_id", &ids).Error; err != nil {
			return err
		}
		if len(ids) == 0 {
			return nil
		}
		if err := yield(ids); err != nil {
			return err
		}
		after = ids[len(ids)-1]
	}
}
