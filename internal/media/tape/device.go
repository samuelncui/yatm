package tape

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/samuelncui/yatm/entity"
	"google.golang.org/protobuf/proto"
)

// Operation determines preparation; only explicit Format authorizes destructive initialization.
type Operation uint8

const (
	Read Operation = iota + 1
	Append
	Format
)

// Device owns one opened transport and captured session controls under its caller's attempt lease.
// SSC command/page semantics establish applicability; model names never authorize an operation.
type Device struct {
	transport          Transport
	report             *entity.InspectDeviceResponse
	navigation         *Navigation
	records            Records
	operation          Operation
	original           []byte
	prepared           []byte
	partition          []byte
	partitionOriginal  []byte
	partitionChanged   bool
	changed            bool
	compressionChanged uint16 // Low byte tracks DCE changes; high byte tracks DDE/RED changes.
	selectorChanged    bool
	cleanup            sync.Once
	cleanupErr         error
	geometry           *WrapMap
	cmPending          bool
	actualPartitions   uint32
}

// NewDevice consumes an opened transport and performs the same passive inspection as the RPC.
func NewDevice(ctx context.Context, identity Identity, transport Transport) (*Device, error) {
	// Inspection never positions or changes the opened drive; construction failure owns its release.
	if transport == nil {
		return nil, fmt.Errorf("Tape transport is missing")
	}
	device := &Device{transport: transport}
	_, err := device.inspect(ctx, identity)
	if err != nil {
		if closeErr := transport.Close(); closeErr != nil {
			return nil, errors.Join(err, &UncertainError{Cause: fmt.Errorf("close Tape transport after inspection failed, %w", closeErr)})
		}
		return nil, err
	}
	return device, nil
}

// Report returns a request-owned observation snapshot, never a durable write authorization.
func (d *Device) Report() *entity.InspectDeviceResponse {
	return proto.Clone(d.report).(*entity.InspectDeviceResponse)
}

