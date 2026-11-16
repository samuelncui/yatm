package executor

import (
	"encoding/hex"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/unix"
)

const (
	// FS_IOC_GETFSUUID is _IOR(0x15, 0, struct fsuuid2) from Linux 6.5 uapi/linux/fs.h.
	fsIocGetFSUUID = 0x80111500
	zfsSuperMagic  = 0x2fc12fc1
)

type fsuuid2 struct {
	length uint8
	uuid   [16]byte
}

// filesystemScope names the superblock UUID, or ZFS's dataset-derived fsid; any other filesystem,
// including one whose statfs fsid only encodes the device number, has no stable identity.
func filesystemScope(directory string) string {
	fd, err := unix.Open(directory, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return ""
	}
	defer unix.Close(fd)
	var value fsuuid2
	if _, _, errno := unix.Syscall(unix.SYS_IOCTL, uintptr(fd), fsIocGetFSUUID, uintptr(unsafe.Pointer(&value))); errno == 0 {
		if value.length > 0 && int(value.length) <= len(value.uuid) {
			return "fs:" + hex.EncodeToString(value.uuid[:value.length])
		}
	}
	var stat unix.Statfs_t
	if err := unix.Fstatfs(fd, &stat); err != nil || stat.Type != zfsSuperMagic {
		return ""
	}
	return "zfs:" + hex.EncodeToString(unsafe.Slice((*byte)(unsafe.Pointer(&stat.Fsid)), unsafe.Sizeof(stat.Fsid)))
}

func nativeStat(info os.FileInfo) (nativeObject, bool) {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return nativeObject{}, false
	}
	object := nativeObject{device: stat.Dev, inode: stat.Ino}
	if birth, ok := info.(interface{ birth() int64 }); ok {
		object.birthNS = birth.birth()
	}
	return object, true
}

// Lstat observes one entry with statx, recording the birth time a filesystem reports in the same
// call; a kernel without statx falls back to lstat and leaves the birth time unknown.
func Lstat(path string) (os.FileInfo, error) {
	var stat unix.Statx_t
	err := unix.Statx(unix.AT_FDCWD, path, unix.AT_SYMLINK_NOFOLLOW, unix.STATX_BASIC_STATS|unix.STATX_BTIME, &stat)
	if errors.Is(err, unix.ENOSYS) {
		return os.Lstat(path)
	}
	if err != nil {
		return nil, &fs.PathError{Op: "lstat", Path: path, Err: err}
	}
	return newStatxInfo(filepath.Base(path), &stat), nil
}

type statxInfo struct {
	name    string
	size    int64
	mode    fs.FileMode
	modTime time.Time
	birthNS int64
	sys     syscall.Stat_t
}

func newStatxInfo(name string, stat *unix.Statx_t) *statxInfo {
	info := &statxInfo{name: name, size: int64(stat.Size), mode: fs.FileMode(stat.Mode & 0o777),
		modTime: time.Unix(stat.Mtime.Sec, int64(stat.Mtime.Nsec))}
	switch uint32(stat.Mode) & unix.S_IFMT {
	case unix.S_IFBLK:
		info.mode |= fs.ModeDevice
	case unix.S_IFCHR:
		info.mode |= fs.ModeDevice | fs.ModeCharDevice
	case unix.S_IFDIR:
		info.mode |= fs.ModeDir
	case unix.S_IFIFO:
		info.mode |= fs.ModeNamedPipe
	case unix.S_IFLNK:
		info.mode |= fs.ModeSymlink
	case unix.S_IFSOCK:
		info.mode |= fs.ModeSocket
	}
	if stat.Mode&unix.S_ISGID != 0 {
		info.mode |= fs.ModeSetgid
	}
	if stat.Mode&unix.S_ISUID != 0 {
		info.mode |= fs.ModeSetuid
	}
	if stat.Mode&unix.S_ISVTX != 0 {
		info.mode |= fs.ModeSticky
	}
	if stat.Mask&unix.STATX_BTIME != 0 {
		info.birthNS = stat.Btime.Sec*int64(time.Second) + int64(stat.Btime.Nsec)
	}
	// Existing consumers read only the device and inode through the ordinary stat shape.
	info.sys = syscall.Stat_t{Dev: unix.Mkdev(stat.Dev_major, stat.Dev_minor), Ino: stat.Ino, Mode: uint32(stat.Mode), Size: int64(stat.Size)}
	return info
}

func (i *statxInfo) Name() string       { return i.name }
func (i *statxInfo) Size() int64        { return i.size }
func (i *statxInfo) Mode() fs.FileMode  { return i.mode }
func (i *statxInfo) ModTime() time.Time { return i.modTime }
func (i *statxInfo) IsDir() bool        { return i.mode.IsDir() }
func (i *statxInfo) Sys() any           { return &i.sys }
func (i *statxInfo) birth() int64       { return i.birthNS }
