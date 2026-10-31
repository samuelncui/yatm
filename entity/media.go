package entity

import (
	"database/sql"
	"database/sql/driver"
	"fmt"
	"strings"
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

// NormalizeTapeBarcode returns the six-character upper-case form of a user-supplied Tape barcode.
func NormalizeTapeBarcode(value string) (string, error) {
	barcode := strings.ToUpper(strings.TrimSpace(value))
	if len(barcode) != 6 {
		return "", fmt.Errorf("Tape barcode must contain 6 characters")
	}
	for _, character := range barcode {
		if (character < 'A' || character > 'Z') && (character < '0' || character > '9') {
			return "", fmt.Errorf("Tape barcode contains an invalid character, barcode=%q", barcode)
		}
	}
	return barcode, nil
}
