package tape

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"slices"
	"sync/atomic"
	"time"
)

// directoryLayout describes one LTO generation's tape directory, page 0x103. A directory entry holds
// one wrap, or one half wrap in Ultrium-1, with its data set ID and record and filemark counts.
type directoryLayout struct {
	name string
	// version is the page version nibble observed on hardware, or zero where none was observed.
	version        byte
	header, unit   int
	entries        int
	halfWraps      bool
	recordOffsets  []int
	filemarkOffset []int
	passOffset     int
}

// directoryLayouts follow the cartridge generation, not the drive vendor, because interchange requires
// every drive to read the same cartridge memory. Ultrium-1 is ECMA-319 Annex D; Ultrium-5 and -6 are
// checked against hardware images; the others use the publicly reported field layout and are
// unverified. A directory is accepted only when its exact length and every checksum fit one layout.
var directoryLayouts = []directoryLayout{
	{name: "Ultrium-1", header: 16, unit: 16, entries: 96, halfWraps: true, recordOffsets: []int{4}, filemarkOffset: []int{8}, passOffset: -1},
	{name: "Ultrium-2", header: 16, unit: 28, entries: 64, recordOffsets: []int{8, 12}, filemarkOffset: []int{16, 20}},
	{name: "Ultrium-3", header: 16, unit: 32, entries: 44, recordOffsets: []int{8, 12}, filemarkOffset: []int{16, 20}},
	{name: "Ultrium-4", header: 16, unit: 32, entries: 56, recordOffsets: []int{8, 12}, filemarkOffset: []int{16, 20}},
	{name: "Ultrium-5", version: 4, header: 16, unit: 32, entries: 80, recordOffsets: []int{8, 12}, filemarkOffset: []int{16, 20}},
	{name: "Ultrium-6", version: 5, header: 48, unit: 32, entries: 136, recordOffsets: []int{8, 12}, filemarkOffset: []int{16, 20}},
	{name: "Ultrium-7", header: 48, unit: 32, entries: 112, recordOffsets: []int{8, 12}, filemarkOffset: []int{16, 20}},
	{name: "Ultrium-7 Type M", header: 48, unit: 32, entries: 168, recordOffsets: []int{8, 12}, filemarkOffset: []int{16, 20}},
	{name: "Ultrium-8", header: 48, unit: 32, entries: 208, recordOffsets: []int{8, 12}, filemarkOffset: []int{16, 20}},
	{name: "Ultrium-9", header: 48, unit: 32, entries: 280, recordOffsets: []int{8, 12}, filemarkOffset: []int{16, 20}},
}

// eodPages are the per-partition EOD pages in partition order.
var eodPages = [...]uint16{0x104, 0x10e, 0x10f, 0x110}

type directoryEntry struct {
	pass, dataset  uint32
	records, marks uint64
}

// decodeDirectoryEntries selects the layout whose length and checksums fit the directory page.
func decodeDirectoryEntries(directory []byte) (*directoryLayout, []directoryEntry, error) {
	for i := range directoryLayouts {
		layout := &directoryLayouts[i]
		if len(directory) != layout.header+layout.entries*layout.unit || layout.version != 0 && directory[0]>>4 != layout.version ||
			!cartridgeMemoryChecksum(directory[:layout.header]) {
			continue
		}
		entries := make([]directoryEntry, layout.entries)
		valid := true
		for j := range entries {
			raw := directory[layout.header+j*layout.unit : layout.header+(j+1)*layout.unit]
			if !cartridgeMemoryChecksum(raw) {
				valid = false
				break
			}
			datasetOffset := 4
			if layout.passOffset < 0 {
				datasetOffset = 0
			} else {
				entries[j].pass = binary.BigEndian.Uint32(raw[layout.passOffset:])
			}
			entries[j].dataset = binary.BigEndian.Uint32(raw[datasetOffset:])
			for _, offset := range layout.recordOffsets {
				entries[j].records += uint64(binary.BigEndian.Uint32(raw[offset:]))
			}
			for _, offset := range layout.filemarkOffset {
				entries[j].marks += uint64(binary.BigEndian.Uint32(raw[offset:]))
			}
		}
		if valid {
			return layout, entries, nil
		}
	}
	return nil, nil, fmt.Errorf("unsupported cartridge directory version or length: %w", ErrUnsupported)
}

// decodeCartridgeDirectory derives exclusive wrap endpoints from the tape directory and EOD pages.
// The format qualification and unresolved extension bits are described in docs/research/tape-read-ordering.md.
func decodeCartridgeDirectory(data []byte, partitions uint32) (*WrapMap, error) {
	// Tables authenticate page identities and addresses before any page-specific interpretation.
	if partitions < 1 || partitions > uint32(len(eodPages)) {
		return nil, fmt.Errorf("actual cartridge partition count is unavailable or unsupported: %w", ErrUnsupported)
	}
	pages, err := cartridgeDirectoryPages(data)
	if err != nil {
		return nil, err
	}
	layout, entries, err := decodeDirectoryEntries(pages[0x103])
	if err != nil {
		return nil, err
	}

	// Invalid dataset markers delimit complete runs independently of totals or partition-start flags.
	type interval struct{ first, last int }
	var runs []interval
	inRun := false
	for i, entry := range entries {
		if entry.dataset >= 0xfffffffe {
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

	// A FORMAT with fewer partitions can retain unused EOD pages in cartridge memory. Verify any
	// retained page's representation without treating its old payload as an active partition.
	for i, id := range eodPages {
		eod := pages[id]
		if uint32(i) >= partitions && eod == nil {
			continue
		}
		// Ultrium-1 has ECMA-319's 64-byte EOD page; later generations use the version-2 96-byte page.
		if layout.halfWraps && (len(eod) != 64 || binary.BigEndian.Uint16(eod[:2])&0x0fff != id) ||
			!layout.halfWraps && (len(eod) != 96 || binary.BigEndian.Uint16(eod[:2]) != 0x2000|id) {
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
	observed := &WrapMap{AllWraps: true, Source: "LTO-CM directory " + layout.name, ObservedAt: time.Now(),
		valid: new(atomic.Bool)}
	used := make([]bool, len(runs))
	for partition, id := range eodPages[:partitions] {
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
		if run < 0 || layout.passOffset >= 0 && entries[terminal].pass != binary.BigEndian.Uint32(eod[4:8]) ||
			entries[terminal].dataset != binary.BigEndian.Uint32(eod[24:28]) {
			return nil, fmt.Errorf("cartridge EOD page %#x disagrees with its directory terminal: %w", id, ErrMalformed)
		}
		used[run] = true
		var records, marks uint64
		for i := runs[run].first; i <= runs[run].last; i++ {
			records += entries[i].records
			marks += entries[i].marks
			// Ultrium-1 entries are half wraps; a wrap ends at its second half or at EOD.
			if layout.halfWraps && i%2 == 0 && i != runs[run].last {
				continue
			}
			wrap := uint32(i)
			if layout.halfWraps {
				wrap /= 2
			}
			// A cumulative count is already the exclusive REOWP endpoint; no conversion applies.
			observed.Endpoints = append(observed.Endpoints, WrapEndpoint{Partition: uint32(partition), Wrap: wrap, Object: records + marks})
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
	pages := make(map[uint16][]byte, 1+len(eodPages))
	for word, address := range descriptors {
		id := word & 0xfff
		if id != 0x103 && !slices.Contains(eodPages[:], id) {
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
