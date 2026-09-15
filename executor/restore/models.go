package restore

import (
	"github.com/samuelncui/yatm/entity"
)

const batchSize = 256

type Config struct {
	ID   int64                  `gorm:"primaryKey;autoIncrement:false;check:id = 1"`
	Spec *entity.RestoreJobSpec `gorm:"type:blob;not null"`
	// Only the offline legacy migrator sets this frozen output root; new Jobs use Destination.
	LegacyRoot     string
	OperationID    string `gorm:"type:varchar(36)"`
	ManifestFrozen bool
}

func (Config) TableName() string {
	return "config"
}

type Copy struct {
	ID                   int64  `gorm:"primaryKey;index:idx_copies_media_status_order,priority:5"`
	FileID               int64  `gorm:"not null;index"`
	ItemID               int64  `gorm:"not null;uniqueIndex:idx_copies_item_media,priority:1;index:idx_copies_file_status,priority:1"`
	FileVersionID        int64  `gorm:"index"`
	Signature            []byte `gorm:"type:varbinary(256)"`
	Mode                 uint32
	MtimeNS              int64
	Status               entity.CopyStatus `gorm:"not null;index:idx_copies_status;index:idx_copies_file_status,priority:2;index:idx_copies_media_status_order,priority:2"`
	Size                 int64             `gorm:"not null"`
	Hash                 []byte            `gorm:"type:blob;not null"`
	TargetPath           string            `gorm:"type:text;not null;index:idx_copies_target"`
	MediaID              int64             `gorm:"not null;uniqueIndex:idx_copies_item_media,priority:2;index:idx_copies_media_status_order,priority:1"`
	MediaPath            string            `gorm:"type:text;not null;index:idx_copies_media_status_order,priority:4"`
	StorageOrder         []byte            `gorm:"type:blob;not null;default:(X'');index:idx_copies_media_status_order,priority:3"`
	MediaIdentity        string
	MediaProfile         *entity.MediaProfile `gorm:"type:blob"`
	PositionID           int64
	PositionContentToken []byte `gorm:"type:varbinary(32)"`
	Health               entity.PositionHealth
	HealthCheckedAt      int64
	HealthPublished      bool
	LibraryParentID      int64
	LibraryName          string
	LastArchivedAt       *int64
}

func (Copy) TableName() string {
	return "copies"
}

// Output reserves an item's final path across indexing and transfer retries.
type Output struct {
	ItemID        int64  `gorm:"primaryKey;autoIncrement:false"`
	Path          string `gorm:"not null;uniqueIndex"`
	Completed     bool
	ResultFileID  int64
	Linked        bool
	Damaged       bool
	ActualHash    []byte `gorm:"type:varbinary(32)"`
	ActualSize    int64
	ResultMessage string
	Ready         bool
	Finalized     bool
	ReadMediaID   int64
}

// FileSelection freezes the one selected version that may reconnect each File.
type FileSelection struct {
	FileID             int64 `gorm:"primaryKey;autoIncrement:false"`
	ReconnectVersionID int64
	ParentID           int64
	Name               string
}

func (FileSelection) TableName() string { return "files" }
