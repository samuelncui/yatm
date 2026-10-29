package tape

import (
	"context"
	"encoding/binary"

	"github.com/samuelncui/yatm/entity"
	"google.golang.org/protobuf/proto"
)

// EncryptionAES256GCM identifies the public algorithm; its device index is discovered on each reader.
const EncryptionAES256GCM = "AES-256-GCM-128"

type encryptionState struct {
	nexus, scope, encrypt, decrypt, algorithm, control byte
	counter                                            uint32
}

type encryptionAlgorithm struct {
	index, encrypt, decrypt byte
}

func securityPage(ctx context.Context, transport Transport, page uint16) ([]byte, error) {
	// Each public status/capability page is read independently with a bounded byte allocation.
	cdb := []byte{0xa2, 0x20, byte(page >> 8), byte(page), 0, 0, 0, 0, 0x10, 0, 0, 0}
	data, err := query(ctx, transport, cdb, MaxQueryBytes)
	if err != nil {
		return nil, err
	}
	return securityEnvelope(data, page, 4)
}

func securityEnvelope(data []byte, page uint16, minimum int) ([]byte, error) {
	if len(data) < minimum || binary.BigEndian.Uint16(data[:2]) != page {
		return nil, ErrMalformed
	}
	length := int(binary.BigEndian.Uint16(data[2:4])) + 4
	if length < minimum || length > len(data) {
		return nil, ErrMalformed
	}
	return data[:length], nil
}

func decodeEncryptionState(data []byte) (encryptionState, error) {
	// Key counters are opaque instances; neither zero nor nonzero reports whether key material is present.
	data, err := securityEnvelope(data, 0x20, 24)
	if err != nil {
		return encryptionState{}, err
	}
	state := encryptionState{nexus: data[4] >> 5, scope: data[4] & 7, encrypt: data[5], decrypt: data[6], algorithm: data[7], counter: binary.BigEndian.Uint32(data[8:12]), control: data[12] >> 4 & 7}
	if data[4]&0x18 != 0 || state.nexus > 2 || state.scope > 2 || state.encrypt > 2 || state.decrypt > 3 || data[12]&0x80 != 0 {
		return encryptionState{}, ErrMalformed
	}
	if state.encrypt == 0 && state.decrypt == 0 && len(data) != 24 {
		return encryptionState{}, ErrMalformed
	}
	for offset := 24; offset < len(data); {
		if len(data)-offset < 4 {
			return encryptionState{}, ErrMalformed
		}
		length := int(binary.BigEndian.Uint16(data[offset+2:offset+4])) + 4
		if length > len(data)-offset {
			return encryptionState{}, ErrMalformed
		}
		offset += length
	}
	return state, nil
}

func decodeEncryptionCapabilities(data []byte) ([]encryptionAlgorithm, error) {
	// Preserve reader-local indexes and only name an algorithm with its reported public code and key length.
	data, err := securityEnvelope(data, 0x10, 20)
	if err != nil {
		return nil, err
	}
	var algorithms []encryptionAlgorithm
	seen := make(map[byte]bool)
	for offset := 20; offset < len(data); {
		if len(data)-offset < 4 {
			return nil, ErrMalformed
		}
		length := int(binary.BigEndian.Uint16(data[offset+2:offset+4])) + 4
		if length < 24 || length > len(data)-offset || seen[data[offset]] {
			return nil, ErrMalformed
		}
		page := data[offset : offset+length]
		seen[page[0]] = true
		if binary.BigEndian.Uint32(page[20:24]) == 0x00010014 && binary.BigEndian.Uint16(page[10:12]) == 32 {
			algorithms = append(algorithms, encryptionAlgorithm{index: page[0], encrypt: page[4] & 3, decrypt: page[4] >> 2 & 3})
		}
		offset += length
	}
	return algorithms, nil
}

func (d *Device) reportEncryptionState(state encryptionState) {
	// Status contains non-secret settings, not the recorded block's key or encrypted content identity.
	limits := feature(d.report, entity.DeviceFeatureKind_DEVICE_FEATURE_KIND_HARDWARE_ENCRYPTION).GetEncryption()
	if limits == nil {
		limits = new(entity.TapeEncryptionLimits)
		feature(d.report, entity.DeviceFeatureKind_DEVICE_FEATURE_KIND_HARDWARE_ENCRYPTION).Limits = &entity.DeviceFeature_Encryption{Encryption: limits}
	}
	limits.CurrentAlgorithmIndex = nil
	if state.encrypt != 0 || state.decrypt != 0 {
		limits.CurrentAlgorithmIndex = proto.Uint32(uint32(state.algorithm))
	}
	limits.EncryptionMode, limits.DecryptionMode = proto.Uint32(uint32(state.encrypt)), proto.Uint32(uint32(state.decrypt))
	limits.ITNexusScope, limits.KeyScope = proto.Uint32(uint32(state.nexus)), proto.Uint32(uint32(state.scope))
	limits.ParametersControl, limits.KeyInstanceCounter = proto.Uint32(uint32(state.control)), proto.Uint32(state.counter)
}
