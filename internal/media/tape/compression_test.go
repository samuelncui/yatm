package tape

import (
	"context"
	"encoding/binary"
	"errors"
	"testing"

	"github.com/samuelncui/yatm/entity"
)

func TestCompressionWritesApplyCoherentPolicyAndRestore(t *testing.T) {
	for _, algorithm := range []uint32{1, 0xff} {
		for _, enabled := range []bool{false, true} {
			// Actual standard IDs remain opaque; MP10 cannot override an explicit off request.
			f := newDeviceFixture()
			binary.BigEndian.PutUint32(f.mode[20:24], algorithm)
			binary.BigEndian.PutUint32(f.mode[24:28], algorithm)
			if enabled {
				f.mode[18], f.mode[46] = 0x40, 0
			}
			d := fixtureDevice(t, f)
			_, err := d.Configure(context.Background(), &entity.TarMediaProfile{RecordMode: entity.TapeRecordMode_TAPE_RECORD_MODE_VARIABLE, RecordBytes: 512, HardwareCompression: enabled}, Append)
			if err != nil || (f.mode[18]&0x80 != 0) != enabled || (f.mode[46] == 1) != enabled || binary.BigEndian.Uint32(f.mode[20:24]) != algorithm || f.writes != 0 || f.motions != 0 {
				t.Fatalf("algorithm=%x enabled=%t err=%v mode=%x", algorithm, enabled, err, f.mode)
			}

			// Cleanup restores the captured write policy and preserves fresh unrelated device configuration.
			f.mode[35], f.mode[38], f.mode[42] = 7, 0x12, 0x18
			if err := d.Cleanup(context.Background()); err != nil || (f.mode[18]&0x80 != 0) == enabled || (f.mode[46] == 1) == enabled || f.mode[35] != 7 || f.mode[38] != 0x12 || f.mode[42] != 0x18 || f.selects != 2 || f.closed != 1 {
				t.Fatalf("cleanup=%v mode=%x selects=%d", err, f.mode, f.selects)
			}
		}
	}
}

func TestKnownAbsentCompressionOffNeedsNoCompressionControl(t *testing.T) {
	// A complete current-mode response establishes legacy off without requiring a missing MP0F page.
	f := newDeviceFixture()
	f.mode = append(f.mode[:16], f.mode[32:]...)
	binary.BigEndian.PutUint16(f.mode, uint16(len(f.mode)-2))
	f.mode[30] = 0
	transport := newSerializedTransport(func(command Command) (Response, error) {
		if command.CDB[0] == 0x55 {
			pages, err := selectionPages(command.Data)
			if err != nil || len(pages[0x0f]) != 0 || len(pages[0x10]) != 0 {
				t.Fatalf("known off selected unsupported compression controls: pages=%v err=%v", pages, err)
			}
		}
		return f.execute(command)
	}, func() error { f.closed++; return nil })
	d, err := NewDevice(context.Background(), Identity{}, transport)
	if err != nil {
		t.Fatal(err)
	}
	_, err = d.Configure(context.Background(), &entity.TarMediaProfile{RecordMode: entity.TapeRecordMode_TAPE_RECORD_MODE_VARIABLE, RecordBytes: 512}, Append)
	if err != nil || f.selects != 1 || f.writes != 0 {
		t.Fatalf("off=%v selects=%d mode=%x", err, f.selects, f.mode)
	}
	if err := d.Cleanup(context.Background()); err != nil || blockLength(f.mode) != 512 || f.mode[30] != 0 {
		t.Fatalf("cleanup=%v mode=%x", err, f.mode)
	}
}

func TestKnownDCCZeroOffNeedsNoCompressionSelection(t *testing.T) {
	// DCC zero with current DCE/selector off permits record-only writes without unsupported compression controls.
	f := newDeviceFixture()
	f.mode[18], f.mode[46] = 0, 0
	transport := newSerializedTransport(func(command Command) (Response, error) {
		if command.CDB[0] == 0x55 {
			pages, err := selectionPages(command.Data)
			if err != nil || len(pages[0x0f]) != 0 || len(pages[0x10]) != 0 {
				t.Fatalf("DCC zero selected compression controls: pages=%v err=%v", pages, err)
			}
		}
		return f.execute(command)
	}, func() error { f.closed++; return nil })
	d, err := NewDevice(context.Background(), Identity{}, transport)
	if err != nil {
		t.Fatal(err)
	}
	_, err = d.Configure(context.Background(), &entity.TarMediaProfile{RecordMode: entity.TapeRecordMode_TAPE_RECORD_MODE_VARIABLE, RecordBytes: 512}, Append)
	if err != nil || f.selects != 1 || f.writes != 0 {
		t.Fatalf("off=%v selects=%d writes=%d", err, f.selects, f.writes)
	}
	if err := d.Cleanup(context.Background()); err != nil || f.selects != 2 || f.mode[18] != 0 || f.mode[46] != 0 {
		t.Fatalf("cleanup=%v mode=%x selects=%d", err, f.mode, f.selects)
	}
}

