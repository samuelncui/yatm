package library

import (
	"bytes"
	"context"
	"database/sql/driver"
	"encoding/json"
	"fmt"
)

type TrackingKind string

const (
	TrackingNative TrackingKind = "native_object"
)

// TrackingDetails holds native lifetime guards, not extensible user configuration.
type TrackingDetails struct {
	BirthNS    int64  `json:"birth_ns,omitempty,string"`
	Generation uint64 `json:"generation,omitempty"`
}

func (d *TrackingDetails) Scan(value any) error {
	if value == nil {
		*d = TrackingDetails{}
		return nil
	}
	switch value := value.(type) {
	case []byte:
		return json.Unmarshal(value, d)
	case string:
		return json.Unmarshal([]byte(value), d)
	default:
		return fmt.Errorf("invalid native tracking details %T", value)
	}
}

func (d TrackingDetails) Value() (driver.Value, error) { return json.Marshal(d) }

// SameObject reports whether two keys name the same object: equal kind, scope and value, and equal
// lifetime guards wherever both observations recorded one. Keys of different scopes prove nothing.
func (k *FileTrackingKey) SameObject(other *FileTrackingKey) bool {
	if k.Kind != other.Kind || k.Scope == "" || k.Scope != other.Scope || !bytes.Equal(k.KeyValue, other.KeyValue) {
		return false
	}
	if k.Details.BirthNS != 0 && other.Details.BirthNS != 0 && k.Details.BirthNS != other.Details.BirthNS {
		return false
	}
	return k.Details.Generation == 0 || other.Details.Generation == 0 || k.Details.Generation == other.Details.Generation
}

// NativeReplaced reports whether observed native evidence proves that the recorded object was replaced:
// an observation in the same filesystem names another object. Another filesystem proves nothing.
func NativeReplaced(recorded, observed []*FileTrackingKey) bool {
	for _, old := range recorded {
		if old.Kind != TrackingNative || old.Scope == "" || len(old.KeyValue) == 0 {
			continue
		}
		for _, key := range observed {
			if key.Kind == TrackingNative && key.Scope == old.Scope && !key.SameObject(old) {
				return true
			}
		}
	}
	return false
}

// SameTrackingKeys reports whether an observation recorded exactly the stored evidence, so publishing
// it would change nothing.
func SameTrackingKeys(stored, observed []*FileTrackingKey) bool {
	if len(stored) != len(observed) {
		return false
	}
	for _, key := range observed {
		matched := false
		for _, previous := range stored {
			if key.Kind == previous.Kind && key.Scope == previous.Scope && bytes.Equal(key.KeyValue, previous.KeyValue) && key.Details == previous.Details {
				matched = true
				break
			}
		}
		if !matched {
			return false
		}
	}
	return true
}

// TrackingConfirmed reports whether a fresh observation names the recorded objects, so the current
// content is the recorded one. Absent evidence on both sides confirms nothing more than metadata.
func TrackingConfirmed(stored, observed []*FileTrackingKey) bool {
	if len(stored) != len(observed) {
		return false
	}
	for _, key := range observed {
		matched := false
		for _, previous := range stored {
			if key.SameObject(previous) {
				matched = true
				break
			}
		}
		if !matched {
			return false
		}
	}
	return true
}

// FileTrackingKey supplies candidates, not unique ownership of an observed identifier.
type FileTrackingKey struct {
	FileID       int64           `gorm:"primaryKey;autoIncrement:false" json:"file_id"`
	Kind         TrackingKind    `gorm:"primaryKey;type:varchar(32);index:idx_file_tracking_lookup,priority:1" json:"kind"`
	Scope        string          `gorm:"type:varchar(256);index:idx_file_tracking_lookup,priority:2" json:"scope"`
	KeyValue     []byte          `gorm:"type:varbinary(256);index:idx_file_tracking_lookup,priority:3" json:"key_value"`
	Details      TrackingDetails `gorm:"type:blob" json:"details"`
	ObservedAtNS int64           `json:"observed_at_ns,string"`
	LocationID   int64           `gorm:"index" json:"location_id"`
}

// ReadFileTrackingKeys reads the retained evidence of several Files at once, keyed by File ID, so a
// caller that already holds recorded associations does not query once per row.
func (l *Library) ReadFileTrackingKeys(ctx context.Context, fileIDs []int64) (map[int64][]*FileTrackingKey, error) {
	result := make(map[int64][]*FileTrackingKey, len(fileIDs))
	if len(fileIDs) == 0 {
		return result, nil
	}
	var keys []*FileTrackingKey
	if err := l.readDB().WithContext(ctx).Where("file_id IN ?", fileIDs).Find(&keys).Error; err != nil {
		return nil, err
	}
	for _, key := range keys {
		result[key.FileID] = append(result[key.FileID], key)
	}
	return result, nil
}
