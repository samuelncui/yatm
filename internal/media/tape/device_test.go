package tape

import (
	"context"
	"encoding/binary"
	"errors"
	"syscall"
	"testing"
	"unsafe"

	"github.com/samuelncui/yatm/entity"
)

type deviceFixture struct {
	noPartitionList                                                                   bool
	deferredPartitionCount, failedFormat, wrongPartitionControls, wrongFormattedCount bool
	mode                                                                              []byte
	selects, motions, writes, closed, formats, security                               int
	absentEncryption, failedReadback, failureCleanup                                  bool
	unsupported                                                                       byte
	actualPartitions                                                                  byte
	readResponse                                                                      *Response
	attributes                                                                        []Command
}

func newDeviceFixture() *deviceFixture {
	// Synthetic SSC pages cover record controls, EOD generation, layout and rewritable medium facts.
	mode := make([]byte, 88)
	binary.BigEndian.PutUint16(mode, 86)
	mode[3], mode[7], mode[14] = 0x10, 8, 2
	mode[16], mode[17], mode[18], mode[19] = 0x0f, 14, 0xc0, 0x80
	binary.BigEndian.PutUint32(mode[20:24], 1)
	binary.BigEndian.PutUint32(mode[24:28], 1)
	mode[32], mode[33], mode[42] = 0x10, 14, 0x10
	mode[46] = 1
	mode[48], mode[49], mode[50], mode[52] = 0x11, 6, 3, 0x24
	mode[56], mode[57] = 0x1d, 30
	return &deviceFixture{mode: mode, actualPartitions: 1}
}

func (f *deviceFixture) execute(command Command) (Response, error) {
	// Every device query is bounded; state-changing commands are counted and mutate only this fixture.
	var data []byte
	switch command.CDB[0] {
	case 0x12:
		response, err := inspectionFixture(command)
		if command.CDB[1] == 0 {
			command.Data[2], command.Data[3] = 5, 2
		}
		return response, err
	case 0x5a:
		data = append([]byte(nil), f.mode...)
		if command.CDB[2] == 0x7f {
			data[18], data[19], data[46], data[51], data[52] = 0x80, 0xe0, 1, 0xff, 0xe0
		}
		if f.failedReadback && f.selects == 1 {
			data[14] = 2
		}
	case 0x34:
		response, err := inspectionFixture(command)
		if command.CDB[1] == 6 {
			binary.BigEndian.PutUint32(command.Data[4:8], uint32(f.mode[35]))
		} else {
			command.Data[1] = f.mode[35]
		}
		return response, err
	case 0xa2:
		f.security++
		if command.CDB[1] == 0 {
			protocol := byte(0x20)
			if f.absentEncryption {
				protocol = 0
			}
			data = []byte{0, 0, 0, 0, 0, 0, 0, 1, protocol}
		} else {
			return inspectionFixture(command)
		}
	case 0xa3:
		opcode := command.CDB[3]
		if opcode == f.unsupported {
			data = []byte{0, 1}
			break
		}
		length := 6
		if opcode == 0x55 {
			length = 10
		}
		if opcode == 0x34 || opcode == 0x2b {
			length = 10
		}
		if opcode == 0x92 {
			length = 16
		}
		data = make([]byte, 4+length)
		data[1], data[3], data[4] = 3, byte(length), opcode
		for i := 5; i < len(data); i++ {
			data[i] = 0xff
		}
		if opcode == 0x34 {
			data[5] = command.CDB[5]
		}
	case 0x55:
		f.selects++
		if f.failureCleanup && f.selects > 1 {
			return Response{Completed: true, Status: 2, Sense: fixedSense(0x70, 5, 0x26, 0, false, 0, 0)}, nil
		}
		if command.CDB[1] != 0x10 || command.Data[0] != 0 || command.Data[1] != 0 {
			return Response{}, errors.New("saved or malformed mode selection")
		}
		f.mode[3] = f.mode[3]&0x80 | command.Data[3]&0x7f
		if command.Data[7] == 0 {
			count := f.mode[51]
			copy(f.mode[48:56], command.Data[8:16])
			if f.deferredPartitionCount {
				f.mode[51] = count
			}
			if f.wrongPartitionControls {
				f.mode[52] ^= 0x40
			}
		} else {
			copy(f.mode[13:16], command.Data[13:16])
			pages, err := selectionPages(command.Data)
			if err != nil {
				return Response{}, err
			}
			_, current, err := modeData(f.mode)
			if err != nil {
				return Response{}, err
			}
			for _, code := range []byte{0x0f, 0x10} {
				if page := pages[code]; len(page) != 0 {
					copy(current[code], page)
				}
			}
		}
	case 4:
		f.formats++
		if f.failedFormat {
			return Response{Completed: true, Status: 2, Sense: fixedSense(0x70, 5, 0x24, 0, false, 0, 0)}, nil
		}
		if f.wrongFormattedCount {
			break
		}
		f.actualPartitions = 1
		f.mode[51] = 0
	case 1:
		f.motions++
	case 0x92:
		f.motions++
		f.mode[35] = command.CDB[3]
	case 8:
		if f.readResponse != nil {
			return *f.readResponse, nil
		}
	case 0x0a, 0x10:
		f.writes++
	case 0x8d:
		f.attributes = append(f.attributes, Command{CDB: append([]byte(nil), command.CDB...), Direction: command.Direction, Data: append([]byte(nil), command.Data...)})
	case 0x8c:
		if command.CDB[1] == 3 {
			if f.noPartitionList {
				return Response{Completed: true, Status: 2, Sense: fixedSense(0x70, 5, 0x24, 0, false, 0, 0)}, nil
			}
			data = []byte{0, 2, 0, f.actualPartitions}
		} else {
			data = []byte{0, 0, 0, 11, 8, 6, 1, 0, 6, 'A', 'B', 'C', '1', '2', '3'}
		}
	default:
		return inspectionFixture(command)
	}
	copy(command.Data, data)
	residual := int32(0)
	if command.Direction == FromDevice {
		residual = int32(len(command.Data) - len(data))
	}
	return Response{Completed: true, Residual: residual}, nil
}

