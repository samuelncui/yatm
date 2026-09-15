package entity

import (
	"database/sql"
	"database/sql/driver"
)

var (
	_ = sql.Scanner(&MediaProfile{})
	_ = driver.Valuer(&MediaProfile{})
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
