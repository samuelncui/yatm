package executor

import "syscall"

func nativeLifetime(*syscall.Stat_t) (int64, uint64) { return 0, 0 }
