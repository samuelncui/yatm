package executor

import (
	"golang.org/x/sys/unix"
	"syscall"
)

const missingXattr = unix.ENODATA

func nativeLifetime(*syscall.Stat_t) (int64, uint64) { return 0, 0 }
