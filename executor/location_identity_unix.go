//go:build linux || darwin || freebsd

package executor

import (
	"fmt"
	"os"
	"syscall"
)

func locationNativeIdentity(info os.FileInfo) string {
	// Device/inode without a scope is not a portable identity across filesystems.
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return ""
	}
	birth, generation := nativeLifetime(stat)
	return fmt.Sprintf("%d:%d:%d:%d", stat.Dev, stat.Ino, birth, generation)
}
