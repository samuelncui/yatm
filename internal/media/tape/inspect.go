package tape

import (
	"context"
	"encoding/binary"
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/samuelncui/yatm/entity"
	"google.golang.org/protobuf/proto"
)

// Inspect issues passive queries under an existing lease. Observation failures retain a typed
// report; ErrUncertain additionally requires the Executor to retain its unavailable resource key.
func Inspect(ctx context.Context, identity Identity) (report *entity.InspectDeviceResponse, err error) {
	transport, err := OpenTransport(identity.SG, true)
	if err != nil {
		report = newReport(identity)
		report.Reason, report.Detail = Reason(err), err.Error()
		return report, err
	}
	defer func() {
		if closeErr := transport.Close(); closeErr != nil {
			err = errors.Join(err, closeErr)
			report.Reason, report.Detail = Reason(err), err.Error()
		}
	}()
	return inspectTransport(ctx, identity, transport)
}

func newReport(identity Identity) *entity.InspectDeviceResponse {
	// Query evidence and fake conformance tests never establish physical validation.
	tape := new(entity.TapeDeviceReport)
	for kind := entity.DeviceFeatureKind_DEVICE_FEATURE_KIND_INITIALIZE; kind <= entity.DeviceFeatureKind_DEVICE_FEATURE_KIND_SOFTWARE_READ_ORDER; kind++ {
		tape.Features = append(tape.Features, &entity.DeviceFeature{
			Feature: kind, Support: entity.DeviceFeatureSupport_DEVICE_FEATURE_SUPPORT_UNKNOWN,
			Availability: entity.DeviceFeatureAvailability_DEVICE_FEATURE_AVAILABILITY_UNKNOWN,
			Validation:   entity.DeviceFeatureValidation_DEVICE_FEATURE_VALIDATION_UNVALIDATED,
			Reason:       entity.DeviceReason_DEVICE_REASON_NOT_IMPLEMENTED,
			Detail:       "No applicable operation or cleanup qualification is established",
		})
	}
	report := &entity.InspectDeviceResponse{
		Device: &entity.DeviceSummary{Device: identity.Configured, Kind: entity.DeviceKind_DEVICE_KIND_TAPE},
		Report: &entity.InspectDeviceResponse_Tape{Tape: tape},
	}
	for _, kind := range []entity.DeviceFeatureKind{entity.DeviceFeatureKind_DEVICE_FEATURE_KIND_SOFTWARE_READ_ORDER, entity.DeviceFeatureKind_DEVICE_FEATURE_KIND_NON_EJECTING_RELOAD} {
		feature(report, kind).Reason = entity.DeviceReason_DEVICE_REASON_NOT_READY
	}
	feature(report, entity.DeviceFeatureKind_DEVICE_FEATURE_KIND_SOFTWARE_READ_ORDER).Detail = "Software read ordering requires a prepared read session with eligible seek, applicable wrap geometry and matching measured costs; passive inspection does not determine AUTO availability"
	feature(report, entity.DeviceFeatureKind_DEVICE_FEATURE_KIND_NON_EJECTING_RELOAD).Detail = "Non-ejecting reload and REOWP refresh lifecycle remain unvalidated"
	if identity.Key != "" {
		report.AccessIdentity = proto.String(identity.Key)
	}
	return report
}

func feature(report *entity.InspectDeviceResponse, kind entity.DeviceFeatureKind) *entity.DeviceFeature {
	for _, feature := range report.GetTape().Features {
		if feature.Feature == kind {
			return feature
		}
	}
	panic("missing Tape feature")
}

func inspectTransport(ctx context.Context, identity Identity, transport Transport) (*entity.InspectDeviceResponse, error) {
	return (&Device{transport: transport}).inspect(ctx, identity)
}

