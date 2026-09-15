package archive

import (
	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/executor/jobstorage"
)

const batchSize = 256

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

// RawInput retains a Job's allocated File identity across complete indexing retries.
type RawInput struct {
	ID         int64  `gorm:"primaryKey;autoIncrement"`
	TargetPath string `gorm:"uniqueIndex:idx_raw_inputs_path,length:750;type:varchar(4096)"`
	FileID     int64
}

func (RawInput) TableName() string { return "raw_inputs" }

type Item = jobstorage.ArchiveItem
