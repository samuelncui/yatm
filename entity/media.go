package entity

import (
	"database/sql"
	"database/sql/driver"
)

var (
	_ = sql.Scanner(&MediaProfile{})
	_ = driver.Valuer(&MediaProfile{})
	_ = sql.Scanner(&TapePositionMap{})
	_ = driver.Valuer(&TapePositionMap{})
)

func (x *MediaProfile) Scan(src any) error {
	return Scan(x, src)
}

func (x *MediaProfile) Value() (driver.Value, error) {
	if x == nil {
		return nil, nil
	}
	return Value(x)
}

func (x *TapePositionMap) Scan(src any) error {
	return Scan(x, src)
}

func (x *TapePositionMap) Value() (driver.Value, error) {
	if x == nil {
		return nil, nil
	}
	return Value(x)
}
