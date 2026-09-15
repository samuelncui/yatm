package library

import (
	"context"
	"database/sql/driver"
	"encoding/json"
	"fmt"

	"github.com/samuelncui/yatm/entity"
)

type TrackingKind string

const (
	TrackingNative TrackingKind = "native_object"
	TrackingUUID   TrackingKind = "yatm_uuid"
)

// TrackingDetails holds native lifetime guards, not extensible user configuration.
type TrackingDetails struct {
	BirthNS    int64  `json:"birth_ns,omitempty"`
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
	FileID     int64           `gorm:"primaryKey;autoIncrement:false" json:"file_id"`
	Kind       TrackingKind    `gorm:"primaryKey;type:varchar(32);index:idx_file_tracking_lookup,priority:1" json:"kind"`
	Scope      string          `gorm:"type:varchar(256);index:idx_file_tracking_lookup,priority:2" json:"scope"`
	KeyValue   []byte          `gorm:"type:varbinary(256);index:idx_file_tracking_lookup,priority:3" json:"key_value"`
	Details    TrackingDetails `gorm:"type:blob" json:"details"`
	ObservedAt int64           `json:"observed_at_ms"`
	LocationID int64           `gorm:"index" json:"location_id"`
}

// TrackingCandidatesPage excludes every original still bound to another Location.
// A retained key without a FileLocation is eligible only after a successful removal.
func (l *Library) TrackingCandidatesPage(ctx context.Context, location *Location, after int64, limit int) ([]*FileTrackingKey, error) {
	ids := l.db.WithContext(ctx).Model(&FileTrackingKey{}).Select("file_tracking_keys.file_id").
		Joins("JOIN locations ON locations.id = file_tracking_keys.location_id").
		Joins("LEFT JOIN file_locations ON file_locations.file_id = file_tracking_keys.file_id").
		Where("file_tracking_keys.file_id > ? AND locations.executor_id = ? AND locations.binding = ?", after, location.ExecutorID, entity.OnlineBinding_CONFIRMED).
		Where("file_locations.file_id IS NULL OR file_locations.location_id = ?", location.ID).
		Group("file_tracking_keys.file_id").Order("file_tracking_keys.file_id").Limit(limit)
	var keys []*FileTrackingKey
	err := l.db.WithContext(ctx).Where("file_id IN (?)", ids).Order("file_id, kind").Find(&keys).Error
	return keys, err
}
