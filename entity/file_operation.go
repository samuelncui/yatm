package entity

import (
	"database/sql/driver"

	"google.golang.org/protobuf/encoding/protojson"
)

func (x *LocationFileFacts) Scan(src any) error           { return Scan(x, src) }
func (x *LocationFileFacts) Value() (driver.Value, error) { return Value(x) }

// MarshalJSON keeps timestamps exact when a containing manifest row uses a JSON serializer.
func (x *LocationFileFacts) MarshalJSON() ([]byte, error) {
	return (protojson.MarshalOptions{UseProtoNames: true}).Marshal(x)
}

func (x *LocationFileFacts) UnmarshalJSON(data []byte) error {
	return protojson.Unmarshal(data, x)
}
