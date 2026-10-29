package tape

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"sync/atomic"
	"time"

	"github.com/samuelncui/yatm/entity"
)

// decodeCartridgeDirectory accepts the studied LTO-5 v4 directory and v2 EOD pages.
// The format qualification and unresolved extension bits are described in docs/research/tape-capacity.md.
func decodeCartridgeDirectory(data []byte, partitions uint32) (*WrapMap, error) {
	// Tables authenticate page identities and addresses before any page-specific interpretation.
	if partitions != 1 && partitions != 2 {
		return nil, fmt.Errorf("actual cartridge partition count is unavailable or unsupported: %w", ErrUnsupported)
	}
	pages, err := cartridgeDirectoryPages(data)
	if err != nil {
		return nil, err
	}
	directory := pages[0x103]
	if len(directory) != 16+80*32 || binary.BigEndian.Uint16(directory[:2]) != 0x4103 {
		return nil, fmt.Errorf("unsupported cartridge directory version or length: %w", ErrUnsupported)
	}
	if !cartridgeMemoryChecksum(directory[:16]) {
		return nil, fmt.Errorf("cartridge directory header checksum: %w", ErrMalformed)
	}

	// Invalid dataset markers delimit complete runs independently of totals or partition-start flags.
	type entry struct {
		pass, dataset  uint32
		records, marks uint64
	}
	entries := make([]entry, (len(directory)-16)/32)
	type interval struct{ first, last int }
	var runs []interval
	inRun := false
	for i := range entries {
		raw := directory[16+i*32 : 48+i*32]
		if !cartridgeMemoryChecksum(raw) {
			return nil, fmt.Errorf("cartridge directory wrap %d checksum: %w", i, ErrMalformed)
		}
		entries[i] = entry{pass: binary.BigEndian.Uint32(raw[:4]), dataset: binary.BigEndian.Uint32(raw[4:8]),
			records: uint64(binary.BigEndian.Uint32(raw[8:12])) + uint64(binary.BigEndian.Uint32(raw[12:16])),
			marks:   uint64(binary.BigEndian.Uint32(raw[16:20])) + uint64(binary.BigEndian.Uint32(raw[20:24]))}
		if entries[i].dataset >= 0xfffffffe {
			inRun = false
			continue
		}
		if !inRun {
			runs = append(runs, interval{first: i, last: i})
			inRun = true
		} else {
			runs[len(runs)-1].last = i
		}
	}

	// A physical single-partition FORMAT can retain an unused second EOD page in cartridge memory.
	// Verify any retained page's representation without treating its old payload as an active partition.
	pageIDs := [...]uint16{0x104, 0x10e}
	for i, id := range pageIDs {
		eod := pages[id]
		if uint32(i) >= partitions && eod == nil {
			continue
		}
		if len(eod) != 96 || binary.BigEndian.Uint16(eod[:2]) != 0x2000|id {
			return nil, fmt.Errorf("unsupported cartridge EOD page %#x: %w", id, ErrUnsupported)
		}
		if !cartridgeMemoryChecksum(eod) {
			return nil, fmt.Errorf("cartridge EOD page %#x checksum: %w", id, ErrMalformed)
		}
	}

	// Each active EOD owns exactly one whole run; no suffix may be selected to make low counters fit.
	if len(runs) != int(partitions) {
		return nil, fmt.Errorf("cartridge directory valid runs do not match EOD pages: %w", ErrMalformed)
	}
	observed := &WrapMap{AllWraps: true, Source: "LTO-CM directory v4/EOD v2", ObservedAt: time.Now(),
		EndpointConvention: entity.TapePositionEndpointConvention_TAPE_POSITION_ENDPOINT_CONVENTION_EXCLUSIVE,
		valid:              new(atomic.Bool)}
	used := make([]bool, len(runs))
	for partition, id := range pageIDs[:partitions] {
		eod := pages[id]
		if binary.BigEndian.Uint16(eod[32:34]) != 1 {
			return nil, fmt.Errorf("cartridge EOD page %#x is not a valid completed directory: %w", id, ErrMalformed)
		}
		terminal, run := binary.BigEndian.Uint32(eod[28:32]), -1
		for i, candidate := range runs {
			if uint32(candidate.last) == terminal && !used[i] {
				run = i
				break
			}
		}
		if run < 0 || entries[terminal].pass != binary.BigEndian.Uint32(eod[4:8]) ||
			entries[terminal].dataset != binary.BigEndian.Uint32(eod[24:28]) {
			return nil, fmt.Errorf("cartridge EOD page %#x disagrees with its directory terminal: %w", id, ErrMalformed)
		}
		used[run] = true
		var records, marks uint64
		for i := runs[run].first; i <= runs[run].last; i++ {
			records += entries[i].records
			marks += entries[i].marks
			observed.Endpoints = append(observed.Endpoints, WrapEndpoint{
				Partition: uint32(partition), Wrap: uint32(i), Object: records + marks})
		}
		// Separate conservation proves zero extension contribution within this restricted low-word layout.
		// Unknown packed bits are never decoded as zero, and an unexplained wider total is unavailable.
		if records != cartridgeMemoryUint48(eod[12:18]) || marks != cartridgeMemoryUint48(eod[18:24]) {
			return nil, fmt.Errorf("cartridge EOD page %#x has inconsistent or unsupported extended counters: %w", id, ErrMalformed)
		}
	}
	observed.valid.Store(true)
	return observed, nil
}

