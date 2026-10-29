package executor

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"sync"

	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/internal/media/tape"
)

type tarFixtureRecord struct {
	data       []byte
	mark       bool
	compressed bool
	key        []byte
}

// tarDeviceFixture supplies synthetic SSC observations and physical records, never host hardware.
type tarDeviceFixture struct {
	lock                                                   sync.Mutex
	mode                                                   []byte
	records                                                []tarFixtureRecord
	cursor, opens, closes                                  int
	writes, filemarks, failMark, unloads                   int
	warningMark                                            int
	barcode                                                string
	barcodeWrites                                          int
	barcodeStored                                          *string
	cleanupError, poison                                   error
	capacityEOD, capacityEW                                *uint32
	capacityQueries                                        []int
	readCompression, writeCompression, selectedCompression []bool
	decodeAlgorithmOnRead                                  *uint32
	cipherScope, cipherNexus, encrypt, decrypt             byte
	cipherAlgorithm                                        byte
	algorithmIndex                                         byte
	cipherCounter                                          uint32
	cipherKey                                              []byte
	scriptKey, scriptGuard                                 string
	hideAlgorithm                                          bool
	securityEvents                                         []string
	locate10, shortPosition                                bool
	navigationSupport                                      *byte
	positionUnknown, positionUnknownUntilMotion            bool
	positionUnsupported                                    bool
	positionBadBOP                                         bool
	positionDelta                                          uint64
	positionError, locateError                             error
	locateCommands                                         [][]byte
	positionCommands                                       []byte
}

func (f *tarDeviceFixture) securityPage(page uint16) ([]byte, error) {
	// Model independent SSC security pages, including the public algorithm descriptor and empty initial scope.
	var data []byte
	switch page {
	case 0:
		data = make([]byte, 14)
		for i, supported := range []uint16{1, 0x10, 0x11, 0x12, 0x20} {
			binary.BigEndian.PutUint16(data[4+2*i:], supported)
		}
	case 1:
		data = []byte{0, 1, 0, 2, 0, 0x10}
	case 0x10:
		data = make([]byte, 44)
		data[4] = 1 // CFG_P: application-managed configuration is permitted.
		data[20], data[23], data[24], data[25] = f.algorithmIndex, 20, 0x8a, 0xb0
		data[32] = 0xc0 // Drive-generated nonce and omitted KAD are explicitly supported.
		binary.BigEndian.PutUint16(data[30:32], 32)
		binary.BigEndian.PutUint32(data[40:44], 0x00010014)
		if f.hideAlgorithm {
			binary.BigEndian.PutUint32(data[40:44], 0xffffffff)
		}
	case 0x11:
		data = make([]byte, 5) // Plaintext key format zero.
	case 0x12:
		data = make([]byte, 16)
		data[4], data[7] = 1, 3 // Local locking and Local/Public scope control.
	case 0x20:
		data = make([]byte, 24)
		data[4], data[5], data[6], data[7] = f.cipherNexus<<5|f.cipherScope, f.encrypt, f.decrypt, f.cipherAlgorithm
		binary.BigEndian.PutUint32(data[8:12], f.cipherCounter)
		data[12] = 0x10
	default:
		return nil, fmt.Errorf("unexpected fixture security page %04x", page)
	}
	binary.BigEndian.PutUint16(data, page)
	binary.BigEndian.PutUint16(data[2:4], uint16(len(data)-4))
	return data, nil
}

func newTarDeviceFixture() *tarDeviceFixture {
	// Current controls differ from both offered record modes, exercising actual restore/readback.
	mode := make([]byte, 88)
	binary.BigEndian.PutUint16(mode, 86)
	mode[3], mode[7], mode[14] = 0x10, 8, 2
	mode[16], mode[17], mode[18] = 0x0f, 14, 0x40
	mode[23], mode[27] = 1, 1
	mode[32], mode[33], mode[42] = 0x10, 14, 0x10
	mode[48], mode[49], mode[50], mode[52] = 0x11, 6, 3, 0x24
	mode[56], mode[57] = 0x1d, 30
	return &tarDeviceFixture{mode: mode, barcode: "ABC001L5", algorithmIndex: 7}
}

