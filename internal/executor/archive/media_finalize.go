package archive

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"syscall"

	"github.com/samuelncui/acp"
	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/internal/dataformat"
	"github.com/samuelncui/yatm/internal/library"
	mediapkg "github.com/samuelncui/yatm/internal/media"
	"gorm.io/gorm"
)

var errInvalidTapeIndex = errors.New("invalid final Tape index")

func (a *jobArchiveRunner) reconcileWriteResult(ctx context.Context, result *mediapkg.WriteResult) error {
	if result == nil || result.Media == nil {
		resetErr := a.resetStagedItems(ctx)
		return errors.Join(mediapkg.ErrFinalizeUnusable, fmt.Errorf("Media write result is incomplete"), resetErr)
	}

	var validationErr error
	err := a.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := prefixStagedPaths(tx, result.PathPrefix); err != nil {
			return err
		}
		if result.Inventory == nil {
			return nil
		}
		if err := result.Inventory(ctx, func(entry *mediapkg.LTFSIndexEntry) error {
			return attachTapeStorage(tx, entry)
		}); err != nil {
			return errors.Join(errInvalidTapeIndex, err)
		}
		if result.VerifiedPrefix {
			_, err := keepVerifiedPrefix(tx)
			return err
		}
		missing, err := resetUnverifiedItems(tx)
		if err != nil {
			return err
		}
		if missing > 0 {
			validationErr = fmt.Errorf("LTFS index is missing staged files, count=%d", missing)
		}
		return nil
	})
	if err == nil {
		return validationErr
	}
	resetErr := a.resetStagedItems(ctx)
	return errors.Join(mediapkg.ErrFinalizeUnusable, err, resetErr)
}

func prefixStagedPaths(tx *gorm.DB, prefix string) error {
	if prefix == "." || prefix == "" {
		return nil
	}
	var cursor int64
	for {
		var items []*Item
		if err := tx.Select("id", "target_path", "media_path").Where(
			"status = ? AND id > ?", entity.CopyStatus_COPY_STATUS_STAGED, cursor,
		).Order("id").Limit(batchSize).Find(&items).Error; err != nil {
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
			updated := tx.Model(&Item{}).Where(
				"id = ? AND status = ? AND media_path = ?", item.ID, entity.CopyStatus_COPY_STATUS_STAGED, item.MediaPath,
			).Update("media_path", mediaPath)
			if updated.Error != nil || updated.RowsAffected != 1 {
				return fmt.Errorf(
					"prefix staged Archive path failed, id=%d affected=%d, %w",
					item.ID, updated.RowsAffected, updated.Error,
				)
			}
		}
	}
}

func attachTapeStorage(tx *gorm.DB, entry *mediapkg.LTFSIndexEntry) error {
	var items []*Item
	result := tx.Select("id", "result").Where(
		"status = ? AND media_path = ?", entity.CopyStatus_COPY_STATUS_STAGED, entry.Path,
	).Limit(2).Find(&items)
	if result.Error != nil {
		return fmt.Errorf("query staged Archive item failed, path=%q, %w", entry.Path, result.Error)
	}
	if len(items) == 0 {
		return nil
	}
	if len(items) != 1 || items[0].Result == nil {
		return errors.Join(errInvalidTapeIndex, fmt.Errorf("invalid staged Archive item, path=%q", entry.Path))
	}
	item := items[0]
	if item.Result.Storage != nil {
		return errors.Join(errInvalidTapeIndex, fmt.Errorf("LTFS index contains duplicate path, path=%q", entry.Path))
	}
	if item.Result.SizeBytes != entry.Size {
		return nil
	}
	item.Result.Storage = entry.Storage
	updated := tx.Model(&Item{}).Where(
		"id = ? AND status = ?", item.ID, entity.CopyStatus_COPY_STATUS_STAGED,
	).Update("result", item.Result)
	if updated.Error != nil || updated.RowsAffected != 1 {
		return fmt.Errorf(
			"store LTFS position failed, path=%q affected=%d, %w", entry.Path, updated.RowsAffected, updated.Error,
		)
	}
	return nil
}

func resetUnverifiedItems(tx *gorm.DB) (int64, error) {
	var cursor string
	var reset int64
	for {
		var items []*Item
		if err := tx.Where("status = ? AND target_path > ?", entity.CopyStatus_COPY_STATUS_STAGED, cursor).
			Order("target_path").Limit(batchSize).Find(&items).Error; err != nil {
			return 0, fmt.Errorf("query staged Archive verification failed, cursor=%q, %w", cursor, err)
		}
		if len(items) == 0 {
			return reset, nil
		}
		for _, item := range items {
			cursor = item.TargetPath
			if item.Result != nil && item.Result.Storage != nil {
				continue
			}
			if err := resetOneItem(tx, item.ID); err != nil {
				return 0, err
			}
			reset++
		}
	}
}

func keepVerifiedPrefix(tx *gorm.DB) (int64, error) {
	var cursor string
	var kept int64
	closed := false
	for {
		var items []*Item
		if err := tx.Where("status IN ? AND target_path > ?", []entity.CopyStatus{
			entity.CopyStatus_COPY_STATUS_PENDING, entity.CopyStatus_COPY_STATUS_STAGED,
		}, cursor).Order("target_path").Limit(batchSize).Find(&items).Error; err != nil {
			return 0, fmt.Errorf("query Archive verified prefix failed, cursor=%q, %w", cursor, err)
		}
		if len(items) == 0 {
			return kept, nil
		}
		for _, item := range items {
			cursor = item.TargetPath
			verified := item.Status == entity.CopyStatus_COPY_STATUS_STAGED && item.Result != nil && item.Result.Storage != nil
			if !closed && verified {
				kept++
				continue
			}
			closed = true
			if item.Status == entity.CopyStatus_COPY_STATUS_STAGED {
				if err := resetOneItem(tx, item.ID); err != nil {
					return 0, err
				}
			}
		}
	}
}

func resetOneItem(tx *gorm.DB, id int64) error {
	updated := tx.Model(&Item{}).Where(
		"id = ? AND status = ?", id, entity.CopyStatus_COPY_STATUS_STAGED,
	).Updates(map[string]any{
		"status": entity.CopyStatus_COPY_STATUS_PENDING, "media_path": "", "media_id": nil, "result": nil,
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

func libraryMedia(value *mediapkg.Descriptor) (*library.Media, error) {
	if value == nil {
		return nil, nil
	}
	created, err := dataformat.Nanoseconds(value.CreateTime)
	if err != nil {
		return nil, err
	}
	var destroyed *int64
	if value.DestroyTime != nil {
		stamp, err := dataformat.Nanoseconds(*value.DestroyTime)
		if err != nil {
			return nil, err
		}
		destroyed = &stamp
	}
	return &library.Media{
		ID: value.ID, Kind: value.Kind, Identity: value.Identity, Name: value.Name, Profile: value.Profile,
		CreatedAtNS: created, DestroyedAtNS: destroyed,
		CapacityBytes: value.CapacityBytes, WrittenBytes: value.WrittenBytes,
	}, nil
}
