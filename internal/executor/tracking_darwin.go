package executor

import (
	"syscall"
	"time"

	"github.com/samuelncui/yatm/internal/dataformat"
)

func nativeLifetime(stat *syscall.Stat_t) (int64, uint64) {
	// Unrepresentable optional birth evidence stays unknown; it must never wrap into another instant.
	birth, err := dataformat.Nanoseconds(time.Unix(stat.Birthtimespec.Sec, stat.Birthtimespec.Nsec))
	if err != nil {
		return 0, uint64(stat.Gen)
	}
	return birth, uint64(stat.Gen)
}
