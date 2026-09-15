package library

import (
	"context"
	"errors"
	"fmt"

	"github.com/samuelncui/yatm/entity"
	"gorm.io/gorm"
)

// LocationMigration is an installation checkpoint, not an exported Library preference.
type LocationMigration struct {
	ExecutorID string `gorm:"primaryKey"`
	Source     string
	Target     string
	CreatedAt  int64 `gorm:"autoCreateTime:milli"`
}

func (l *Library) MigrateLocationPaths(ctx context.Context, executorID, source, target string) error {
	// Migrate once; restarting never reapplies YAML over Settings edits or deletions.
	return l.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var checkpoint LocationMigration
		if err := tx.First(&checkpoint, "executor_id = ?", executorID).Error; err == nil {
			return nil
		} else if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}

		// Merge identical canonical roots and retain existing names, rules and organization.
		for _, item := range []struct {
			name, root string
			target     bool
		}{
			{"Source", source, false}, {"Restore", target, true},
		} {
			if item.root == "" {
				continue
			}
			var location Location
			err := tx.Where("executor_id = ? AND root_path = ?", executorID, item.root).First(&location).Error
			if errors.Is(err, gorm.ErrRecordNotFound) {
				location = Location{Name: item.name, ExecutorID: executorID, RootPath: item.root, RestoreTarget: item.target,
					Exclusions: &entity.OnlineExclusions{Format: "gitignore"}, Binding: entity.OnlineBinding_CONFIRMED}
				if err := tx.Create(&location).Error; err != nil {
					return fmt.Errorf("migrate %s directory failed, %w", item.name, err)
				}
				continue
			}
			if err != nil {
				return err
			}
			location.RestoreTarget = location.RestoreTarget || item.target
			if err := tx.Save(&location).Error; err != nil {
				return err
			}
		}

		// Commit the checkpoint with the registrations; failures roll back the whole migration.
		return tx.Create(&LocationMigration{ExecutorID: executorID, Source: source, Target: target}).Error
	})
}

func (l *Library) LocationMigrationMessages(ctx context.Context, executorID string) ([]string, error) {
	var checkpoint LocationMigration
	if err := l.db.WithContext(ctx).First(&checkpoint, "executor_id = ?", executorID).Error; errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	} else if err != nil {
		return nil, err
	}
	var messages []string
	if checkpoint.Source != "" {
		messages = append(messages, fmt.Sprintf("Migrated paths.source %q to Locations. Settings now owns this registration.", checkpoint.Source))
	}
	if checkpoint.Target != "" {
		messages = append(messages, fmt.Sprintf("Migrated paths.target %q to a restore destination. Settings now owns this registration.", checkpoint.Target))
	}
	return messages, nil
}
