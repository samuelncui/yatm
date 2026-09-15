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

// AdmitObservation publishes a checked ordinary file, never a physical directory cache.
// The caller holds the Location gate and performs all filesystem I/O before this transaction.
func (l *Library) AdmitObservation(ctx context.Context, locationID int64, token string, p *OnlinePosition) (*FileLocation, error) {
	var result *FileLocation
	err := l.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var location Location
		if err := tx.First(&location, locationID).Error; err != nil {
			return err
		}
		if location.BindingToken != token || location.Binding != entity.OnlineBinding_CONFIRMED {
			return ErrOnlineConflict
		}
		// Reading an unchanged observation does not publish a new catalog revision.
		previous, err := matchingObservation(tx, &location, p)
		if err != nil {
			return err
		}
		if previous != nil {
			result = previous
			return nil
		}
		result, err = admitObservation(tx, &location, p)
		if err != nil {
			return err
		}
		return tx.Save(&location).Error
	})
	return result, err
}

// MatchesObservation checks whether the existing content basis remains applicable to new facts.
func (l *Library) MatchesObservation(ctx context.Context, location *Location, p *OnlinePosition) (bool, error) {
	previous, err := matchingObservation(l.db.WithContext(ctx), location, p)
	return previous != nil, err
}

func matchingObservation(tx *gorm.DB, location *Location, p *OnlinePosition) (*FileLocation, error) {
	if p == nil {
		return nil, fmt.Errorf("missing observation")
	}
	if p.Independent {
		return nil, nil
	}
	if p.CopyResult != nil && p.CopyResult.AdmittedFileID == nil {
		return nil, nil
	}
	var previous FileLocation
	if err := tx.Where("location_id = ? AND path = ?", location.ID, p.Path).Limit(1).Find(&previous).Error; err != nil {
		return nil, err
	}
	if previous.FileID == 0 || !previous.CurrentBinding(location) || previous.Size != p.Size || previous.Mode != p.Mode || previous.MtimeNS != p.MtimeNS {
		return nil, nil
	}
	if p.FileID != 0 && previous.FileID != p.FileID {
		return nil, nil
	}
	if len(p.Hash) > 0 && !bytes.Equal(p.Hash, previous.Hash) {
		return nil, nil
	}
	if len(p.Signature) > 0 && !bytes.Equal(p.Signature, previous.Signature) {
		return nil, nil
	}
	if p.TrackingKeys != nil {
		var keys []*FileTrackingKey
		if err := tx.Where("file_id = ?", previous.FileID).Find(&keys).Error; err != nil {
			return nil, err
		}
		if len(keys) != len(p.TrackingKeys) {
			return nil, nil
		}
		for _, observed := range p.TrackingKeys {
			matches := false
			for _, stored := range keys {
				if observed.Kind == stored.Kind && observed.Scope == stored.Scope && bytes.Equal(observed.KeyValue, stored.KeyValue) && observed.Details == stored.Details {
					matches = true
					break
				}
			}
			if !matches {
				return nil, nil
			}
		}
	}
	return &previous, nil
}

func admitObservation(tx *gorm.DB, location *Location, p *OnlinePosition) (*FileLocation, error) {
	// Path continuity wins for ordinary observations; Analyze may supply an already resolved File.
	if p == nil || !fs.FileMode(p.Mode).IsRegular() || p.Size < 0 {
		return nil, fmt.Errorf("invalid file observation")
	}
	if err := entity.ValidateRelativePath(p.Path); err != nil {
		return nil, err
	}
	if err := validateCopyAdmission(tx, location, p); err != nil {
		return nil, err
	}
	var previous FileLocation
	if err := tx.Where("location_id = ? AND path = ?", location.ID, p.Path).Limit(1).Find(&previous).Error; err != nil {
		return nil, err
	}
	if p.Independent && previous.FileID != 0 {
		return nil, ErrOnlineConflict
	}
	if p.FileID == 0 {
		p.FileID = previous.FileID
	}
	if previous.FileID != 0 && previous.FileID != p.FileID {
		return nil, ErrOnlineConflict
	}
	if p.FileID == 0 {
		file, err := createImportedFile(tx, "Unforged/"+location.Name+"/"+p.Path)
		if err != nil {
			return nil, err
		}
		p.FileID = file.ID
	}
	var file File
	if err := tx.First(&file, p.FileID).Error; err != nil {
		return nil, err
	}
	if file.Kind != entity.FileKind_FILE_KIND_REGULAR {
		return nil, ErrOnlineConflict
	}
	var occupied FileLocation
	if err := tx.Where("file_id = ?", p.FileID).Limit(1).Find(&occupied).Error; err != nil {
		return nil, err
	}
	if occupied.FileID != 0 && (occupied.LocationID != location.ID || occupied.Path != p.Path) {
		return nil, ErrOnlineConflict
	}

	// Preserve applicable content only. Metadata changes must not inherit old backup coverage.
	unchanged := previous.CurrentBinding(location) && previous.Size == p.Size && previous.Mode == p.Mode && previous.MtimeNS == p.MtimeNS
	if unchanged && len(p.TrackingKeys) > 0 {
		var oldNative FileTrackingKey
		if err := tx.Where("file_id = ? AND kind = ?", previous.FileID, TrackingNative).Limit(1).Find(&oldNative).Error; err != nil {
			return nil, err
		}
		for _, key := range p.TrackingKeys {
			if key.Kind == TrackingNative && oldNative.FileID != 0 && (key.Scope != oldNative.Scope || !bytes.Equal(key.KeyValue, oldNative.KeyValue) || key.Details != oldNative.Details) {
				unchanged = false
			}
		}
	}
	if len(p.Signature) == 0 && len(p.Hash) == 0 && unchanged {
		p.Signature, p.Hash = previous.Signature, previous.Hash
	}
	original := &FileLocation{FileID: p.FileID, LocationID: location.ID, Path: p.Path, Size: p.Size, Mode: p.Mode,
		MtimeNS: p.MtimeNS, Hash: p.Hash, Signature: p.Signature, ObservedBindingToken: location.BindingToken}
	if err := tx.Save(original).Error; err != nil {
		return nil, err
	}
	if p.TrackingKeys != nil {
		if err := tx.Where("file_id = ?", p.FileID).Delete(&FileTrackingKey{}).Error; err != nil {
			return nil, err
		}
		for _, key := range p.TrackingKeys {
			key.FileID, key.LocationID, key.ObservedAt = p.FileID, location.ID, time.Now().UnixMilli()
			if err := tx.Create(key).Error; err != nil {
				return nil, err
			}
		}
	}
	if err := reconcileCoveredVersions(tx, tx.Where("file_id = ?", p.FileID)); err != nil {
		return nil, err
	}
	if err := consumeCopyAdmission(tx, p); err != nil {
		return nil, err
	}
	return original, nil
}
