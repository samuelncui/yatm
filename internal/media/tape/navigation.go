package tape

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/samuelncui/yatm/entity"
	"google.golang.org/protobuf/proto"
)

// Navigation exposes standard SSC logical-object addressing within the prepared record envelope.
// It does not establish physical qualification; consumers verify fresh positions and completion
// before publishing anchors and retain the attempt lease through every issued command.
type Navigation struct {
	records Records
	bits    uint32
	long    bool
	opcode  byte
}

// Navigation returns an eligible pairing without another form probe or physical validation claim.
// Missing optional evidence leaves baseline sequential access available.
func (d *Device) Navigation(ctx context.Context) (*Navigation, error) {
	// Reuse passive form evidence only while command admission remains available.
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if d.operation == 0 || d.navigation == nil {
		return nil, nil
	}

	// Configure owns the SSC record envelope; a reported contradiction or failed qualification removes seek.
	for _, kind := range []entity.DeviceFeatureKind{entity.DeviceFeatureKind_DEVICE_FEATURE_KIND_READ_POSITION, entity.DeviceFeatureKind_DEVICE_FEATURE_KIND_LOCATE} {
		f := feature(d.report, kind)
		if f.Support != entity.DeviceFeatureSupport_DEVICE_FEATURE_SUPPORT_SUPPORTED ||
			f.Validation == entity.DeviceFeatureValidation_DEVICE_FEATURE_VALIDATION_FAILED {
			return nil, nil
		}
		if f.Reason != 0 && f.Reason != entity.DeviceReason_DEVICE_REASON_NOT_READY {
			return nil, nil
		}
	}
	return d.navigation, nil
}

func (n *Navigation) AddressBits() uint32 { return n.bits }

// Position preserves GOOD unknown flags separately from unsupported, malformed or uncertain errors.
func (n *Navigation) Position(ctx context.Context) (Position, error) {
	return n.records.Position(ctx, n.long)
}

// Locate chooses one observed form before motion and never retries another form after issuance.
// The consumer owns source-lane failure; Transport owns serialization and uncertain outcomes.
func (n *Navigation) Locate(ctx context.Context, partition uint32, object uint64) error {
	// The consumed tar pairing is scoped to partition zero, not every representable partition field.
	if partition != 0 {
		return fmt.Errorf("Tape navigation pairing is scoped to partition zero: %w", ErrUnsupported)
	}

	// Select and range-check the established form before entering the transport lane.
	if n.bits == 32 && object > math.MaxUint32 {
		return fmt.Errorf("Tape object exceeds navigation address width")
	}
	cdb, err := locateCDB(n.opcode, partition, object)
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return n.records.locate(ctx, cdb)
}

func locateCDB(opcode byte, partition uint32, object uint64) ([]byte, error) {
	// SSC implicit object addressing uses CP=1, IMMED=0, and BT/DEST_TYPE/BAM=0.
	if partition > math.MaxUint8 {
		return nil, fmt.Errorf("Tape partition exceeds the LOCATE field")
	}
	switch opcode {
	case 0x2b:
		if object > math.MaxUint32 {
			return nil, fmt.Errorf("Tape object exceeds LOCATE(10) address width")
		}
		cdb := make([]byte, 10)
		cdb[0], cdb[1], cdb[8] = opcode, 2, byte(partition)
		binary.BigEndian.PutUint32(cdb[3:7], uint32(object))
		return cdb, nil
	case 0x92:
		cdb := make([]byte, 16)
		cdb[0], cdb[1], cdb[3] = opcode, 2, byte(partition)
		binary.BigEndian.PutUint64(cdb[4:12], object)
		return cdb, nil
	default:
		return nil, ErrUnsupported
	}
}