func fixtureDevice(t *testing.T, f *deviceFixture) *Device {
	t.Helper()
	transport := newSerializedTransport(f.execute, func() error { f.closed++; return nil })
	device, err := NewDevice(context.Background(), Identity{Configured: "fixture"}, transport)
	if err != nil {
		t.Fatal(err)
	}
	return device
}

func TestDevicePartitionListResponse(t *testing.T) {
	for _, tc := range []struct {
		name  string
		data  []byte
		count uint32
		err   error
	}{
		{"single", []byte{0, 2, 0, 1}, 1, nil},
		{"multiple", []byte{0, 2, 0, 2}, 2, nil},
		{"short", []byte{0, 2, 0}, 0, ErrMalformed},
		{"wrong length", []byte{0, 3, 0, 2}, 0, ErrMalformed},
		{"nonzero first", []byte{0, 2, 1, 2}, 0, ErrMalformed},
		{"no partitions", []byte{0, 2, 0, 0}, 0, ErrMalformed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Standard four-byte responses include their own two-byte length and partition range.
			var observed Command
			transport := newSerializedTransport(func(command Command) (Response, error) {
				observed = command
				copy(command.Data, tc.data)
				return Response{Completed: true, Residual: int32(len(command.Data) - len(tc.data))}, nil
			}, func() error { return nil })
			defer transport.Close()

			// The real query/decoder must retain malformed-layout refusal without guessing a count.
			count, err := (&Device{transport: transport}).partitionCount(context.Background())
			if count != tc.count || !errors.Is(err, tc.err) {
				t.Fatalf("partition count=%d, error=%v; want count=%d, error=%v", count, err, tc.count, tc.err)
			}
			if len(observed.CDB) != 16 || observed.CDB[0] != 0x8c || observed.CDB[1] != 3 ||
				binary.BigEndian.Uint32(observed.CDB[10:14]) != 4 || len(observed.Data) != 4 {
				t.Fatalf("partition-list query CDB=%x, allocation=%d", observed.CDB, len(observed.Data))
			}
		})
	}
}

func TestNewDeviceInspectionFailurePreservesCloseUncertainty(t *testing.T) {
	// Failed construction has no Device cleanup owner; failed closure must retain the attempt lease.
	inspectionErr := errors.New("inspection failed before command acceptance")
	for _, closeErr := range []error{nil, syscall.EIO} {
		commands, closes := 0, 0
		transport := newSerializedTransport(func(Command) (Response, error) {
			commands++
			return Response{}, inspectionErr
		}, func() error {
			closes++
			return closeErr
		})
		device, err := NewDevice(context.Background(), Identity{Configured: "fixture"}, transport)
		if device != nil || !errors.Is(err, inspectionErr) || commands != 1 || closes != 1 {
			t.Fatalf("device=%v err=%v commands=%d closes=%d", device, err, commands, closes)
		}
		if errors.Is(err, ErrUncertain) != (closeErr != nil) || closeErr != nil && !errors.Is(err, closeErr) {
			t.Fatalf("close=%v construction error=%v", closeErr, err)
		}
	}
}

