package entity

import (
	"database/sql/driver"
	"fmt"
	reflect "reflect"
	sync "sync"

	"github.com/modern-go/reflect2"
	"google.golang.org/protobuf/proto"
)

var (
	typeMap sync.Map
)

// Scan implement database/sql.Scanner
func Scan(dst proto.Message, src interface{}) error {
	cacheKey := reflect2.RTypeOf(dst)
	typ, has := loadType(cacheKey)
	if !has {
		ptrType := reflect.TypeOf(dst)
		if ptrType.Kind() != reflect.Ptr {
			return fmt.Errorf("scan dst is not an ptr, has= %T", dst)
		}

		typ = reflect2.Type2(ptrType.Elem())
		storeType(cacheKey, typ)
	}
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

func loadType(key uintptr) (reflect2.Type, bool) {
	i, has := typeMap.Load(key)
	if !has {
		return nil, false
	}
	return i.(reflect2.Type), true
}

func storeType(key uintptr, typ reflect2.Type) {
	typeMap.Store(key, typ)
}
