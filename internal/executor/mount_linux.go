package executor

import (
	"fmt"

	"golang.org/x/sys/unix"
)

// SameMount checks the native mount identity without following a leaf symlink.
func SameMount(root, filename string) (bool, error) {
	// Device numbers alone cannot distinguish a bind mount of the same filesystem.
	var rootStat, fileStat unix.Statx_t
	flags := unix.AT_SYMLINK_NOFOLLOW | unix.AT_NO_AUTOMOUNT
	if err := unix.Statx(unix.AT_FDCWD, root, flags, unix.STATX_MNT_ID, &rootStat); err != nil {
		return false, err
	}
	if err := unix.Statx(unix.AT_FDCWD, filename, flags, unix.STATX_MNT_ID, &fileStat); err != nil {
		return false, err
	}
	if rootStat.Mask&unix.STATX_MNT_ID == 0 || fileStat.Mask&unix.STATX_MNT_ID == 0 {
		return false, fmt.Errorf("filesystem operation requires kernel mount identity support")
	}
	return rootStat.Mnt_id == fileStat.Mnt_id, nil
}
