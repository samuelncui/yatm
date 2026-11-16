package tape

import (
	"bytes"
	"fmt"

	"google.golang.org/protobuf/proto"
)

func compressionState(pages map[byte][]byte) *bool {
	// MP0F DCE is the write compression state; the legacy selector must agree when present.
	page, configuration := pages[0x0f], pages[0x10]
	if len(page) >= 16 {
		enabled := page[2]&0x80 != 0
		if enabled && page[2]&0x40 == 0 || len(configuration) >= 16 && (configuration[14] > 1 || (configuration[14] == 1) != enabled) {
			return nil
		}
		return proto.Bool(enabled)
	}
	if len(configuration) >= 16 && configuration[14] <= 1 {
		return proto.Bool(configuration[14] == 1)
	}
	return nil
}

func compressionOff(pages map[byte][]byte) ([]byte, []byte, error) {
	// Tar FORMAT/APPEND always write uncompressed records: DCE and the legacy selector are forced off.
	page, configuration := pages[0x0f], pages[0x10]
	if len(page) != 0 && len(page) < 16 || len(configuration) < 16 {
		return nil, nil, ErrMalformed
	}
	if configuration[14] > 1 {
		return nil, nil, fmt.Errorf("active legacy compression policy is unknown")
	}
	if compressionState(pages) == nil {
		return nil, nil, fmt.Errorf("active hardware compression state is unknown")
	}
	if configuration[14] == 0 && (len(page) == 0 || page[2]&0xc0 == 0) {
		return nil, nil, nil
	}
	compression := append([]byte(nil), page...)
	configuration = append([]byte(nil), configuration...)
	configuration[14] = 0
	if len(compression) != 0 {
		compression[2] &^= 0x80
	}
	return compression, configuration, nil
}

func checkCompressionChanges(current, desired, changes map[byte][]byte) error {
	// Only requested DCE and legacy-selector differences require writable masks.
	for _, code := range []byte{0x0f, 0x10} {
		page := desired[code]
		if len(page) == 0 {
			continue
		}
		before, mask := current[code], changes[code]
		if len(before) != len(page) {
			return fmt.Errorf("required compression changeability is unknown")
		}
		index := 2
		if code == 0x10 {
			index = 14
		}
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
	return nil
}

func compressionControlsDiffer(current, desired map[byte][]byte) bool {
	// Algorithm selection is never changed; only enable and exception controls trigger a mask query.
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

func checkUntouchedCompression(pages, original map[byte][]byte, dceChanged, selectorChanged bool) error {
	// Unrequested enable/exception changes are not silently copied into a supposedly restored session.
	if page := original[0x0f]; len(page) >= 16 && len(pages[0x0f]) >= 16 {
		changed := byte(0)
		if dceChanged {
			changed = 0x80
		}
		if (pages[0x0f][2]^page[2])&0x80&^changed != 0 || (pages[0x0f][3]^page[3])&0xe0 != 0 {
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
