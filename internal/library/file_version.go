package library

import (
	"bytes"
	"context"
	"fmt"

	"github.com/samuelncui/yatm/entity"
	"gorm.io/gorm"
)

// FileVersion retains one File's archived content, independently of physical copies.
type FileVersion struct {
	ID                int64  `gorm:"primaryKey;autoIncrement" json:"id"`
	FileID            int64  `gorm:"not null;uniqueIndex:idx_file_versions_content,priority:1;index:idx_file_versions_history,priority:1" json:"file_id"`
	Signature         []byte `gorm:"type:varbinary(256);not null;uniqueIndex:idx_file_versions_content,priority:2;index:idx_file_versions_signature" json:"signature"`
	Hash              []byte `gorm:"type:varbinary(32)" json:"hash"`
	Size              int64  `json:"size"`
	Mode              uint32 `json:"mode"`
	MtimeNS           int64  `json:"mtime_ns,string"`
	FirstArchivedAtNS *int64 `json:"first_archived_at_ns,omitempty,string"`
	LastArchivedAtNS  *int64 `gorm:"index:idx_file_versions_history,priority:2" json:"last_archived_at_ns,omitempty,string"`
}

func (v *FileVersion) BeforeSave(*gorm.DB) error {
	if v.FileID <= 0 || len(v.Signature) == 0 {
		return fmt.Errorf("FileVersion requires a File and nonempty content signature")
	}
	return nil
}

func (v *FileVersion) ToEntity() *entity.FileVersion {
	return &entity.FileVersion{Id: v.ID, FileId: v.FileID, Signature: v.Signature, Sha256: v.Hash,
		SizeBytes: v.Size, Mode: v.Mode, MtimeNs: v.MtimeNS,
		FirstArchivedAtNs: v.FirstArchivedAtNS, LastArchivedAtNs: v.LastArchivedAtNS}
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
	var owner fileRow
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
		if err := recordVersionArchives(tx, value.ID, value.FirstArchivedAtNS, value.LastArchivedAtNS); err != nil {
			return nil, err
		}
		return value, nil
	}

	// A repeated copy changes only the last successful operation time, not saved content.
	if stored.Size != value.Size || !bytes.Equal(stored.Hash, value.Hash) {
		return nil, fmt.Errorf("archived content facts disagree, file_version_id=%d", stored.ID)
	}
	if value.LastArchivedAtNS != nil {
		if stored.LastArchivedAtNS == nil || *value.LastArchivedAtNS > *stored.LastArchivedAtNS {
			stored.LastArchivedAtNS = value.LastArchivedAtNS
			if err := tx.Model(&stored).Update("last_archived_at_ns", stored.LastArchivedAtNS).Error; err != nil {
				return nil, fmt.Errorf("update last archive time failed, %w", err)
			}
		}
	}

	// Retain each evidenced save, including intermediate returns to this content state.
	if err := recordVersionArchives(tx, stored.ID, value.FirstArchivedAtNS, value.LastArchivedAtNS); err != nil {
		return nil, err
	}
	return &stored, nil
}

func (l *Library) GetFileVersion(ctx context.Context, id int64) (*FileVersion, error) {
	if id <= 0 {
		return nil, fmt.Errorf("invalid FileVersion ID %d", id)
	}
	var value FileVersion
	if err := l.readDB().WithContext(ctx).First(&value, id).Error; err != nil {
		return nil, fmt.Errorf("get FileVersion failed, %w", err)
	}
	return &value, nil
}

// LatestFileVersion selects saved content by its most recent successful archive, not row creation order.
func (l *Library) LatestFileVersion(ctx context.Context, fileID int64) (*FileVersion, error) {
	var version FileVersion
	if err := l.readDB().WithContext(ctx).Where("file_id = ?", fileID).Order("last_archived_at_ns DESC, id DESC").First(&version).Error; err != nil {
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
	if err := l.readDB().WithContext(ctx).Where("file_id = ? AND id > ?", fileID, after).
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
