package media

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/internal/media/tape"
)

// TarRecordDevice supplies qualified record evidence and serialized Tape navigation.
// Preparation, identity, mode changes and device ownership remain with the constructor owner.
type TarRecordDevice interface {
	Read(context.Context, []byte) (tape.RecordResult, error)
	Write(context.Context, []byte) (tape.RecordResult, error)
	Rewind(context.Context) error
	Space(context.Context, byte, int32) error
	Filemarks(context.Context, uint32) error
	Sync(context.Context) error
}

// TarNavigator supplies reload-stable host-next logical addresses, with one object
// per complete record or filemark. Preparation must qualify that convention before
// providing this optional interface; command support alone is insufficient.
type TarNavigator interface {
	Position(context.Context) (tape.Position, error)
	Locate(context.Context, uint32, uint64) error
	AddressBits() uint32
}

// TarSessionOptions contains facts established by preparation, never inferred from tar bytes.
type TarSessionOptions struct {
	Media     *Descriptor
	Mode      tape.RecordMode
	Navigator TarNavigator
	Capacity  *TarCapacityOptions
	TapeFile  uint64
	Complete  func(context.Context) error
	Cleanup   func(context.Context) error
	// Finish replaces preparation cleanup after successful physical completion and includes unloading.
	Finish               func(context.Context) error
	Stop                 error // Prepared physical boundary already closes content admission.
	ReadOrder            entity.TapeReadOrder
	ReadModel            *ReadCostModel
	ReadOrderReason      string
	ReadTiming           *entity.TapeReadTiming
	ReadCompletion       *ReadCompletion
	ReadInitialPosition  *entity.TapeBytePosition
	ReadInitialFileKnown bool
	ReadPlanningBudget   time.Duration
	// ReadOrderPlanned receives the effective order and reason once planning or its fallback settles.
	ReadOrderPlanned func(entity.TapeReadOrder, string)
}

func checkTarOptions(device TarRecordDevice, options TarSessionOptions) error {
	// Refuse an unbounded or incompatible record allocation before any device operation.
	if device == nil || options.Media == nil || options.Cleanup == nil {
		return errors.New("tar session requires a device, Media and preparation cleanup")
	}
	mode := options.Mode
	if mode.MaximumBytes <= 0 || mode.MaximumBytes > tape.MaxTransferBytes || mode.MaximumBytes%512 != 0 {
		return errors.New("tar record maximum must be supported and 512-byte aligned")
	}
	if mode.FixedBytes != 0 && mode.FixedBytes != mode.MaximumBytes {
		return errors.New("tar fixed record size must equal its maximum")
	}
	if options.Navigator != nil && options.Navigator.AddressBits() != 32 && options.Navigator.AddressBits() != 64 {
		return errors.New("tar navigator must supply a 32-bit or 64-bit logical address")
	}
	return nil
}

func tarPrefix(file uint64) string { return fmt.Sprintf("archives/%016x", file) }

func checkTarPath(name string, file uint64) error {
	if !utf8.ValidString(name) {
		return errors.New("tar member path is not UTF-8")
	}
	if err := entity.ValidateRelativePath(name); err != nil {
		return err
	}
	if !strings.HasPrefix(name, tarPrefix(file)+"/") {
		return fmt.Errorf("tar member has a different Tape file namespace, path=%q", name)
	}
	return nil
}

func tarStorage(file, offset uint64) *entity.StoragePosition {
	metadata := &entity.TarMetadata{TapeFile: file, HeaderOffsetBytes: offset}
	return &entity.StoragePosition{Order: entity.TarStorageOrder(metadata), Metadata: &entity.StorageMetadata{
		Backend: &entity.StorageMetadata_Tar{Tar: metadata},
	}}
}

func tarLocator(request ReadRequest, recordBytes uint32) (*entity.TarMetadata, error) {
	// Read only frozen locators; a physical path cannot supply missing placement evidence.
	if request.Storage == nil || request.Storage.Metadata == nil || request.Storage.Metadata.GetTar() == nil {
		return nil, errors.New("tar source requires member storage metadata")
	}
	locator := request.Storage.Metadata.GetTar()
	if err := entity.ValidateTarMetadata(locator, recordBytes); err != nil {
		return nil, err
	}
	if !bytes.Equal(request.Storage.Order, entity.TarStorageOrder(locator)) {
		return nil, errors.New("tar storage order does not match its locator")
	}
	if err := checkTarPath(request.Path, locator.TapeFile); err != nil {
		return nil, err
	}
	return locator, nil
}
