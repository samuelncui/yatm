package tools

import (
	"database/sql"
	"database/sql/driver"
	"strings"
)

var (
	_ = sql.Scanner((*SortPath)(nil))
	_ = driver.Valuer(SortPath(""))

	ZeroByte = []byte{0x00}
	ZeroStr  = string(ZeroByte)
)

type SortPath string

func (s SortPath) Value() (driver.Value, error) {
	return []byte(strings.ReplaceAll(string(s), "/", ZeroStr)), nil
}

func (s *SortPath) Scan(value any) error {
	*s = SortPath(strings.ReplaceAll(string(value.([]byte)), ZeroStr, "/"))
	return nil
}

func (s SortPath) String() string {
	return string(s)
}
