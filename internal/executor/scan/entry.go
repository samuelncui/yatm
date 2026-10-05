package scan

import (
	"context"

	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/internal/executor/observation"
	"github.com/samuelncui/yatm/internal/library"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const batchSize = 256

// saveEntries bounds SQL parameters independently of ACP's delivery batch size.
func (r *runner) saveEntries(ctx context.Context, rows []*Entry) error {
	if len(rows) == 0 {
		return nil
	}
	return r.db.WithContext(ctx).
		Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "id"}}, UpdateAll: true}).
		CreateInBatches(rows, batchSize).Error
}

// Entry is the single durable manifest for inventory, original, preview and check work.
type Entry struct {
	ID           int64             `gorm:"primaryKey"`
	FileID       int64             `gorm:"index"`
	LocationID   int64             `gorm:"not null;uniqueIndex:idx_entries_path,priority:1"`
	Path         string            `gorm:"not null;uniqueIndex:idx_entries_path,priority:2"`
	ScopePath    string            `gorm:"index"`
	Change       entity.ScanChange `gorm:"index"`
	Size         int64
	Mode         uint32
	MtimeNS      int64                 `json:"mtime_ns,string"`
	SHA256       []byte                `gorm:"type:varbinary(32)"`
	Signature    []byte                `gorm:"type:varbinary(256);index"`
	NeedsHash    bool                  `gorm:"index"`
	Before       *entity.ObservedEntry `gorm:"type:blob"`
	After        *entity.ObservedEntry `gorm:"type:blob"`
	Expected     *entity.ExpectedFile  `gorm:"type:blob"`
	SourcePath   string
	PositionID   int64
	Storage      *entity.StoragePosition `gorm:"type:blob"`
	StorageOrder []byte                  `gorm:"type:blob;index"`
	Finding      entity.ScanFinding      `gorm:"index"`
	ActualSize   int64
	ActualHash   []byte
	ReadMode     uint32
	ReadMtimeNS  int64 `json:"read_mtime_ns,string"`
	CheckedAtNS  int64 `json:"checked_at_ns,string"`
	Detail       string
	Published    bool
	// PreviewError is why this entry has no Preview. An existing bundle is the content store's
	// answer, so a successful outcome is never recorded per entry.
	PreviewError   string
	MatchingCopies int64
	Compared       bool                 `gorm:"index"`
	Evidence       observation.Evidence `gorm:"embedded"`
}

func (Entry) TableName() string { return "entries" }

func (e *Entry) BeforeSave(*gorm.DB) error {
	if e.Change == entity.ScanChange_SCAN_CHANGE_UNSPECIFIED {
		e.Change = entity.ScanChange_SCAN_CHANGE_ADDED
	}
	if e.Finding == entity.ScanFinding_SCAN_FINDING_UNSPECIFIED {
		e.Finding = entity.ScanFinding_SCAN_FINDING_NOT_CHECKED
	}
	return nil
}

func (e *Entry) ToEntity() *entity.ScanEntry {
	return &entity.ScanEntry{Id: e.ID, LocationId: e.LocationID, Path: e.Path, Change: e.Change,
		SizeBytes: e.Size, Mode: e.Mode, MtimeNs: e.MtimeNS, Sha256: e.SHA256, Signature: e.Signature,
		Before: e.Before, After: e.After, PositionId: e.PositionID, Finding: e.Finding,
		ActualSizeBytes: e.ActualSize, ActualHash: e.ActualHash, CheckedAtNs: e.CheckedAtNS,
		Detail: e.Detail, PreviewError: e.PreviewError,
		MatchingCopies: e.MatchingCopies, Storage: e.Storage}
}

type Item = Entry
type Evidence = observation.Evidence

// Position states this Scan entry as the manifest entry a publication consumes.
func (e *Entry) Position() *library.ObservedEntry {
	return &library.ObservedEntry{Path: e.Path, FileID: e.FileID, Size: e.Size, Mode: e.Mode, MtimeNS: e.MtimeNS, Hash: e.SHA256, Signature: e.Signature, TrackingKeys: e.Evidence.Keys()}
}
