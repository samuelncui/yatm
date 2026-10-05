package dataformat

import (
	"fmt"
	"time"
)

// Nanoseconds converts a source instant to the persisted signed Unix nanosecond format.
// A missing source time keeps the established zero sentinel instead of overflowing UnixNano.
func Nanoseconds(value time.Time) (int64, error) {
	if value.IsZero() {
		return 0, nil
	}
	stamp := value.UnixNano()
	if !time.Unix(0, stamp).Equal(value) {
		return 0, fmt.Errorf("timestamp %s is outside the signed Unix nanosecond range", value.Format(time.RFC3339Nano))
	}
	return stamp, nil
}
