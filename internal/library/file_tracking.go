package library

import (
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
