package entity

import "database/sql/driver"

func (x *IgnoreRules) Scan(src any) error           { return Scan(x, src) }
func (x *IgnoreRules) Value() (driver.Value, error) { return Value(x) }
func (x *Location) Scan(src any) error              { return Scan(x, src) }
func (x *Location) Value() (driver.Value, error)    { return Value(x) }
func (x *ObservedEntry) Scan(src any) error         { return Scan(x, src) }
func (x *ObservedEntry) Value() (driver.Value, error) {
	return Value(x)
}
func (x *ExpectedFile) Scan(src any) error           { return Scan(x, src) }
func (x *ExpectedFile) Value() (driver.Value, error) { return Value(x) }
