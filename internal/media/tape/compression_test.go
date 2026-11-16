package tape

import (
	"context"
	"encoding/binary"
	"errors"
	"testing"

	"github.com/samuelncui/yatm/entity"
)

func TestTarWritesForceCompressionOffAndRestore(t *testing.T) {
	for _, algorithm := range []uint32{1, 0xff} {
		for _, enabled := range []bool{false, true} {
			// Actual standard IDs remain opaque; DCE and the legacy selector are off for every tar write.
			f := newDeviceFixture()
			binary.BigEndian.PutUint32(f.mode[20:24], algorithm)
			binary.BigEndian.PutUint32(f.mode[24:28], algorithm)
			if !enabled {
				f.mode[18], f.mode[46] = 0x40, 0
			}
			var writing []bool
			transport := newSerializedTransport(func(command Command) (Response, error) {
				if command.CDB[0] == 0x0a {
					writing = append(writing, f.mode[18]&0x80 != 0 || f.mode[46] != 0)
				}
				return f.execute(command)
			}, func() error { f.closed++; return nil })
			d, err := NewDevice(context.Background(), Identity{}, transport)
			if err != nil {
				t.Fatal(err)
			}
			records, err := d.Configure(context.Background(), &entity.TarMediaProfile{RecordMode: entity.TapeRecordMode_TAPE_RECORD_MODE_VARIABLE, RecordBytes: 512}, Append)
			if err != nil || f.mode[18]&0x80 != 0 || f.mode[46] != 0 || binary.BigEndian.Uint32(f.mode[20:24]) != algorithm || f.writes != 0 || f.motions != 0 {
				t.Fatalf("algorithm=%x enabled=%t err=%v mode=%x", algorithm, enabled, err, f.mode)
			}
			if _, err := records.Write(context.Background(), make([]byte, 512)); err != nil || len(writing) != 1 || writing[0] {
				t.Fatalf("write=%v compression during write=%v", err, writing)
			}

			// Cleanup restores the captured compression controls and preserves fresh unrelated device configuration.
			f.mode[35], f.mode[38], f.mode[42] = 7, 0x12, 0x18
			if err := d.Cleanup(context.Background()); err != nil || (f.mode[18]&0x80 != 0) != enabled || (f.mode[46] == 1) != enabled || f.mode[35] != 7 || f.mode[38] != 0x12 || f.mode[42] != 0x18 || f.selects != 2 || f.closed != 1 {
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
		_, err = d.Configure(context.Background(), &entity.TarMediaProfile{RecordMode: entity.TapeRecordMode_TAPE_RECORD_MODE_VARIABLE, RecordBytes: 512}, Read)
		err = errors.Join(err, d.Cleanup(context.Background())) // The caller owns release.
		if !errors.Is(err, ErrUncertain) || f.selects != 1 || f.writes != 0 || f.closed != 1 {
			t.Fatalf("selector=%t failure=%v selects=%d writes=%d closed=%d", selector, err, f.selects, f.writes, f.closed)
		}
	}
}

func TestReadRecordModeChangeDoesNotSelectWriteControls(t *testing.T) {
	// READ prepares records only and leaves write controls alone, even when they contradict.
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
	_, err = d.Configure(context.Background(), &entity.TarMediaProfile{RecordMode: entity.TapeRecordMode_TAPE_RECORD_MODE_VARIABLE, RecordBytes: 512}, Read)
	if err != nil || f.mode[18] != 0x80 || f.mode[46] != 0x80 {
		t.Fatalf("read=%v mode=%x", err, f.mode)
	}
	if err := d.Cleanup(context.Background()); err != nil || selections != 2 || f.mode[18] != 0x80 || f.mode[46] != 0x80 {
		t.Fatalf("cleanup=%v selections=%d mode=%x", err, selections, f.mode)
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
			err = errors.Join(err, d.Cleanup(context.Background())) // The caller owns release.
			if err == nil || f.selects != 0 || f.writes != 0 || f.motions != 0 || f.closed != 1 {
				t.Fatalf("preparation=%v selects=%d writes=%d motions=%d closed=%d", err, f.selects, f.writes, f.motions, f.closed)
			}
		})
	}
}

