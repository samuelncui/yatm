package entity

import (
	"database/sql/driver"

	"google.golang.org/protobuf/encoding/protojson"
)

func (x *PreviewSettings) Scan(src any) error           { return Scan(x, src) }
func (x *PreviewSettings) Value() (driver.Value, error) { return Value(x) }

func (x *PreviewJobSettings) Scan(src any) error           { return Scan(x, src) }
func (x *PreviewJobSettings) Value() (driver.Value, error) { return Value(x) }

// Persisted Settings use the protobuf JSON shape for nested generator oneofs.
func (x *PreviewSettings) MarshalJSON() ([]byte, error)    { return protojson.Marshal(x) }
func (x *PreviewSettings) UnmarshalJSON(data []byte) error { return protojson.Unmarshal(data, x) }
