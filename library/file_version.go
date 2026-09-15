package library

import (
	"bytes"
	"context"
	"fmt"
	"io/fs"
	"time"

	"github.com/samuelncui/yatm/entity"
	"gorm.io/gorm"
)

// FileVersion retains one File's archived content, independently of physical copies.
type FileVersion struct {
	ID              int64  `gorm:"primaryKey;autoIncrement" json:"id"`
	FileID          int64  `gorm:"not null;uniqueIndex:idx_file_versions_content,priority:1;index:idx_file_versions_history,priority:1" json:"file_id"`
	Signature       []byte `gorm:"type:varbinary(256);not null;uniqueIndex:idx_file_versions_content,priority:2;index:idx_file_versions_signature" json:"signature"`
	Hash            []byte `gorm:"type:varbinary(32)" json:"hash"`
	Size            int64  `json:"size"`
	Mode            uint32 `json:"mode"`
	MtimeNS         int64  `json:"mtime_ns"`
	FirstArchivedAt *int64 `json:"first_archived_at_ms"`
	LastArchivedAt  *int64 `gorm:"index:idx_file_versions_history,priority:2" json:"last_archived_at_ms"`
}

func (v *FileVersion) BeforeSave(*gorm.DB) error {
	if v.FileID <= 0 || len(v.Signature) == 0 {
		return fmt.Errorf("FileVersion requires a File and nonempty content signature")
	}
	return nil
}

func (v *FileVersion) ToEntity() *entity.FileVersion {
	return &entity.FileVersion{Id: v.ID, FileId: v.FileID, Signature: v.Signature, Sha256: v.Hash,
		Size: v.Size, Mode: v.Mode, MtimeNs: v.MtimeNS,
		FirstArchivedAtMs: v.FirstArchivedAt, LastArchivedAtMs: v.LastArchivedAt}
}

// HasRestoreIntegrity is a Restore consumer requirement, not a signature admission rule.
func (v *FileVersion) HasRestoreIntegrity() bool {
	return len(v.Hash) == 32 && v.Size >= 0
}

// recordVersion belongs to the transaction publishing archive inventory or a covered original.
func recordVersion(tx *gorm.DB, value *FileVersion) (*FileVersion, error) {
	// Content equality never supplies a different File's ownership or restore metadata.
	if err := value.BeforeSave(tx); err != nil {
		return nil, err
	}
	var owner File
	if err := tx.Select("id", "kind").First(&owner, value.FileID).Error; err != nil {
		return nil, fmt.Errorf("resolve archived File failed, %w", err)
	}
	if owner.Kind != entity.FileKind_FILE_KIND_REGULAR {
		return nil, fmt.Errorf("directories cannot own FileVersions")
	}
	var stored FileVersion
	if err := tx.Where("file_id = ? AND signature = ?", value.FileID, value.Signature).Limit(1).Find(&stored).Error; err != nil {
		return nil, fmt.Errorf("find archived content failed, %w", err)
	}
	if stored.ID == 0 {
		if err := tx.Create(value).Error; err != nil {
			return nil, fmt.Errorf("create FileVersion failed, %w", err)
		}
		if err := recordVersionArchives(tx, value.ID, value.FirstArchivedAt, value.LastArchivedAt); err != nil {
			return nil, err
		}
		return value, nil
	}

	// A repeated copy changes only the last successful operation time, not saved content.
	if stored.Size != value.Size || !bytes.Equal(stored.Hash, value.Hash) {
		return nil, fmt.Errorf("archived content facts disagree, file_version_id=%d", stored.ID)
	}
	if value.LastArchivedAt != nil {
		if stored.LastArchivedAt == nil || *value.LastArchivedAt > *stored.LastArchivedAt {
			stored.LastArchivedAt = value.LastArchivedAt
			if err := tx.Model(&stored).Update("last_archived_at", stored.LastArchivedAt).Error; err != nil {
				return nil, fmt.Errorf("update last archive time failed, %w", err)
			}
		}
	}

	// Retain each evidenced save, including intermediate returns to this content state.
	if err := recordVersionArchives(tx, stored.ID, value.FirstArchivedAt, value.LastArchivedAt); err != nil {
		return nil, err
	}
	return &stored, nil
}

func (l *Library) GetFileVersion(ctx context.Context, id int64) (*FileVersion, error) {
	if id <= 0 {
		return nil, fmt.Errorf("invalid FileVersion ID %d", id)
	}
	var value FileVersion
	if err := l.db.WithContext(ctx).First(&value, id).Error; err != nil {
		return nil, fmt.Errorf("get FileVersion failed, %w", err)
	}
	return &value, nil
}

// LatestFileVersion selects saved content by its most recent successful archive, not row creation order.
func (l *Library) LatestFileVersion(ctx context.Context, fileID int64) (*FileVersion, error) {
	var version FileVersion
	if err := l.db.WithContext(ctx).Where("file_id = ?", fileID).Order("last_archived_at DESC, id DESC").First(&version).Error; err != nil {
		return nil, fmt.Errorf("File %d has no saved version: %w", fileID, err)
	}
	return &version, nil
}

func (l *Library) ListFileVersions(ctx context.Context, fileID, after int64, limit int) ([]*FileVersion, bool, error) {
	// An ID cursor avoids reordering history when unchanged content is archived again.
	if fileID <= 0 || after < 0 || limit <= 0 || limit > 1000 {
		return nil, false, fmt.Errorf("invalid FileVersion page")
	}
	var values []*FileVersion
	if err := l.db.WithContext(ctx).Where("file_id = ? AND id > ?", fileID, after).
		Order("id").Limit(limit + 1).Find(&values).Error; err != nil {
		return nil, false, fmt.Errorf("list FileVersions failed, %w", err)
	}

	// Keep the sentinel out of the returned page.
	more := len(values) > limit
	if more {
		values = values[:limit]
	}
	return values, more, nil
}

// hydrateFileFacts supplies presentation facts; none are stored as File identity.
func hydrateFileFacts(tx *gorm.DB, file *File) error {
	// Logical directories derive their presentation from catalog facts only.
	file.Mode, file.ModTime = 0, time.UnixMilli(file.UpdatedAt)
	file.Hash, file.Signature, file.Size = nil, nil, 0
	if file.Kind == entity.FileKind_FILE_KIND_DIRECTORY {
		file.Mode = uint32(fs.ModeDir | 0755)
		return nil
	}

	// An unsigned current original must not fall back to an older archived signature.
	var original FileLocation
	if err := tx.Where("file_id = ?", file.ID).Limit(1).Find(&original).Error; err != nil {
		return err
	}
	if original.FileID != 0 {
		file.Mode, file.ModTime, file.Size = original.Mode, time.Unix(0, original.MtimeNS), original.Size
		file.Hash, file.Signature = original.Hash, original.Signature
		return nil
	}

	// Archived-only summaries remain distinct from a current-original observation.
	var version FileVersion
	if err := tx.Where("file_id = ?", file.ID).Order("last_archived_at DESC, id DESC").Limit(1).Find(&version).Error; err != nil {
		return err
	}
	if version.ID != 0 {
		file.Mode, file.ModTime, file.Size = version.Mode, time.Unix(0, version.MtimeNS), version.Size
		file.Hash, file.Signature = version.Hash, version.Signature
	}
	return nil
}