func (d *Device) inspect(ctx context.Context, identity Identity) (*entity.InspectDeviceResponse, error) {
	// Each query has a native timeout; caller cancellation stops later admission.
	// An extra inspection deadline must not turn an optional capacity timeout into device failure.
	report := newReport(identity)
	d.report = report
	transport := d.transport
	report.Transport, report.ObservedAtNs = proto.String("Linux SG v3"), proto.Int64(time.Now().UnixNano())
	defer func() {
		for _, f := range report.GetTape().Features {
			f.Scope = &entity.DeviceFeatureScope{Model: report.Model, Firmware: report.Firmware, Transport: report.Transport, DensityCode: report.GetTape().DensityCode}
			mediumDependent := f.Feature <= entity.DeviceFeatureKind_DEVICE_FEATURE_KIND_EARLY_WARNING || f.Feature == entity.DeviceFeatureKind_DEVICE_FEATURE_KIND_HARDWARE_COMPRESSION || f.Feature == entity.DeviceFeatureKind_DEVICE_FEATURE_KIND_HARDWARE_DECOMPRESSION || f.Feature == entity.DeviceFeatureKind_DEVICE_FEATURE_KIND_HARDWARE_ENCRYPTION || f.Feature == entity.DeviceFeatureKind_DEVICE_FEATURE_KIND_END_OF_WRAP_POSITION || f.Feature == entity.DeviceFeatureKind_DEVICE_FEATURE_KIND_NON_EJECTING_RELOAD || f.Feature == entity.DeviceFeatureKind_DEVICE_FEATURE_KIND_SOFTWARE_READ_ORDER
			if report.GetTape().MediumPresent != nil && !report.GetTape().GetMediumPresent() && mediumDependent {
				f.Availability, f.Reason = entity.DeviceFeatureAvailability_DEVICE_FEATURE_AVAILABILITY_UNAVAILABLE, entity.DeviceReason_DEVICE_REASON_NO_MEDIUM
				f.Detail = "No cartridge is loaded; operation prerequisites remain unqualified"
			} else if report.Reason == entity.DeviceReason_DEVICE_REASON_NOT_READY && mediumDependent {
				f.Availability, f.Reason = entity.DeviceFeatureAvailability_DEVICE_FEATURE_AVAILABILITY_UNAVAILABLE, report.Reason
				f.Detail = "Cartridge readiness observation failed; operation prerequisites remain unavailable"
			}
			if f.Feature == entity.DeviceFeatureKind_DEVICE_FEATURE_KIND_END_OF_WRAP_POSITION && f.GetGeometry() != nil && (f.Reason == entity.DeviceReason_DEVICE_REASON_NO_MEDIUM || f.Reason == entity.DeviceReason_DEVICE_REASON_NOT_READY) {
				f.Detail = "REOWP CDB form reported; no endpoint response observed for an unavailable cartridge"
				if d.geometry != nil {
					f.Detail = "Previously observed REOWP endpoint response invalidated by cartridge readiness loss"
				}
			}
		}
	}()
	observe := func(kind entity.DeviceFeatureKind, err error) bool {
		if err == nil {
			return true
		}
		reason := Reason(err)
		if errors.Is(err, ErrUncertain) || ctx.Err() != nil || reason == entity.DeviceReason_DEVICE_REASON_NO_MEDIUM || reason == entity.DeviceReason_DEVICE_REASON_NOT_READY {
			d.invalidateGeometry()
			if limits := feature(report, entity.DeviceFeatureKind_DEVICE_FEATURE_KIND_END_OF_WRAP_POSITION).GetGeometry(); limits != nil {
				limits.ReportedWraps, limits.Fresh = nil, nil
			}
		}
		if kind != 0 {
			f := feature(report, kind)
			f.Reason, f.Detail = reason, err.Error()
			if reason == entity.DeviceReason_DEVICE_REASON_UNSUPPORTED {
				f.Support = entity.DeviceFeatureSupport_DEVICE_FEATURE_SUPPORT_UNSUPPORTED
			}
		}
		if reason == entity.DeviceReason_DEVICE_REASON_NO_MEDIUM {
			report.GetTape().MediumPresent = proto.Bool(false)
		}
		if report.Reason == 0 || errors.Is(err, ErrUncertain) || ctx.Err() != nil ||
			reason == entity.DeviceReason_DEVICE_REASON_NO_MEDIUM || reason == entity.DeviceReason_DEVICE_REASON_NOT_READY {
			report.Reason, report.Detail = reason, err.Error()
		}
		return !errors.Is(err, ErrUncertain) && ctx.Err() == nil
	}

	// Drive identity remains observable without a loaded cartridge.
	data, err := query(ctx, transport, []byte{0x12, 0, 0, 0, 96, 0}, 96)
	if err == nil {
		err = decodeInquiry(data, report)
	}
	if !observe(0, err) || err != nil {
		return report, err
	}
	data, err = query(ctx, transport, []byte{0x12, 1, 0x80, 0, 255, 0}, 255)
	if err == nil {
		if len(data) < 4 || data[1] != 0x80 || int(binary.BigEndian.Uint16(data[2:4]))+4 > len(data) {
			err = ErrMalformed
		} else {
			serial := strings.TrimSpace(string(data[4 : 4+int(binary.BigEndian.Uint16(data[2:4]))]))
			if !utf8.ValidString(serial) || strings.ContainsRune(serial, 0) {
				err = ErrMalformed
			} else if serial != "" {
				report.SerialNumber = proto.String(serial)
			}
		}
	}
	if !observe(0, err) {
		return report, err
	}

	// Readiness does not prove writable media, a tar format, or physical operation qualification.
	_, err = query(ctx, transport, []byte{0, 0, 0, 0, 0, 0}, 0)
	if err == nil {
		report.GetTape().MediumPresent = proto.Bool(true)
	}
	if Reason(err) == entity.DeviceReason_DEVICE_REASON_NO_MEDIUM {
		report.GetTape().MediumPresent = proto.Bool(false)
		report.Reason, report.Detail = Reason(err), err.Error()
	}
	if !observe(0, err) {
		return report, err
	}

	// Bounds are reported facts and leave transfer/completion eligibility explicitly unknown.
	data, err = query(ctx, transport, []byte{5, 0, 0, 0, 0, 0}, 6)
	if err == nil {
		err = decodeBlockLimits(data, report, transport)
	}
	if !observe(0, err) {
		return report, err
	}
	data, err = query(ctx, transport, []byte{0x5a, 0, 0x3f, 0, 0, 0, 0, 0x10, 0, 0}, MaxQueryBytes)
	var modePartition *uint32
	var currentPages map[byte][]byte
	if err == nil {
		modePartition, currentPages, err = decodeMode(data, report.GetTape())
	}
	if !observe(0, err) {
		return report, err
	}

	// Optional writable-mask discovery retains current controls while clean query failures remain feature-local.
	if len(currentPages[0x0f]) >= 16 {
		_, changes, changeErr := d.readMode(ctx, true)
		changeErr = errors.Join(changeErr, ctx.Err())
		d.reportCompression(currentPages, changes, changeErr)
		if errors.Is(changeErr, ErrUncertain) || ctx.Err() != nil || Reason(changeErr) == entity.DeviceReason_DEVICE_REASON_NO_MEDIUM || Reason(changeErr) == entity.DeviceReason_DEVICE_REASON_NOT_READY {
			if !observe(0, changeErr) {
				return report, changeErr
			}
		}
	} else {
		d.reportCompression(currentPages, nil, nil)
	}

	// A non-secret encryption status query reports current hardware mode without changing keys.
	data, err = encryptionStatus(ctx, transport, report.GetTape())
	cipherOff := err == nil && encryptionOff(data)
	if !observe(0, err) {
		return report, err
	}
	if err == nil {
		encryptionErr := d.inspectEncryption(ctx, data)
		if errors.Is(encryptionErr, ErrUncertain) || ctx.Err() != nil || Reason(encryptionErr) == entity.DeviceReason_DEVICE_REASON_NO_MEDIUM || Reason(encryptionErr) == entity.DeviceReason_DEVICE_REASON_NOT_READY {
			if !observe(0, encryptionErr) {
				return report, encryptionErr
			}
		}
	}

	// A clean optional long-form rejection can use short form; neither qualifies LOCATE or reload.
	data, err = query(ctx, transport, []byte{0x34, 6, 0, 0, 0, 0, 0, 0, 0, 0}, 32)
	long := true
	if Reason(err) == entity.DeviceReason_DEVICE_REASON_UNSUPPORTED {
		long = false
		data, err = query(ctx, transport, []byte{0x34, 0, 0, 0, 0, 0, 0, 0, 0, 0}, 20)
	}
	var position Position
	if err == nil {
		position, err = decodePosition(data, long)
		if err == nil {
			f := feature(report, entity.DeviceFeatureKind_DEVICE_FEATURE_KIND_READ_POSITION)
			f.Support, f.Evidence = entity.DeviceFeatureSupport_DEVICE_FEATURE_SUPPORT_PARTIAL, entity.DeviceFeatureEvidence_DEVICE_FEATURE_EVIDENCE_REPORTED
			f.Reason, f.Detail = 0, "Position form observed; motion, reload and locator semantics remain unqualified"
			if position.ObjectKnown {
				f.Availability = entity.DeviceFeatureAvailability_DEVICE_FEATURE_AVAILABILITY_READY
			} else {
				f.Reason, f.Detail = entity.DeviceReason_DEVICE_REASON_NOT_READY, "Position form observed; exact logical position is currently unknown"
			}
			limits := new(entity.TapeNavigationLimits)
			if long {
				limits.ReadPositionLong, limits.AddressBits = proto.Bool(true), proto.Uint32(64)
			} else {
				limits.ReadPositionShort, limits.AddressBits = proto.Bool(true), proto.Uint32(32)
			}
			f.Limits = &entity.DeviceFeature_Navigation{Navigation: limits}
		}
	}
	if err != nil {
		f := feature(report, entity.DeviceFeatureKind_DEVICE_FEATURE_KIND_READ_POSITION)
		f.Availability, f.Reason, f.Detail = entity.DeviceFeatureAvailability_DEVICE_FEATURE_AVAILABILITY_UNAVAILABLE, Reason(err), err.Error()
		if f.Reason == entity.DeviceReason_DEVICE_REASON_UNSUPPORTED {
			f.Support = entity.DeviceFeatureSupport_DEVICE_FEATURE_SUPPORT_UNSUPPORTED
		}
		if errors.Is(err, ErrUncertain) || ctx.Err() != nil || f.Reason == entity.DeviceReason_DEVICE_REASON_NO_MEDIUM || f.Reason == entity.DeviceReason_DEVICE_REASON_NOT_READY {
			if !observe(0, err) {
				return report, err
			}
		}
	}
	var navigationErr error
	var latestPosition Position
	d.navigation, latestPosition, navigationErr = inspectNavigation(ctx, transport, report, position, long, err)
	navigationErr = errors.Join(navigationErr, ctx.Err())
	if errors.Is(navigationErr, ErrUncertain) || ctx.Err() != nil || Reason(navigationErr) == entity.DeviceReason_DEVICE_REASON_NO_MEDIUM || Reason(navigationErr) == entity.DeviceReason_DEVICE_REASON_NOT_READY {
		if !observe(0, navigationErr) {
			return report, navigationErr
		}
	}

	// Geometry reporting is independent of logical seek and never proves freshness or a cost model.
	if err := d.inspectGeometry(ctx); err != nil {
		if !observe(0, err) {
			return report, err
		}
	}

	// Current mode supplies partition independently of optional seek; every exact position cross-checks it.
	if !report.GetTape().GetMediumPresent() || report.Reason == entity.DeviceReason_DEVICE_REASON_NOT_READY {
		d.reportCapacity(nil, false, nil)
		if report.Reason == entity.DeviceReason_DEVICE_REASON_NO_MEDIUM || report.Reason == entity.DeviceReason_DEVICE_REASON_NOT_READY {
			f := feature(report, entity.DeviceFeatureKind_DEVICE_FEATURE_KIND_CAPACITY_TO_WARNING)
			f.Availability, f.Reason = entity.DeviceFeatureAvailability_DEVICE_FEATURE_AVAILABILITY_UNAVAILABLE, report.Reason
			f.Detail = "Readiness loss invalidated the active partition observation"
		}
		return report, nil
	}
	partition := modePartition
	for _, observed := range []Position{position, latestPosition} {
		if !observed.ObjectKnown {
			continue
		}
		if partition != nil && *partition != observed.Partition {
			d.reportCapacity(nil, false, ErrMalformed)
			feature(report, entity.DeviceFeatureKind_DEVICE_FEATURE_KIND_CAPACITY_TO_WARNING).Detail = "Current mode or READ POSITION observations disagree on the active partition"
			return report, nil
		}
		partition = proto.Uint32(observed.Partition)
	}
	if partition != nil {
		sample, issued, capacityErr := preparationCapacity(ctx, transport, *partition)
		capacityErr = errors.Join(capacityErr, ctx.Err())
		d.reportCapacity(sample, issued, capacityErr)
		if errors.Is(capacityErr, ErrUncertain) || ctx.Err() != nil || Reason(capacityErr) == entity.DeviceReason_DEVICE_REASON_NO_MEDIUM || Reason(capacityErr) == entity.DeviceReason_DEVICE_REASON_NOT_READY {
			if !observe(0, capacityErr) {
				return report, capacityErr
			}
		}
	} else {
		d.reportCapacity(nil, false, nil)
	}

	// Reload admission uses exact command forms and current observations, without moving the cartridge.
	if err := d.inspectReload(ctx, cipherOff); err != nil {
		if errors.Is(err, ErrUncertain) || ctx.Err() != nil || Reason(err) == entity.DeviceReason_DEVICE_REASON_NO_MEDIUM || Reason(err) == entity.DeviceReason_DEVICE_REASON_NOT_READY {
			if !observe(0, err) {
				return report, err
			}
		}
	}

	return report, nil
}

