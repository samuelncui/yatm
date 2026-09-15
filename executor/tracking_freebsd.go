package executor

import "syscall"

func nativeLifetime(stat *syscall.Stat_t) (int64, uint64) {
	return stat.Birthtimespec.Nano(), stat.Gen
}
