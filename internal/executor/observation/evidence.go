package observation

import (
	"bytes"

	"github.com/samuelncui/yatm/internal/library"
)

// Evidence is private matching input, not a user-configurable identity policy.
type Evidence struct {
	NativeScope string `gorm:"index:,composite:native,priority:1"`
	NativeKey   []byte `gorm:"type:varbinary(256);index:,composite:native,priority:2"`
	BirthNS     int64  `json:"birth_ns,string"`
	Generation  uint64
}

func FromKeys(keys []*library.FileTrackingKey) Evidence {
	// Convert known mechanisms explicitly; unsupported evidence never changes matching policy.
	var evidence Evidence
	for _, key := range keys {
		switch key.Kind {
		case library.TrackingNative:
			evidence.NativeScope, evidence.NativeKey = key.Scope, key.KeyValue
			evidence.BirthNS, evidence.Generation = key.Details.BirthNS, key.Details.Generation
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