func TestUntouchedCompressionDriftRetainsCleanupUncertainty(t *testing.T) {
	for _, selector := range []bool{false, true} {
		// An actual record-only selection must not change untouched controls and then release as restored.
		f := newDeviceFixture()
		transport := newSerializedTransport(func(command Command) (Response, error) {
			response, err := f.execute(command)
			if command.CDB[0] == 0x55 && f.selects == 1 {
				if selector {
					f.mode[46] = 0
				} else {
					f.mode[19] = 0
				}
			}
			return response, err
		}, func() error { f.closed++; return nil })
		d, err := NewDevice(context.Background(), Identity{}, transport)
		if err != nil {
			t.Fatal(err)
		}
		_, err = d.Configure(context.Background(), &entity.TarMediaProfile{RecordMode: entity.TapeRecordMode_TAPE_RECORD_MODE_VARIABLE, RecordBytes: 512, HardwareCompression: true}, Read)
		if !errors.Is(err, ErrUncertain) || f.selects != 1 || f.writes != 0 || f.closed != 1 {
			t.Fatalf("selector=%t failure=%v selects=%d writes=%d closed=%d", selector, err, f.selects, f.writes, f.closed)
		}
	}
}

func TestReadDecoderRejectsReservedOrUnchangeableControls(t *testing.T) {
	for _, reserved := range []bool{false, true} {
		// Required decoding cannot normalize a reserved original or enable a nonchangeable disabled decoder.
		f := newDeviceFixture()
		f.mode[19] = 0
		if reserved {
			f.mode[19] = 0x60
		}
		transport := newSerializedTransport(func(command Command) (Response, error) {
			response, err := f.execute(command)
			if command.CDB[0] == 0x5a && command.CDB[2] == 0x7f {
				command.Data[19] = 0
			}
			return response, err
		}, func() error { f.closed++; return nil })
		d, err := NewDevice(context.Background(), Identity{}, transport)
		if err != nil {
			t.Fatal(err)
		}
		_, err = d.Configure(context.Background(), &entity.TarMediaProfile{RecordMode: entity.TapeRecordMode_TAPE_RECORD_MODE_FIXED, RecordBytes: 512, HardwareCompression: true}, Read)
		if err == nil || f.selects != 0 || f.writes != 0 || f.closed != 1 {
			t.Fatalf("reserved=%t read=%v selects=%d writes=%d", reserved, err, f.selects, f.writes)
		}
	}
}

func TestReadRecordModeChangeDoesNotSelectWriteControls(t *testing.T) {
	// Established decoding permits record-only preparation even when unrelated write controls contradict.
	f := newDeviceFixture()
	f.mode[18], f.mode[46] = 0x80, 0x80
	selections := 0
	transport := newSerializedTransport(func(command Command) (Response, error) {
		if command.CDB[0] == 0x55 {
			selections++
			pages, err := selectionPages(command.Data)
			if err != nil || len(pages[0x0f]) != 0 || len(pages[0x10]) != 0 {
				t.Fatalf("READ selected write controls: pages=%v err=%v", pages, err)
			}
		}
		return f.execute(command)
	}, func() error { f.closed++; return nil })
	d, err := NewDevice(context.Background(), Identity{}, transport)
	if err != nil {
		t.Fatal(err)
	}
	_, err = d.Configure(context.Background(), &entity.TarMediaProfile{RecordMode: entity.TapeRecordMode_TAPE_RECORD_MODE_VARIABLE, RecordBytes: 512, HardwareCompression: true}, Read)
	if err != nil || f.mode[18] != 0x80 || f.mode[46] != 0x80 {
		t.Fatalf("read=%v mode=%x", err, f.mode)
	}
	if err := d.Cleanup(context.Background()); err != nil || selections != 2 || f.mode[18] != 0x80 || f.mode[46] != 0x80 {
		t.Fatalf("cleanup=%v selections=%d mode=%x", err, selections, f.mode)
	}
}

