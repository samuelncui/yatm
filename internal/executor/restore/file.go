package restore

// File freezes one selected version and its final output.
// It reserves an item's final path across indexing and transfer retries.
type File struct {
	ItemID           int64  `gorm:"primaryKey;autoIncrement:false"`
	Path             string `gorm:"not null;uniqueIndex:idx_restore_file_path,where:path <> ''"`
	FileID           int64  `gorm:"index"`
	FileVersionID    int64
	ParentID         int64
	Name             string
	Reconnect        bool
	LastArchivedAtNS *int64 `json:"last_archived_at_ns,string,omitempty"`
	DesiredPath      string
	Signature        []byte `gorm:"type:varbinary(256)"`
	Hash             []byte `gorm:"type:varbinary(32)"`
	Size             int64
	Mode             uint32
	MtimeNS          int64 `json:"mtime_ns,string"`
	Completed        bool
	ResultFileID     int64
	Linked           bool
	Damaged          bool
	ActualHash       []byte `gorm:"type:varbinary(32)"`
	ActualSize       int64
	ResultMessage    string
	Ready            bool
	Finalized        bool
	ReadMediaID      int64
}

func (File) TableName() string { return "files" }
