package executor

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"sync"

	"github.com/samuelncui/yatm/internal/library"
)

// Tracker derives native identity for one operation. A native key is scoped by the stable identity
// of the filesystem holding the object, resolved once per device for the operation's lifetime, so a
// remount, a container bind mount or another host attaching the same filesystem keeps its keys.
type Tracker struct {
	mu     sync.Mutex
	scopes map[uint64]string
}

func NewTracker() *Tracker { return &Tracker{scopes: make(map[uint64]string)} }

// Observe derives native identity from an existing observation of path without opening the file.
// A filesystem without a stable identity yields no key rather than a device-number scope.
func (t *Tracker) Observe(path string, info os.FileInfo) []*library.FileTrackingKey {
	native, ok := nativeStat(info)
	if !ok {
		return nil
	}
	t.mu.Lock()
	scope, resolved := t.scopes[native.device]
	if !resolved {
		directory := path
		if !info.IsDir() {
			directory = filepath.Dir(path)
		}
		scope = filesystemScope(directory)
		t.scopes[native.device] = scope
	}
	t.mu.Unlock()
	if scope == "" {
		return nil
	}
	value := make([]byte, 8)
	binary.BigEndian.PutUint64(value, native.inode)
	return []*library.FileTrackingKey{{Kind: library.TrackingNative, Scope: scope, KeyValue: value,
		Details: library.TrackingDetails{BirthNS: native.birthNS, Generation: native.generation}}}
}

type nativeObject struct {
	device, inode, generation uint64
	birthNS                   int64
}
