package library

import (
	"context"
	"errors"
	"fmt"

	"github.com/samuelncui/yatm/entity"
	"gorm.io/gorm"
)

func (l *Library) ImportLocationPaths(ctx context.Context, executorID, source, target string) error {
	// Explicit configuration conversion creates missing registrations without overwriting existing preferences.
	return l.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// Merge identical canonical roots and retain existing names, rules and organization.
		for _, item := range []struct {
			name, root string
			target     bool
		}{
			{"Source", source, source == target}, {"Restore", target, true},
		} {
			if item.root == "" {
				continue
			}
			var location Location
			err := tx.Where("executor_id = ? AND root_path = ?", executorID, item.root).First(&location).Error
			if errors.Is(err, gorm.ErrRecordNotFound) {
				location = Location{Name: item.name, ExecutorID: executorID, RootPath: item.root, RestoreTarget: item.target,
					Config: &entity.LocationConfig{Ignore: &entity.IgnoreRules{Format: "gitignore"}}}
				if err := tx.Create(&location).Error; err != nil {
					return fmt.Errorf("migrate %s directory failed, %w", item.name, err)
				}
				continue
			}
			if err != nil {
				return err
			}
			// Existing registrations are user configuration, even before the import checkpoint exists.
		}

		return nil
	})
}
