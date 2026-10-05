//go:build linux || darwin

package executor

import (
	"encoding/binary"
	"fmt"
	"os"
	"syscall"

	"github.com/samuelncui/yatm/internal/library"
)

// ObserveTracking derives native identity from the existing observation without opening the file or reading xattrs.
func ObserveTracking(source *library.Location, scanned os.FileInfo) []*library.FileTrackingKey {
	stat, ok := scanned.Sys().(*syscall.Stat_t)
	if !ok {
		return nil
	}
	value := make([]byte, 8)
	binary.BigEndian.PutUint64(value, stat.Ino)
	birth, generation := nativeLifetime(stat)
	return []*library.FileTrackingKey{{Kind: library.TrackingNative,
		Scope: fmt.Sprintf("%s/fs/%d", source.ExecutorID, stat.Dev), KeyValue: value,
		Details: library.TrackingDetails{BirthNS: birth, Generation: generation}}}
}
