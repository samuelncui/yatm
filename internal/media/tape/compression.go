package tape

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"

	"github.com/samuelncui/yatm/entity"
	"google.golang.org/protobuf/proto"
)

func compressionState(pages map[byte][]byte) (*bool, *bool) {
	// MP0F separates write compression from read decoding; the legacy selector must agree when present.
	page, configuration := pages[0x0f], pages[0x10]
	if len(page) >= 16 {
		enabled := page[2]&0x80 != 0
		decode := proto.Bool(page[3]&0x80 != 0)
		if enabled && page[2]&0x40 == 0 || len(configuration) >= 16 && (configuration[14] > 1 || (configuration[14] == 1) != enabled) {
			return nil, decode
		}
		return proto.Bool(enabled), decode
	}
	if len(configuration) >= 16 && configuration[14] <= 1 {
		return proto.Bool(configuration[14] == 1), nil
	}
	return nil, nil
}

func (d *Device) refreshCompressionAlgorithms(pages map[byte][]byte) {
	// Current-PC observations update IDs without replacing prior masks, support or physical validation.
	for _, kind := range []entity.DeviceFeatureKind{entity.DeviceFeatureKind_DEVICE_FEATURE_KIND_HARDWARE_COMPRESSION, entity.DeviceFeatureKind_DEVICE_FEATURE_KIND_HARDWARE_DECOMPRESSION} {
		limits := feature(d.report, kind).GetCompression()
		if limits == nil {
			continue
		}
		limits.CurrentAlgorithm = nil
		if page := pages[0x0f]; len(page) >= 16 {
			index := 4
			if kind == entity.DeviceFeatureKind_DEVICE_FEATURE_KIND_HARDWARE_DECOMPRESSION {
				index = 8
			}
			limits.CurrentAlgorithm = proto.Uint32(binary.BigEndian.Uint32(page[index : index+4]))
		}
	}
}