func (f *tarDeviceFixture) Resolve(device string) (tape.Identity, error) {
	return tape.Identity{Configured: device, Key: "/sys/tar-fixture", SG: "fixture-sg"}, nil
}

func (f *tarDeviceFixture) CheckOwnership(context.Context, tape.Identity) error { return nil }

func (f *tarDeviceFixture) Inspect(context.Context, tape.Identity) (*entity.InspectDeviceResponse, error) {
	return nil, tape.ErrUnsupported
}

func (f *tarDeviceFixture) Open(tape.Identity) (tape.Transport, error) {
	// The external script writes this test-only handoff; no native command may load a key.
	f.lock.Lock()
	defer f.lock.Unlock()
	if f.scriptKey != "" {
		data, err := os.ReadFile(f.scriptKey)
		if err == nil {
			if f.opens != f.closes {
				return nil, errors.New("Encrypt script overlapped a raw descriptor")
			}
			key, err := hex.DecodeString(string(data))
			if err != nil {
				return nil, err
			}
			f.cipherKey = key
			f.cipherScope, f.cipherNexus, f.encrypt, f.decrypt = 2, 2, 2, 2
			f.cipherAlgorithm = f.algorithmIndex
			f.cursor = 0 // Existing adapters may LOAD, invalidating the previous cursor.
			f.securityEvents = append(f.securityEvents, "script")
			if err := os.Remove(f.scriptKey); err != nil {
				return nil, err
			}
		} else if !os.IsNotExist(err) {
			return nil, err
		}
	}

	// Register raw ownership so the external adapter can reject overlapping access.
	if f.scriptGuard != "" {
		if err := os.WriteFile(f.scriptGuard, nil, 0600); err != nil {
			return nil, err
		}
	}
	f.opens++
	return &tarFixtureHandle{fixture: f}, nil
}

type tarFixtureHandle struct {
	fixture       *tarDeviceFixture
	closed        bool
	positionReady bool
}

func tarFixtureSense(key, flags, ascq byte, information int32) []byte {
	sense := make([]byte, 18)
	sense[0], sense[2], sense[7], sense[13] = 0xf0, key|flags, 10, ascq
	binary.BigEndian.PutUint32(sense[3:7], uint32(information))
	return sense
}

