package library

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

const identicalTempName = ".identical-results"

// PrepareIdenticalTemp owns disposable Find and component SQLite files for one service lifetime.
// The returned release must run after requests and retained results have closed.
func (l *Library) PrepareIdenticalTemp(work string) (func() error, error) {
	// Lock the configured Work directory before inspecting an earlier run's results.
	if err := os.MkdirAll(work, 0700); err != nil {
		return nil, fmt.Errorf("create identical work directory failed, %w", err)
	}
	lockPath := filepath.Join(work, identicalTempName+".lock")
	fd, err := unix.Open(lockPath, unix.O_CREAT|unix.O_RDWR|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0600)
	if err != nil {
		return nil, fmt.Errorf("open identical result lock failed, %w", err)
	}
	lock := os.NewFile(uintptr(fd), lockPath)
	if err := unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB); err != nil {
		_ = lock.Close()
		return nil, fmt.Errorf("acquire identical result lock failed, %w", err)
	}

	// Only this private directory can contain results eligible for startup cleanup.
	root := filepath.Join(work, identicalTempName)
	if err := prepareIdenticalTempRoot(root); err != nil {
		_ = lock.Close()
		return nil, err
	}
	l.identicalTempRoot = root
	return lock.Close, nil
}

func prepareIdenticalTempRoot(root string) error {
	// Reject a redirected or shared directory before removing any prior-run entries.
	info, err := os.Lstat(root)
	if os.IsNotExist(err) {
		if err := os.Mkdir(root, 0700); err != nil {
			return fmt.Errorf("create identical result directory failed, %w", err)
		}
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect identical result directory failed, %w", err)
	}
	if !info.IsDir() || info.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("identical result directory must be a private directory: %s", root)
	}

	// Prior runs may have left partially built snapshots; never inspect or remove other paths.
	entries, err := os.ReadDir(root)
	if err != nil {
		return fmt.Errorf("list identical result directory failed, %w", err)
	}
	for _, entry := range entries {
		if !strings.HasPrefix(entry.Name(), "yatm-identical-") {
			continue
		}
		if err := os.RemoveAll(filepath.Join(root, entry.Name())); err != nil {
			return fmt.Errorf("remove stale identical result %q failed, %w", entry.Name(), err)
		}
	}
	return nil
}

func (l *Library) identicalDirectory(prefix string) (string, error) {
	return os.MkdirTemp(l.identicalTempRoot, prefix)
}