func inspectNavigation(ctx context.Context, transport Transport, report *entity.InspectDeviceResponse, observed Position, observedLong bool, positionErr error) (*Navigation, Position, error) {
	// Select the reported host-next observation form independently from the motion command width.
	unknown, positionSupported, long := false, false, false
	for _, candidate := range []bool{true, false} {
		// The initial short observation already means the actual long query was cleanly rejected.
		if candidate && !observedLong {
			continue
		}
		action := uint16(0)
		if candidate {
			action = 6
		}
		form := make([]byte, 10)
		form[0], form[1] = 0x34, byte(action)
		data, err := reportCommand(ctx, transport, 0x34, &action)
		if err != nil {
			return nil, observed, navigationReportError(report, err)
		}
		support, err := commandFormSupport(data, form)
		if err != nil {
			return nil, observed, navigationReportError(report, err)
		}
		if support != entity.DeviceFeatureSupport_DEVICE_FEATURE_SUPPORT_SUPPORTED {
			unknown = unknown || support == entity.DeviceFeatureSupport_DEVICE_FEATURE_SUPPORT_UNKNOWN
			continue
		}
		long, positionSupported = candidate, true
		break
	}

	// The effective range is the intersection of the observed READ POSITION and supported LOCATE.
	for _, opcode := range []byte{0x92, 0x2b} {
		if !positionSupported {
			break
		}
		bits := uint32(32)
		object := uint64(math.MaxUint32)
		if long && opcode == 0x92 {
			bits = 64
			object = math.MaxUint64
		}
		form, _ := locateCDB(opcode, 0, object)
		data, err := reportCommand(ctx, transport, opcode, nil)
		if err != nil {
			return nil, observed, navigationReportError(report, err)
		}
		support, err := commandFormSupport(data, form)
		if err != nil {
			return nil, observed, navigationReportError(report, err)
		}
		if support != entity.DeviceFeatureSupport_DEVICE_FEATURE_SUPPORT_SUPPORTED {
			unknown = unknown || support == entity.DeviceFeatureSupport_DEVICE_FEATURE_SUPPORT_UNKNOWN
			continue
		}

		// A different selected form needs its own passive observation; a failed same-form query is not retried.
		if long != observedLong {
			observed, positionErr = (Records{Transport: transport}).Position(ctx, long)
		}
		limits := &entity.TapeNavigationLimits{AddressBits: proto.Uint32(bits)}
		if long {
			limits.ReadPositionLong = proto.Bool(true)
		} else {
			limits.ReadPositionShort = proto.Bool(true)
		}
		for _, kind := range []entity.DeviceFeatureKind{entity.DeviceFeatureKind_DEVICE_FEATURE_KIND_READ_POSITION, entity.DeviceFeatureKind_DEVICE_FEATURE_KIND_LOCATE} {
			f := feature(report, kind)
			f.Support, f.Evidence = entity.DeviceFeatureSupport_DEVICE_FEATURE_SUPPORT_SUPPORTED, entity.DeviceFeatureEvidence_DEVICE_FEATURE_EVIDENCE_REPORTED
			f.Validation = entity.DeviceFeatureValidation_DEVICE_FEATURE_VALIDATION_UNVALIDATED
			f.Limits = &entity.DeviceFeature_Navigation{Navigation: proto.Clone(limits).(*entity.TapeNavigationLimits)}
			f.Availability, f.Reason = entity.DeviceFeatureAvailability_DEVICE_FEATURE_AVAILABILITY_UNKNOWN, entity.DeviceReason_DEVICE_REASON_NOT_READY
			f.Detail = "Standard SSC logical-object pair reported; current logical position is unknown; physical reload qualification remains unvalidated"
			if positionErr != nil {
				f.Availability, f.Reason, f.Detail = entity.DeviceFeatureAvailability_DEVICE_FEATURE_AVAILABILITY_UNAVAILABLE, Reason(positionErr), positionErr.Error()
			} else if observed.ObjectKnown && (observed.Partition != 0 || observed.Object > object) {
				f.Availability = entity.DeviceFeatureAvailability_DEVICE_FEATURE_AVAILABILITY_UNAVAILABLE
				f.Detail = "Observed position is outside the partition-zero navigation range; physical reload qualification remains unvalidated"
			} else if observed.ObjectKnown && observed.Partition == 0 && observed.Object <= object {
				f.Availability, f.Reason = entity.DeviceFeatureAvailability_DEVICE_FEATURE_AVAILABILITY_READY, 0
				f.Detail = "Standard SSC logical-object pair and exact current position observed; physical reload qualification remains unvalidated"
			}
		}
		return &Navigation{records: Records{Transport: transport, Timeout: 10 * time.Minute}, bits: bits, long: long, opcode: opcode}, observed, positionErr
	}

	// No complete pair means no motion capability; vendor/unknown support never becomes unsupported hardware.
	f := feature(report, entity.DeviceFeatureKind_DEVICE_FEATURE_KIND_LOCATE)
	f.Evidence = entity.DeviceFeatureEvidence_DEVICE_FEATURE_EVIDENCE_REPORTED
	f.Support, f.Availability, f.Reason = entity.DeviceFeatureSupport_DEVICE_FEATURE_SUPPORT_UNSUPPORTED, entity.DeviceFeatureAvailability_DEVICE_FEATURE_AVAILABILITY_UNAVAILABLE, entity.DeviceReason_DEVICE_REASON_UNSUPPORTED
	f.Detail = "No standard READ POSITION/LOCATE pair is reported"
	if unknown {
		f.Support, f.Availability, f.Reason = entity.DeviceFeatureSupport_DEVICE_FEATURE_SUPPORT_UNKNOWN, entity.DeviceFeatureAvailability_DEVICE_FEATURE_AVAILABILITY_UNKNOWN, entity.DeviceReason_DEVICE_REASON_NOT_READY
		f.Detail = "READ POSITION/LOCATE pair support is not established"
	}
	return nil, observed, nil
}

func navigationReportError(report *entity.InspectDeviceResponse, err error) error {
	// Unsupported optional reporting is unknown operation support, not a hardware capability conclusion.
	f := feature(report, entity.DeviceFeatureKind_DEVICE_FEATURE_KIND_LOCATE)
	f.Availability, f.Reason, f.Detail = entity.DeviceFeatureAvailability_DEVICE_FEATURE_AVAILABILITY_UNKNOWN, Reason(err), err.Error()
	if errors.Is(err, ErrUncertain) {
		f.Availability = entity.DeviceFeatureAvailability_DEVICE_FEATURE_AVAILABILITY_UNAVAILABLE
	}
	return err
}
