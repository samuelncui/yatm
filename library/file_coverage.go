package library

import (
	"bytes"
	"fmt"

	"github.com/samuelncui/yatm/entity"
	"gorm.io/gorm"
)

// reconcileCoveredVersions retains saved original contents within their publication transaction.
// The caller scopes originals to the changed Location or inventory, or the complete imported catalog.
func reconcileCoveredVersions(tx, originals *gorm.DB) error {
	// Match only observed content with physical coverage, never another File's entire history.
	copies := tx.Model(ModelPosition).Select("1").
		Where("positions.signature = file_locations.signature AND positions.is_dir = ?", false)
	versions := tx.Model(&FileVersion{}).Select("1").
		Where("file_versions.file_id = file_locations.file_id AND file_versions.signature = file_locations.signature")
	query := originals.Model(&FileLocation{}).Select("file_locations.*").
		Joins("JOIN files ON files.id = file_locations.file_id").
		Where("files.kind = ? AND file_locations.signature IS NOT NULL", entity.FileKind_FILE_KIND_REGULAR).
		Where("EXISTS (?) AND NOT EXISTS (?)", copies, versions)

	// Page by immutable File identity; new versions remove candidates without offset skipping.
	var after int64
	for {
		var values []*FileLocation
		if err := query.Session(&gorm.Session{}).Where("file_locations.file_id > ?", after).
			Order("file_locations.file_id").Limit(batchSize).Find(&values).Error; err != nil {
			return fmt.Errorf("find covered originals after File %d failed, %w", after, err)
		}
		if len(values) == 0 {
			return nil
		}
		for _, original := range values {
			version, err := versionFromCoverage(tx, original)
			if err != nil {
				return err
			}
			if _, err := recordVersion(tx, version); err != nil {
				return fmt.Errorf("retain covered original failed, file_id=%d, %w", original.FileID, err)
			}
			after = original.FileID
		}
	}
}

func versionFromCoverage(tx *gorm.DB, original *FileLocation) (*FileVersion, error) {
	// A matching signature must not hide contradictory content facts on any candidate copy.
	version := &FileVersion{FileID: original.FileID, Signature: original.Signature, Hash: original.Hash,
		Size: original.Size, Mode: original.Mode, MtimeNS: original.MtimeNS}
	var after int64
	for {
		var copies []*Position
		if err := tx.Where("signature = ? AND is_dir = ? AND id > ?", original.Signature, false, after).
			Order("id").Limit(batchSize).Find(&copies).Error; err != nil {
			return nil, fmt.Errorf("read original coverage failed, file_id=%d, %w", original.FileID, err)
		}
		if len(copies) == 0 {
			break
		}
		for _, copy := range copies {
			if copy.Size != version.Size {
				return nil, fmt.Errorf("covered content size disagrees, file_id=%d position_id=%d", original.FileID, copy.ID)
			}
			if len(copy.Hash) > 0 {
				if len(version.Hash) > 0 && !bytes.Equal(copy.Hash, version.Hash) {
					return nil, fmt.Errorf("covered content hash disagrees, file_id=%d position_id=%d", original.FileID, copy.ID)
				}
				version.Hash = copy.Hash
			}
			after = copy.ID
		}
	}

	// Existing archive dates are evidence; copy mtimes and the association time are not.
	var dates struct {
		FirstArchivedAt *int64
		LastArchivedAt  *int64
	}
	if err := tx.Model(&FileVersion{}).
		Select("MIN(first_archived_at) AS first_archived_at, MAX(last_archived_at) AS last_archived_at").
		Where("signature = ? AND size = ?", version.Signature, version.Size).
		Where("hash = ? OR hash IS NULL", version.Hash).Scan(&dates).Error; err != nil {
		return nil, fmt.Errorf("read evidenced archive dates failed, file_id=%d, %w", original.FileID, err)
	}
	version.FirstArchivedAt, version.LastArchivedAt = dates.FirstArchivedAt, dates.LastArchivedAt
	return version, nil
}
