package library

import (
	"context"
	"fmt"
	"path"

	"github.com/samuelncui/yatm/entity"
	"gorm.io/gorm"
)

// FileLocation is the single current online original of an independently organized File.
type FileLocation struct {
	FileID               int64  `gorm:"primaryKey;autoIncrement:false" json:"file_id"`
	LocationID           int64  `gorm:"not null;uniqueIndex:idx_file_locations_path,priority:1;index:idx_file_locations_parent,priority:1" json:"location_id"`
	Path                 string `gorm:"type:varchar(4096);not null;uniqueIndex:idx_file_locations_path,priority:2,length:750" json:"path"`
	ParentPath           string `gorm:"type:varchar(4096);index:idx_file_locations_parent,priority:2,length:750" json:"parent_path"`
	Size                 int64  `json:"size"`
	Mode                 uint32 `json:"mode"`
	MtimeNS              int64  `json:"mtime_ns"`
	Signature            []byte `gorm:"type:varbinary(256);index:idx_file_locations_signature" json:"signature"`
	Hash                 []byte `gorm:"type:varbinary(32)" json:"hash"`
	ObservedBindingToken string `gorm:"type:varchar(36)" json:"observed_binding_token"`
}

func (p *FileLocation) BeforeSave(*gorm.DB) error {
	if p.FileID <= 0 || p.LocationID <= 0 {
		return fmt.Errorf("original requires a File and Location")
	}
	if err := entity.ValidateRelativePath(p.Path); err != nil {
		return err
	}
	p.ParentPath = originalParent(p.Path)
	p.Signature = nullableSignature(p.Signature)
	return nil
}

func (p *FileLocation) ToEntity() *entity.FileLocation {
	return &entity.FileLocation{FileId: p.FileID, LocationId: p.LocationID, Path: p.Path, ParentPath: p.ParentPath,
		Size: p.Size, Mode: p.Mode, MtimeNs: p.MtimeNS, Signature: p.Signature, Sha256: p.Hash,
		ObservedBindingToken: p.ObservedBindingToken}
}

// CurrentBinding checks association validity, not current physical existence or content.
func (p *FileLocation) CurrentBinding(location *Location) bool {
	return location != nil && location.Binding == entity.OnlineBinding_CONFIRMED &&
		location.BindingToken != "" && p.ObservedBindingToken == location.BindingToken
}

func originalParent(value string) string {
	parent := path.Dir(value)
	if parent == "." {
		return ""
	}
	return parent + "/"
}

func nullableSignature(value []byte) []byte {
	if len(value) == 0 {
		return nil
	}
	return value
}

func (l *Library) GetFileLocation(ctx context.Context, fileID int64) (*FileLocation, error) {
	var value FileLocation
	if err := l.db.WithContext(ctx).Where("file_id = ?", fileID).Limit(1).Find(&value).Error; err != nil {
		return nil, fmt.Errorf("get original failed, %w", err)
	}
	if value.FileID == 0 {
		return nil, nil
	}
	return &value, nil
}

func (l *Library) GetFileLocationAtPath(ctx context.Context, locationID int64, relative string) (*FileLocation, error) {
	var value FileLocation
	if err := l.db.WithContext(ctx).Where("location_id = ? AND path = ?", locationID, relative).Limit(1).Find(&value).Error; err != nil {
		return nil, fmt.Errorf("get original path owner failed, %w", err)
	}
	if value.FileID == 0 {
		return nil, nil
	}
	return &value, nil
}
