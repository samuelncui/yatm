package library

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/samuelncui/yatm/entity"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// FileVersionArchive retains an evidenced successful save time, not a content or directory snapshot.
type FileVersionArchive struct {
	VersionID  int64 `gorm:"primaryKey;autoIncrement:false" json:"version_id"`
	ArchivedAt int64 `gorm:"primaryKey;autoIncrement:false" json:"archived_at_ms"`
}

func (v *FileVersionArchive) BeforeSave(*gorm.DB) error {
	if v.VersionID <= 0 || v.ArchivedAt <= 0 {
		return fmt.Errorf("archive observation requires a version and positive save time")
	}
	return nil
}

func recordVersionArchives(tx *gorm.DB, versionID int64, dates ...*int64) error {
	// Repeated publication of an evidenced endpoint cannot create another observation.
	for _, date := range dates {
		if date == nil {
			continue
		}
		observation := &FileVersionArchive{VersionID: versionID, ArchivedAt: *date}
		if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(observation).Error; err != nil {
			return fmt.Errorf("record archive observation failed, version_id=%d, %w", versionID, err)
		}
	}
	return nil
}

// ResolveRestoreVersion resolves saved content without replacing a missing temporal match with newer content.
func (l *Library) ResolveRestoreVersion(
	ctx context.Context,
	fileID int64,
	policy *entity.RestoreVersionPolicy,
) (*entity.RestoreVersionResolution, error) {
	// Keep ordinary latest selection unchanged, including versions with unknown dates.
	if fileID <= 0 {
		return nil, fmt.Errorf("invalid Restore File ID %d", fileID)
	}
	if policy != nil && policy.BeforeAtMs != nil && policy.GetBeforeAtMs() < 0 {
		return nil, fmt.Errorf("Restore cutoff must not be negative")
	}
	latest, err := l.LatestFileVersion(ctx, fileID)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return &entity.RestoreVersionResolution{FileId: fileID, Match: entity.RestoreVersionMatch_RESTORE_VERSION_NO_SAVED_VERSION}, nil
	}
	if err != nil {
		return nil, err
	}
	if policy == nil || policy.BeforeAtMs == nil {
		return &entity.RestoreVersionResolution{FileId: fileID, Version: latest.ToEntity(), ArchivedAtMs: latest.LastArchivedAt}, nil
	}

	// Read only the newest eligible observation; the version relation scopes the indexed history.
	db := l.db.WithContext(ctx)
	var observed FileVersionArchive
	if err := db.Model(&FileVersionArchive{}).Select("file_version_archives.*").
		Joins("JOIN file_versions ON file_versions.id = file_version_archives.version_id").
		Where("file_versions.file_id = ? AND file_version_archives.archived_at <= ?", fileID, policy.GetBeforeAtMs()).
		Order("file_version_archives.archived_at DESC, file_version_archives.version_id DESC").Limit(1).Find(&observed).Error; err != nil {
		return nil, fmt.Errorf("resolve archive history failed, file_id=%d, %w", fileID, err)
	}

	// Earlier catalog evidence may contain only endpoints; never infer intervening saves from them.
	for _, column := range []string{"first_archived_at", "last_archived_at"} {
		var endpoint FileVersionArchive
		if err := db.Model(&FileVersion{}).Select("id AS version_id, "+column+" AS archived_at").
			Where("file_id = ? AND "+column+" <= ?", fileID, policy.GetBeforeAtMs()).
			Order(column + " DESC, id DESC").Limit(1).Find(&endpoint).Error; err != nil {
			return nil, fmt.Errorf("resolve archive endpoint failed, file_id=%d, %w", fileID, err)
		}
		if endpoint.ArchivedAt > observed.ArchivedAt ||
			(endpoint.ArchivedAt == observed.ArchivedAt && endpoint.VersionID > observed.VersionID) {
			observed = endpoint
		}
	}
	if observed.VersionID != 0 {
		version, err := l.GetFileVersion(ctx, observed.VersionID)
		if err != nil {
			return nil, err
		}
		return &entity.RestoreVersionResolution{FileId: fileID, Version: version.ToEntity(), ArchivedAtMs: &observed.ArchivedAt}, nil
	}

	// Unknown dates cannot prove that every backup is later than the requested cutoff.
	var undated FileVersion
	if err := db.Model(&FileVersion{}).Where("file_id = ? AND first_archived_at IS NULL", fileID).
		Select("id").Limit(1).Find(&undated).Error; err != nil {
		return nil, fmt.Errorf("resolve undated versions failed, file_id=%d, %w", fileID, err)
	}
	match := entity.RestoreVersionMatch_RESTORE_VERSION_AFTER_CUTOFF
	if undated.ID != 0 {
		match = entity.RestoreVersionMatch_RESTORE_VERSION_DATE_UNKNOWN
	}
	return &entity.RestoreVersionResolution{FileId: fileID, Match: match}, nil
}

func (l *Library) exportVersionArchives(ctx context.Context, encoder *json.Encoder) error {
	// Continue on both primary-key fields so one repeatedly saved version may span many pages.
	var versionID, archivedAt int64
	for {
		var rows []*FileVersionArchive
		if err := l.db.WithContext(ctx).
			Where("version_id > ? OR (version_id = ? AND archived_at > ?)", versionID, versionID, archivedAt).
			Order("version_id, archived_at").Limit(batchSize).Find(&rows).Error; err != nil {
			return fmt.Errorf("export archive observations failed, %w", err)
		}
		if len(rows) == 0 {
			return nil
		}
		for _, row := range rows {
			if err := encoder.Encode(jsonlOutputRecord{Type: recordTypeFileVersionArchive, Data: row}); err != nil {
				return err
			}
		}
		last := rows[len(rows)-1]
		versionID, archivedAt = last.VersionID, last.ArchivedAt
	}
}
