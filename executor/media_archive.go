package executor

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"syscall"

	"github.com/samuelncui/acp"
	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/executor/jobstorage"
	mediapkg "github.com/samuelncui/yatm/media"
	"gorm.io/gorm"
)

func allocateMediaPathPrefix(root string, value uint64) (string, error) {
	for {
		name := strconv.FormatUint(value, 36)
		_, err := os.Lstat(filepath.Join(root, name))
		if errors.Is(err, os.ErrNotExist) {
			return name, nil
		}
		if err != nil {
			return "", fmt.Errorf("inspect Media path prefix failed, path=%q, %w", name, err)
		}
		if value == ^uint64(0) {
			return "", fmt.Errorf("allocate Media path prefix failed, timestamp space is exhausted")
		}
		value++
	}
}

func prefixArchiveStagedPaths(ctx context.Context, db *gorm.DB, prefix string) error {
	if prefix == "." || prefix == "" {
		return nil
	}
	return db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var cursor int64
		for {
			var items []*jobstorage.ArchiveItem
			if err := tx.Select("id", "target_path", "media_path").Where(
				"status = ? AND id > ?", entity.CopyStatus_STAGED, cursor,
			).Order("id").Limit(256).Find(&items).Error; err != nil {
				return fmt.Errorf("query staged Archive paths failed, cursor=%d, %w", cursor, err)
			}
			if len(items) == 0 {
				return nil
			}
			for _, item := range items {
				cursor = item.ID
				if item.MediaPath != item.TargetPath {
					return fmt.Errorf("staged Archive path is unexpected, id=%d path=%q", item.ID, item.MediaPath)
				}
				mediaPath := filepath.ToSlash(filepath.Join(prefix, item.TargetPath))
				updated := tx.Model(&jobstorage.ArchiveItem{}).Where(
					"id = ? AND status = ? AND media_path = ?", item.ID, entity.CopyStatus_STAGED, item.MediaPath,
				).Update("media_path", mediaPath)
				if updated.Error != nil || updated.RowsAffected != 1 {
					return fmt.Errorf(
						"prefix staged Archive path failed, id=%d affected=%d, %w",
						item.ID, updated.RowsAffected, updated.Error,
					)
				}
			}
		}
	})
}

func resetArchiveStaged(ctx context.Context, db *gorm.DB) error {
	updated := db.WithContext(ctx).Model(&jobstorage.ArchiveItem{}).
		Where("status = ?", entity.CopyStatus_STAGED).
		Updates(map[string]any{
			"status": entity.CopyStatus_PENDING, "media_path": "", "media_id": nil, "result": nil,
		})
	if updated.Error != nil {
		return fmt.Errorf("reset staged Archive items failed, %w", updated.Error)
	}
	return nil
}

func resetOneArchiveItem(tx *gorm.DB, id int64) error {
	updated := tx.Model(&jobstorage.ArchiveItem{}).Where(
		"id = ? AND status = ?", id, entity.CopyStatus_STAGED,
	).Updates(map[string]any{
		"status": entity.CopyStatus_PENDING, "media_path": "", "media_id": nil, "result": nil,
	})
	if updated.Error != nil || updated.RowsAffected != 1 {
		return fmt.Errorf(
			"reset Archive item failed, id=%d affected=%d, %w", id, updated.RowsAffected, updated.Error,
		)
	}
	return nil
}

func targetNoSpaceError(err error) error {
	if errors.Is(err, acp.ErrTargetNoSpace) || errors.Is(err, syscall.ENOSPC) ||
		errors.Is(err, mediapkg.ErrTargetNoSpace) || errors.Is(err, mediapkg.ErrCapacityBoundary) {
		return mediapkg.ErrTargetNoSpace
	}
	return nil
}
