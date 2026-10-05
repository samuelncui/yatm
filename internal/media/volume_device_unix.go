//go:build linux || darwin || freebsd

package media

import (
	"os"
	"path/filepath"
	"syscall"
)

// SeparateFilesystem reports whether root is a filesystem boundary rather than a directory
// on its parent's filesystem. It compares device numbers and never mounts anything.
func SeparateFilesystem(root string) bool {
	info, err := os.Stat(root)
	if err != nil {
		return false
	}
	self, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return false
	}
	parent, err := os.Stat(filepath.Dir(root))
	if err != nil {
		return false
	}
	above, ok := parent.Sys().(*syscall.Stat_t)
	if !ok {
		return false
	}
	return self.Dev != above.Dev
}
