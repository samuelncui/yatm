package library

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"time"

	"github.com/samuelncui/yatm/entity"
	"gorm.io/gorm"
)

// ScanSource yields Diff entries in strict path order.
type ScanSource func(context.Context, func(*entity.ScanEntry) error) error

// ApplyScan atomically applies one validated Scan Diff to Library metadata.
func (l *Library) ApplyScan(
	ctx context.Context,
	mediaID int64,
	source ScanSource,
) (*Media, error) {
	// Validate the complete Diff source before opening the publication transaction.
	if source == nil {
		return nil, fmt.Errorf("apply Scan failed, source is nil")
	}

	// Keep inventory and saved-content associations atomic without modifying physical files.
	var applied *Media
	if err := l.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// Lock the operation to one existing Media identity and discard its derived directory index.
		stored := new(Media)
		if err := tx.First(stored, mediaID).Error; err != nil {
			return fmt.Errorf("read scanned Media failed, media_id=%d, %w", mediaID, err)
		}
		if stored.Kind != entity.MediaKind_MEDIA_KIND_VOLUME && stored.Kind != entity.MediaKind_MEDIA_KIND_TAPE {
			return fmt.Errorf("scanned Media kind is unsupported, media_id=%d", mediaID)
		}
		if err := tx.Where("media_id = ? AND is_dir = ?", mediaID, true).Delete(ModelPosition).Error; err != nil {
			return fmt.Errorf("clear scanned Media directories failed, media_id=%d, %w", mediaID, err)
		}

		// Apply each ordered physical-file change without retaining the Diff manifest in memory.
		var previous string
		if err := source(ctx, func(entry *entity.ScanEntry) error {
			if err := validateScanEntry(entry, previous); err != nil {
				return err
			}
			previous = entry.Path
			return applyScanEntry(tx, mediaID, entry)
		}); err != nil {
			return fmt.Errorf("apply Scan entries failed, media_id=%d, %w", mediaID, err)
		}

		// Rebuild the physical browsing index and derive capacity from the same committed Positions.
		if err := rebuildMediaPositionIndex(ctx, tx, mediaID); err != nil {
			return err
		}
		if err := reconcileCoveredVersions(tx, tx.Where("file_locations.signature IN (?)",
			tx.Model(ModelPosition).Select("signature").Where("media_id = ? AND is_dir = ?", mediaID, false))); err != nil {
			return err
		}
		if err := tx.Model(ModelPosition).Where("media_id = ? AND is_dir = ?", mediaID, false).
			Select("COALESCE(SUM(size), 0)").Scan(&stored.WrittenBytes).Error; err != nil {
			return fmt.Errorf("sum scanned Media bytes failed, media_id=%d, %w", mediaID, err)
		}
		if err := tx.Model(stored).Update("written_bytes", stored.WrittenBytes).Error; err != nil {
			return fmt.Errorf("update scanned Media bytes failed, media_id=%d, %w", mediaID, err)
		}
		applied = stored
		return nil
	}); err != nil {
		return nil, err
	}
	return applied, nil
}

func validateScanEntry(entry *entity.ScanEntry, previous string) error {
	if entry == nil {
		return fmt.Errorf("Scan entry is nil")
	}
	if err := entity.ValidateRelativePath(entry.Path); err != nil {
		return err
	}
	if previous != "" && entry.Path <= previous {
		return fmt.Errorf("Scan entries are not strictly ordered, previous=%q path=%q", previous, entry.Path)
	}
	if entry.Change == entity.ScanChange_SCAN_CHANGE_REMOVED {
		return nil
	}
	if entry.Change != entity.ScanChange_SCAN_CHANGE_ADDED &&
		entry.Change != entity.ScanChange_SCAN_CHANGE_CHANGED {
		return fmt.Errorf("unsupported Scan change, path=%q change=%s", entry.Path, entry.Change)
	}
	if entry.Size < 0 {
		return fmt.Errorf("invalid Scan size, path=%q size=%d", entry.Path, entry.Size)
	}
	if !fs.FileMode(entry.Mode).IsRegular() {
		return fmt.Errorf("Scan entry is not a regular file, path=%q mode=%#o", entry.Path, entry.Mode)
	}
	if len(entry.Sha256) != 0 && len(entry.Sha256) != 32 {
		return fmt.Errorf("invalid Scan SHA-256, path=%q length=%d", entry.Path, len(entry.Sha256))
	}
	return nil
}

func applyScanEntry(tx *gorm.DB, mediaID int64, entry *entity.ScanEntry) error {
	stored := new(Position)
	err := tx.Where("media_id = ? AND path = ? AND is_dir = ?", mediaID, entry.Path, false).First(stored).Error
	found := err == nil
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return fmt.Errorf("read scanned Media Position failed, path=%q, %w", entry.Path, err)
	}

	switch entry.Change {
	case entity.ScanChange_SCAN_CHANGE_ADDED:
		if found {
			return fmt.Errorf("added Scan path already exists, path=%q", entry.Path)
		}
		if err := tx.Create(positionFromScan(mediaID, entry)).Error; err != nil {
			return fmt.Errorf("create scanned Media Position failed, path=%q, %w", entry.Path, err)
		}
	case entity.ScanChange_SCAN_CHANGE_CHANGED:
		if !found {
			return fmt.Errorf("changed Scan path is missing, path=%q", entry.Path)
		}
		updated := positionFromScan(mediaID, entry)
		preservePositionHealth(updated, stored)
		if err := tx.Model(stored).Updates(map[string]any{
			"signature": updated.Signature, "mode": updated.Mode, "mod_time": updated.ModTime,
			"write_time": updated.WriteTime, "size": updated.Size, "hash": updated.Hash,
			"storage_order": updated.StorageOrder, "storage_metadata": updated.StorageMetadata,
			"health": updated.Health, "checked_at": updated.CheckedAt, "health_job_id": updated.HealthJobID,
		}).Error; err != nil {
			return fmt.Errorf("replace scanned Media Position failed, path=%q, %w", entry.Path, err)
		}
	case entity.ScanChange_SCAN_CHANGE_REMOVED:
		if !found {
			return fmt.Errorf("removed Scan path is missing, path=%q", entry.Path)
		}
		if err := tx.Delete(stored).Error; err != nil {
			return fmt.Errorf("delete scanned Media Position failed, path=%q, %w", entry.Path, err)
		}
	default:
		return fmt.Errorf("unsupported Scan change, path=%q change=%s", entry.Path, entry.Change)
	}
	return nil
}

func positionFromScan(mediaID int64, entry *entity.ScanEntry) *Position {
	modified := time.Unix(0, entry.MtimeNs)
	signature := append([]byte(nil), entry.Signature...)
	if len(signature) == 0 && len(entry.Sha256) == 32 {
		signature, _ = NewFileSignature(entry.Sha256, entry.Size)
	}
	return &Position{
		MediaID: mediaID, Path: entry.Path, Mode: entry.Mode, ModTime: modified, WriteTime: modified,
		Size: entry.Size, Hash: append([]byte(nil), entry.Sha256...), Signature: signature,
		StorageOrder: append([]byte{}, entry.Storage.GetOrder()...), StorageMetadata: entry.Storage.GetMetadata(),
	}
}

// CountSignatureCopies reports recorded physical copies without claiming current readability or health.
func (l *Library) CountSignatureCopies(ctx context.Context, signature []byte) (int64, error) {
	if len(signature) == 0 {
		return 0, nil
	}
	var count int64
	err := l.db.WithContext(ctx).Model(ModelPosition).Where("signature = ? AND is_dir = ?", signature, false).Count(&count).Error
	return count, err
}