func TestCompressionReadThenAppendRetainsFirstCapturedControls(t *testing.T) {
	// Label/tail reads prepare records before the APPEND write turns DCE and the selector off.
	f := newDeviceFixture()
	d := fixtureDevice(t, f)
	read := &entity.TarMediaProfile{RecordMode: entity.TapeRecordMode_TAPE_RECORD_MODE_FIXED, RecordBytes: 512}
	if _, err := d.Configure(context.Background(), read, Read); err != nil || f.mode[18]&0x80 == 0 || f.mode[46] != 1 {
		t.Fatalf("read=%v mode=%x", err, f.mode)
	}
	if _, err := d.Configure(context.Background(), read, Append); err != nil || f.mode[18]&0x80 != 0 || f.mode[19]&0x80 == 0 || f.mode[46] != 0 {
		t.Fatalf("append=%v mode=%x", err, f.mode)
	}
	if err := d.Cleanup(context.Background()); err != nil || f.mode[18]&0x80 == 0 || f.mode[19]&0x80 == 0 || f.mode[46] != 1 {
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
	err = errors.Join(err, d.Cleanup(context.Background())) // The caller owns release.
	if err == nil || errors.Is(err, ErrUncertain) || f.selects != 2 || f.writes != 0 || f.closed != 1 || f.mode[18]&0x80 == 0 || f.mode[46] != 1 || binary.BigEndian.Uint32(f.mode[20:24]) != 1 {
		t.Fatalf("failure=%v selects=%d writes=%d mode=%x", err, f.selects, f.writes, f.mode)
	}
}

func TestCompressionAlgorithmDriftCannotClaimVerifiedCleanup(t *testing.T) {
	for _, tc := range []struct {
		name      string
		operation Operation
	}{
		{"write policy", Format},
		{"record-only read", Read},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Persistently changed CA is unavailable for cleanup regardless of which controls were selected.
			f := newDeviceFixture()
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
			_, err = d.Configure(context.Background(), &entity.TarMediaProfile{RecordMode: entity.TapeRecordMode_TAPE_RECORD_MODE_VARIABLE, RecordBytes: 512}, tc.operation)
			err = errors.Join(err, d.Cleanup(context.Background())) // The caller owns release.
			if !errors.Is(err, ErrUncertain) || f.selects != 1 || f.writes != 0 || f.closed != 1 || !errors.Is(d.Cleanup(context.Background()), ErrUncertain) {
				t.Fatalf("failure=%v selects=%d writes=%d closed=%d", err, f.selects, f.writes, f.closed)
			}
		})
	}
}

func TestCompressionReadinessLossBlocksWritePreparation(t *testing.T) {
	for _, noMedium := range []bool{false, true} {
		// A readiness rejection of the changeability query stops preparation before any mode selection.
		f := newDeviceFixture()
		transport := newSerializedTransport(func(command Command) (Response, error) {
			if command.CDB[0] == 0x5a && command.CDB[2] == 0x7f {
				asc := byte(4)
				if noMedium {
					asc = 0x3a
				}
				return Response{Completed: true, Status: 2, Sense: fixedSense(0x70, 2, asc, 0, false, 0, 0)}, nil
			}
			return f.execute(command)
		}, func() error { f.closed++; return nil })
		d, err := NewDevice(context.Background(), Identity{}, transport)
		if err != nil {
			t.Fatal(err)
		}
		_, err = d.Configure(context.Background(), &entity.TarMediaProfile{RecordMode: entity.TapeRecordMode_TAPE_RECORD_MODE_VARIABLE, RecordBytes: 512}, Format)
		err = errors.Join(err, d.Cleanup(context.Background())) // The caller owns release.
		if err == nil || f.selects != 0 || f.writes != 0 || f.closed != 1 {
			t.Fatalf("noMedium=%t preparation=%v selects=%d writes=%d", noMedium, err, f.selects, f.writes)
		}
	}
}
