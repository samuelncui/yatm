package archive

import "github.com/samuelncui/yatm/entity"

type Config struct {
	ID           int64                  `gorm:"primaryKey;autoIncrement:false;check:id = 1"`
	Spec         *entity.ArchiveJobSpec `gorm:"type:blob;not null"`
	Preview      *entity.ScanJobSpec    `gorm:"type:blob"`
	PreviewJobID int64
	PreviewError string
}

func (Config) TableName() string {
	return "config"
}
