package tape

import (
	"context"
	"encoding/binary"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/samuelncui/yatm/entity"
	"google.golang.org/protobuf/proto"
)

func (d *Device) readMode(ctx context.Context, changeable bool) ([]byte, map[byte][]byte, error) {
	page := byte(0x3f)
	if changeable {
		page = 0x7f
	}
	data, err := query(ctx, d.transport, []byte{0x5a, 0, page, 0, 0, 0, 0, 0x10, 0, 0}, MaxQueryBytes)
	if err != nil {
		return nil, nil, err
	}
	data, pages, err := modeData(data)
	if err == nil && !changeable {
		if page := pages[0x11]; len(page) >= 8 && page[3] > page[2] {
			return nil, nil, ErrMalformed
		}
		observeMode(data, pages, d.report.GetTape())
		d.refreshCompressionAlgorithms(pages)
	}
	return data, pages, err
}

func (d *Device) requireCommand(ctx context.Context, form []byte, mandatory bool) error {
	// RSOC rejection does not revoke an applicable mandatory SSC command; explicit unsupported does.
	data, err := reportCommand(ctx, d.transport, form[0], nil)
	if Reason(err) == entity.DeviceReason_DEVICE_REASON_UNSUPPORTED && mandatory {
		return nil
	}
	if err != nil {
		return err
	}
	if len(data) < 2 {
		return ErrMalformed
	}
	support := data[1] & 7
	if support == 0 && mandatory {
		return nil
	}
	observed, err := commandFormSupport(data, form)
	if err != nil {
		return err
	}
	if observed == entity.DeviceFeatureSupport_DEVICE_FEATURE_SUPPORT_UNSUPPORTED {
		return fmt.Errorf("Tape opcode %02x required form is unsupported: %w", form[0], ErrUnsupported)
	}
	if observed != entity.DeviceFeatureSupport_DEVICE_FEATURE_SUPPORT_SUPPORTED {
		return fmt.Errorf("Tape opcode %02x has no applicable standard support", form[0])
	}
	return nil
}

func reportCommand(ctx context.Context, transport Transport, opcode byte, action *uint16) ([]byte, error) {
	// A service-action-specific report establishes the actual READ POSITION form, not a bit-mask guess.
	cdb := []byte{0xa3, 0x0c, 1, opcode, 0, 0, 0, 0, 0, 64, 0, 0}
	if action != nil {
		cdb[2] = 2
		binary.BigEndian.PutUint16(cdb[4:6], *action)
	}
	return query(ctx, transport, cdb, 64)
}

func commandFormSupport(data, form []byte) (entity.DeviceFeatureSupport, error) {
	// Only standard support and the required CDB field mask establish this form's applicability.
	if len(data) < 2 {
		return entity.DeviceFeatureSupport_DEVICE_FEATURE_SUPPORT_UNKNOWN, ErrMalformed
	}
	switch data[1] & 7 {
	case 0, 5:
		return entity.DeviceFeatureSupport_DEVICE_FEATURE_SUPPORT_UNKNOWN, nil
	case 1:
		return entity.DeviceFeatureSupport_DEVICE_FEATURE_SUPPORT_UNSUPPORTED, nil
	case 3:
	default:
		return entity.DeviceFeatureSupport_DEVICE_FEATURE_SUPPORT_UNKNOWN, ErrMalformed
	}
	if len(data) < 4 {
		return entity.DeviceFeatureSupport_DEVICE_FEATURE_SUPPORT_UNKNOWN, ErrMalformed
	}
	length := int(binary.BigEndian.Uint16(data[2:4]))
	if length != len(form) || length+4 > len(data) || data[4] != form[0] {
		return entity.DeviceFeatureSupport_DEVICE_FEATURE_SUPPORT_UNKNOWN, ErrMalformed
	}

	// SPC usage maps evaluate or ignore an entire address field; partial maps are not smaller ranges.
	start, end := 0, 0
	switch form[0] {
	case 0x2b:
		if len(form) != 10 {
			return entity.DeviceFeatureSupport_DEVICE_FEATURE_SUPPORT_UNKNOWN, ErrMalformed
		}
		start, end = 3, 7
	case 0x92:
		if len(form) != 16 {
			return entity.DeviceFeatureSupport_DEVICE_FEATURE_SUPPORT_UNKNOWN, ErrMalformed
		}
		start, end = 4, 12
	}
	if end != 0 {
		value := data[start+4]
		if value != 0 && value != 0xff {
			return entity.DeviceFeatureSupport_DEVICE_FEATURE_SUPPORT_UNKNOWN, ErrMalformed
		}
		for _, mask := range data[start+4 : end+4] {
			if mask != value {
				return entity.DeviceFeatureSupport_DEVICE_FEATURE_SUPPORT_UNKNOWN, ErrMalformed
			}
		}
	}

	// Only required nonzero values need evaluated bits; service actions are literal identifiers.
	for i := 1; i < len(form)-1; i++ {
		// RSOC service-action CDB usage contains the literal requested action, not a supported-bit mask.
		if form[0] == 0x34 && i == 1 {
			if data[i+4] != form[i] {
				return entity.DeviceFeatureSupport_DEVICE_FEATURE_SUPPORT_UNKNOWN, ErrMalformed
			}
			continue
		}
		if form[i]&^data[i+4] != 0 {
			return entity.DeviceFeatureSupport_DEVICE_FEATURE_SUPPORT_UNSUPPORTED, nil
		}
	}
	return entity.DeviceFeatureSupport_DEVICE_FEATURE_SUPPORT_SUPPORTED, nil
}

