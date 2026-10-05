package legacy

import (
	"database/sql"
	"database/sql/driver"
	"strings"
)

var (
	_ = sql.Scanner((*sortPath)(nil))
	_ = driver.Valuer(sortPath(""))
)

const sortPathSeparator = "\x00"

// sortPath preserves the ordering encoding used only by transitional v0.1.x Job tables.
type sortPath string

func (s sortPath) Value() (driver.Value, error) {
	return []byte(strings.ReplaceAll(string(s), "/", sortPathSeparator)), nil
}

func (s *sortPath) Scan(value any) error {
	*s = sortPath(strings.ReplaceAll(string(value.([]byte)), sortPathSeparator, "/"))
	return nil
}

func (s sortPath) String() string {
	return string(s)
}
