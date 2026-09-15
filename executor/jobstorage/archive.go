package jobstorage

import "github.com/samuelncui/yatm/entity"

// ArchiveItem is the single GORM representation shared by Archive runners and Media sessions.
type ArchiveItem struct {
	ID     int64             `gorm:"primaryKey"`
	Status entity.CopyStatus `gorm:"not null;index:idx_items_status_target,priority:1;index:idx_items_status_media,priority:1"`
	Size   int64             `gorm:"not null"`

	TargetPath string `gorm:"type:varchar(4096);not null;default:'';uniqueIndex:idx_items_target_path,length:750;index:idx_items_status_target,priority:2,length:750"`
	MediaPath  string `gorm:"type:varchar(4096);not null;default:'';index:idx_items_status_media,priority:2,length:750"`
	MediaID    *int64
	Data       *entity.ArchiveManifestFile `gorm:"type:blob;not null"`
	Result     *entity.ArchiveCopyResult   `gorm:"type:blob"`
}

func (ArchiveItem) TableName() string {
	return "items"
}
