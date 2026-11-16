package tape

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"time"

	"github.com/samuelncui/yatm/entity"
	"google.golang.org/protobuf/proto"
)

// WrapEndpoint is a directory endpoint: the first logical object after the wrap, or EOD.
type WrapEndpoint struct {
	Partition uint32
	Wrap      uint32
	Object    uint64
}

// WrapMap retains the documented response order and gaps. AllWraps describes the reporting form,
// not H6 coverage; absent Fresh is unknown. Costs need separate evidence.
type WrapMap struct {
	Endpoints  []WrapEndpoint
	AllWraps   bool
	ObservedAt time.Time
	Fresh      *bool
	Source     string
	valid      *atomic.Bool
}

// Valid tracks this opened session's snapshot lifetime independently of freshness or qualification.
func (m *WrapMap) Valid() bool { return m != nil && m.valid != nil && m.valid.Load() }

// Complete reports a live all-wraps observation with at least one endpoint.
func (m *WrapMap) Complete() bool { return m.Valid() && m.AllWraps && len(m.Endpoints) != 0 }

// Geometry returns a request-owned observation, trying CM once when REOWP has no complete directory.
func (d *Device) Geometry(ctx context.Context) (*WrapMap, error) {
	// CM is an optional preparation query, never part of ordinary Inspect or a per-file operation.
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if d.cmPending && !d.geometry.Complete() {
		d.cmPending = false
		if err := d.observeCartridgeDirectory(ctx); err != nil {
			return nil, err
		}
	}

	// Copies retain the same lifetime marker; invalidation cannot leave an earlier copy usable.
	if !d.geometry.Valid() {
		return nil, nil
	}
	copy := *d.geometry
	copy.Endpoints = append([]WrapEndpoint(nil), d.geometry.Endpoints...)
	if copy.Fresh != nil {
		copy.Fresh = proto.Bool(*copy.Fresh)
	}
	return &copy, nil
}

func (d *Device) invalidateGeometry() {
	d.cmPending = false
	d.actualPartitions = 0
	if d.geometry != nil {
		d.geometry.valid.Store(false)
	}
}

func wrapCommand(all bool) []byte {
	cdb := []byte{0xa3, 0x1f, 0x45, 0, 0, 0, 0, 0, 0, 0, 0, 0}
	length := uint32(10)
	if all {
		cdb[3], length = 2, MaxQueryBytes
	}
	binary.BigEndian.PutUint32(cdb[6:10], length)
	return cdb
}

func decodeWrapMap(data []byte, all bool) (*WrapMap, bool, error) {
	// IBM GA32-0928-08 §5.2.20 and HPE 20-STG-L7-TRMV3-ED5 independently document this framing.
	// HPE source: https://support.hpe.com/hpesc/public/docDisplay?docId=sd00006909en_us
	// HPE short/long response topics: GUID-D7147C7F-2016-0901-0922-00000000067B / 67E.
	if len(data) < 4 || data[2] != 0 || data[3] != 0 {
		return nil, false, ErrMalformed
	}
	end := 2 + int(binary.BigEndian.Uint16(data[:2]))
	if end < 4 || !all && end != 10 || all && (end-4)%12 != 0 {
		return nil, false, ErrMalformed
	}
	if end > len(data) {
		// Allocation truncation is a bounded partial observation, not proof of a complete populated-wrap map.
		if all && len(data) == MaxQueryBytes && end > MaxQueryBytes {
			return nil, false, nil
		}
		return nil, false, ErrMalformed
	}
	observed := &WrapMap{AllWraps: all, ObservedAt: time.Now(), valid: new(atomic.Bool)}
	observed.valid.Store(true)
	if !all {
		// First-wrap reporting has no partition/wrap identifiers; do not fabricate wrap zero or an association.
		return observed, true, nil
	}
	seen := make(map[[2]uint32]bool)
	for offset := 4; offset < end; offset += 12 {
		descriptor := data[offset : offset+12]
		wrap, partition := uint32(binary.BigEndian.Uint16(descriptor[:2])), uint32(binary.BigEndian.Uint16(descriptor[2:4]))
		key := [2]uint32{partition, wrap}
		if descriptor[4] != 0 || descriptor[5] != 0 || seen[key] {
			return nil, false, ErrMalformed
		}
		seen[key] = true
		object := uint64(0)
		for _, value := range descriptor[6:12] {
			object = object<<8 | uint64(value)
		}
		observed.Endpoints = append(observed.Endpoints, WrapEndpoint{Partition: partition, Wrap: wrap, Object: object})
	}
	return observed, true, nil
}

