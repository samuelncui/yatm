package executor

import (
	"golang.org/x/sys/unix"
	"syscall"
)

const missingXattr = unix.ENOATTR

func nativeLifetime(stat *syscall.Stat_t) (int64, uint64) {
	return stat.Birthtimespec.Nano(), uint64(stat.Gen)
}