func cartridgeDirectoryPages(data []byte) (map[uint16][]byte, error) {
	// Both page tables must be complete inside the bounded READ BUFFER image.
	if len(data) < 64 || len(data) > cartridgeMemoryMaxBytes {
		return nil, ErrMalformed
	}
	_, start, err := cartridgeMemoryTable(data, 36, false)
	if err != nil {
		return nil, err
	}
	descriptors, end, err := cartridgeMemoryTable(data, start, true)
	if err != nil {
		return nil, err
	}

	// Only the supported directory and EOD pages are interpreted; later partition forms stay unknown.
	pages := make(map[uint16][]byte, 3)
	for word, address := range descriptors {
		id := word & 0xfff
		if id == 0x10f || id == 0x110 {
			return nil, fmt.Errorf("more than two cartridge EOD pages: %w", ErrUnsupported)
		}
		if id != 0x103 && id != 0x104 && id != 0x10e {
			continue
		}
		if pages[id] != nil || address < start || address+4 > end {
			return nil, ErrMalformed
		}
		length := int(binary.BigEndian.Uint16(data[address+2 : address+4]))
		next := end
		for other, at := range descriptors {
			if other != word && at == address {
				return nil, ErrMalformed
			}
			if at > address {
				next = min(next, at)
			}
		}
		if length < 4 || address+length > next || binary.BigEndian.Uint16(data[address:address+2]) != word {
			return nil, ErrMalformed
		}
		pages[id] = data[address : address+length]
	}
	return pages, nil
}

func cartridgeMemoryTable(data []byte, start int, unprotected bool) (map[uint16]int, int, error) {
	// The later CM framing excludes its leading padding descriptor from the unprotected table checksum.
	if start < 36 || start+8 > len(data) || start%4 != 0 {
		return nil, 0, ErrMalformed
	}
	checksumStart := start
	if unprotected && binary.BigEndian.Uint16(data[start:start+2]) == 0x0ffc {
		checksumStart += 4
	}
	descriptors := make(map[uint16]int)
	firstPage := len(data)
	for offset := start; offset+8 <= len(data) && offset+8 <= firstPage; offset += 4 {
		word := binary.BigEndian.Uint16(data[offset : offset+2])
		address := int(binary.BigEndian.Uint16(data[offset+2 : offset+4]))
		switch word {
		case 0x0fff:
			if address < offset+8 || address > len(data) || address%4 != 0 ||
				!cartridgeMemoryChecksum(data[checksumStart:offset+8]) {
				return nil, 0, ErrMalformed
			}
			return descriptors, address, nil
		case 0x0ffc, 0x0ffe:
			continue
		}
		if _, exists := descriptors[word]; exists || address < offset+8 || address+4 > len(data) || address%4 != 0 {
			return nil, 0, ErrMalformed
		}
		descriptors[word] = address
		firstPage = min(firstPage, address)
	}
	return nil, 0, ErrMalformed
}

func cartridgeMemoryUint48(data []byte) uint64 {
	return uint64(binary.BigEndian.Uint16(data[:2]))<<32 | uint64(binary.BigEndian.Uint32(data[2:6]))
}

func cartridgeMemoryChecksum(data []byte) bool {
	if len(data) < 4 {
		return false
	}
	// ECMA-319 13.2: GF(256), polynomial 0x11d, roots alpha^126 through alpha^129.
	var registers [4]byte
	for _, value := range data[:len(data)-4] {
		feedback := value ^ registers[0]
		registers = [4]byte{registers[1], registers[2], registers[3], 0}
		for i, coefficient := range [...]byte{56, 207, 56, 1} {
			var product byte
			for a, b := feedback, coefficient; b != 0; b >>= 1 {
				if b&1 != 0 {
					product ^= a
				}
				high := a & 0x80
				a <<= 1
				if high != 0 {
					a ^= 0x1d
				}
			}
			registers[i] ^= product
		}
	}
	return bytes.Equal(registers[:], data[len(data)-4:])
}
