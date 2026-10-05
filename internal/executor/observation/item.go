// Package observation stores bounded filesystem observations and resolves deterministic File continuity.
// It has no Executor dependency and performs no filesystem I/O or Library publication.
package observation

import (
	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/internal/library"
)

type Item struct {
	Directory  bool
	ID         int64             `gorm:"primaryKey"`
	Path       string            `gorm:"type:varchar(4096);not null;uniqueIndex"`
	ScopePath  string            `gorm:"type:varchar(4096);index"`
	Change     entity.ScanChange `gorm:"not null;index"`
	NeedsHash  bool              `gorm:"not null;index:idx_observations_hash"`
	Size       int64
	Mode       uint32
	MtimeNS    int64  `json:"mtime_ns,string"`
	Hash       []byte `gorm:"column:sha256;type:varbinary(32)"`
	Signature  []byte `gorm:"type:varbinary(256);index"`
	FileID     int64  `gorm:"index"`
	PositionID int64
	Before     *entity.ObservedEntry    `gorm:"type:blob"`
	Reference  *entity.LocationEntryRef `gorm:"serializer:json;type:blob"`
	Evidence   Evidence                 `gorm:"embedded"`
}

func (Item) TableName() string { return "observations" }

// Position states this observation as the manifest entry a publication consumes.
func (i *Item) Position() *library.ObservedEntry {
	return &library.ObservedEntry{Path: i.Path, FileID: i.FileID,
		Size: i.Size, Mode: i.Mode, MtimeNS: i.MtimeNS, Hash: i.Hash, Signature: i.Signature, TrackingKeys: i.Evidence.Keys()}
}
