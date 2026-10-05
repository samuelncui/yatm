package restore

import "github.com/samuelncui/yatm/entity"

// Copy is one persisted physical candidate in the Restore Job bundle.
type Copy struct {
	ID                int64             `gorm:"primaryKey;index:idx_copies_media_status_order,priority:5;index:idx_copies_media_order,priority:2"`
	ItemID            int64             `gorm:"not null;uniqueIndex:idx_copies_item_media,priority:1;index:idx_copies_item_status,priority:1"`
	Status            entity.CopyStatus `gorm:"not null;index:idx_copies_status;index:idx_copies_item_status,priority:2;index:idx_copies_media_status_order,priority:2"`
	MediaID           int64             `gorm:"not null;uniqueIndex:idx_copies_item_media,priority:2;index:idx_copies_media_status_order,priority:1;index:idx_copies_media_order,priority:1"`
	MediaPath         string            `gorm:"type:text;not null;index:idx_copies_media_status_order,priority:4"`
	StorageOrder      []byte            `gorm:"type:blob;not null;default:(X'');index:idx_copies_media_status_order,priority:3"`
	MediaIdentity     string
	MediaProfile      *entity.MediaProfile `gorm:"type:blob"`
	PositionID        int64
	Health            entity.PositionHealth
	HealthCheckedAtNS int64 `json:"health_checked_at_ns,string"`
	HealthPublished   bool
}

func (Copy) TableName() string {
	return "copies"
}

// copyCandidate joins one physical copy row with the selected File facts needed at runtime.
// It is assembled explicitly and is never passed to GORM.
type copyCandidate struct {
	ID           int64
	ItemID       int64
	MediaID      int64
	MediaPath    string
	StorageOrder []byte
	PositionID   int64

	FileID        int64
	FileVersionID int64
	Signature     []byte
	Hash          []byte
	Size          int64
	Mode          uint32
	MtimeNS       int64 `json:"mtime_ns,string"`
	TargetPath    string
}
