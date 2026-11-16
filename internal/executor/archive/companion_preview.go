package archive

import (
	"context"
	"fmt"

	"github.com/samuelncui/yatm/entity"
	scanjob "github.com/samuelncui/yatm/internal/executor/scan"
)

func (a *jobArchiveRunner) createCompanionPreview(ctx context.Context) {
	// Prepare the child only after every item is archived, from the content facts the copies read
	// rather than rewalking roots or reading the originals again for their hashes.
	config := new(Config)
	if err := a.db.WithContext(ctx).First(config, 1).Error; err != nil {
		a.logger.WithError(err).Error("read archive job config for companion Preview failed")
		return
	}
	if config.Preview == nil || config.PreviewJobID != 0 {
		return
	}
	job, err := scanjob.CreateIndexed(ctx, a.exe, a.job.Priority, config.Preview,
		func(ctx context.Context, yield func(string, *entity.ExpectedFile) error) error {
			var after int64
			for {
				var items []*Item
				if err := a.db.WithContext(ctx).Where("id > ?", after).Order("id").Limit(batchSize).Find(&items).Error; err != nil {
					return err
				}
				if len(items) == 0 {
					return nil
				}
				for _, item := range items {
					if item.Data == nil || item.Data.Expected == nil {
						return fmt.Errorf("Archive item %d lacks archived content", item.ID)
					}
					if err := yield(item.Data.SourcePath, item.Data.Expected); err != nil {
						return err
					}
				}
				after = items[len(items)-1].ID
			}
		})

	// Companion failure never rolls back Archive preparation or changes its success state.
	config.PreviewError = ""
	if err != nil {
		config.PreviewError = err.Error()
		a.logger.WithError(err).Error("create companion Preview failed")
	} else {
		config.PreviewJobID = job.ID
	}
	if err := a.db.WithContext(ctx).Save(config).Error; err != nil {
		a.logger.WithError(err).Error("record companion Preview result failed")
	}
}
