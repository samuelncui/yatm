package tape

import (
	"context"
	"encoding/binary"
	"testing"

	"github.com/samuelncui/yatm/entity"
)

func TestCompressionReportKeepsCurrentAlgorithmsAndChangeabilityDistinct(t *testing.T) {
	// Current page values and PC1 bit masks have independent meanings, including observed algorithm zero.
	f := newDeviceFixture()
	f.mode[18], f.mode[46] = 0x40, 0
	binary.BigEndian.PutUint32(f.mode[20:24], 0xff)
	binary.BigEndian.PutUint32(f.mode[24:28], 0)
	transport := newSerializedTransport(func(command Command) (Response, error) {
		response, err := f.execute(command)
		if command.CDB[0] == 0x5a && command.CDB[2] == 0x7f {
			command.Data[19] = 0
			binary.BigEndian.PutUint32(command.Data[20:24], 0)
			binary.BigEndian.PutUint32(command.Data[24:28], 0xffffffff)
			// A missing selector mask does not turn the separately observed DCE mask into false.
			binary.BigEndian.PutUint16(command.Data[:2], 30)
		}
		return response, err
	}, func() error { return nil })
	defer transport.Close()
	report, err := inspectTransport(context.Background(), Identity{}, transport)
	write := feature(report, entity.DeviceFeatureKind_DEVICE_FEATURE_KIND_HARDWARE_COMPRESSION)
	read := feature(report, entity.DeviceFeatureKind_DEVICE_FEATURE_KIND_HARDWARE_DECOMPRESSION)
	if err != nil || report.Reason != 0 || write.GetCompression().GetCurrentAlgorithm() != 0xff || write.GetCompression().EnableChangeable == nil || !write.GetCompression().GetEnableChangeable() || write.Availability != entity.DeviceFeatureAvailability_DEVICE_FEATURE_AVAILABILITY_UNKNOWN {
		t.Fatalf("write=%v overall=%v err=%v", write, report.Reason, err)
	}
	if read.GetCompression().CurrentAlgorithm == nil || read.GetCompression().GetCurrentAlgorithm() != 0 || read.GetCompression().EnableChangeable == nil || read.GetCompression().GetEnableChangeable() || read.Availability != entity.DeviceFeatureAvailability_DEVICE_FEATURE_AVAILABILITY_READY {
		t.Fatalf("read=%v", read)
	}
	if write.Validation != entity.DeviceFeatureValidation_DEVICE_FEATURE_VALIDATION_UNVALIDATED || read.Validation != entity.DeviceFeatureValidation_DEVICE_FEATURE_VALIDATION_UNVALIDATED {
		t.Fatal("software observation fabricated physical qualification")
	}
}

func TestCompressionOptionalMaskFailuresDoNotBlockCapacityOrBaseline(t *testing.T) {
	for _, malformed := range []bool{false, true} {
		// A fresh optional mask failure leaves current controls observable and changeability absent.
		queries := 0
		transport := newSerializedTransport(func(command Command) (Response, error) {
			if command.CDB[0] == 0x5a && command.CDB[2] == 0x7f {
				if malformed {
					return Response{Completed: true, Residual: int32(len(command.Data) - 1)}, nil
				}
				return Response{Completed: true, Status: 2, Sense: fixedSense(0x70, 5, 0x24, 0, false, 0, 0)}, nil
			}
			if command.CDB[0] == 0x4d {
				queries++
			}
			return inspectionFixture(command)
		}, func() error { return nil })
		defer transport.Close()
		report, err := inspectTransport(context.Background(), Identity{}, transport)
		for _, kind := range []entity.DeviceFeatureKind{entity.DeviceFeatureKind_DEVICE_FEATURE_KIND_HARDWARE_COMPRESSION, entity.DeviceFeatureKind_DEVICE_FEATURE_KIND_HARDWARE_DECOMPRESSION} {
			f := feature(report, kind)
			if err != nil || report.Reason != 0 || queries != 1 || f.Support != entity.DeviceFeatureSupport_DEVICE_FEATURE_SUPPORT_SUPPORTED || f.GetCompression() == nil || f.GetCompression().EnableChangeable != nil || f.GetCompression().GetCurrentAlgorithm() != 1 {
				t.Fatalf("malformed=%t feature=%v overall=%v err=%v queries=%d", malformed, f, report.Reason, err, queries)
			}
		}
	}
}

