package library

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"strconv"
	"strings"

	"github.com/samuelncui/yatm/entity"
	"gorm.io/gorm"
)

type RestoreOutcome string

const (
	RestoreReconnected RestoreOutcome = "reconnected"
	RestoreNewFile     RestoreOutcome = "new_file"
	RestoreIgnored     RestoreOutcome = "ignored"
	RestoreDamaged     RestoreOutcome = "damaged"
)

// RestoreResult is the outcome returned by a successful Library publication.
type RestoreResult struct {
	SourceFileID    int64          `json:"source_file_id"`
	SourceVersionID int64          `json:"source_version_id"`
	ResultFileID    int64          `json:"result_file_id"`
	LocationID      int64          `json:"location_id"`
	Path            string         `json:"path"`
	Signature       []byte         `json:"signature"`
	ExpectedHash    []byte         `json:"expected_hash"`
	ExpectedSize    int64          `json:"expected_size"`
	ActualHash      []byte         `json:"actual_hash"`
	ActualSize      int64          `json:"actual_size"`
	Outcome         RestoreOutcome `json:"outcome"`
}

// RestorePublication supplies verified filesystem observations; the caller holds the destination gate.
type RestorePublication struct {
	Result    RestoreResult
	ParentID  int64
	Name      string
	Reconnect bool
	Original  *FileLocation
	Tracking  []*FileTrackingKey
	// Version is the trusted immutable Job snapshot, never supplied by an API caller.
	Version *FileVersion
}

// RestoredName marks an independent recovered item without changing the source's organization.
func RestoredName(name string, versionID int64) string {
	ext := path.Ext(name)
	stem := strings.TrimSuffix(name, ext)
	if stem == "" {
		stem, ext = name, ""
	}
	return stem + ".restored." + strconv.FormatInt(versionID, 36) + ext
}