func (d *Device) requireEncryptionOff(ctx context.Context) error {
	// The status page exposes modes, not keys or encrypted-content identity. Unknown never means off.
	data, err := encryptionStatus(ctx, d.transport, d.report.GetTape())
	if err != nil {
		return fmt.Errorf("observe encryption/decryption state failed, %w", err)
	}
	if !encryptionOff(data) {
		return fmt.Errorf("baseline requires encryption and decryption disabled without external control")
	}
	return nil
}

func encryptionOff(data []byte) bool {
	// A missing status is allowed only after the complete protocol list established absence.
	return data == nil || len(data) >= 13 && data[5] == 0 && data[6] == 0 && data[4] == 0 &&
		(data[12]&0x70 == 0x10 || data[12]&0x70 == 0x20)
}

func encryptionStatus(ctx context.Context, transport Transport, report *entity.TapeDeviceReport) ([]byte, error) {
	// A complete standard protocol list distinguishes absent tape encryption from unknown status access.
	data, err := query(ctx, transport, []byte{0xa2, 0, 0, 0, 0, 0, 0, 0, 0x10, 0, 0, 0}, MaxQueryBytes)
	if err != nil {
		return nil, err
	}
	if len(data) < 8 || int(binary.BigEndian.Uint16(data[6:8]))+8 > len(data) {
		return nil, ErrMalformed
	}
	present, unknown := false, false
	for _, protocol := range data[8 : 8+int(binary.BigEndian.Uint16(data[6:8]))] {
		present = present || protocol == 0x20
		unknown = unknown || protocol != 0 && protocol != 0x20
	}
	if !present {
		if unknown {
			return nil, fmt.Errorf("nonstandard encryption capability is unknown")
		}
		report.EncryptionEnabled = proto.Bool(false)
		return nil, nil
	}

	// The tape status page contains only non-secret current modes, never a recoverable key.
	data, err = query(ctx, transport, []byte{0xa2, 0x20, 0, 0x20, 0, 0, 0, 0, 0x10, 0, 0, 0}, MaxQueryBytes)
	if err != nil {
		return nil, err
	}
	if err := decodeEncryption(data, report); err != nil {
		return nil, err
	}
	return data, nil
}

func decodeEncryption(data []byte, report *entity.TapeDeviceReport) error {
	state, err := decodeEncryptionState(data)
	if err != nil {
		return err
	}
	report.EncryptionEnabled = proto.Bool(state.encrypt != 0)
	return nil
}

func (d *Device) attribute(ctx context.Context, identifier uint16) ([]byte, error) {
	// MAM requests remain bounded even when the response advertises a longer list of attributes.
	cdb := make([]byte, 16)
	cdb[0] = 0x8c
	binary.BigEndian.PutUint16(cdb[8:10], identifier)
	binary.BigEndian.PutUint32(cdb[10:14], MaxQueryBytes)
	data, err := query(ctx, d.transport, cdb, MaxQueryBytes)
	if err != nil {
		return nil, err
	}
	if len(data) < 4 {
		return nil, ErrMalformed
	}
	available := uint64(binary.BigEndian.Uint32(data[:4]))
	end := min(uint64(len(data)), available+4)
	for offset := uint64(4); offset < end; {
		if end-offset < 5 {
			return nil, ErrMalformed
		}
		length := uint64(binary.BigEndian.Uint16(data[offset+3 : offset+5]))
		if offset+5+length > end {
			return nil, ErrMalformed
		}
		if binary.BigEndian.Uint16(data[offset:offset+2]) == identifier {
			if data[offset+2]&3 != 1 {
				return nil, ErrMalformed
			}
			return append([]byte(nil), data[offset+5:offset+5+length]...), nil
		}
		offset += 5 + length
	}
	if available+4 > uint64(len(data)) {
		return nil, ErrMalformed
	}
	return nil, nil
}

