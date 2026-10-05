package library

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"unicode/utf8"
)

const (
	pageCursorVersion         = byte(1)
	searchCursorKind          = byte(1)
	tagCursorKind             = byte(2)
	duplicateGroupCursorKind  = byte(3)
	duplicateMemberCursorKind = byte(4)
	fileListCursorKind        = byte(5)
	pageCursorPrefix          = 2 + 16 + 8
	maxPageCursorSize         = 4096
)

func encodePageCursor(kind byte, input, key string, id int64) string {
	// Bind the opaque cursor to the request that produced it.
	fingerprint := sha256.Sum256([]byte(input))
	value := make([]byte, pageCursorPrefix+len(key))
	value[0] = pageCursorVersion
	value[1] = kind
	copy(value[2:18], fingerprint[:16])
	binary.BigEndian.PutUint64(value[18:26], uint64(id))
	copy(value[26:], key)
	return base64.RawURLEncoding.EncodeToString(value)
}

func decodePageCursor(value string, kind byte, input string) (string, int64, bool, error) {
	if value == "" {
		return "", 0, false, nil
	}
	if len(value) > maxPageCursorSize {
		return "", 0, false, fmt.Errorf("page cursor exceeds %d bytes", maxPageCursorSize)
	}

	// Decode and validate the versioned cursor envelope before using its sort key.
	decoded, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil {
		return "", 0, false, fmt.Errorf("decode page cursor failed, %w", err)
	}
	if len(decoded) < pageCursorPrefix {
		return "", 0, false, fmt.Errorf("page cursor is truncated")
	}
	if decoded[0] != pageCursorVersion {
		return "", 0, false, fmt.Errorf("unsupported page cursor version %d", decoded[0])
	}
	if decoded[1] != kind {
		return "", 0, false, fmt.Errorf("page cursor belongs to another operation")
	}
	fingerprint := sha256.Sum256([]byte(input))
	if !bytes.Equal(decoded[2:18], fingerprint[:16]) {
		return "", 0, false, fmt.Errorf("page cursor belongs to another query")
	}
	key := string(decoded[26:])
	if !utf8.ValidString(key) {
		return "", 0, false, fmt.Errorf("page cursor contains invalid UTF-8")
	}
	return key, int64(binary.BigEndian.Uint64(decoded[18:26])), true, nil
}