func decodeInquiry(data []byte, report *entity.InspectDeviceResponse) error {
	// Additional length describes available data, including bytes beyond this bounded allocation.
	if len(data) < 36 || int(data[4])+5 < 36 {
		return ErrMalformed
	}
	if data[0]&0x1f != 1 {
		return ErrUnsupported
	}
	for _, field := range []struct {
		start, end int
		target     **string
	}{{8, 16, &report.Vendor}, {16, 32, &report.Model}, {32, 36, &report.Firmware}} {
		value := strings.TrimSpace(string(data[field.start:field.end]))
		if !utf8.ValidString(value) || strings.ContainsRune(value, 0) {
			return ErrMalformed
		}
		if value != "" {
			*field.target = proto.String(value)
		}
	}
	return nil
}

func decodeBlockLimits(data []byte, report *entity.InspectDeviceResponse, transport Transport) error {
	// SSC-3 §7.4 uses a zero maximum to mean unspecified, not a zero-size transfer envelope.
	if len(data) != 6 || data[0]&0xe0 != 0 {
		return ErrMalformed
	}
	minimum := uint32(binary.BigEndian.Uint16(data[4:6]))
	maximum := uint32(data[1])<<16 | uint32(data[2])<<8 | uint32(data[3])
	if maximum != 0 && minimum > maximum {
		return ErrMalformed
	}
	limits := &entity.TapeRecordLimits{MinimumBytes: proto.Uint32(minimum), AlignmentBytes: proto.Uint32(1 << uint(data[0]&0x1f))}
	if maximum != 0 {
		limits.MaximumBytes = proto.Uint32(maximum)
		limits.Fixed, limits.Variable = proto.Bool(true), proto.Bool(minimum != maximum)
	}
	limits.TransportMaximumBytes = transportMaximumBytes(transport)
	for _, kind := range []entity.DeviceFeatureKind{entity.DeviceFeatureKind_DEVICE_FEATURE_KIND_SEQUENTIAL_READ, entity.DeviceFeatureKind_DEVICE_FEATURE_KIND_SEQUENTIAL_WRITE} {
		f := feature(report, kind)
		f.Support = entity.DeviceFeatureSupport_DEVICE_FEATURE_SUPPORT_PARTIAL
		f.Limits, f.Evidence = &entity.DeviceFeature_Records{Records: proto.Clone(limits).(*entity.TapeRecordLimits)}, entity.DeviceFeatureEvidence_DEVICE_FEATURE_EVIDENCE_REPORTED
		f.Reason, f.Detail = entity.DeviceReason_DEVICE_REASON_NOT_IMPLEMENTED, "Record bounds reported; acceptance and completion prerequisites remain unqualified"
	}
	return nil
}