// Barcode reads the cartridge memory barcode identity; an unavailable attribute is reported as empty.
func (d *Device) Barcode(ctx context.Context) (string, error) {
	data, err := d.attribute(ctx, 0x0806)
	if Reason(err) == entity.DeviceReason_DEVICE_REASON_UNSUPPORTED {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	value := strings.TrimSpace(string(data))
	if !utf8.ValidString(value) || strings.ContainsRune(value, 0) {
		return "", ErrMalformed
	}
	return value, nil
}

// WriteBarcode assigns the cartridge memory barcode during FORMAT, as LTFS mkltfs does. The SPC-4 host-type
// BARCODE attribute is a 32-byte ASCII value, left-aligned and space-padded.
func (d *Device) WriteBarcode(ctx context.Context, barcode string) error {
	if d.operation != Format {
		return fmt.Errorf("Tape barcode assignment requires FORMAT preparation")
	}
	cdb, data, err := writeBarcodeCommand(barcode)
	if err != nil {
		return err
	}
	response, err := d.transport.Execute(ctx, Command{CDB: cdb, Direction: ToDevice, Data: data, Timeout: 2 * time.Minute, validate: controlResponse})
	if err != nil {
		return err
	}
	if err := CheckResponse(response); err != nil {
		return err
	}
	if response.Residual != 0 {
		return &UncertainError{Cause: ErrMalformed}
	}
	return nil
}

// writeBarcodeCommand encodes WRITE ATTRIBUTE (8Dh) for partition zero with one BARCODE (0806h) attribute.
func writeBarcodeCommand(barcode string) ([]byte, []byte, error) {
	if barcode == "" || len(barcode) > 32 {
		return nil, nil, fmt.Errorf("Tape barcode attribute length is invalid")
	}
	for i := 0; i < len(barcode); i++ {
		if barcode[i] < 0x20 || barcode[i] > 0x7e {
			return nil, nil, fmt.Errorf("Tape barcode attribute is not printable ASCII")
		}
	}
	data := make([]byte, 4+5+32)
	binary.BigEndian.PutUint32(data[:4], uint32(len(data)-4))
	binary.BigEndian.PutUint16(data[4:6], 0x0806)
	data[6] = 1 // ASCII format.
	binary.BigEndian.PutUint16(data[7:9], 32)
	copy(data[9:], barcode)
	for i := 9 + len(barcode); i < len(data); i++ {
		data[i] = ' '
	}
	cdb := make([]byte, 16)
	cdb[0] = 0x8d
	binary.BigEndian.PutUint32(cdb[10:14], uint32(len(data)))
	return cdb, data, nil
}

func (d *Device) partitionCount(ctx context.Context) (uint32, error) {
	// Partition lists have a two-byte available-data length, unlike attribute-value responses.
	data, err := query(ctx, d.transport, []byte{0x8c, 3, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 4, 0, 0}, 4)
	if err != nil {
		return 0, err
	}
	if len(data) != 4 || binary.BigEndian.Uint16(data[:2]) != 2 || data[2] != 0 || data[3] == 0 {
		return 0, ErrMalformed
	}
	return uint32(data[3]), nil
}

func (d *Device) actualPartitionCount(ctx context.Context, partition []byte) (uint32, error) {
	// POFM mode pages may contain a pending layout; read/append cannot treat it as physical evidence.
	count, err := d.partitionCount(ctx)
	if err == nil {
		return count, nil
	}
	if Reason(err) == entity.DeviceReason_DEVICE_REASON_UNSUPPORTED && len(partition) >= 8 && partition[4]&4 == 0 && partition[3] <= partition[2] {
		return uint32(partition[3]) + 1, nil
	}
	return 0, fmt.Errorf("actual Tape partition layout is unavailable, %w", err)
}
