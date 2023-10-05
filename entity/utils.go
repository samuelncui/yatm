package entity

import (
	"bytes"
	"database/sql/driver"
	"fmt"
	"io"

	"github.com/klauspost/compress/zstd"
	"github.com/modern-go/reflect2"
	"github.com/samuelncui/yatm/tools"
	"google.golang.org/protobuf/proto"
)

const (
	compressThreshold = 1024
)

var (
	magicHeaderV2 = []byte{0xff, 'y', 'm', '\x02'}

	zstdEncoderPool = tools.NewPool(func() *zstd.Encoder {
		encoder, _ := zstd.NewWriter(nil) // there will be no error without options
		return encoder
	})
	zstdDecoderPool = tools.NewPool(func() *zstd.Decoder {
		decoder, _ := zstd.NewReader(nil) // there will be no error without options
		return decoder
	})
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

	if bytes.HasPrefix(buf, magicHeaderV2) {
		decoder := zstdDecoderPool.Get()

		err := decoder.Reset(bytes.NewBuffer(buf[len(magicHeaderV2):]))
		if err != nil {
			return fmt.Errorf("zstd reset decoder fail, %w", err)
		}

		buf, err = io.ReadAll(decoder)
		if err != nil {
			return fmt.Errorf("zstd read decoder fail, %w", err)
		}

		decoder.Reset(nil)
		zstdDecoderPool.Put(decoder)
	}

	if err := proto.Unmarshal(buf, dst); err != nil {
		return fmt.Errorf("process define extra scanner, protobuf unmarshal fail, %w", err)
	}

	return nil
}

// Value implement database/sql/driver.Valuer
func Value(src proto.Message) (driver.Value, error) {
	buf, err := proto.Marshal(src)
	if err != nil {
		return nil, fmt.Errorf("process define extra valuer, protobuf marshal fail, %w", err)
	}

	if len(buf) <= compressThreshold {
		return buf, nil
	}

	buffer := bytes.NewBuffer(make([]byte, 0, len(buf)))
	buffer.Write(magicHeaderV2)

	encoder := zstdEncoderPool.Get()
	encoder.Reset(buffer)
	_, err = encoder.Write(buf)
	if err != nil {
		return nil, fmt.Errorf("zstd write to encoder fail, %w", err)
	}
	err = encoder.Close()
	if err != nil {
		return nil, fmt.Errorf("zstd close encoder fail, %w", err)
	}

	buf = buffer.Bytes()
	encoder.Reset(nil)
	zstdEncoderPool.Put(encoder)

	return buf, nil
}
