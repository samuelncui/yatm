package tape

import (
	"context"
	"errors"

	"github.com/samuelncui/yatm/entity"
)

func (d *Device) inspectEncryption(ctx context.Context, status []byte) error {
	// Diagnostics describe reported capabilities; the configured Tape script owns key management.
	f := feature(d.report, entity.DeviceFeatureKind_DEVICE_FEATURE_KIND_HARDWARE_ENCRYPTION)
	if status == nil {
		f.Support, f.Availability, f.Reason = entity.DeviceFeatureSupport_DEVICE_FEATURE_SUPPORT_UNSUPPORTED, entity.DeviceFeatureAvailability_DEVICE_FEATURE_AVAILABILITY_UNAVAILABLE, entity.DeviceReason_DEVICE_REASON_UNSUPPORTED
		f.Evidence, f.Detail = entity.DeviceFeatureEvidence_DEVICE_FEATURE_EVIDENCE_REPORTED, "Tape Data Encryption protocol is absent"
		return nil
	}

	// Current modes remain useful independently of optional named-algorithm observations.
	state, err := decodeEncryptionState(status)
	if err != nil {
		return err
	}
	d.reportEncryptionState(state)
	f.Support, f.Evidence = entity.DeviceFeatureSupport_DEVICE_FEATURE_SUPPORT_PARTIAL, entity.DeviceFeatureEvidence_DEVICE_FEATURE_EVIDENCE_REPORTED

	// Query optional algorithm details while preserving the known protocol and status.
	data, err := securityPage(ctx, d.transport, 0x10)
	if err != nil {
		f.Availability, f.Reason, f.Detail = entity.DeviceFeatureAvailability_DEVICE_FEATURE_AVAILABILITY_UNKNOWN, Reason(err), "Encryption algorithm observation is unavailable"
		return errors.Join(err, ctx.Err())
	}

	// Malformed optional algorithms retain the known status and their own diagnostic reason.
	algorithms, err := decodeEncryptionCapabilities(data)
	if err != nil {
		f.Availability, f.Reason, f.Detail = entity.DeviceFeatureAvailability_DEVICE_FEATURE_AVAILABILITY_UNKNOWN, Reason(err), err.Error()
		return err
	}

	// An unnamed algorithm cannot revoke the Tape Data Encryption protocol already observed.
	limits := f.GetEncryption()
	limits.Algorithms = nil
	for _, algorithm := range algorithms {
		if algorithm.encrypt != 2 && algorithm.decrypt != 2 {
			continue
		}
		limits.Algorithms = append(limits.Algorithms, &entity.TapeEncryptionAlgorithm{Name: EncryptionAES256GCM, Index: uint32(algorithm.index), KeyBytes: 32})
		if algorithm.encrypt == 2 && algorithm.decrypt == 2 {
			f.Support = entity.DeviceFeatureSupport_DEVICE_FEATURE_SUPPORT_SUPPORTED
		} else if f.Support != entity.DeviceFeatureSupport_DEVICE_FEATURE_SUPPORT_SUPPORTED {
			f.Support = entity.DeviceFeatureSupport_DEVICE_FEATURE_SUPPORT_PARTIAL
		}
	}
	f.Availability, f.Reason = entity.DeviceFeatureAvailability_DEVICE_FEATURE_AVAILABILITY_UNKNOWN, 0
	f.Detail = "Reported encryption algorithms and current modes; the configured Tape script owns preparation"
	return nil
}