func transportMaximumBytes(transport Transport) *uint32 {
	// A growable per-fd reservation is a current allocation, not an observed adapter maximum.
	if _, growable := transport.(interface {
		EnsureTransfer(context.Context, int) error
	}); growable {
		return nil
	}
	if bounded, ok := transport.(interface{ TransferLimit() int }); ok {
		if limit := bounded.TransferLimit(); limit > 0 && limit <= MaxTransferBytes {
			return proto.Uint32(uint32(limit))
		}
	}
	return nil
}

func modeData(data []byte) ([]byte, map[byte][]byte, error) {
	// Refuse truncated headers/pages before publishing current mode facts.
	if len(data) < 8 || int(binary.BigEndian.Uint16(data[:2]))+2 > len(data) || data[4]&1 != 0 {
		return nil, nil, ErrMalformed
	}
	data = data[:int(binary.BigEndian.Uint16(data[:2]))+2]
	if len(data) < 8 {
		return nil, nil, ErrMalformed
	}
	descriptors := int(binary.BigEndian.Uint16(data[6:8]))
	if descriptors != 0 && descriptors != 8 || 8+descriptors > len(data) {
		return nil, nil, ErrMalformed
	}
	pages := make(map[byte][]byte)
	for offset := 8 + descriptors; offset < len(data); {
		if len(data)-offset < 2 {
			return nil, nil, ErrMalformed
		}
		header, length := 2, int(data[offset+1])
		if data[offset]&0x40 != 0 {
			if len(data)-offset < 4 {
				return nil, nil, ErrMalformed
			}
			header, length = 4, int(binary.BigEndian.Uint16(data[offset+2:offset+4]))
		}
		end := offset + header + length
		if end > len(data) {
			return nil, nil, ErrMalformed
		}
		if header == 2 {
			if _, duplicate := pages[data[offset]&0x3f]; duplicate {
				return nil, nil, ErrMalformed
			}
			pages[data[offset]&0x3f] = data[offset:end]
		}
		offset = end
	}
	if page, ok := pages[0x0f]; ok && len(page) < 16 {
		return nil, nil, ErrMalformed
	}
	if page, ok := pages[0x11]; ok && len(page) < 8 {
		return nil, nil, ErrMalformed
	}

	return data, pages, nil
}

