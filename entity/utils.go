package entity

import (
	"database/sql/driver"
	"fmt"

	"github.com/modern-go/reflect2"
	"google.golang.org/protobuf/proto"
)

// Scan implement database/sql.Scanner
func Scan(dst proto.Message, src interface{}) error {
	typ := reflect2.TypeOf(dst).(reflect2.PtrType).Elem()
	typ.Set(dst, typ.New())

	var buf []byte
	switch v := src.(type) {
	case string:
		buf = []byte(v)
	case []byte:
		buf = v
	case nil:
		return nil
	default:
		return fmt.Errorf("process define extra scanner, unexpected type for i18n, %T", v)
	}

	if len(buf) == 0 {
		return nil
	}

	if err := proto.Unmarshal(buf, dst); err != nil {
		return fmt.Errorf("process define extra scanner, json unmarshal fail, %w", err)
	}
	return nil
}

// Value implement database/sql/driver.Valuer
func Value(src proto.Message) (driver.Value, error) {
	buf, err := proto.Marshal(src)
	if err != nil {
		return nil, fmt.Errorf("process define extra valuer, json marshal fail, %w", err)
	}
	return buf, nil
}
