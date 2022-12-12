package tools

import (
	"fmt"
	"syscall"
)

type FileSystem struct {
	TypeName      string
	MountPoint    string
	TotalSize     int64
	AvailableSize int64
}

func GetFileSystem(path string) (*FileSystem, error) {
	stat := new(syscall.Statfs_t)

	if err := syscall.Statfs(path, stat); err != nil {
		return nil, fmt.Errorf("read statfs fail, err= %w", err)
	}

	return &FileSystem{
		// TypeName:      UnpaddingInt8s(stat.Fstypename[:]),
		// MountPoint:    UnpaddingInt8s(stat.Mntonname[:]),
		TotalSize:     int64(stat.Blocks) * int64(stat.Bsize),
		AvailableSize: int64(stat.Bavail) * int64(stat.Bsize),
	}, nil
}
