// Package observation stores bounded filesystem observations and resolves deterministic File continuity.
// It has no Executor dependency and performs no filesystem I/O or Library publication.
package observation

import (
	"bytes"

	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/library"
)

const BatchSize = 256

type Item struct {
	ID          int64             `gorm:"primaryKey"`
	Path        string            `gorm:"type:varchar(4096);not null;uniqueIndex"`
	ScopePath   string            `gorm:"type:varchar(4096);index"`
	Change      entity.ScanChange `gorm:"not null;index"`
	NeedsHash   bool              `gorm:"not null;index:idx_observations_hash"`
	Size        int64
	Mode        uint32
	MtimeNS     int64
	Hash        []byte `gorm:"type:varbinary(32)"`
	Signature   []byte `gorm:"type:varbinary(256);index"`
	FileID      int64  `gorm:"index"`
	PositionID  int64
	Before      *entity.OnlinePosition       `gorm:"type:blob"`
	Reference   *entity.LocationEntryRef     `gorm:"serializer:json;type:blob"`
	CopyResult  *library.FileOperationResult `gorm:"serializer:json;type:blob"`
	Independent bool
	Evidence    Evidence `gorm:"embedded"`
}

func (Item) TableName() string { return "observations" }

func (i *Item) Position(sourceID int64) *library.OnlinePosition {
	return &library.OnlinePosition{ID: i.PositionID, SourceID: sourceID, Path: i.Path, FileID: i.FileID,
		Size: i.Size, Mode: i.Mode, MtimeNS: i.MtimeNS, Hash: i.Hash, Signature: i.Signature, TrackingKeys: i.Evidence.Keys(),
		CopyResult: i.CopyResult, Independent: i.Independent}
}

// Evidence is private matching input, not a user-configurable identity policy.
type Evidence struct {
	NativeScope string `gorm:"index:,composite:native,priority:1"`
	NativeKey   []byte `gorm:"type:varbinary(256);index:,composite:native,priority:2"`
	BirthNS     int64
	Generation  uint64
	UUIDScope   string `gorm:"index:,composite:uuid,priority:1"`
	UUID        []byte `gorm:"type:varbinary(256);index:,composite:uuid,priority:2"`
}

func FromKeys(keys []*library.FileTrackingKey) Evidence {
	// Convert known mechanisms explicitly; unsupported evidence never changes matching policy.
	var evidence Evidence
	for _, key := range keys {
		switch key.Kind {
		case library.TrackingNative:
			evidence.NativeScope, evidence.NativeKey = key.Scope, key.KeyValue
			evidence.BirthNS, evidence.Generation = key.Details.BirthNS, key.Details.Generation
		case library.TrackingUUID:
			evidence.UUIDScope, evidence.UUID = key.Scope, key.KeyValue
		}
	}
	return evidence
}

func (e Evidence) Keys() []*library.FileTrackingKey {
	var keys []*library.FileTrackingKey
	if len(e.NativeKey) > 0 {
		keys = append(keys, &library.FileTrackingKey{Kind: library.TrackingNative, Scope: e.NativeScope, KeyValue: e.NativeKey,
			Details: library.TrackingDetails{BirthNS: e.BirthNS, Generation: e.Generation}})
	}
	if len(e.UUID) > 0 {
		keys = append(keys, &library.FileTrackingKey{Kind: library.TrackingUUID, Scope: e.UUIDScope, KeyValue: e.UUID})
	}
	return keys
}

func (e Evidence) ReplacedNative(keys []*library.FileTrackingKey) bool {
	// Missing native evidence proves neither continuity nor replacement; metadata rules still apply.
	if e.NativeScope == "" || len(e.NativeKey) == 0 {
		return false
	}
	for _, key := range keys {
		if key.Kind == library.TrackingNative && len(key.KeyValue) > 0 {
			return key.Scope != e.NativeScope || !bytes.Equal(key.KeyValue, e.NativeKey) ||
				key.Details.BirthNS != e.BirthNS || key.Details.Generation != e.Generation
		}
	}
	return false
}

type Original struct {
	FileID    int64 `gorm:"primaryKey;autoIncrement:false"`
	Path      string
	Signature []byte `gorm:"type:varbinary(256)"`
	Hash      []byte `gorm:"type:varbinary(32)"`
	Size      int64
	Evidence  Evidence `gorm:"embedded"`
}

func (Original) TableName() string { return "originals" }
