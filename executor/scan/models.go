package scan

import (
	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/executor/observation"
)

const batchSize = 256

type Config struct {
	ID             int64               `gorm:"primaryKey;autoIncrement:false;check:id = 1"`
	Spec           *entity.ScanJobSpec `gorm:"type:blob;not null"`
	MediaKind      entity.MediaKind
	MediaIdentity  string
	MediaProfile   *entity.MediaProfile `gorm:"type:blob"`
	IndexedInput   bool
	BaselineFrozen bool
}

func (Config) TableName() string { return "config" }

// Scope freezes one independent observation range, including failure and publication facts.
type Scope struct {
	ID          int64 `gorm:"primaryKey"`
	LocationID  int64 `gorm:"index"`
	Path        string
	Snapshot    *entity.Location `gorm:"type:blob"`
	Error       string
	PublishedAt int64
}

func (Scope) TableName() string { return "scopes" }

func (s *Scope) ToEntity() *entity.ScanScopeResult {
	return &entity.ScanScopeResult{Id: s.ID, LocationId: s.LocationID, Path: s.Path, Error: s.Error, PublishedAtMs: s.PublishedAt}
}

// Entry is the single durable manifest for inventory, original, preview and check work.
type Entry struct {
	ID             int64             `gorm:"primaryKey"`
	LocationID     int64             `gorm:"not null;uniqueIndex:idx_entries_path,priority:1"`
	Path           string            `gorm:"not null;uniqueIndex:idx_entries_path,priority:2"`
	ScopePath      string            `gorm:"index"`
	Change         entity.ScanChange `gorm:"index"`
	Size           int64
	Mode           uint32
	MtimeNs        int64
	SHA256         []byte                 `gorm:"type:varbinary(32)"`
	Signature      []byte                 `gorm:"type:varbinary(256);index"`
	NeedsHash      bool                   `gorm:"index"`
	Before         *entity.OnlinePosition `gorm:"type:blob"`
	After          *entity.OnlinePosition `gorm:"type:blob"`
	Expected       *entity.ExpectedFile   `gorm:"type:blob"`
	SourcePath     string
	PositionID     int64
	ContentToken   []byte
	Storage        *entity.StoragePosition `gorm:"type:blob"`
	StorageOrder   []byte                  `gorm:"type:blob;index"`
	Finding        entity.ScanFinding      `gorm:"index"`
	ActualSize     int64
	ActualHash     []byte
	ReadMode       uint32
	ReadMtimeNs    int64
	CheckedAt      int64
	Detail         string
	Stale          bool
	Published      bool
	Preview        entity.ScanPreviewOutcome
	PreviewError   string
	MatchingCopies int64
	Evidence       observation.Evidence `gorm:"embedded"`
}

func (Entry) TableName() string { return "entries" }
func (e *Entry) ToEntity() *entity.ScanEntry {
	return &entity.ScanEntry{Id: e.ID, LocationId: e.LocationID, Path: e.Path, Change: e.Change,
		Size: e.Size, Mode: e.Mode, MtimeNs: e.MtimeNs, Sha256: e.SHA256, Signature: e.Signature,
		Before: e.Before, After: e.After, PositionId: e.PositionID, Finding: e.Finding,
		ActualSize: e.ActualSize, ActualHash: e.ActualHash, CheckedAtMs: e.CheckedAt,
		Detail: e.Detail, Stale: e.Stale, Preview: e.Preview, PreviewError: e.PreviewError,
		MatchingCopies: e.MatchingCopies, Storage: e.Storage}
}

type Item = observation.Item
type Original = observation.Original
type Evidence = observation.Evidence
