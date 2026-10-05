package scan

import "github.com/samuelncui/yatm/entity"

type Config struct {
	ID                 int64                      `gorm:"primaryKey;autoIncrement:false;check:id = 1"`
	Spec               *entity.ScanJobSpec        `gorm:"type:blob;not null"`
	PreviewJobSettings *entity.PreviewJobSettings `gorm:"type:blob"`
	MediaKind          entity.MediaKind
	MediaIdentity      string
	MediaProfile       *entity.MediaProfile `gorm:"type:blob"`
	IndexedInput       bool
	BaselineFrozen     bool
}

func (Config) TableName() string { return "config" }
