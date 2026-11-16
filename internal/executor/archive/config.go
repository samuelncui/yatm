package archive

import "github.com/samuelncui/yatm/entity"

type Config struct {
	ID           int64                  `gorm:"primaryKey;autoIncrement:false;check:id = 1"`
	Spec         *entity.ArchiveJobSpec `gorm:"type:blob;not null"`
	Preview      *entity.ScanJobSpec    `gorm:"type:blob"`
	PreviewJobID int64
	PreviewError string

	// Files a target Media count left out: content that already reached it, during preparation or
	// before a copy, and content the chosen Media already held.
	SkippedTargetReached  int64 `gorm:"not null;default:0"`
	SkippedAlreadyOnMedia int64 `gorm:"not null;default:0"`
}

func (Config) TableName() string {
	return "config"
}