func (d *Device) inspectGeometry(ctx context.Context) error {
	// This maintenance-in service action is vendor-defined; only public documented families authorize its meaning.
	d.invalidateGeometry()
	d.geometry = nil
	d.cmPending = true
	f := feature(d.report, entity.DeviceFeatureKind_DEVICE_FEATURE_KIND_END_OF_WRAP_POSITION)
	vendor := strings.TrimSpace(d.report.GetVendor())
	switch vendor {
	case "IBM":
	case "HP", "HPE":
		if !strings.HasPrefix(strings.TrimSpace(d.report.GetModel()), "Ultrium") {
			f.Detail = "No applicable documented REOWP product family is established"
			return nil
		}
	default:
		f.Detail = "No applicable documented REOWP protocol family is established"
		return nil
	}

	// Preserve exact standard-form checks before considering the narrowly scoped vendor query policy.
	f.Detail = "Documented REOWP form, complete response and load history remain unobserved"
	f.Reason = entity.DeviceReason_DEVICE_REASON_NOT_READY
	action := uint16(0x1f)
	data, err := reportCommand(ctx, d.transport, 0xa3, &action)
	if err != nil {
		return d.geometryReportError(err)
	}
	all := true
	support, err := commandFormSupport(data, wrapCommand(true))
	if err != nil {
		return d.geometryReportError(err)
	}
	if data[1]&7 == 3 && (len(data) < 16 || data[5] != 0x1f) {
		return d.geometryReportError(ErrMalformed)
	}

	// A documented HPE family may observe one exact form when RSOC reports vendor-specific support.
	// This is query admission, not standard support; unrelated CDB-form validation remains unchanged.
	vendorReport := vendor != "IBM" && data[1]&7 == 5
	if vendorReport {
		if len(data) < 16 || data[0] != 0 || data[1]&0x78 != 0 ||
			binary.BigEndian.Uint16(data[2:4]) != 12 || data[4] != 0xa3 || data[5] != 0x1f {
			return d.geometryReportError(ErrMalformed)
		}
		if data[1]&0x80 != 0 && (len(data) < 28 || binary.BigEndian.Uint16(data[16:18]) != 10) {
			return d.geometryReportError(ErrMalformed)
		}
	}
	if !vendorReport && support != entity.DeviceFeatureSupport_DEVICE_FEATURE_SUPPORT_SUPPORTED {
		if support == entity.DeviceFeatureSupport_DEVICE_FEATURE_SUPPORT_UNKNOWN {
			return nil
		}
		all = false
		support, err = commandFormSupport(data, wrapCommand(false))
		if err != nil {
			return d.geometryReportError(err)
		}
		if support != entity.DeviceFeatureSupport_DEVICE_FEATURE_SUPPORT_SUPPORTED {
			f.Support = support
			f.Evidence = entity.DeviceFeatureEvidence_DEVICE_FEATURE_EVIDENCE_REPORTED
			if support == entity.DeviceFeatureSupport_DEVICE_FEATURE_SUPPORT_UNSUPPORTED {
				f.Availability, f.Reason = entity.DeviceFeatureAvailability_DEVICE_FEATURE_AVAILABILITY_UNAVAILABLE, entity.DeviceReason_DEVICE_REASON_UNSUPPORTED
			}
			return nil
		}
	}

	// Retain proven standard-form evidence without treating a vendor-specific report as an accepted query.
	limits := &entity.TapeGeometryLimits{AllWraps: proto.Bool(all), Source: proto.String("A3/1F/45")}
	if !vendorReport {
		f.Support, f.Evidence = entity.DeviceFeatureSupport_DEVICE_FEATURE_SUPPORT_PARTIAL, entity.DeviceFeatureEvidence_DEVICE_FEATURE_EVIDENCE_REPORTED
		f.Limits = &entity.DeviceFeature_Geometry{Geometry: limits}
	}

	// An absent/unready cartridge cannot produce endpoint facts; an issued failure never tries another form.
	if !d.report.GetTape().GetMediumPresent() || d.report.Reason == entity.DeviceReason_DEVICE_REASON_NOT_READY {
		return nil
	}
	length := MaxQueryBytes
	if !all {
		length = 10
	}
	data, err = query(ctx, d.transport, wrapCommand(all), length)
	if err != nil {
		return d.geometryReportError(err)
	}
	f.Support, f.Evidence = entity.DeviceFeatureSupport_DEVICE_FEATURE_SUPPORT_PARTIAL, entity.DeviceFeatureEvidence_DEVICE_FEATURE_EVIDENCE_OBSERVED
	f.Limits = &entity.DeviceFeature_Geometry{Geometry: limits}

	// Decode only reported objects and identities; observation does not normalize boundaries or establish freshness.
	observed, complete, err := decodeWrapMap(data, all)
	if err != nil {
		return d.geometryReportError(err)
	}
	if !complete {
		f.Detail = "REOWP response exceeds the bounded observation; complete coverage and freshness remain unknown"
		return nil
	}
	observed.Source, d.geometry = "A3/1F/45", observed
	count := len(observed.Endpoints)
	if !all {
		count = 1
	} else {
		f.Support = entity.DeviceFeatureSupport_DEVICE_FEATURE_SUPPORT_SUPPORTED
	}
	limits.ReportedWraps = proto.Uint32(uint32(count))
	f.Availability, f.Reason = entity.DeviceFeatureAvailability_DEVICE_FEATURE_AVAILABILITY_READY, 0
	f.Detail = "REOWP reporting form observed; full-wrap coverage, freshness, geometry and timing remain unvalidated"
	return nil
}