func decodeMode(data []byte, report *entity.TapeDeviceReport) (*uint32, map[byte][]byte, error) {
	data, pages, err := modeData(data)
	if err != nil {
		return nil, nil, err
	}
	if page, ok := pages[0x11]; ok && page[3] > page[2] {
		return nil, nil, ErrMalformed
	}
	return observeMode(data, pages, report), pages, nil
}

func observeMode(data []byte, pages map[byte][]byte, report *entity.TapeDeviceReport) *uint32 {
	descriptors := int(binary.BigEndian.Uint16(data[6:8]))

	// Non-secret observed settings are distinct from changeability and cleanup qualification.
	var partition *uint32
	if report.GetMediumPresent() {
		report.WriteProtected = proto.Bool(data[3]&0x80 != 0)
		if page := pages[0x10]; len(page) >= 16 {
			partition = proto.Uint32(uint32(page[3]))
		}
		if page, ok := pages[0x11]; ok && len(page) >= 8 {
			report.PartitionCount = proto.Uint32(uint32(page[3]) + 1)
		}
	}
	report.BufferedWrites = proto.Bool(data[3]&0x70 != 0)
	if descriptors == 8 {
		if report.GetMediumPresent() {
			report.DensityCode = proto.Uint32(uint32(data[8]))
		}
		block := uint32(data[13])<<16 | uint32(data[14])<<8 | uint32(data[15])
		report.RecordBytes, report.FixedRecords = proto.Uint32(block), proto.Bool(block != 0)
	}
	report.CompressionEnabled, report.DecompressionEnabled = compressionState(pages)
	return partition
}
