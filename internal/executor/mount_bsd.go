//go:build darwin || freebsd

package executor

import "golang.org/x/sys/unix"

// SameMount checks the native mount identity without following a leaf symlink.
func SameMount(root, filename string) (bool, error) {
	// Compare actual mount roots, not merely the device containing the objects.
	var rootStat, fileStat unix.Statfs_t
	if err := unix.Statfs(root, &rootStat); err != nil {
		return false, err
	}
	if err := unix.Statfs(filename, &fileStat); err != nil {
		return false, err
	}
	return rootStat.Fsid == fileStat.Fsid && rootStat.Mntonname == fileStat.Mntonname, nil
}