func (d *Device) geometryReportError(err error) error {
	// Optional unsupported/malformed queries do not disable baseline I/O; readiness or ownership loss is attempt-wide.
	f := feature(d.report, entity.DeviceFeatureKind_DEVICE_FEATURE_KIND_END_OF_WRAP_POSITION)
	d.invalidateGeometry()
	f.Availability, f.Reason, f.Detail = entity.DeviceFeatureAvailability_DEVICE_FEATURE_AVAILABILITY_UNKNOWN, Reason(err), err.Error()
	f.Limits, d.geometry = nil, nil
	if f.Reason == entity.DeviceReason_DEVICE_REASON_UNSUPPORTED {
		f.Availability = entity.DeviceFeatureAvailability_DEVICE_FEATURE_AVAILABILITY_UNAVAILABLE
	}
	if errors.Is(err, ErrUncertain) || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || f.Reason == entity.DeviceReason_DEVICE_REASON_NO_MEDIUM || f.Reason == entity.DeviceReason_DEVICE_REASON_NOT_READY {
		return err
	}
	d.cmPending = true
	return nil
}

func (d *Device) observeCartridgeDirectory(ctx context.Context) error {
	// The directory format follows the cartridge generation; the decoder qualifies it for any drive.
	if !d.report.GetTape().GetMediumPresent() || d.report.Reason == entity.DeviceReason_DEVICE_REASON_NOT_READY {
		return nil
	}
	// Configure already confirms actual partitions for tar. Pre-mount LTFS needs one MAM observation;
	// a pending MODE SELECT page or a retained CM EOD descriptor cannot supply this authority.
	partitions := d.actualPartitions
	var err error
	if partitions == 0 {
		partitions, err = d.partitionCount(ctx)
	}
	if err == nil && (partitions < 1 || partitions > uint32(len(eodPages))) {
		err = fmt.Errorf("actual cartridge partition count %d is unsupported: %w", partitions, ErrUnsupported)
	}
	if err == nil {
		d.actualPartitions = partitions
		for _, buffer := range cartridgeMemoryBuffers {
			var observed *WrapMap
			if observed, err = readCartridgeDirectory(ctx, d.transport, buffer, partitions); err == nil {
				d.invalidateGeometry()
				d.geometry, d.actualPartitions = observed, partitions
				feature(d.report, entity.DeviceFeatureKind_DEVICE_FEATURE_KIND_SOFTWARE_READ_ORDER).Detail =
					observed.Source + " observed; interpolation and timing require separate qualification"
				return nil
			}
			if !cartridgeMemoryBufferMiss(err) {
				break
			}
		}
	}

	// CM never grants REOWP command support. Only uncertain completion or lost ownership aborts preparation.
	optimization := feature(d.report, entity.DeviceFeatureKind_DEVICE_FEATURE_KIND_SOFTWARE_READ_ORDER)
	optimization.Detail = "Cartridge memory directory is unavailable: " + err.Error()
	reason := Reason(err)
	var failure *CommandError
	mediumChanged := errors.As(err, &failure) && failure.Sense.Key == 6 && failure.Sense.ASC == 0x28
	if errors.Is(err, ErrUncertain) || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) ||
		mediumChanged || reason == entity.DeviceReason_DEVICE_REASON_NO_MEDIUM || reason == entity.DeviceReason_DEVICE_REASON_NOT_READY {
		d.invalidateGeometry()
		return err
	}
	return nil
}

func readCartridgeDirectory(ctx context.Context, transport Transport, buffer byte, partitions uint32) (*WrapMap, error) {
	data, err := readCartridgeMemory(ctx, transport, buffer)
	if err != nil {
		return nil, err
	}
	if len(data) == 0 {
		return nil, fmt.Errorf("cartridge memory buffer %#02x is empty: %w", buffer, ErrUnsupported)
	}
	return decodeCartridgeDirectory(data, partitions)
}

// cartridgeMemoryBufferMiss reports a buffer that holds no decodable image, so the next documented
// buffer may hold it; uncertainty, cancellation and medium changes end the observation instead.
func cartridgeMemoryBufferMiss(err error) bool {
	var failure *CommandError
	if errors.Is(err, ErrUncertain) || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) ||
		errors.As(err, &failure) && failure.Sense.Key == 6 {
		return false
	}
	reason := Reason(err)
	return reason != entity.DeviceReason_DEVICE_REASON_NO_MEDIUM && reason != entity.DeviceReason_DEVICE_REASON_NOT_READY
}
