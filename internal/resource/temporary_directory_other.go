//go:build !linux && !darwin && !freebsd

package resource

import "fmt"

func temporaryParentIdentity(path string) (uint64, uint64, error) {
	return 0, 0, fmt.Errorf("Restore probe parent identity is unavailable on this platform: %q", path)
}
