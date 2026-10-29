package tape

import (
	"context"
	"testing"

	"github.com/samuelncui/yatm/entity"
)

func TestDevicePreservesBufferedModeAndSpeed(t *testing.T) {
	// A record-mode change must preserve the whole non-WP header through preparation and cleanup.
	f := newDeviceFixture()
	f.mode[3] = 0x13
	d := fixtureDevice(t, f)
	_, err := d.Configure(context.Background(), &entity.TarMediaProfile{
		RecordMode: entity.TapeRecordMode_TAPE_RECORD_MODE_VARIABLE, RecordBytes: 512,
	}, Format)
	if err != nil || f.mode[3] != 0x13 || f.selects != 1 {
		t.Fatalf("preparation error=%v, mode=%02x, selects=%d", err, f.mode[3], f.selects)
	}

	// Restoring block length and compression must not reset the original nonzero speed.
	if err := d.Cleanup(context.Background()); err != nil || f.mode[3] != 0x13 || f.selects != 2 {
		t.Fatalf("cleanup error=%v, mode=%02x, selects=%d", err, f.mode[3], f.selects)
	}
}

func TestDeviceFailedFormatPreservesHeaderWithoutOtherModeChanges(t *testing.T) {
	// Matching record/compression controls leave partition restoration as the only cleanup mutation.
	f := newDeviceFixture()
	f.mode[3], f.mode[51], f.actualPartitions, f.failedFormat = 0x13, 1, 2, true
	d := fixtureDevice(t, f)
	_, err := d.Configure(context.Background(), &entity.TarMediaProfile{
		RecordMode: entity.TapeRecordMode_TAPE_RECORD_MODE_FIXED, RecordBytes: 512, HardwareCompression: true,
	}, Format)
	if err != nil || d.changed || f.selects != 0 {
		t.Fatalf("preparation error=%v, changed=%t, selects=%d", err, d.changed, f.selects)
	}

	// A rejected FORMAT still restores its pending partition controls with buffering and speed intact.
	if err := d.Initialize(context.Background()); err == nil || f.mode[3] != 0x13 || f.selects != 1 {
		t.Fatalf("initialization error=%v, mode=%02x, selects=%d", err, f.mode[3], f.selects)
	}
	if err := d.Cleanup(context.Background()); err != nil || f.mode[3] != 0x13 || f.mode[51] != 1 || f.selects != 2 {
		t.Fatalf("cleanup error=%v, mode=%02x, additional=%d, selects=%d", err, f.mode[3], f.mode[51], f.selects)
	}
}

func TestDevicePendingPartitionCountNeedsPhysicalConfirmation(t *testing.T) {
	for _, failure := range []string{"", "controls", "physical count"} {
		t.Run(failure, func(t *testing.T) {
			// A pending count may stay old; control bits and the committed physical layout remain authoritative.
			f := newDeviceFixture()
			f.mode[51], f.actualPartitions, f.deferredPartitionCount = 1, 2, true
			f.wrongPartitionControls, f.wrongFormattedCount = failure == "controls", failure == "physical count"
			d := fixtureDevice(t, f)
			_, err := d.Configure(context.Background(), &entity.TarMediaProfile{
				RecordMode: entity.TapeRecordMode_TAPE_RECORD_MODE_VARIABLE, RecordBytes: 512,
			}, Format)
			if err != nil {
				t.Fatal(err)
			}

			// Accept only a successful FORMAT whose actual partition list confirms the requested single partition.
			err = d.Initialize(context.Background())
			if (err == nil) != (failure == "") || failure == "controls" && f.formats != 0 {
				t.Fatalf("failure=%q, initialization error=%v, formats=%d", failure, err, f.formats)
			}
			if failure == "" && (f.actualPartitions != 1 || d.Report().GetTape().GetPartitionCount() != 1) {
				t.Fatal("successful initialization did not confirm one physical partition")
			}
			f.wrongPartitionControls = false
			if err := d.Cleanup(context.Background()); err != nil {
				t.Fatal(err)
			}
		})
	}
}