func TestReadRecordOnlySelectionStillVerifiesRequiredDecoder(t *testing.T) {
	// Required decoding is checked in existing readback even when the selection sends only a record descriptor.
	f := newDeviceFixture()
	corrupted := false
	transport := newSerializedTransport(func(command Command) (Response, error) {
		response, err := f.execute(command)
		if command.CDB[0] == 0x5a && command.CDB[2] == 0x3f && f.selects == 1 && !corrupted {
			command.Data[19] = 0
			corrupted = true
		}
		return response, err
	}, func() error { f.closed++; return nil })
	d, err := NewDevice(context.Background(), Identity{}, transport)
	if err != nil {
		t.Fatal(err)
	}
	_, err = d.Configure(context.Background(), &entity.TarMediaProfile{RecordMode: entity.TapeRecordMode_TAPE_RECORD_MODE_VARIABLE, RecordBytes: 512, HardwareCompression: true}, Read)
	if err == nil || f.selects != 2 || f.writes != 0 || f.closed != 1 {
		t.Fatalf("read=%v selects=%d writes=%d closed=%d", err, f.selects, f.writes, f.closed)
	}
}

func TestCompressedReadChangesOnlyRequiredDecoderControls(t *testing.T) {
	// A decoder is independent of DCC/write readiness, and RED exceptions cannot reach the tar parser.
	f := newDeviceFixture()
	f.mode[18], f.mode[19], f.mode[46] = 0, 0x40, 0
	f.mode[3], f.mode[42], f.mode[58] = 0x90, 0, 1
	binary.BigEndian.PutUint32(f.mode[20:24], 0)
	binary.BigEndian.PutUint32(f.mode[24:28], 0)
	var selections []map[byte][]byte
	transport := newSerializedTransport(func(command Command) (Response, error) {
		if command.CDB[0] == 0x55 {
			pages, err := selectionPages(command.Data)
			if err != nil {
				t.Fatal(err)
			}
			selections = append(selections, pages)
		}
		return f.execute(command)
	}, func() error { f.closed++; return nil })
	d, err := NewDevice(context.Background(), Identity{}, transport)
	if err != nil {
		t.Fatal(err)
	}
	_, err = d.Configure(context.Background(), &entity.TarMediaProfile{RecordMode: entity.TapeRecordMode_TAPE_RECORD_MODE_FIXED, RecordBytes: 512, HardwareCompression: true}, Read)
	if err != nil || f.mode[18] != 0 || f.mode[19]&0xe0 != 0x80 || f.mode[46] != 0 || len(selections) != 1 || len(selections[0][0x10]) != 0 {
		t.Fatalf("read=%v mode=%x selections=%v", err, f.mode, selections)
	}

	// Last-read algorithm detection may update the ID; cleanup restores DDE/RED without replacing that ID.
	binary.BigEndian.PutUint32(f.mode[24:28], 0xff)
	if err := d.Cleanup(context.Background()); err != nil || f.mode[18] != 0 || f.mode[19]&0xe0 != 0x40 || binary.BigEndian.Uint32(f.mode[24:28]) != 0xff || f.writes != 0 || len(selections) != 2 || len(selections[1][0x10]) != 0 {
		t.Fatalf("cleanup=%v mode=%x selections=%v", err, f.mode, selections)
	}
	if limits := feature(d.Report(), entity.DeviceFeatureKind_DEVICE_FEATURE_KIND_HARDWARE_DECOMPRESSION).GetCompression(); limits.GetCurrentAlgorithm() != 0xff || limits.EnableChangeable == nil {
		t.Fatalf("fresh last-read algorithm observation missing: %v", limits)
	}
}

func TestCompressionRefusesUnknownOrUnchangeableBeforeMutation(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup func(*deviceFixture)
		mask  bool
	}{
		{"unknown selector", func(f *deviceFixture) { f.mode[46] = 0x80 }, false},
		{"contradictory state", func(f *deviceFixture) { f.mode[46] = 0 }, false},
		{"DCE with no capability", func(f *deviceFixture) { f.mode[18] = 0x80 }, false},
		{"unchangeable selector", func(*deviceFixture) {}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Unknown active policy cannot be normalized and later claimed as a safely restored original.
			f := newDeviceFixture()
			tc.setup(f)
			transport := newSerializedTransport(func(command Command) (Response, error) {
				response, err := f.execute(command)
				if tc.mask && command.CDB[0] == 0x5a && command.CDB[2] == 0x7f {
					command.Data[46] = 0
				}
				return response, err
			}, func() error { f.closed++; return nil })
			d, err := NewDevice(context.Background(), Identity{}, transport)
			if err != nil {
				t.Fatal(err)
			}
			_, err = d.Configure(context.Background(), &entity.TarMediaProfile{RecordMode: entity.TapeRecordMode_TAPE_RECORD_MODE_VARIABLE, RecordBytes: 512}, Format)
			if err == nil || f.selects != 0 || f.writes != 0 || f.motions != 0 || f.closed != 1 {
				t.Fatalf("preparation=%v selects=%d writes=%d motions=%d closed=%d", err, f.selects, f.writes, f.motions, f.closed)
			}
		})
	}
}

