// Package media defines the shared behavior of archive media backends.
package media

import (
	"context"
	"errors"
	"os"

	"github.com/samuelncui/acp"
	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/library"
	"gorm.io/gorm"
)

var (
	// ErrCapacityBoundary tells a runner to stop before the next file and finalize the current deterministic prefix.
	ErrCapacityBoundary = errors.New("Media capacity boundary reached")
	// ErrTargetNoSpace is the backend-neutral form of a physical target-full error.
	ErrTargetNoSpace = errors.New("Media target has no space")
	// ErrFinalizeUnusable prevents publication when Backend validation cannot establish a durable result.
	ErrFinalizeUnusable = errors.New("Media finalize result is unusable")
)

// Access describes the strongest supported access pattern for one direction.
type Access int

const (
	AccessConcurrentRandom Access = iota
	AccessRandom
	AccessSequential
)

// Capabilities describes one prepared Media session.
type Capabilities struct {
	Read  Access
	Write Access
}

// OnlineReadable reports whether consumers can use the Media without extraction.
func (c Capabilities) OnlineReadable() bool {
	return c.Read == AccessConcurrentRandom
}

// Backend creates concrete read and write sessions from public Media targets.
type Backend interface {
	NewReadSession(context.Context, *gorm.DB, *entity.ReadMediaTarget) (ReadSession, error)
	NewWriteSession(context.Context, *gorm.DB, *entity.ArchiveMediaTarget) (WriteSession, error)
}

// Session describes one inspected physical Media.
type Session interface {
	Capabilities() Capabilities
	Inspect() *library.Media
}

// ReadSession exposes one prepared Media as an ACP source.
type ReadSession interface {
	Session
	SourcePath(relative string) (string, error)
	Finalize(context.Context) error
}

// InventoryEntry describes a freshly enumerated ordinary file, never a catalog expectation.
type InventoryEntry struct {
	Path    string
	Info    os.FileInfo
	Storage *entity.StoragePosition
}

// InventorySession exposes bounded physical enumeration while retaining the read-session identity boundary.
type InventorySession interface {
	ReadSession
	WalkInventory(context.Context, func(*InventoryEntry) error) error
}

// WriteSession exposes one prepared Media as an ACP target.
type WriteSession interface {
	Session
	TargetPath(relative string) (string, error)
	Finalize(context.Context, error) error
}

// DeviceOptions maps a Media access pattern onto ACP scheduling.
func DeviceOptions(access Access) []acp.DeviceOption {
	switch access {
	case AccessConcurrentRandom:
		return nil
	case AccessRandom:
		return []acp.DeviceOption{acp.DeviceThreads(1)}
	case AccessSequential:
		return []acp.DeviceOption{acp.LinearDevice(true)}
	default:
		return nil
	}
}

// CapabilitiesForProfile derives runtime behavior from one typed Media profile.
func CapabilitiesForProfile(profile *entity.MediaProfile) (Capabilities, error) {
	if profile == nil {
		return Capabilities{}, errors.New("Media profile is nil")
	}
	switch value := profile.Unpack().(type) {
	case *entity.TapeMediaProfile:
		return Capabilities{Read: AccessSequential, Write: AccessSequential}, nil
	case *entity.VolumeMediaProfile:
		return VolumeCapabilities(value.Type)
	default:
		return Capabilities{}, errors.New("Media profile kind is unspecified")
	}
}

// AccessToEntity converts runtime capabilities for durable storage.
func AccessToEntity(value Access) entity.MediaAccess {
	switch value {
	case AccessConcurrentRandom:
		return entity.MediaAccess_MEDIA_ACCESS_CONCURRENT_RANDOM
	case AccessRandom:
		return entity.MediaAccess_MEDIA_ACCESS_RANDOM
	case AccessSequential:
		return entity.MediaAccess_MEDIA_ACCESS_SEQUENTIAL
	default:
		return entity.MediaAccess_MEDIA_ACCESS_UNSPECIFIED
	}
}