func (d *Device) reportCompression(pages, changes map[byte][]byte, changeErr error) {
	// Current algorithm IDs are observations, not an inferred supported-algorithm list or qualification.
	page := pages[0x0f]
	for _, kind := range []entity.DeviceFeatureKind{entity.DeviceFeatureKind_DEVICE_FEATURE_KIND_HARDWARE_COMPRESSION, entity.DeviceFeatureKind_DEVICE_FEATURE_KIND_HARDWARE_DECOMPRESSION} {
		f := feature(d.report, kind)
		f.Support, f.Availability = entity.DeviceFeatureSupport_DEVICE_FEATURE_SUPPORT_UNKNOWN, entity.DeviceFeatureAvailability_DEVICE_FEATURE_AVAILABILITY_UNKNOWN
		f.Evidence, f.Validation = entity.DeviceFeatureEvidence_DEVICE_FEATURE_EVIDENCE_UNSPECIFIED, entity.DeviceFeatureValidation_DEVICE_FEATURE_VALIDATION_UNVALIDATED
		f.Reason, f.Detail, f.Limits = entity.DeviceReason_DEVICE_REASON_NOT_READY, "Compression mode prerequisites are not established", nil
		if len(page) < 16 {
			continue
		}
		limits := new(entity.TapeCompressionLimits)
		f.Limits = &entity.DeviceFeature_Compression{Compression: limits}
		f.Evidence = entity.DeviceFeatureEvidence_DEVICE_FEATURE_EVIDENCE_REPORTED
		index, mask, algorithm := 3, byte(0x80), 8
		if kind == entity.DeviceFeatureKind_DEVICE_FEATURE_KIND_HARDWARE_COMPRESSION {
			index, algorithm = 2, 4
			limits.CurrentAlgorithm = proto.Uint32(binary.BigEndian.Uint32(page[4:8]))
			if page[2]&0x40 == 0 {
				f.Support, f.Availability, f.Reason = entity.DeviceFeatureSupport_DEVICE_FEATURE_SUPPORT_UNSUPPORTED, entity.DeviceFeatureAvailability_DEVICE_FEATURE_AVAILABILITY_UNAVAILABLE, entity.DeviceReason_DEVICE_REASON_UNSUPPORTED
				f.Detail = "DCC reports no hardware write compression capability"
				continue
			}
		}
		limits.CurrentAlgorithm = proto.Uint32(binary.BigEndian.Uint32(page[algorithm : algorithm+4]))
		f.Support = entity.DeviceFeatureSupport_DEVICE_FEATURE_SUPPORT_SUPPORTED
		if changeErr == nil && len(changes[0x0f]) >= 16 {
			limits.EnableChangeable = proto.Bool(changes[0x0f][index]&mask != 0)
		}
		if page[index]&mask != 0 || limits.GetEnableChangeable() {
			f.Availability, f.Reason = entity.DeviceFeatureAvailability_DEVICE_FEATURE_AVAILABILITY_READY, 0
		}
		f.Detail = "Current standard compression controls observed; physical behavior and cleanup remain unvalidated"
		if index == 2 && (d.report.GetTape().CompressionEnabled == nil || limits.GetCurrentAlgorithm() == 0) {
			f.Availability, f.Reason = entity.DeviceFeatureAvailability_DEVICE_FEATURE_AVAILABILITY_UNKNOWN, entity.DeviceReason_DEVICE_REASON_NOT_READY
		}
		if index == 3 && page[3]&0x60 != 0 {
			if page[3]&0x60 == 0x60 || len(changes[0x0f]) < 16 || page[3]&0x60&^changes[0x0f][3] != 0 {
				f.Availability, f.Reason = entity.DeviceFeatureAvailability_DEVICE_FEATURE_AVAILABILITY_UNKNOWN, entity.DeviceReason_DEVICE_REASON_NOT_READY
			}
		}
		if index == 2 && !d.report.GetTape().GetCompressionEnabled() && len(pages[0x10]) >= 16 && (len(changes[0x10]) < 16 || changes[0x10][14]&1 == 0) {
			f.Availability, f.Reason = entity.DeviceFeatureAvailability_DEVICE_FEATURE_AVAILABILITY_UNKNOWN, entity.DeviceReason_DEVICE_REASON_NOT_READY
			f.Detail = "Write enable-bit changeability is observed; required legacy-selector transition is not established"
		}
		if changeErr != nil {
			f.Detail = fmt.Sprintf("Current standard compression controls observed; changeability query failed: %v", changeErr)
			if errors.Is(changeErr, ErrUncertain) {
				f.Availability, f.Reason = entity.DeviceFeatureAvailability_DEVICE_FEATURE_AVAILABILITY_UNAVAILABLE, Reason(changeErr)
			}
		}
	}
}

func compressionPolicy(pages map[byte][]byte, enabled bool, operation Operation) ([]byte, []byte, error) {
	// READ needs only applicable decoding; FORMAT/APPEND must establish the explicit write policy.
	page, configuration := pages[0x0f], pages[0x10]
	if len(page) != 0 && len(page) < 16 || len(configuration) < 16 {
		return nil, nil, ErrMalformed
	}
	compression := append([]byte(nil), page...)
	if operation == Read {
		if !enabled {
			return nil, nil, nil
		}
		if len(compression) == 0 {
			return nil, nil, fmt.Errorf("required hardware decompression controls are unavailable: %w", ErrUnsupported)
		}
		if compression[3]&0x60 == 0x60 {
			return nil, nil, fmt.Errorf("reserved decompression exception policy: %w", ErrMalformed)
		}
		if compression[3]&0xe0 == 0x80 {
			return nil, nil, nil
		}
		compression[3] = compression[3]&^0x60 | 0x80
		return compression, nil, nil
	}
	if configuration[14] > 1 {
		return nil, nil, fmt.Errorf("active legacy compression policy is unknown")
	}
	if active, _ := compressionState(pages); active == nil {
		return nil, nil, fmt.Errorf("active hardware compression state is unknown")
	}
	if enabled && (len(compression) == 0 || compression[2]&0x40 == 0 || binary.BigEndian.Uint32(compression[4:8]) == 0) {
		return nil, nil, fmt.Errorf("required hardware compression algorithm is unavailable: %w", ErrUnsupported)
	}
	if !enabled && configuration[14] == 0 && (len(compression) == 0 || compression[2]&0xc0 == 0) {
		return nil, nil, nil
	}
	configuration = append([]byte(nil), configuration...)
	configuration[14] = 0
	if len(compression) != 0 {
		compression[2] &^= 0x80
	}
	if enabled {
		configuration[14] = 1
		compression[2] |= 0x80
	}
	return compression, configuration, nil
}