func (l *Library) PublishRestore(ctx context.Context, publication *RestorePublication) (*RestoreResult, error) {
	// Reject incomplete caller facts before changing Library organization.
	if publication == nil {
		return nil, fmt.Errorf("Restore publication is missing")
	}
	result := publication.Result
	if result.SourceFileID <= 0 || result.SourceVersionID <= 0 || result.LocationID <= 0 {
		return nil, fmt.Errorf("Restore publication identities are invalid")
	}
	if len(result.Signature) == 0 || len(result.ExpectedHash) != 32 || len(result.ActualHash) != 32 || result.ExpectedSize < 0 || result.ActualSize < 0 {
		return nil, fmt.Errorf("Restore publication content facts are incomplete")
	}
	if err := entity.ValidateRelativePath(result.Path); err != nil {
		return nil, err
	}

	// Publish the restored original and any new logical File in one metadata commit.
	err := l.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var location Location
		if err := tx.First(&location, result.LocationID).Error; err != nil {
			return err
		}

		// A frozen Job remains authoritative after catalog history is merged or removed.
		var version FileVersion
		err := tx.Where("id = ? AND file_id = ?", result.SourceVersionID, result.SourceFileID).First(&version).Error
		sourceExists := err == nil
		if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		if sourceExists && (version.Size != result.ExpectedSize || !bytes.Equal(version.Signature, result.Signature) ||
			!bytes.Equal(version.Hash, result.ExpectedHash)) {
			return fmt.Errorf("Restore source version identity changed")
		}
		if publication.Version != nil {
			version = *publication.Version
		} else if !sourceExists {
			return err
		}
		if version.ID != result.SourceVersionID || version.FileID != result.SourceFileID || version.Size != result.ExpectedSize ||
			!bytes.Equal(version.Signature, result.Signature) || !bytes.Equal(version.Hash, result.ExpectedHash) {
			return fmt.Errorf("Restore source version identity changed")
		}

		// Damaged and ignored outputs return their outcome without creating original associations.
		matched := result.ExpectedSize == result.ActualSize && bytes.Equal(result.ExpectedHash, result.ActualHash)
		if !matched {
			if result.Outcome != RestoreDamaged {
				return fmt.Errorf("Restore output does not match the selected version")
			}
			result.ResultFileID = 0
			return nil
		}
		if location.Ignored(result.Path, false) {
			result.Outcome, result.ResultFileID = RestoreIgnored, 0
			return nil
		}
		if publication.Original == nil || !fs.FileMode(publication.Original.Mode).IsRegular() ||
			publication.Original.Size != result.ActualSize || !bytes.Equal(publication.Original.Hash, result.ActualHash) {
			return fmt.Errorf("Restore output observation is incomplete")
		}

		// Even a stale index owns its path; content equality does not authorize taking another File's slot.
		var owner FileLocation
		if err := tx.Where("location_id = ? AND path = ?", location.ID, result.Path).Limit(1).Find(&owner).Error; err != nil {
			return err
		}
		if owner.FileID != 0 {
			return fmt.Errorf("Restore output is already linked to File %d", owner.FileID)
		}
		var original FileLocation
		if err := tx.Where("file_id = ?", version.FileID).Limit(1).Find(&original).Error; err != nil {
			return err
		}
		result.ResultFileID, result.Outcome = version.FileID, RestoreReconnected
		if !publication.Reconnect || !sourceExists || original.FileID != 0 {
			file, err := newRestoredFile(tx, publication.ParentID, publication.Name, version.ID)
			if err != nil {
				return err
			}
			result.ResultFileID, result.Outcome = file.ID, RestoreNewFile
			version.ID, version.FileID = 0, file.ID
			if _, err := recordVersion(tx, &version); err != nil {
				return err
			}
		}

		// Publish only this output; a Restore never creates cached physical directory rows.
		observation := *publication.Original
		observation.FileID, observation.LocationID = result.ResultFileID, location.ID
		observation.Path = result.Path
		observation.Signature = result.Signature
		if err := tx.Create(&observation).Error; err != nil {
			return err
		}
		if err := replaceTrackingKeys(tx, result.ResultFileID, location.ID, publication.Tracking); err != nil {
			return err
		}
		for parent := path.Dir(result.Path); parent != "."; parent = path.Dir(parent) {
			var count int64
			if err := tx.Model(&FileLocation{}).Where("location_id = ? AND path = ?", location.ID, parent).Count(&count).Error; err != nil {
				return err
			}
			if count != 0 {
				return fmt.Errorf("Restore output parent is an indexed file: %q", parent)
			}
		}
		if err := tx.Save(&location).Error; err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("publish Restore result failed, %w", err)
	}
	return &result, nil
}

func newRestoredFile(tx *gorm.DB, parentID int64, name string, versionID int64) (*File, error) {
	// Freeze the source's logical parent; never guess another placement after it disappears.
	if parentID != 0 {
		var parent fileRow
		if err := tx.First(&parent, parentID).Error; err != nil {
			return nil, err
		}
		if parent.Kind != entity.FileKind_FILE_KIND_DIRECTORY {
			return nil, fmt.Errorf("Restore Library parent is not a directory")
		}
	}
	if err := entity.ValidatePathComponent(name); err != nil {
		return nil, fmt.Errorf("Restore Library filename is invalid, %w", err)
	}

	// Only a new name receives suffixes; existing organization and annotations are untouched.
	base := RestoredName(name, versionID)
	ext := path.Ext(base)
	stem := strings.TrimSuffix(base, ext)
	for suffix := 1; ; suffix++ {
		candidate := base
		if suffix > 1 {
			candidate = fmt.Sprintf("%s (%d)%s", stem, suffix, ext)
		}
		var count int64
		if err := tx.Model(ModelFile).Where("parent_id = ? AND name = ?", parentID, candidate).Count(&count).Error; err != nil {
			return nil, err
		}
		if count != 0 {
			continue
		}
		file := &File{ParentID: parentID, Name: candidate, Kind: entity.FileKind_FILE_KIND_REGULAR}
		if err := createFileRow(tx, file); err != nil {
			return nil, err
		}
		return file, nil
	}
}
