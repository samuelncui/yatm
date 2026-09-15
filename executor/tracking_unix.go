//go:build linux || darwin

package executor

import (
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"runtime"
	"syscall"

	"github.com/google/uuid"
	"github.com/samuelncui/yatm/library"
	"golang.org/x/sys/unix"
)

// ObserveTracking reads optional identity evidence from the same ordinary file that was observed.
func ObserveTracking(source *library.Location, name string, scanned os.FileInfo) ([]*library.FileTrackingKey, error) {
	return observeTracking(source, name, scanned, source.WriteTrackingUUID)
}

// ReadTracking observes existing evidence without creating tracking attributes.
func ReadTracking(source *library.Location, name string, scanned os.FileInfo) ([]*library.FileTrackingKey, error) {
	return observeTracking(source, name, scanned, false)
}

func observeTracking(source *library.Location, name string, scanned os.FileInfo, writeUUID bool) ([]*library.FileTrackingKey, error) {
	// Never attach evidence from a replaced path to a previously checked observation.
	f, err := os.Open(name)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !os.SameFile(scanned, info) || scanned.Size() != info.Size() || scanned.ModTime() != info.ModTime() || scanned.Mode() != info.Mode() {
		return nil, fmt.Errorf("file changed while observing tracking evidence: %q", name)
	}

	// Native keys are meaningful only within the Executor/filesystem namespace and lifetime guards.
	var keys []*library.FileTrackingKey
	if stat, ok := info.Sys().(*syscall.Stat_t); ok {
		value := make([]byte, 8)
		binary.BigEndian.PutUint64(value, stat.Ino)
		birth, generation := nativeLifetime(stat)
		keys = append(keys, &library.FileTrackingKey{Kind: library.TrackingNative,
			Scope: fmt.Sprintf("%s/fs/%d", source.ExecutorID, stat.Dev), KeyValue: value,
			Details: library.TrackingDetails{BirthNS: birth, Generation: generation}})
	}

	// Tracking attributes are optional evidence. Never replace an existing attribute.
	attribute := "user.yatm.tracking_uuid"
	if runtime.GOOS == "darwin" {
		attribute = "yatm.tracking_uuid"
	}
	buffer := make([]byte, 256)
	n, err := unix.Fgetxattr(int(f.Fd()), attribute, buffer)
	if missingAttribute(err) && writeUUID {
		value, randomErr := uuid.NewRandom()
		if randomErr == nil {
			_ = unix.Fsetxattr(int(f.Fd()), attribute, []byte(value.String()), unix.XATTR_CREATE)
			n, err = unix.Fgetxattr(int(f.Fd()), attribute, buffer)
		}
	}
	if err == nil && n > 0 {
		if value, err := uuid.Parse(string(buffer[:n])); err == nil {
			keys = append(keys, &library.FileTrackingKey{Kind: library.TrackingUUID, Scope: source.ExecutorID, KeyValue: value[:]})
		}
	}
	return keys, nil
}

func missingAttribute(err error) bool { return errors.Is(err, missingXattr) }
