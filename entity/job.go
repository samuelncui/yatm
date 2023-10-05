package entity

import (
	"database/sql"
	"database/sql/driver"
)

const (
	JobStatusVisible = 128
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
	// Scan(x, src)
	// return nil
}

func (x *JobState) Value() (driver.Value, error) {
	return Value(x)
	// val, _ := Value(x)
	// return val, nil
}
