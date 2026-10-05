package restore

import "github.com/samuelncui/yatm/entity"

const batchSize = 256

type Config struct {
	ID   int64                  `gorm:"primaryKey;autoIncrement:false;check:id = 1"`
	Spec *entity.RestoreJobSpec `gorm:"type:blob;not null"`
	// Only the offline legacy migrator sets this frozen output root; new Jobs use Destination.
	LegacyRoot     string
	ManifestFrozen bool
}

func (Config) TableName() string {
	return "config"
}
