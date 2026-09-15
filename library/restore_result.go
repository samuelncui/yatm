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
	"time"

	"github.com/google/uuid"
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

// RestoreResult is successful recovery provenance, independent of a mutable original or Job checkpoint.
type RestoreResult struct {
	OperationID     string         `gorm:"primaryKey;type:varchar(36)" json:"operation_id"`
	ItemID          int64          `gorm:"primaryKey;autoIncrement:false" json:"item_id"`
	SourceFileID    int64          `gorm:"index" json:"source_file_id"`
	SourceVersionID int64          `json:"source_version_id"`
	ResultFileID    int64          `gorm:"index" json:"result_file_id"`
	LocationID      int64          `gorm:"index" json:"location_id"`
	BindingToken    string         `gorm:"type:varchar(36)" json:"binding_token"`
	Path            string         `gorm:"type:varchar(4096)" json:"path"`
	Signature       []byte         `gorm:"type:varbinary(256)" json:"signature"`
	ExpectedHash    []byte         `gorm:"type:varbinary(32)" json:"expected_hash"`
	ExpectedSize    int64          `json:"expected_size"`
	ActualHash      []byte         `gorm:"type:varbinary(32)" json:"actual_hash"`
	ActualSize      int64          `json:"actual_size"`
	Outcome         RestoreOutcome `gorm:"type:varchar(24)" json:"outcome"`
	RestoredAt      int64          `gorm:"autoCreateTime:milli" json:"restored_at_ms"`
}

// RestorePublication supplies verified filesystem observations; the caller holds the destination gate.
type RestorePublication struct {
	Result    RestoreResult
	ParentID  int64
	Name      string
	Reconnect bool
	Original  *FileLocation
	Tracking  []*FileTrackingKey
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

func (l *Library) GetRestoreResult(ctx context.Context, operationID string, itemID int64) (*RestoreResult, error) {
	var result RestoreResult
	err := l.db.WithContext(ctx).Where("operation_id = ? AND item_id = ?", operationID, itemID).First(&result).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read Restore result failed, %w", err)
	}
	if result.BindingToken == "" {
		return nil, fmt.Errorf("imported Restore result cannot authorize a local retry")
	}
	return &result, nil
}

func (l *Library) PublishRestore(ctx context.Context, publication *RestorePublication) (*RestoreResult, error) {
	// Reject incomplete caller facts before a transaction can create organization or provenance.
	if publication == nil {
		return nil, fmt.Errorf("Restore publication is missing")
	}
	result := publication.Result
	if _, err := uuid.Parse(result.OperationID); err != nil {
		return nil, fmt.Errorf("Restore operation identity is invalid, %w", err)
	}
	if result.ItemID <= 0 || result.SourceFileID <= 0 || result.SourceVersionID <= 0 || result.LocationID <= 0 {
		return nil, fmt.Errorf("Restore publication identities are invalid")
	}
	if result.BindingToken == "" {
		return nil, ErrOnlineUnverified
	}
	if len(result.Signature) == 0 || len(result.ExpectedHash) != 32 || len(result.ActualHash) != 32 || result.ExpectedSize < 0 || result.ActualSize < 0 {
		return nil, fmt.Errorf("Restore publication content facts are incomplete")
	}
	if err := entity.ValidateRelativePath(result.Path); err != nil {
		return nil, err
	}

	// One Library commit publishes both the result identity and its original association.
	err := l.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var stored RestoreResult
		err := tx.Where("operation_id = ? AND item_id = ?", result.OperationID, result.ItemID).First(&stored).Error
		if err == nil {
			if stored.BindingToken == "" {
				return fmt.Errorf("imported Restore result cannot authorize a local retry")
			}
			if stored.SourceFileID != result.SourceFileID || stored.SourceVersionID != result.SourceVersionID ||
				stored.LocationID != result.LocationID || stored.BindingToken != result.BindingToken || stored.Path != result.Path ||
				!bytes.Equal(stored.Signature, result.Signature) || !bytes.Equal(stored.ExpectedHash, result.ExpectedHash) || stored.ExpectedSize != result.ExpectedSize {
				return fmt.Errorf("Restore operation was already published with different facts")
			}
			result = stored
			return nil
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}

		// A local binding generation also protects against imported/reused numeric identities.
		var location Location
		if err := tx.First(&location, result.LocationID).Error; err != nil {
			return err
		}
		if location.Binding != entity.OnlineBinding_CONFIRMED || location.BindingToken == "" {
			return ErrOnlineUnverified
		}
		if location.BindingToken != result.BindingToken {
			return ErrOnlineConflict
		}
		var version FileVersion
		if err := tx.First(&version, result.SourceVersionID).Error; err != nil {
			return err
		}
		if version.FileID != result.SourceFileID || version.Size != result.ExpectedSize ||
			!bytes.Equal(version.Signature, result.Signature) || !bytes.Equal(version.Hash, result.ExpectedHash) {
			return fmt.Errorf("Restore source version identity changed")
		}

		// Complete damaged bytes and ignored outputs are provenance only, never indexed originals.
		matched := result.ExpectedSize == result.ActualSize && bytes.Equal(result.ExpectedHash, result.ActualHash)
		if !matched {
			if result.Outcome != RestoreDamaged {
				return fmt.Errorf("Restore output does not match the selected version")
			}
			result.ResultFileID = 0
			return tx.Create(&result).Error
		}
		if OnlineExcluded(location.Exclusions, result.Path) {
			result.Outcome, result.ResultFileID = RestoreIgnored, 0
			return tx.Create(&result).Error
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
		if !publication.Reconnect || original.FileID != 0 {
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
		observation.Path, observation.ObservedBindingToken = result.Path, location.BindingToken
		observation.Signature = result.Signature
		if err := tx.Create(&observation).Error; err != nil {
			return err
		}
		if err := tx.Where("file_id = ?", result.ResultFileID).Delete(&FileTrackingKey{}).Error; err != nil {
			return err
		}
		for _, key := range publication.Tracking {
			key.FileID, key.LocationID, key.ObservedAt = result.ResultFileID, location.ID, time.Now().UnixMilli()
			if err := tx.Create(key).Error; err != nil {
				return err
			}
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
		return tx.Create(&result).Error
	})
	if err != nil {
		return nil, fmt.Errorf("publish Restore result failed, %w", err)
	}
	return &result, nil
}

func newRestoredFile(tx *gorm.DB, parentID int64, name string, versionID int64) (*File, error) {
	// Freeze the source's logical parent; never guess another placement after it disappears.
	if parentID != 0 {
		var parent File
		if err := tx.First(&parent, parentID).Error; err != nil {
			return nil, err
		}
		if parent.Kind != entity.FileKind_FILE_KIND_DIRECTORY {
			return nil, fmt.Errorf("Restore Library parent is not a directory")
		}
	}
	if name == "" || name == "." || name == ".." || strings.ContainsAny(name, "/\\\x00") {
		return nil, fmt.Errorf("Restore Library filename is invalid")
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
		if err := tx.Model(&File{}).Where("parent_id = ? AND name = ?", parentID, candidate).Count(&count).Error; err != nil {
			return nil, err
		}
		if count != 0 {
			continue
		}
		file := &File{ParentID: parentID, Name: candidate, Kind: entity.FileKind_FILE_KIND_REGULAR}
		if err := tx.Create(file).Error; err != nil {
			return nil, err
		}
		return file, nil
	}
}
