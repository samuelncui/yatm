package fileops

import "golang.org/x/sys/unix"

func sameMount(root, filename string) (bool, error) {
	// Distinguish actual mount roots, including mounts of the same filesystem.
	var rootStat, fileStat unix.Statfs_t
	if err := unix.Statfs(root, &rootStat); err != nil {
		return false, err
	}
	if err := unix.Statfs(filename, &fileStat); err != nil {
		return false, err
	}
	return rootStat.Fsid == fileStat.Fsid && rootStat.Mntonname == fileStat.Mntonname, nil
}
