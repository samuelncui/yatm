package executor

import (
	"encoding/hex"
	"os"
	"syscall"
	"time"
	"unsafe"

	"github.com/samuelncui/yatm/internal/dataformat"
	"golang.org/x/sys/unix"
)

const (
	attrBitMapCount = 5
	attrVolInfo     = 0x80000000
	attrVolUUID     = 0x00040000
)

type attrList struct {
	bitmapCount uint16
	reserved    uint16
	commonAttr  uint32
	volAttr     uint32
	dirAttr     uint32
	fileAttr    uint32
	forkAttr    uint32
}

// filesystemScope names the volume UUID of the mount holding directory.
func filesystemScope(directory string) string {
	var stat unix.Statfs_t
	if err := unix.Statfs(directory, &stat); err != nil {
		return ""
	}
	mount, err := unix.BytePtrFromString(unix.ByteSliceToString(stat.Mntonname[:]))
	if err != nil {
		return ""
	}
	request := attrList{bitmapCount: attrBitMapCount, volAttr: attrVolInfo | attrVolUUID}
	var reply struct {
		length uint32
		uuid   [16]byte
	}
	if _, _, errno := unix.Syscall6(unix.SYS_GETATTRLIST, uintptr(unsafe.Pointer(mount)), uintptr(unsafe.Pointer(&request)),
		uintptr(unsafe.Pointer(&reply)), unsafe.Sizeof(reply), 0, 0); errno != 0 || reply.length < uint32(unsafe.Sizeof(reply)) {
		return ""
	}
	if reply.uuid == [16]byte{} {
		return ""
	}
	return "fs:" + hex.EncodeToString(reply.uuid[:])
}

func nativeStat(info os.FileInfo) (nativeObject, bool) {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return nativeObject{}, false
	}
	object := nativeObject{device: uint64(stat.Dev), inode: stat.Ino, generation: uint64(stat.Gen)}
	// Unrepresentable optional birth evidence stays unknown; it must never wrap into another instant.
	if birth, err := dataformat.Nanoseconds(time.Unix(stat.Birthtimespec.Sec, stat.Birthtimespec.Nsec)); err == nil {
		object.birthNS = birth
	}
	return object, true
}

// Lstat observes one entry; Darwin's stat already reports the birth time.
func Lstat(path string) (os.FileInfo, error) { return os.Lstat(path) }