func checkCompressionChanges(current, desired, changes map[byte][]byte) error {
	// Only requested DCE/DDE/RED and legacy-selector differences require writable masks.
	for _, code := range []byte{0x0f, 0x10} {
		page := desired[code]
		if len(page) == 0 {
			continue
		}
		before, mask := current[code], changes[code]
		if len(before) != len(page) {
			return fmt.Errorf("required compression changeability is unknown")
		}
		indices := []int{2, 3}
		if code == 0x10 {
			indices = []int{14}
		}
		for _, index := range indices {
			if before[index] == page[index] {
				continue
			}
			if len(mask) != len(page) {
				return fmt.Errorf("required compression changeability is unknown")
			}
			if (before[index]^page[index])&^mask[index] != 0 {
				return fmt.Errorf("required compression controls are not changeable: %w", ErrUnsupported)
			}
		}
	}
	return nil
}

func compressionControlsDiffer(current, desired map[byte][]byte) bool {
	// Algorithm selection is never changed; only required enable and exception controls trigger a mask query.
	if page := desired[0x0f]; len(page) != 0 && (current[0x0f][2] != page[2] || current[0x0f][3] != page[3]) {
		return true
	}
	return len(desired[0x10]) != 0 && current[0x10][14] != desired[0x10][14]
}

func checkCompressionReadback(pages, desired map[byte][]byte) error {
	// The selected compression algorithm is preserved; last-read decompression IDs may change automatically.
	if page := desired[0x0f]; len(page) != 0 {
		actual := pages[0x0f]
		if len(actual) < 16 || actual[2]&0xc0 != page[2]&0xc0 || actual[3]&0xe0 != page[3]&0xe0 || !bytes.Equal(actual[4:8], page[4:8]) {
			return fmt.Errorf("Tape compression mode readback differs")
		}
	}
	if page := desired[0x10]; len(page) != 0 {
		if len(pages[0x10]) < 16 || pages[0x10][14] != page[14] {
			return fmt.Errorf("Tape compression selector readback differs")
		}
	}
	return nil
}

func checkCapturedWriteAlgorithm(pages, original map[byte][]byte) error {
	// CA never changes through our policy or a read; only DA is allowed to follow last-read detection.
	if len(original[0x0f]) != 0 && (len(pages[0x0f]) < 16 || !bytes.Equal(pages[0x0f][4:8], original[0x0f][4:8])) {
		return fmt.Errorf("captured write compression algorithm is not established")
	}
	return nil
}

func checkUntouchedCompression(pages, original map[byte][]byte, changed uint16, selectorChanged bool) error {
	// Unrequested enable/exception changes are not silently copied into a supposedly restored session.
	if page := original[0x0f]; len(page) >= 16 && len(pages[0x0f]) >= 16 {
		if (pages[0x0f][2]^page[2])&0x80&^byte(changed) != 0 || (pages[0x0f][3]^page[3])&0xe0&^byte(changed>>8) != 0 {
			return fmt.Errorf("untouched compression controls changed")
		}
	}
	if page := original[0x10]; len(page) >= 16 && !selectorChanged {
		if len(pages[0x10]) < 16 || pages[0x10][14] != page[14] {
			return fmt.Errorf("untouched compression selector changed")
		}
	}
	return nil
}