func TestCompressionReadThenAppendRetainsFirstCapturedControls(t *testing.T) {
	// Label/tail reads can enable DDE before the immutable append policy changes DCE and the selector.
	f := newDeviceFixture()
	f.mode[19] = 0
	d := fixtureDevice(t, f)
	read := &entity.TarMediaProfile{RecordMode: entity.TapeRecordMode_TAPE_RECORD_MODE_FIXED, RecordBytes: 512, HardwareCompression: true}
	if _, err := d.Configure(context.Background(), read, Read); err != nil {
		t.Fatal(err)
	}
	appendProfile := &entity.TarMediaProfile{RecordMode: entity.TapeRecordMode_TAPE_RECORD_MODE_FIXED, RecordBytes: 512}
	if _, err := d.Configure(context.Background(), appendProfile, Append); err != nil || f.mode[18]&0x80 != 0 || f.mode[19]&0x80 == 0 || f.mode[46] != 0 {
		t.Fatalf("append=%v mode=%x", err, f.mode)
	}
	if err := d.Cleanup(context.Background()); err != nil || f.mode[18]&0x80 == 0 || f.mode[19]&0x80 != 0 || f.mode[46] != 1 || f.selects != 3 {
		t.Fatalf("cleanup=%v mode=%x selects=%d", err, f.mode, f.selects)
	}
}

func TestCompressionReadbackFailureRestoresAndNeverWrites(t *testing.T) {
	// A settled selection with wrong algorithm readback fails preparation and still restores touched controls.
	f := newDeviceFixture()
	corrupted := false
	transport := newSerializedTransport(func(command Command) (Response, error) {
		response, err := f.execute(command)
		if command.CDB[0] == 0x5a && command.CDB[2] == 0x3f && f.selects == 1 && !corrupted {
			binary.BigEndian.PutUint32(command.Data[20:24], 0xff)
			corrupted = true
		}
		return response, err
	}, func() error { f.closed++; return nil })
	d, err := NewDevice(context.Background(), Identity{}, transport)
	if err != nil {
		t.Fatal(err)
	}
	_, err = d.Configure(context.Background(), &entity.TarMediaProfile{RecordMode: entity.TapeRecordMode_TAPE_RECORD_MODE_VARIABLE, RecordBytes: 512}, Format)
	if err == nil || errors.Is(err, ErrUncertain) || f.selects != 2 || f.writes != 0 || f.closed != 1 || f.mode[18]&0x80 == 0 || f.mode[46] != 1 || binary.BigEndian.Uint32(f.mode[20:24]) != 1 {
		t.Fatalf("failure=%v selects=%d writes=%d mode=%x", err, f.selects, f.writes, f.mode)
	}
}

func TestCompressionAlgorithmDriftCannotClaimVerifiedCleanup(t *testing.T) {
	for _, tc := range []struct {
		name              string
		operation         Operation
		fixed, decoderOff bool
	}{
		{"write policy", Format, false, false},
		{"record-only read", Read, false, false},
		{"decoder-only read", Read, true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Persistently changed CA is unavailable for cleanup regardless of which controls were selected.
			f := newDeviceFixture()
			if tc.decoderOff {
				f.mode[19] = 0
			}
			transport := newSerializedTransport(func(command Command) (Response, error) {
				response, err := f.execute(command)
				if command.CDB[0] == 0x5a && command.CDB[2] == 0x3f && f.selects == 1 {
					binary.BigEndian.PutUint32(command.Data[20:24], 0xff)
				}
				return response, err
			}, func() error { f.closed++; return nil })
			d, err := NewDevice(context.Background(), Identity{}, transport)
			if err != nil {
				t.Fatal(err)
			}
			mode := entity.TapeRecordMode_TAPE_RECORD_MODE_VARIABLE
			if tc.fixed {
				mode = entity.TapeRecordMode_TAPE_RECORD_MODE_FIXED
			}
			_, err = d.Configure(context.Background(), &entity.TarMediaProfile{RecordMode: mode, RecordBytes: 512, HardwareCompression: tc.operation == Read}, tc.operation)
			if !errors.Is(err, ErrUncertain) || f.selects != 1 || f.writes != 0 || f.closed != 1 || !errors.Is(d.Cleanup(context.Background()), ErrUncertain) {
				t.Fatalf("failure=%v selects=%d writes=%d closed=%d", err, f.selects, f.writes, f.closed)
			}
		})
	}
}
