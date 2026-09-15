package entity

import "database/sql/driver"

func (x *OnlineExclusions) Scan(src any) error           { return Scan(x, src) }
func (x *OnlineExclusions) Value() (driver.Value, error) { return Value(x) }
func (x *Location) Scan(src any) error                   { return Scan(x, src) }
func (x *Location) Value() (driver.Value, error)         { return Value(x) }
func (x *OnlinePosition) Scan(src any) error             { return Scan(x, src) }
func (x *OnlinePosition) Value() (driver.Value, error)   { return Value(x) }
func (x *ExpectedFile) Scan(src any) error               { return Scan(x, src) }
func (x *ExpectedFile) Value() (driver.Value, error)     { return Value(x) }