func TestCompressionDecoderDoesNotRequireWriteCapability(t *testing.T) {
	// DCC zero conclusively rejects write compression while an enabled independent DDE remains available.
	f := newDeviceFixture()
	f.mode[18], f.mode[46] = 0, 0
	d := fixtureDevice(t, f)
	write := feature(d.Report(), entity.DeviceFeatureKind_DEVICE_FEATURE_KIND_HARDWARE_COMPRESSION)
	read := feature(d.Report(), entity.DeviceFeatureKind_DEVICE_FEATURE_KIND_HARDWARE_DECOMPRESSION)
	if write.Support != entity.DeviceFeatureSupport_DEVICE_FEATURE_SUPPORT_UNSUPPORTED || read.Support != entity.DeviceFeatureSupport_DEVICE_FEATURE_SUPPORT_SUPPORTED || read.Availability != entity.DeviceFeatureAvailability_DEVICE_FEATURE_AVAILABILITY_READY {
		t.Fatalf("write=%v read=%v", write, read)
	}
	_, err := d.Configure(context.Background(), &entity.TarMediaProfile{RecordMode: entity.TapeRecordMode_TAPE_RECORD_MODE_FIXED, RecordBytes: 512, HardwareCompression: true}, Read)
	if err != nil || f.selects != 0 {
		t.Fatalf("read=%v selects=%d", err, f.selects)
	}
	if err := d.Cleanup(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestCompressionReadinessLossBlocksDirectPreparation(t *testing.T) {
	for _, noMedium := range []bool{false, true} {
		// A late actual readiness rejection invalidates the medium, unlike optional unsupported/malformed masks.
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
		report := d.Report()
		want := entity.DeviceReason_DEVICE_REASON_NOT_READY
		if noMedium {
			want = entity.DeviceReason_DEVICE_REASON_NO_MEDIUM
		}
		_, err = d.Configure(context.Background(), &entity.TarMediaProfile{RecordMode: entity.TapeRecordMode_TAPE_RECORD_MODE_VARIABLE, RecordBytes: 512}, Format)
		failing := feature(report, entity.DeviceFeatureKind_DEVICE_FEATURE_KIND_HARDWARE_COMPRESSION)
		if err == nil || report.Reason != want || report.GetTape().GetMediumPresent() == noMedium || failing.Availability != entity.DeviceFeatureAvailability_DEVICE_FEATURE_AVAILABILITY_UNAVAILABLE || f.selects != 0 || f.writes != 0 {
			t.Fatalf("preparation=%v report=%v selects=%d writes=%d", err, report, f.selects, f.writes)
		}
	}
}

func TestCompressionCurrentModeRefreshDoesNotUseChangeableValues(t *testing.T) {
	// An unchanged preparation still observes fresh current IDs; masks never replace those IDs.
	f := newDeviceFixture()
	d := fixtureDevice(t, f)
	binary.BigEndian.PutUint32(f.mode[20:24], 0xff)
	binary.BigEndian.PutUint32(f.mode[24:28], 0)
	_, err := d.Configure(context.Background(), &entity.TarMediaProfile{RecordMode: entity.TapeRecordMode_TAPE_RECORD_MODE_FIXED, RecordBytes: 512, HardwareCompression: true}, Read)
	if err != nil || f.selects != 0 {
		t.Fatalf("read=%v selects=%d", err, f.selects)
	}
	binary.BigEndian.PutUint32(f.mode[20:24], 7)
	binary.BigEndian.PutUint32(f.mode[24:28], 8)
	if _, _, err := d.readMode(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	write := feature(d.Report(), entity.DeviceFeatureKind_DEVICE_FEATURE_KIND_HARDWARE_COMPRESSION)
	read := feature(d.Report(), entity.DeviceFeatureKind_DEVICE_FEATURE_KIND_HARDWARE_DECOMPRESSION)
	if write.GetCompression().GetCurrentAlgorithm() != 0xff || read.GetCompression().CurrentAlgorithm == nil || read.GetCompression().GetCurrentAlgorithm() != 0 || write.GetCompression().EnableChangeable == nil || read.GetCompression().EnableChangeable == nil || write.Support != entity.DeviceFeatureSupport_DEVICE_FEATURE_SUPPORT_SUPPORTED || read.Validation != entity.DeviceFeatureValidation_DEVICE_FEATURE_VALIDATION_UNVALIDATED {
		t.Fatalf("current IDs or prior qualification changed: write=%v read=%v", write, read)
	}
	if err := d.Cleanup(context.Background()); err != nil {
		t.Fatal(err)
	}
}
