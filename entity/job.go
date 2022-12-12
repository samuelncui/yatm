package entity

import (
	"database/sql"
	"database/sql/driver"
)

var (
	_ = sql.Scanner(&JobParam{})
	_ = driver.Valuer(&JobParam{})
)

func (x *JobParam) Scan(src any) error {
	return Scan(x, src)
}

func (x *JobParam) Value() (driver.Value, error) {
	return Value(x)
}

var (
	_ = sql.Scanner(&JobState{})
	_ = driver.Valuer(&JobState{})
)

func (x *JobState) Scan(src any) error {
	return Scan(x, src)
}

func (x *JobState) Value() (driver.Value, error) {
	return Value(x)
}
