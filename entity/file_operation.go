package entity

import "database/sql/driver"

func (x *LocationFileFacts) Scan(src any) error           { return Scan(x, src) }
func (x *LocationFileFacts) Value() (driver.Value, error) { return Value(x) }