func TestDeviceConfigurationDoesNotAdvertiseGrownReservationAsMaximum(t *testing.T) {
	for _, actual := range []int{512 << 10, 64 << 10} {
		// Preparation requires exact reserve readback without turning the current allocation into a maximum.
		f := newDeviceFixture()
		transport := &sgTransport{limit: 32 << 10, serializedTransport: newSerializedTransport(f.execute, func() error { f.closed++; return nil })}
		transport.ioctl = func(operation uintptr, pointer unsafe.Pointer) error {
			value := (*int32)(pointer)
			if operation == sgSetReservedSize {
				if *value != 512<<10 {
					t.Fatalf("selected reservation clamped: %d", *value)
				}
			} else if operation == sgReservedSize {
				*value = int32(actual)
			} else {
				t.Fatalf("unexpected reservation ioctl: %x", operation)
			}
			return nil
		}
		d, err := NewDevice(context.Background(), Identity{Configured: "fixture"}, transport)
		if err != nil {
			t.Fatal(err)
		}
		_, err = d.Configure(context.Background(), &entity.TarMediaProfile{RecordMode: entity.TapeRecordMode_TAPE_RECORD_MODE_VARIABLE, RecordBytes: 512 << 10}, Read)
		if (err == nil) != (actual == 512<<10) || transport.TransferLimit() != actual {
			t.Fatalf("actual=%d reservation=%d preparation=%v", actual, transport.TransferLimit(), err)
		}
		for _, kind := range []entity.DeviceFeatureKind{entity.DeviceFeatureKind_DEVICE_FEATURE_KIND_SEQUENTIAL_READ, entity.DeviceFeatureKind_DEVICE_FEATURE_KIND_SEQUENTIAL_WRITE} {
			if limits := feature(d.Report(), kind).GetRecords(); limits.TransportMaximumBytes != nil {
				t.Fatalf("current allocation advertised as maximum: %v", limits)
			}
		}
		if actual < 512<<10 && (f.selects != 0 || f.motions != 0 || f.writes != 0) {
			t.Fatal("insufficient reservation admitted device mutation")
		}
		if err := d.Cleanup(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
}

func TestDeviceCapturesRestoresModeAndAcceptsBootstrapPolicy(t *testing.T) {
	// No initialization UUID is available before reading a label; transport policy alone is sufficient.
	f := newDeviceFixture()
	d := fixtureDevice(t, f)
	records, err := d.Configure(context.Background(), &entity.TarMediaProfile{RecordMode: entity.TapeRecordMode_TAPE_RECORD_MODE_VARIABLE, RecordBytes: 512}, Format)
	if err != nil || records.Mode.FixedBytes != 0 || !records.Mode.WarningCounts || blockLength(f.mode) != 0 || f.mode[18]&0x80 != 0 {
		t.Fatalf("preparation=%+v err=%v mode=%x", records, err, f.mode)
	}
	if f.motions != 0 || f.writes != 0 {
		t.Fatal("configuration moved or wrote Tape")
	}

	// Initialization positions BOT; cleanup restores only captured controls and never emits a filemark.
	if err := d.Initialize(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := d.Cleanup(context.Background()); err != nil {
		t.Fatal(err)
	}
	if blockLength(f.mode) != 512 || f.mode[18]&0x80 == 0 || f.mode[19]&0x80 == 0 || f.selects != 2 || f.writes != 0 || f.closed != 1 {
		t.Fatalf("cleanup mode=%x selects=%d writes=%d closed=%d", f.mode, f.selects, f.writes, f.closed)
	}
	if err := d.Cleanup(context.Background()); err != nil || f.closed != 1 {
		t.Fatal("cleanup repeated")
	}
}

func TestDeviceReadSkipsWriteOnlyPrerequisitesAndKnownAbsentControls(t *testing.T) {
	// Missing compression and tape-encryption capabilities do not require unsupported control commands.
	f := newDeviceFixture()
	f.absentEncryption = true
	f.mode = append(f.mode[:16], f.mode[32:]...)
	binary.BigEndian.PutUint16(f.mode, uint16(len(f.mode)-2))
	f.mode[3] |= 0x80
	f.mode[26], f.mode[30], f.mode[42] = 0, 1, 1
	d := fixtureDevice(t, f)
	_, err := d.Configure(context.Background(), &entity.TarMediaProfile{RecordMode: entity.TapeRecordMode_TAPE_RECORD_MODE_FIXED, RecordBytes: 512}, Read)
	if err != nil || f.selects != 0 || d.Report().GetTape().EncryptionEnabled == nil || d.Report().GetTape().GetEncryptionEnabled() {
		t.Fatalf("read preparation=%v selects=%d report=%v", err, f.selects, d.Report())
	}
	if err := d.Cleanup(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestDevicePreparationFailureRestoresFirstAttemptedMutation(t *testing.T) {
	for _, cleanupFails := range []bool{false, true} {
		// A failed mode readback still owns restoration, even though no Session reached its caller.
		f := newDeviceFixture()
		f.failedReadback, f.failureCleanup = true, cleanupFails
		d := fixtureDevice(t, f)
		_, err := d.Configure(context.Background(), &entity.TarMediaProfile{RecordMode: entity.TapeRecordMode_TAPE_RECORD_MODE_VARIABLE, RecordBytes: 512}, Format)
		if err == nil || errors.Is(err, ErrUncertain) != cleanupFails || f.selects != 2 || f.closed != 1 || f.writes != 0 {
			t.Fatalf("failure=%v selects=%d closed=%d writes=%d", err, f.selects, f.closed, f.writes)
		}
	}
}

func TestDeviceRefusesUnsupportedPartitionTransitionBeforeMutation(t *testing.T) {
	// An explicit unsupported FORMAT command blocks reinitialization before any mode/data mutation.
	f := newDeviceFixture()
	f.mode[51], f.actualPartitions, f.unsupported = 1, 2, 4
	d := fixtureDevice(t, f)
	_, err := d.Configure(context.Background(), &entity.TarMediaProfile{RecordMode: entity.TapeRecordMode_TAPE_RECORD_MODE_VARIABLE, RecordBytes: 512}, Format)
	if !errors.Is(err, ErrUnsupported) || f.selects != 0 || f.writes != 0 || f.formats != 0 {
		t.Fatalf("transition=%v selects=%d writes=%d formats=%d", err, f.selects, f.writes, f.formats)
	}
}

func TestDeviceFormatsActualSinglePartitionAndOptionalBarcode(t *testing.T) {
	// A pending mode-page change is followed by FORMAT and an actual partition-list observation.
	f := newDeviceFixture()
	f.mode[51], f.actualPartitions = 1, 2
	d := fixtureDevice(t, f)
	_, err := d.Configure(context.Background(), &entity.TarMediaProfile{RecordMode: entity.TapeRecordMode_TAPE_RECORD_MODE_VARIABLE, RecordBytes: 512}, Format)
	if err != nil {
		t.Fatal(err)
	}
	if err := d.Initialize(context.Background()); err != nil {
		t.Fatal(err)
	}
	barcode, err := d.Barcode(context.Background())
	if err != nil || barcode != "ABC123" || f.formats != 1 || d.Report().GetTape().GetPartitionCount() != 1 {
		t.Fatalf("barcode=%q err=%v", barcode, err)
	}
	if err := d.Cleanup(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestDeviceWriteBarcodeEncodesPaddedASCIIAttribute(t *testing.T) {
	// Only FORMAT preparation may assign the cartridge memory barcode; the value is ASCII and space-padded to 32 bytes.
	f := newDeviceFixture()
	d := fixtureDevice(t, f)
	profile := &entity.TarMediaProfile{RecordMode: entity.TapeRecordMode_TAPE_RECORD_MODE_VARIABLE, RecordBytes: 512}
	if _, err := d.Configure(context.Background(), profile, Read); err != nil {
		t.Fatal(err)
	}
	if err := d.WriteBarcode(context.Background(), "ABC123"); err == nil || len(f.attributes) != 0 {
		t.Fatalf("read preparation assigned barcode, err=%v commands=%d", err, len(f.attributes))
	}
	if err := d.Cleanup(context.Background()); err != nil {
		t.Fatal(err)
	}

	d = fixtureDevice(t, f)
	if _, err := d.Configure(context.Background(), profile, Format); err != nil {
		t.Fatal(err)
	}
	for _, invalid := range []string{"", "ABC\x00", "123456789012345678901234567890123"} {
		if err := d.WriteBarcode(context.Background(), invalid); err == nil {
			t.Fatalf("invalid barcode %q was accepted", invalid)
		}
	}
	if err := d.WriteBarcode(context.Background(), "ABC123"); err != nil || len(f.attributes) != 1 {
		t.Fatalf("err=%v commands=%d", err, len(f.attributes))
	}
	wantCDB := []byte{0x8d, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 41, 0, 0}
	wantData := append([]byte{0, 0, 0, 37, 8, 6, 1, 0, 32, 'A', 'B', 'C', '1', '2', '3'}, []byte("                          ")...)
	command := f.attributes[0]
	if string(command.CDB) != string(wantCDB) || string(command.Data) != string(wantData) || command.Direction != ToDevice {
		t.Fatalf("cdb=% x data=% x", command.CDB, command.Data)
	}
	if err := d.Cleanup(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestDeviceUncertainReadWithoutModeChangesRetainsCleanupError(t *testing.T) {
	// A poisoned read must retain device unavailability even when there are no controls to restore.
	f := newDeviceFixture()
	d := fixtureDevice(t, f)
	records, err := d.Configure(context.Background(), &entity.TarMediaProfile{RecordMode: entity.TapeRecordMode_TAPE_RECORD_MODE_FIXED, RecordBytes: 512}, Read)
	if err != nil {
		t.Fatal(err)
	}
	f.readResponse = &Response{Completed: true, HostStatus: 3}
	if _, err := records.Read(context.Background(), make([]byte, 512)); !errors.Is(err, ErrUncertain) {
		t.Fatal(err)
	}
	if err := d.Cleanup(context.Background()); !errors.Is(err, ErrUncertain) || f.selects != 0 || f.closed != 1 {
		t.Fatalf("cleanup=%v", err)
	}
}

func TestDeviceFormatChangesActivePartitionOnlyWithReportedForm(t *testing.T) {
	for _, unsupported := range []bool{false, true} {
		// Former LTFS media may start in partition one; reported CP support enables structural preparation.
		f := newDeviceFixture()
		f.mode[35], f.mode[51], f.actualPartitions = 1, 1, 2
		if unsupported {
			f.unsupported = 0x92
		}
		d := fixtureDevice(t, f)
		_, err := d.Configure(context.Background(), &entity.TarMediaProfile{RecordMode: entity.TapeRecordMode_TAPE_RECORD_MODE_VARIABLE, RecordBytes: 512}, Format)
		if unsupported {
			if !errors.Is(err, ErrUnsupported) || f.motions != 0 || f.selects != 0 || f.writes != 0 {
				t.Fatalf("unsupported transition=%v motions=%d", err, f.motions)
			}
			continue
		}
		if err != nil || f.mode[35] != 0 || f.motions != 1 || f.writes != 0 {
			t.Fatalf("partition preparation=%v motions=%d", err, f.motions)
		}
		if err := d.Initialize(context.Background()); err != nil {
			t.Fatal(err)
		}
		if err := d.Cleanup(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
}

func TestReadAndAppendUseActualLayoutInsteadOfPendingModeCount(t *testing.T) {
	for _, operation := range []Operation{Read, Append} {
		// The mode page claims a pending single partition while MAM reports two physical partitions.
		f := newDeviceFixture()
		f.actualPartitions = 2
		d := fixtureDevice(t, f)
		_, err := d.Configure(context.Background(), &entity.TarMediaProfile{RecordMode: entity.TapeRecordMode_TAPE_RECORD_MODE_FIXED, RecordBytes: 512}, operation)
		if err == nil || f.selects != 0 || f.writes != 0 || f.motions != 0 {
			t.Fatalf("pending layout accepted: err=%v", err)
		}
	}
}

func TestPartitionListFallbackRequiresNonPendingCurrentMode(t *testing.T) {
	for _, pending := range []bool{false, true} {
		// Only immediate-layout mode data remains authoritative when the optional partition list is absent.
		f := newDeviceFixture()
		f.noPartitionList = true
		if !pending {
			f.mode[52] &^= 4
		}
		d := fixtureDevice(t, f)
		_, err := d.Configure(context.Background(), &entity.TarMediaProfile{RecordMode: entity.TapeRecordMode_TAPE_RECORD_MODE_FIXED, RecordBytes: 512}, Read)
		if (err != nil) != pending {
			t.Fatalf("pending=%t read preparation=%v", pending, err)
		}
		if !pending {
			if err := d.Cleanup(context.Background()); err != nil {
				t.Fatal(err)
			}
		}
	}
}

func TestWritesRefuseUnknownEODAndBufferModesBeforeMutation(t *testing.T) {
	for _, buffered := range []bool{false, true} {
		// Reserved mode values cannot qualify synchronization or new recorded-EOD semantics.
		f := newDeviceFixture()
		if buffered {
			f.mode[3] = 0x70
		} else {
			f.mode[42] = 0x90
		}
		d := fixtureDevice(t, f)
		_, err := d.Configure(context.Background(), &entity.TarMediaProfile{RecordMode: entity.TapeRecordMode_TAPE_RECORD_MODE_FIXED, RecordBytes: 512}, Format)
		if err == nil || f.selects != 0 || f.writes != 0 || f.motions != 0 {
			t.Fatalf("unknown mode accepted: %v", err)
		}
	}
}
