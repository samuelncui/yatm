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
	"google.golang.org/protobuf/proto"
	"gorm.io/gorm"
)

var errInvalidFinalInventory = errors.New("invalid final Media inventory")

func (a *jobArchiveRunner) reconcileWriteResult(ctx context.Context, result *mediapkg.WriteResult) error {
	// Incomplete physical results leave every candidate pending.
	if result == nil || result.Media == nil {
		resetErr := a.resetStagedItems(ctx)
		return errors.Join(mediapkg.ErrFinalizeUnusable, fmt.Errorf("Media write result is incomplete"), resetErr)
	}

	// Match physical evidence transactionally; missing individual files retain proven matches.
	var validationErr error
	err := a.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		missing, err := finalizeStagedPaths(tx, result)
		if err != nil {
			return err
		}
		if result.Inventory == nil {
			if missing > 0 && result.Stop == nil {
				validationErr = fmt.Errorf("Media inventory is missing staged files, count=%d", missing)
			}
			return nil
		}
		if err := result.Inventory(ctx, func(entry *mediapkg.CompletedFile) error {
			return attachCompletedStorage(tx, entry)
		}); err != nil {
			return errors.Join(errInvalidFinalInventory, err)
		}
		if result.VerifiedPrefix {
			_, err := keepVerifiedPrefix(tx)
			return err
		}
		unverified, err := resetUnverifiedItems(tx)
		missing += unverified
		if err != nil {
			return err
		}
		if missing > 0 && result.Stop == nil {
			validationErr = fmt.Errorf("Media inventory is missing staged files, count=%d", missing)
		}
		return nil
	})
	// A failed inventory invalidates the whole attempt; individual misses do not.
	if err == nil {
		return validationErr
	}
	resetErr := a.resetStagedItems(ctx)
	return errors.Join(mediapkg.ErrFinalizeUnusable, err, resetErr)
}

func finalizeStagedPaths(tx *gorm.DB, result *mediapkg.WriteResult) (int64, error) {
	// Every result reconciles storage; an empty prefix only leaves staged paths unchanged.
	prefix := result.PathPrefix
	rewrite := prefix != "." && prefix != ""
	var cursor, missing int64
	for {
		// Reuse the existing bounded path-update pass instead of a second inventory or per-file lookup.
		var items []*Item
		if err := tx.Select("id", "target_path", "media_path", "result").Where(
			"status = ? AND id > ?", entity.CopyStatus_COPY_STATUS_STAGED, cursor,
		).Order("id").Limit(batchSize).Find(&items).Error; err != nil {
			return 0, fmt.Errorf("query staged Archive paths failed, cursor=%d, %w", cursor, err)
		}
		if len(items) == 0 {
			return missing, nil
		}
		for _, item := range items {
			// Native nil positioning awaits Inventory; tar's private validator rejects missing evidence.
			cursor = item.ID
			if item.MediaPath != item.TargetPath || item.Result == nil {
				return 0, fmt.Errorf("staged Archive path or result is unexpected, id=%d path=%q", item.ID, item.MediaPath)
			}
			storage, err := result.FinalizeStorage(item.Result.Storage)
			if err != nil {
				return 0, errors.Join(errInvalidFinalInventory, err)
			}
			if item.Result.Storage != nil && storage == nil {
				if err := resetOneItem(tx, item.ID); err != nil {
					return 0, err
				}
				missing++
				continue
			}

			// Prefix and any terminal anchor correction share the same ordinary row update.
			updates := map[string]any{}
			if rewrite {
				updates["media_path"] = filepath.ToSlash(filepath.Join(prefix, item.TargetPath))
			}
			if !proto.Equal(storage, item.Result.Storage) {
				item.Result.Storage = storage
				updates["result"] = item.Result
			}
			if len(updates) == 0 {
				continue
			}
			updated := tx.Model(&Item{}).Where(
				"id = ? AND status = ? AND media_path = ?", item.ID, entity.CopyStatus_COPY_STATUS_STAGED, item.MediaPath,
			).Updates(updates)
			if updated.Error != nil || updated.RowsAffected != 1 {
				return 0, fmt.Errorf("finalize staged Archive path failed, id=%d affected=%d, %w", item.ID, updated.RowsAffected, updated.Error)
			}
		}
	}
}

func attachCompletedStorage(tx *gorm.DB, entry *mediapkg.CompletedFile) error {
	// Only a unique staged path can receive physical completion evidence.
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
		return errors.Join(errInvalidFinalInventory, fmt.Errorf("invalid staged Archive item, path=%q", entry.Path))
	}
	item := items[0]
	if item.Result.Storage != nil {
		return errors.Join(errInvalidFinalInventory, fmt.Errorf("Media inventory contains duplicate path, path=%q", entry.Path))
	}
	if item.Result.SizeBytes != entry.Size {
		return nil
	}
	// Attach storage only when the confirmed physical size matches the transferred content.
	item.Result.Storage = entry.Storage
	updated := tx.Model(&Item{}).Where(
		"id = ? AND status = ?", item.ID, entity.CopyStatus_COPY_STATUS_STAGED,
	).Update("result", item.Result)
	if updated.Error != nil || updated.RowsAffected != 1 {
		return fmt.Errorf(
			"store completed Media position failed, path=%q affected=%d, %w", entry.Path, updated.RowsAffected, updated.Error,
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

	// Check runtime instants before admitting the session's metadata into the Library.
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
		TapePositionMap: value.TapePositionMap,
		CreatedAtNS:     created, DestroyedAtNS: destroyed,
		CapacityBytes: value.CapacityBytes, WrittenBytes: value.WrittenBytes,
	}, nil
}
