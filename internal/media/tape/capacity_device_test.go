package tape

import (
	"context"
	"testing"

	"github.com/samuelncui/yatm/entity"
)

func TestCapacityReadinessLossPreventsDirectDevicePreparation(t *testing.T) {
	for _, noMedium := range []bool{false, true} {
		// The direct device path must consume the readiness rejection, even though earlier TUR succeeded.
		fixture := newDeviceFixture()
		transport := newSerializedTransport(func(command Command) (Response, error) {
			if command.CDB[0] == 0x4d {
				asc := byte(4)
				if noMedium {
					asc = 0x3a
				}
				return Response{Completed: true, Status: 2, Sense: fixedSense(0x70, 2, asc, 0, false, 0, 0)}, nil
			}
			return fixture.execute(command)
		}, func() error { fixture.closed++; return nil })
		device, err := NewDevice(context.Background(), Identity{}, transport)
		if err != nil {
			t.Fatal(err)
		}
		_, err = device.Configure(context.Background(), &entity.TarMediaProfile{RecordMode: entity.TapeRecordMode_TAPE_RECORD_MODE_VARIABLE, RecordBytes: 512}, Format)
		if err == nil || fixture.selects != 0 || fixture.formats != 0 || fixture.motions != 0 || fixture.writes != 0 || fixture.closed != 1 {
			t.Fatalf("readiness loss admitted mutation: err=%v fixture=%+v", err, fixture)
		}
	}
}

func TestPreparationCapacityReadinessLossIsFatalButOptionalAbsenceIsNot(t *testing.T) {
	for _, unsupported := range []bool{false, true} {
		// After baseline configuration, a disappearing/not-ready cartridge invalidates preparation.
		fixture := newDeviceFixture()
		fail := false
		transport := newSerializedTransport(func(command Command) (Response, error) {
			if fail && command.CDB[0] == 0x4d {
				key, asc := byte(2), byte(4)
				if unsupported {
					key, asc = 5, 0x20
				}
				return Response{Completed: true, Status: 2, Sense: fixedSense(0x70, key, asc, 0, false, 0, 0)}, nil
			}
			return fixture.execute(command)
		}, func() error { return nil })
		device, err := NewDevice(context.Background(), Identity{}, transport)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := device.Configure(context.Background(), &entity.TarMediaProfile{RecordMode: entity.TapeRecordMode_TAPE_RECORD_MODE_FIXED, RecordBytes: 512}, Read); err != nil {
			t.Fatal(err)
		}

		// Optional unsupported counters retain unknown initial capacity; actual not-ready returns its cause.
		fail = true
		capacity, err := device.Capacity(context.Background())
		if unsupported {
			if err != nil || capacity == nil || capacity.Initial != nil {
				t.Fatalf("optional capacity absence invalidated preparation: capacity=%v err=%v", capacity, err)
			}
		} else if capacity != nil || Reason(err) != entity.DeviceReason_DEVICE_REASON_NOT_READY {
			t.Fatalf("not-ready silently continued: capacity=%v err=%v", capacity, err)
		}
		if err := device.Cleanup(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
}