// Configure establishes SSC record access and captures controls before mutation.
// FORMAT configures layout before the Tape script and writes no data; later access relies on that script's preparation.
func (d *Device) Configure(ctx context.Context, profile *entity.TarMediaProfile, operation Operation) (records Records, err error) {
	// Every failed preparation owns cancellation-independent cleanup, including failed mode readback.
	defer func() {
		if err != nil {
			cleanup, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
			defer cancel()
			err = errors.Join(err, d.Cleanup(cleanup))
		}
	}()
	if profile == nil {
		return Records{}, fmt.Errorf("Tape record policy is missing")
	}
	if err := entity.ValidateTarRecordPolicy(profile.RecordMode, profile.RecordBytes); err != nil {
		return Records{}, err
	}
	if operation < Read || operation > Format || profile.RecordBytes > MaxTransferBytes {
		return Records{}, fmt.Errorf("Tape operation or record size is unsupported")
	}
	if operation != Read {
		d.invalidateGeometry()
	}
	if !d.report.GetTape().GetMediumPresent() || d.report.Reason == entity.DeviceReason_DEVICE_REASON_NOT_READY {
		return Records{}, fmt.Errorf("Tape medium is not ready")
	}

	// An SPC-3 sequential-access device supplies the SSC-2/3 baseline, not vendor-specific forms.
	inquiry, err := query(ctx, d.transport, []byte{0x12, 0, 0, 0, 96, 0}, 96)
	if err != nil {
		return Records{}, err
	}
	if len(inquiry) < 36 || inquiry[0]&0x1f != 1 || inquiry[2] < 5 || inquiry[3]&0x0f != 2 {
		return Records{}, fmt.Errorf("standard sequential-device command scope is not established")
	}
	mode := RecordMode{MaximumBytes: int(profile.RecordBytes), WarningCounts: true}
	fixed := byte(0)
	if profile.RecordMode == entity.TapeRecordMode_TAPE_RECORD_MODE_FIXED {
		mode.FixedBytes = mode.MaximumBytes
		fixed = 1
	}
	limits := feature(d.report, entity.DeviceFeatureKind_DEVICE_FEATURE_KIND_SEQUENTIAL_READ).GetRecords()
	if limits == nil || limits.MinimumBytes == nil || limits.AlignmentBytes == nil ||
		profile.RecordBytes < limits.GetMinimumBytes() || limits.MaximumBytes != nil && profile.RecordBytes > limits.GetMaximumBytes() ||
		limits.GetAlignmentBytes() == 0 || (profile.RecordBytes-limits.GetMinimumBytes())%limits.GetAlignmentBytes() != 0 {
		return Records{}, fmt.Errorf("selected record is outside observed block limits")
	}
	if mode.FixedBytes == 0 && (limits.Variable != nil && !limits.GetVariable() || limits.GetMinimumBytes() > 512 || limits.GetAlignmentBytes() > 512) {
		return Records{}, fmt.Errorf("variable tar records are outside observed block limits")
	}
	if reserve, ok := d.transport.(interface {
		EnsureTransfer(context.Context, int) error
	}); ok {
		if err := reserve.EnsureTransfer(ctx, mode.MaximumBytes); err != nil {
			return Records{}, err
		}
	} else if bounded, ok := d.transport.(interface{ TransferLimit() int }); ok && bounded.TransferLimit() < mode.MaximumBytes {
		return Records{}, fmt.Errorf("selected record exceeds the transport buffer")
	}
	for _, kind := range []entity.DeviceFeatureKind{entity.DeviceFeatureKind_DEVICE_FEATURE_KIND_SEQUENTIAL_READ, entity.DeviceFeatureKind_DEVICE_FEATURE_KIND_SEQUENTIAL_WRITE} {
		if records := feature(d.report, kind).GetRecords(); records != nil {
			records.TransportMaximumBytes = transportMaximumBytes(d.transport)
		}
	}

	// Read current non-secret controls and required medium facts before any MODE SELECT or write.
	current, pages, err := d.readMode(ctx, false)
	if err != nil {
		return Records{}, err
	}
	if len(current) < 16 || binary.BigEndian.Uint16(current[6:8]) != 8 {
		return Records{}, fmt.Errorf("record mode descriptor is unavailable")
	}
	configuration := pages[0x10]
	if len(configuration) < 16 || operation != Format && configuration[3] != 0 || operation != Read && (configuration[10]>>5 > 2 || configuration[10]&0x10 == 0) {
		return Records{}, fmt.Errorf("partition zero and EOD generation are not established")
	}
	compression := pages[0x0f]
	if operation != Read {
		if (current[3]&0x70)>>4 > 2 {
			return Records{}, fmt.Errorf("buffered-write mode is outside the standard scope")
		}
		if len(compression) != 0 && len(compression) < 16 {
			return Records{}, ErrMalformed
		}
		if current[3]&0x80 != 0 {
			return Records{}, fmt.Errorf("Tape medium is write protected")
		}
		medium := pages[0x1d]
		if len(medium) < 32 || medium[2]&1 != 0 {
			return Records{}, fmt.Errorf("rewritable medium prerequisites are unavailable")
		}
	}
	if err := d.requireCommand(ctx, []byte{0x08, fixed, 0xff, 0xff, 0xff, 0}, true); err != nil {
		return Records{}, err
	}
	if err := d.requireCommand(ctx, []byte{1, 0, 0, 0, 0, 0}, true); err != nil {
		return Records{}, err
	}
	if operation != Read {
		for _, form := range [][]byte{{0x0a, fixed, 0xff, 0xff, 0xff, 0}, {0x10, 0, 0xff, 0xff, 0xff, 0}} {
			if err := d.requireCommand(ctx, form, true); err != nil {
				return Records{}, err
			}
		}
	}

	// A multipartition FORMAT requires an explicit, supported delayed single-partition transition.
	count, err := d.actualPartitionCount(ctx, pages[0x11])
	if err != nil {
		return Records{}, err
	}
	d.report.GetTape().PartitionCount = proto.Uint32(count)
	if operation == Format {
		partition := pages[0x11]
		if count != 1 && len(partition) < 8 {
			return Records{}, fmt.Errorf("Tape partition layout is unknown")
		}
		if count != 1 {
			if err := d.preparePartition(ctx, partition, current[3]); err != nil {
				return Records{}, err
			}
		}
	} else if count != 1 {
		return Records{}, fmt.Errorf("tar access requires a known single partition")
	}

	// FORMAT may leave an LTFS partition only through an explicitly reported partition-zero form.
	if operation == Format && configuration[3] != 0 {
		form := make([]byte, 16)
		form[0], form[1] = 0x92, 2
		if err := d.requireCommand(ctx, form, false); err != nil {
			return Records{}, err
		}
		if d.original == nil {
			d.original = sessionMode(current, compression, configuration, blockLength(current))
		}
		motion := Records{Transport: d.transport, Mode: mode, Timeout: 10 * time.Minute}
		if err := motion.Locate(ctx, 0, 0); err != nil {
			return Records{}, err
		}
		current, pages, err = d.readMode(ctx, false)
		if err != nil {
			return Records{}, err
		}
		if len(pages[0x10]) < 16 || pages[0x10][3] != 0 {
			return Records{}, &UncertainError{Cause: fmt.Errorf("partition-zero motion was not confirmed")}
		}
		compression = pages[0x0f]
		configuration = pages[0x10]
	}

	// Resolve independent read decoding or the explicit write policy without replacing algorithm IDs.
	selectedCompression, selectedConfiguration, err := compressionPolicy(pages, profile.HardwareCompression, operation)
	if err != nil {
		return Records{}, err
	}
	if d.original == nil {
		d.original = sessionMode(current, compression, configuration, blockLength(current))
	}
	desired := sessionMode(current, selectedCompression, selectedConfiguration, uint32(mode.FixedBytes))
	d.prepared = desired
	var beforeCompression, beforeConfiguration []byte
	if len(selectedCompression) != 0 {
		beforeCompression = compression
	}
	if len(selectedConfiguration) != 0 {
		beforeConfiguration = configuration
	}
	before := sessionMode(current, beforeCompression, beforeConfiguration, blockLength(current))
	if !bytes.Equal(desired, before) {
		if err := d.requireCommand(ctx, []byte{0x55, 0x10, 0, 0, 0, 0, 0, 0xff, 0xff, 0}, true); err != nil {
			return Records{}, err
		}
		selected := map[byte][]byte{0x0f: selectedCompression, 0x10: selectedConfiguration}
		if compressionControlsDiffer(pages, selected) {
			_, changes, err := d.readMode(ctx, true)
			if err != nil {
				return Records{}, err
			}
			if err := checkCompressionChanges(pages, selected, changes); err != nil {
				return Records{}, err
			}
		}
		d.changed = true
		if len(selectedCompression) != 0 {
			d.compressionChanged |= uint16((compression[2]^selectedCompression[2])&0x80) | uint16((compression[3]^selectedCompression[3])&0xe0)<<8
		}
		d.selectorChanged = d.selectorChanged || len(selectedConfiguration) != 0 && configuration[14] != selectedConfiguration[14]
		var writePolicy *bool
		if operation != Read {
			writePolicy = proto.Bool(profile.HardwareCompression)
		}
		if err := d.selectMode(ctx, desired, operation == Read && profile.HardwareCompression, writePolicy); err != nil {
			return Records{}, err
		}
	}
	d.records, d.operation = Records{Transport: d.transport, Mode: mode, Timeout: 10 * time.Minute}, operation
	d.actualPartitions = count
	d.records.invalidateGeometry = d.invalidateGeometry
	if d.navigation != nil {
		d.navigation.records.invalidateGeometry = d.invalidateGeometry
	}
	return d.records, nil
}

