package entity

import (
	"database/sql"
	"database/sql/driver"

	"google.golang.org/protobuf/encoding/protojson"
)

var (
	_ = sql.Scanner(&StorageMetadata{})
	_ = driver.Valuer(&StorageMetadata{})
	_ = sql.Scanner(&StoragePosition{})
	_ = driver.Valuer(&StoragePosition{})
)

func (x *StoragePosition) Scan(src any) error           { return Scan(x, src) }
func (x *StoragePosition) Value() (driver.Value, error) { return Value(x) }

func (x *StorageMetadata) Scan(src any) error {
	return Scan(x, src)
}

func (x *StorageMetadata) Value() (driver.Value, error) {
	if x == nil {
		return nil, nil
	}
	return Value(x)
}

func (x *StorageMetadata) MarshalJSON() ([]byte, error) {
	return protojson.Marshal(x)
}

func (x *StorageMetadata) UnmarshalJSON(data []byte) error {
	return protojson.Unmarshal(data, x)
}