func (h *tarFixtureHandle) Execute(ctx context.Context, command tape.Command) (tape.Response, error) {
	// One lane owns this fixture; canceled or closed handles cannot touch its recorded state.
	f := h.fixture
	f.lock.Lock()
	defer f.lock.Unlock()
	if err := ctx.Err(); err != nil {
		return tape.Response{}, err
	}
	if h.closed {
		return tape.Response{}, errors.New("fixture handle closed")
	}
	if f.poison != nil {
		return tape.Response{}, f.poison
	}

	// Decode only the bounded commands consumed by the real prepared factory path.
	var data []byte
	switch command.CDB[0] {
	case 0:
	case 0x1b:
		if !bytes.Equal(command.CDB, []byte{0x1b, 0, 0, 0, 0, 0}) || command.Direction != tape.NoData {
			return tape.Response{}, fmt.Errorf("invalid fixture unload command")
		}
		f.unloads++
		f.cursor = 0
		clear(f.cipherKey)
		f.cipherKey = nil
		f.cipherScope, f.cipherNexus, f.encrypt, f.decrypt = 0, 0, 0, 0
		f.securityEvents = append(f.securityEvents, "unload")
	case 0x12:
		if command.CDB[1] == 1 {
			data = []byte{1, 0x80, 0, 3, 'S', 'N', '1'}
		} else {
			data = make([]byte, 96)
			data[0], data[2], data[3], data[4] = 1, 5, 2, 91
			copy(data[8:16], "FIXTURE ")
			copy(data[16:32], "SSC RECORDS     ")
			copy(data[32:36], "0001")
		}
	case 5:
		data = []byte{0, 0, 0x20, 0, 0, 0}
	case 0x5a:
		data = append([]byte(nil), f.mode...)
		if command.CDB[2] == 0x7f || command.CDB[2] == 0x4f {
			data[18], data[19] = 0x80, 0x80
			data[46] = 0xff
		}
	case 0xa2:
		if command.CDB[1] == 0 {
			data = []byte{0, 0, 0, 0, 0, 0, 0, 1, 0x20}
		} else {
			var err error
			data, err = f.securityPage(binary.BigEndian.Uint16(command.CDB[2:4]))
			if err != nil {
				return tape.Response{}, err
			}
		}
	case 0xb5:
		return tape.Response{}, errors.New("native key command must not be issued")
	case 0xa3:
		if command.CDB[3] == 0x92 || command.CDB[3] == 0x2b {
			if f.navigationSupport != nil {
				data = []byte{0, *f.navigationSupport}
				break
			}
			if f.locate10 && command.CDB[3] == 0x92 {
				data = []byte{0, 1}
				break
			}
		}
		length := 6
		if command.CDB[3] == 0x55 || command.CDB[3] == 0x34 || command.CDB[3] == 0x2b {
			length = 10
		}
		if command.CDB[3] == 0x92 {
			length = 16
		}
		if command.CDB[3] == 0xa2 || command.CDB[3] == 0xb5 {
			length = 12
		}
		data = make([]byte, 4+length)
		data[1], data[3], data[4] = 3, byte(length), command.CDB[3]
		for i := 5; i < len(data); i++ {
			data[i] = 0xff
		}
		if command.CDB[3] == 0x34 {
			data[5] = command.CDB[5]
		}
	case 0x8c:
		if command.CDB[1] == 3 {
			data = []byte{0, 2, 0, 1}
		} else {
			data = make([]byte, 9+len(f.barcode))
			binary.BigEndian.PutUint32(data, uint32(5+len(f.barcode)))
			data[4], data[5], data[6] = 8, 6, 1
			binary.BigEndian.PutUint16(data[7:9], uint16(len(f.barcode)))
			copy(data[9:], f.barcode)
		}
	case 0x8d:
		// WRITE ATTRIBUTE carries one padded ASCII BARCODE; barcodeStored can model a divergent cartridge memory.
		f.securityEvents = append(f.securityEvents, "attribute")
		f.barcodeWrites++
		if len(command.Data) != 41 || binary.BigEndian.Uint32(command.Data[:4]) != 37 || binary.BigEndian.Uint16(command.Data[4:6]) != 0x0806 ||
			command.Data[6] != 1 || binary.BigEndian.Uint16(command.Data[7:9]) != 32 || command.Direction != tape.ToDevice {
			return tape.Response{}, errors.New("fixture unsupported attribute write")
		}
		f.barcode = string(bytes.TrimRight(command.Data[9:], " "))
		if f.barcodeStored != nil {
			f.barcode = *f.barcodeStored
		}
	case 0x34:
		f.positionCommands = append(f.positionCommands, command.CDB[1])
		if f.positionError != nil {
			return tape.Response{}, f.positionError
		}
		if f.positionUnsupported || f.shortPosition && command.CDB[1] == 6 {
			sense := tarFixtureSense(5, 0, 0, 0)
			sense[12] = 0x24
			return tape.Response{Completed: true, Status: 2, Residual: int32(len(command.Data)), Sense: sense}, nil
		}
		data = make([]byte, len(command.Data))
		if f.cursor == 0 {
			data[0] = 0x80
		}
		if f.positionBadBOP {
			data[0] |= 0x80
		}
		if len(data) == 32 {
			binary.BigEndian.PutUint64(data[8:16], uint64(f.cursor)+f.positionDelta)
			var file uint64
			for _, record := range f.records[:f.cursor] {
				if record.mark {
					file++
				}
			}
			binary.BigEndian.PutUint64(data[16:24], file)
		} else {
			binary.BigEndian.PutUint32(data[4:8], uint32(uint64(f.cursor)+f.positionDelta))
		}
		if f.positionUnknown || f.positionUnknownUntilMotion && !h.positionReady {
			data[0] |= 4 // LONU/LOLU invalidate only the logical-object value, not the independently known file.
		}
	case 0x4d:
		f.capacityQueries = append(f.capacityQueries, f.filemarks)
		if f.capacityEOD == nil || f.capacityEW == nil {
			sense := tarFixtureSense(5, 0, 0, 0)
			sense[12] = 0x20
			return tape.Response{Completed: true, Status: 2, Residual: int32(len(command.Data)), Sense: sense}, nil
		}
		data = make([]byte, 20)
		data[0], data[3] = 0x0c, 16
		for i, value := range []uint32{*f.capacityEOD, *f.capacityEW} {
			offset := 4 + 8*i
			binary.BigEndian.PutUint16(data[offset:], uint16(4+i))
			data[offset+2], data[offset+3] = 0x60, 4
			binary.BigEndian.PutUint32(data[offset+4:], value)
		}
	case 0x55:
		f.securityEvents = append(f.securityEvents, "mode")
		if command.CDB[1] != 0x10 || command.Data[0] != 0 || command.Data[1] != 0 {
			return tape.Response{}, errors.New("fixture saved mode selection")
		}
		copy(f.mode[13:16], command.Data[13:16])
		for offset := 16; offset < len(command.Data); {
			if len(command.Data)-offset < 2 {
				return tape.Response{}, errors.New("fixture truncated mode page")
			}
			end := offset + 2 + int(command.Data[offset+1])
			if end > len(command.Data) {
				return tape.Response{}, errors.New("fixture incomplete mode page")
			}
			switch command.Data[offset] & 0x3f {
			case 0x0f:
				f.selectedCompression = append(f.selectedCompression, command.Data[offset+2]&0x80 != 0)
				copy(f.mode[16:32], command.Data[offset:end])
			case 0x10:
				copy(f.mode[32:48], command.Data[offset:end])
			default:
				return tape.Response{}, errors.New("fixture unsupported mode page")
			}
			offset = end
		}
	case 1:
		f.securityEvents = append(f.securityEvents, "rewind")
		f.cursor = 0
		h.positionReady = true
	case 0x2b, 0x92:
		f.locateCommands = append(f.locateCommands, append([]byte(nil), command.CDB...))
		if f.locateError != nil {
			return tape.Response{}, f.locateError
		}
		expected := make([]byte, len(command.CDB))
		expected[0], expected[1] = command.CDB[0], 2 // Partition zero, CP=1, IMMED=0 and standard object addressing.
		var object uint64
		if command.CDB[0] == 0x2b && len(command.CDB) == 10 {
			object = uint64(binary.BigEndian.Uint32(command.CDB[3:7]))
			binary.BigEndian.PutUint32(expected[3:7], uint32(object))
		} else if command.CDB[0] == 0x92 && len(command.CDB) == 16 {
			object = binary.BigEndian.Uint64(command.CDB[4:12])
			binary.BigEndian.PutUint64(expected[4:12], object)
		} else {
			return tape.Response{}, errors.New("fixture invalid LOCATE form")
		}
		if !bytes.Equal(command.CDB, expected) || object > uint64(len(f.records)) {
			return tape.Response{}, errors.New("fixture unsupported LOCATE address or fields")
		}
		f.cursor = int(object)
		h.positionReady = true
	case 0x11:
		if bytes.Equal(command.CDB, []byte{0x11, 3, 0, 0, 0, 0}) {
			f.cursor = len(f.records)
			h.positionReady = true
			break
		}
		if command.CDB[1] != 1 {
			return tape.Response{}, fmt.Errorf("fixture only supports forward filemark SPACE")
		}
		count := int(command.CDB[2])<<16 | int(command.CDB[3])<<8 | int(command.CDB[4])
		for count > 0 && f.cursor < len(f.records) {
			if f.records[f.cursor].mark {
				count--
			}
			f.cursor++
		}
		if count != 0 {
			return tape.Response{}, errors.New("fixture filemark not found")
		}
	case 0x0a:
		f.securityEvents = append(f.securityEvents, "write")
		f.writes++
		compressed := f.mode[18]&0x80 != 0
		f.writeCompression = append(f.writeCompression, compressed)
		record := tarFixtureRecord{data: append([]byte(nil), command.Data...), compressed: compressed}
		if f.encrypt == 2 {
			record.key = append([]byte(nil), f.cipherKey...)
		}
		f.records = append(f.records[:f.cursor], record)
		f.cursor++
	case 0x10:
		count := int(command.CDB[2])<<16 | int(command.CDB[3])<<8 | int(command.CDB[4])
		if count != 1 {
			return tape.Response{}, errors.New("fixture expects one completing filemark")
		}
		f.filemarks++
		if f.failMark == f.filemarks {
			f.poison = &tape.UncertainError{Cause: errors.New("fixture filemark completion unknown")}
			return tape.Response{}, f.poison
		}
		f.records = append(f.records[:f.cursor], tarFixtureRecord{mark: true})
		f.cursor++
		if f.warningMark == f.filemarks {
			return tape.Response{Completed: true, Status: 2, Sense: tarFixtureSense(0, 0x40, 2, 0)}, nil
		}
	case 8:
		f.securityEvents = append(f.securityEvents, "read")
		f.readCompression = append(f.readCompression, f.mode[18]&0x80 != 0)
		remaining := int32(len(command.Data))
		if command.CDB[1]&1 != 0 {
			remaining = 1
		}
		if f.cursor == len(f.records) {
			return tape.Response{Completed: true, Status: 2, Residual: int32(len(command.Data)), Sense: tarFixtureSense(8, 0, 5, remaining)}, nil
		}
		record := f.records[f.cursor]
		f.cursor++
		if record.key != nil && (f.decrypt != 2 || !bytes.Equal(record.key, f.cipherKey)) {
			sense := tarFixtureSense(7, 0, 0, 0)
			sense[12] = 0x74
			return tape.Response{Completed: true, Status: 2, Residual: int32(len(command.Data)), Sense: sense}, nil
		}
		if record.compressed && f.mode[19]&0x80 == 0 {
			sense := tarFixtureSense(3, 0, 0, 0)
			sense[12] = 0x11
			return tape.Response{Completed: true, Status: 2, Residual: int32(len(command.Data)), Sense: sense}, nil
		}
		if record.mark {
			return tape.Response{Completed: true, Status: 2, Residual: int32(len(command.Data)), Sense: tarFixtureSense(0, 0x80, 1, remaining)}, nil
		}
		if record.compressed && f.decodeAlgorithmOnRead != nil {
			binary.BigEndian.PutUint32(f.mode[24:28], *f.decodeAlgorithmOnRead)
		}
		if len(record.data) > len(command.Data) {
			copy(command.Data, record.data)
			return tape.Response{Completed: true, Status: 2, Sense: tarFixtureSense(0, 0x20, 0, int32(len(command.Data)-len(record.data)))}, nil
		}
		copy(command.Data, record.data)
		if len(record.data) != len(command.Data) {
			residual := int32(len(command.Data) - len(record.data))
			return tape.Response{Completed: true, Status: 2, Residual: residual, Sense: tarFixtureSense(0, 0x20, 0, residual)}, nil
		}
		return tape.Response{Completed: true}, nil
	default:
		return tape.Response{}, fmt.Errorf("unexpected fixture command %02x", command.CDB[0])
	}
	copy(command.Data, data)
	residual := int32(0)
	if command.Direction == tape.FromDevice {
		residual = int32(len(command.Data) - len(data))
	}
	return tape.Response{Completed: true, Residual: residual}, nil
}

func (h *tarFixtureHandle) Close() error {
	// Release the overlap guard together with this synthetic descriptor.
	h.fixture.lock.Lock()
	defer h.fixture.lock.Unlock()
	if !h.closed {
		h.fixture.closes++
		h.closed = true
		if h.fixture.scriptGuard != "" {
			if err := os.Remove(h.fixture.scriptGuard); err != nil && !os.IsNotExist(err) {
				return err
			}
		}
	}
	return errors.Join(h.fixture.poison, h.fixture.cleanupError)
}