// Initialize establishes partition zero/BOT; label completion establishes the new recorded EOD.
func (d *Device) Initialize(ctx context.Context) error {
	// Only the explicitly configured FORMAT path may alter the physical cartridge layout.
	if d.operation != Format {
		return fmt.Errorf("Tape initialization requires FORMAT preparation")
	}
	d.invalidateGeometry()
	if err := d.records.Rewind(ctx); err != nil {
		return err
	}
	if d.partition == nil {
		d.actualPartitions = 1
		return nil
	}

	// POFM keeps the partition page pending until a non-immediate FORMAT commits the transition.
	d.partitionChanged = true
	if err := d.selectMode(ctx, d.partition, false, nil); err != nil {
		return err
	}
	response, err := d.transport.Execute(ctx, Command{CDB: []byte{4, 0, 1, 0, 0, 0}, Direction: NoData, Timeout: 24 * time.Hour, validate: controlResponse})
	if err != nil {
		return err
	}
	if err := CheckResponse(response); err != nil {
		return err
	}
	count, err := d.partitionCount(ctx)
	if err != nil || count != 1 {
		return errors.Join(err, fmt.Errorf("formatted single-partition layout was not confirmed"))
	}
	d.report.GetTape().PartitionCount = proto.Uint32(1)
	d.actualPartitions = 1
	d.partitionChanged = false
	d.changed = true
	pages, err := selectionPages(d.prepared)
	if err != nil {
		return err
	}
	enabled := len(pages[0x0f]) >= 16 && pages[0x0f][2]&0x80 != 0
	if err := d.selectMode(ctx, d.prepared, false, &enabled); err != nil {
		return err
	}
	if err := d.records.Rewind(ctx); err != nil {
		return err
	}
	return nil
}

// Cleanup restores captured controls after commands settle and releases the opened transport.
func (d *Device) Cleanup(ctx context.Context) error {
	return d.release(ctx, false)
}

// Finish restores owned controls and unloads a normally completed Session before closing it.
func (d *Device) Finish(ctx context.Context) error {
	return d.release(ctx, true)
}

func (d *Device) release(ctx context.Context, unload bool) error {
	d.cleanup.Do(func() {
		// Withdraw every shared snapshot before cleanup can release or lose the opened session.
		d.invalidateGeometry()

		// Restoration failure is physical unavailability even if its command had a known rejection.
		if d.partitionChanged {
			if err := d.selectMode(ctx, d.partitionOriginal, false, nil); err != nil {
				d.cleanupErr = errors.Join(d.cleanupErr, &UncertainError{Cause: fmt.Errorf("restore pending partition controls failed, %w", err)})
			}
		}
		if d.changed {
			if err := d.restoreMode(ctx); err != nil {
				d.cleanupErr = errors.Join(d.cleanupErr, &UncertainError{Cause: fmt.Errorf("restore Tape session mode failed, %w", err)})
			}
		}
		if unload && d.cleanupErr == nil {
			if err := d.records.Unload(ctx); err != nil {
				d.cleanupErr = &UncertainError{Cause: fmt.Errorf("unload completed Tape session failed, %w", err)}
			}
		}
		d.cleanupErr = errors.Join(d.cleanupErr, d.transport.Close())
	})
	return d.cleanupErr
}
