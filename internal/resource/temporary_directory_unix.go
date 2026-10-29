//go:build linux || darwin || freebsd

package resource

import (
	"fmt"
	"os"
	"syscall"
)

func temporaryParentIdentity(path string) (uint64, uint64, error) {
	// A real parent must still name the filesystem that held the registered probe.
	info, err := os.Lstat(path)
	if err != nil {
		return 0, 0, err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !info.IsDir() || !ok {
		return 0, 0, fmt.Errorf("Restore probe parent is not a real native directory: %q", path)
	}
	return uint64(stat.Dev), uint64(stat.Ino), nil
}
