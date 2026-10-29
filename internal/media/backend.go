// Package media defines the shared behavior of archive media backends.
package media

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/samuelncui/acp"
	"github.com/samuelncui/yatm/entity"
)

var (
	// ErrCapacityBoundary tells a runner to stop before the next file and finalize the current deterministic prefix.
	ErrCapacityBoundary = errors.New("Media capacity boundary reached")
	// ErrTargetNoSpace is the backend-neutral form of a physical target-full error.
	ErrTargetNoSpace = errors.New("Media target has no space")
	// ErrFinalizeUnusable prevents publication when Backend validation cannot establish a durable result.
	ErrFinalizeUnusable = errors.New("Media finalize result is unusable")
	// ErrReadUnavailable identifies a stopped Session lane, not corruption of an unread file.
	ErrReadUnavailable = errors.New("Media read session is unavailable")
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
	NewReadSession(context.Context, *entity.ReadMediaTarget) (ReadSession, error)
	NewWriteSession(context.Context, *entity.ArchiveMediaTarget) (WriteSession, error)
}

// Descriptor contains the Library identity, immutable profile and derived geometry for one physical session.
// It is a boundary value, not a Library persistence row.
// Runtime times stay as time.Time; Library converts them to checked persisted nanoseconds.
type Descriptor struct {
	ID              int64
	Kind            entity.MediaKind
	Identity        string
	Name            string
	Profile         *entity.MediaProfile
	TapePositionMap *entity.TapePositionMap
	CreateTime      time.Time
	DestroyTime     *time.Time
	CapacityBytes   int64
	WrittenBytes    int64
}

// Session describes one prepared physical Media.
type Session interface {
	Capabilities() Capabilities
	Media() *Descriptor
}

// ReadRequest binds the frozen on-Media path and its optional physical storage facts.
type ReadRequest struct {
	Path    string
	Storage *entity.StoragePosition
}

// SourceRef selects exactly one native filesystem path or ACP content source.
type SourceRef struct {
	Path    string
	Content acp.ContentSource
}

// Validate rejects absent or ambiguous source variants.
func (r SourceRef) Validate() error {
	if (r.Path == "") == (r.Content == nil) {
		return fmt.Errorf("Media source must select exactly one path or content endpoint")
	}
	return nil
}

// TargetRef selects exactly one native filesystem path or ACP content target.
type TargetRef struct {
	Path    string
	Content acp.ContentTarget
}

// Validate rejects absent or ambiguous target variants.
func (r TargetRef) Validate() error {
	if (r.Path == "") == (r.Content == nil) {
		return fmt.Errorf("Media target must select exactly one path or content endpoint")
	}
	return nil
}

// Storage returns an independent positioning snapshot after a target's successful Finish.
// Native targets and endpoints without positioning retain their existing inventory path.
func (r TargetRef) Storage() *entity.StoragePosition {
	if target, ok := r.Content.(interface {
		storagePosition() *entity.StoragePosition
	}); ok {
		return target.storagePosition()
	}
	return nil
}

// ReadSession exposes one prepared Media as an ACP source.
type ReadSession interface {
	Session
	Source(ReadRequest) (SourceRef, error)
	Finalize(context.Context, error) error
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

// CompletedFile is one physically confirmed on-Media file after finalization.
type CompletedFile struct {
	Path    string
	Size    int64
	Storage *entity.StoragePosition
}

// FinalInventory walks physically confirmed files in bounded order.
type FinalInventory func(context.Context, func(*CompletedFile) error) error

// WriteResult describes physical facts a Job owner may reconcile with its own manifest.
type WriteResult struct {
	Media          *Descriptor
	PathPrefix     string
	VerifiedPrefix bool
	Inventory      FinalInventory
	// Stop retains admission/write causes first observed during finalization; it is not a physical failure.
	Stop            error
	finalizeStorage func(*entity.StoragePosition) (*entity.StoragePosition, error)
}

// FinalizeStorage reconciles staged positioning with this result's physical completion.
// A nil output for a non-nil input excludes that incomplete member. Native backends retain
// their input and existing Inventory; absent storage is never a completion flag.
func (r *WriteResult) FinalizeStorage(staged *entity.StoragePosition) (*entity.StoragePosition, error) {
	if r.finalizeStorage == nil {
		return staged, nil
	}
	return r.finalizeStorage(staged)
}

// WriteSession exposes one prepared Media as an ACP target.
type WriteSession interface {
	Session
	Target(relative string, size int64) (TargetRef, error)
	Finalize(context.Context, error) (*WriteResult, error)
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
