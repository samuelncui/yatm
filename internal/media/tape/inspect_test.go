package tape

import (
	"context"
	"encoding/binary"
	"errors"
	"testing"

	"github.com/samuelncui/yatm/entity"
)

func inspectionFixture(command Command) (Response, error) {
	// Synthetic bounded SSC/SPC responses exercise report projection without a real drive.
	var data []byte
	switch command.CDB[0] {
	case 0x12:
		if command.CDB[1] == 1 {
			data = []byte{1, 0x80, 0, 3, 'S', 'N', '1'}
		} else {
			data = make([]byte, 96)
			data[0], data[4] = 1, 91
			copy(data[8:16], "IBM     ")
			copy(data[16:32], "ULT3580-TD8      ")
			copy(data[32:36], "ABCD")
		}
	case 0:
	case 5:
		data = []byte{0, 0x08, 0, 0, 0, 1}
	case 0x5a:
		data = make([]byte, 56)
		binary.BigEndian.PutUint16(data[:2], 54)
		data[3], data[7], data[8] = 0x90, 8, 0x5e
		data[16], data[17], data[18], data[19] = 0x0f, 14, 0xc0, 0x80
		binary.BigEndian.PutUint32(data[20:24], 1)
		binary.BigEndian.PutUint32(data[24:28], 1)
		data[32], data[33], data[34], data[35] = 0x11, 6, 3, 1
		data[40], data[41] = 0x10, 14
		data[54] = 1
		if command.CDB[2] == 0x7f {
			data[18], data[19], data[54] = 0x80, 0xe0, 1
		}
	case 0x34:
		data = make([]byte, len(command.Data))
		if command.CDB[1] == 6 {
			binary.BigEndian.PutUint64(data[8:16], 123)
			binary.BigEndian.PutUint64(data[16:24], 4)
		} else {
			binary.BigEndian.PutUint32(data[4:8], 123)
		}
	case 0xa2:
		if command.CDB[1] == 0 {
			data = []byte{0, 0, 0, 0, 0, 0, 0, 1, 0x20}
			break
		}
		data = make([]byte, 24)
		data[1], data[3], data[12] = 0x20, 20, 0x10
	case 0xa3:
		length := 10
		if command.CDB[3] == 0x92 {
			length = 16
		}
		data = make([]byte, 4+length)
		data[1], data[3], data[4] = 3, byte(length), command.CDB[3]
		for i := 5; i < len(data); i++ {
			data[i] = 0xff
		}
		if command.CDB[3] == 0x34 {
			data[5] = command.CDB[5]
		}
	case 0x4d:
		data = []byte{0x0c, 0, 0, 16, 0, 4, 0x60, 4, 0, 0, 0, 100, 0, 5, 0x60, 4, 0, 0, 0, 200}
	default:
		return Response{}, errors.New("unexpected query")
	}
	copy(command.Data, data)
	return Response{Completed: true, Residual: int32(len(command.Data) - len(data))}, nil
}

type fixedTransferTransport struct {
	*serializedTransport
	limit int
}

func (t *fixedTransferTransport) TransferLimit() int { return t.limit }

func TestInspectionDoesNotAdvertiseGrowableReservationAsMaximum(t *testing.T) {
	for _, growable := range []bool{false, true} {
		// Both descriptors currently reserve 32 KiB; only an immutable bound establishes a maximum.
		serialized := newSerializedTransport(inspectionFixture, func() error { return nil })
		var transport Transport = &fixedTransferTransport{serializedTransport: serialized, limit: 32 << 10}
		if growable {
			transport = &sgTransport{serializedTransport: serialized, limit: 32 << 10}
		}
		defer transport.Close()
		report, err := inspectTransport(context.Background(), Identity{Configured: "fixture"}, transport)
		if err != nil {
			t.Fatal(err)
		}
		for _, kind := range []entity.DeviceFeatureKind{entity.DeviceFeatureKind_DEVICE_FEATURE_KIND_SEQUENTIAL_READ, entity.DeviceFeatureKind_DEVICE_FEATURE_KIND_SEQUENTIAL_WRITE} {
			limits := feature(report, kind).GetRecords()
			if limits.GetMaximumBytes() != 512<<10 || (limits.TransportMaximumBytes == nil) != growable || !growable && limits.GetTransportMaximumBytes() != 32<<10 {
				t.Fatalf("growable=%t limits=%v", growable, limits)
			}
		}
	}
}

func TestInspectionUsesOnlyPassiveBoundedQueriesAndUnvalidatedFacts(t *testing.T) {
	// Every command is checked at the production read-only allowlist before fake execution.
	var calls int
	transport := newSerializedTransport(func(command Command) (Response, error) {
		calls++
		if !passiveCommand(command) || len(command.Data) > MaxQueryBytes {
			t.Fatalf("unsafe inspection CDB=%x", command.CDB)
		}
		return inspectionFixture(command)
	}, func() error { return nil })
	defer transport.Close()
	report, err := inspectTransport(context.Background(), Identity{Configured: "/dev/nst0", Key: "/sys/device"}, transport)
	if err != nil || calls != 15 || report.GetVendor() != "IBM" || report.GetSerialNumber() != "SN1" {
		t.Fatalf("report=%v err=%v calls=%d", report, err, calls)
	}
	tape := report.GetTape()
	if !tape.GetMediumPresent() || !tape.GetWriteProtected() || tape.GetPartitionCount() != 2 || !tape.GetCompressionEnabled() || !tape.GetDecompressionEnabled() {
		t.Fatalf("mode facts=%v", tape)
	}
	for _, f := range tape.Features {
		if f.Validation != entity.DeviceFeatureValidation_DEVICE_FEATURE_VALIDATION_UNVALIDATED {
			t.Fatalf("passive query certified hardware: %v", f)
		}
		if f.Feature == entity.DeviceFeatureKind_DEVICE_FEATURE_KIND_LOCATE && (f.Support != entity.DeviceFeatureSupport_DEVICE_FEATURE_SUPPORT_SUPPORTED || f.GetNavigation().GetAddressBits() != 64) {
			t.Fatal("reported navigation pair is missing")
		}
	}
	limits := feature(report, entity.DeviceFeatureKind_DEVICE_FEATURE_KIND_SEQUENTIAL_READ).GetRecords()
	if limits.GetMaximumBytes() != 512<<10 || !limits.GetFixed() || !limits.GetVariable() {
		t.Fatalf("record limits=%v", limits)
	}
}

func TestInspectionNoMediumKeepsDriveFactsAndOptionalFailuresAreUnknown(t *testing.T) {
	for _, malformed := range []bool{false, true} {
		// Missing optional metadata does not revoke a completed identity/readiness observation.
		transport := newSerializedTransport(func(command Command) (Response, error) {
			if command.CDB[0] == 0 {
				return Response{Completed: true, Status: 2, Sense: fixedSense(0x70, 2, 0x3a, 0, false, 0, 0)}, nil
			}
			if command.CDB[0] == 5 || command.CDB[0] == 0x5a || command.CDB[0] == 0x34 {
				if malformed {
					clear(command.Data)
					return Response{Completed: true, Residual: int32(len(command.Data) - 1)}, nil
				}
				return Response{Completed: true, Status: 2, Sense: fixedSense(0x70, 5, 0x20, 0, false, 0, 0)}, nil
			}
			return inspectionFixture(command)
		}, func() error { return nil })
		report, err := inspectTransport(context.Background(), Identity{Configured: "drive"}, transport)
		if err != nil || report.GetModel() != "ULT3580-TD8" || report.GetTape().MediumPresent == nil || report.GetTape().GetMediumPresent() {
			t.Fatalf("report=%v err=%v", report, err)
		}
		if report.Reason != entity.DeviceReason_DEVICE_REASON_NO_MEDIUM {
			t.Fatalf("no-medium reason=%v", report.Reason)
		}
		if report.GetTape().DensityCode != nil || report.GetTape().WriteProtected != nil || report.GetTape().PartitionCount != nil {
			t.Fatalf("no-medium report invented cartridge facts=%v", report.GetTape())
		}
		if limits := feature(report, entity.DeviceFeatureKind_DEVICE_FEATURE_KIND_SEQUENTIAL_WRITE).GetRecords(); limits != nil {
			t.Fatalf("optional failure invented limits=%v", limits)
		}
		if _, err := transport.Execute(context.Background(), Command{CDB: []byte{0, 0, 0, 0, 0, 0}, Direction: NoData, Timeout: 1e9}); err != nil {
			t.Fatalf("optional body/rejection poisoned transport: %v", err)
		}
		_ = transport.Close()
	}
}

func TestInspectionStopsAfterUnknownTransportOutcome(t *testing.T) {
	// An issued command whose completion is unknown ends all later inspection commands.
	calls := 0
	transport := newSerializedTransport(func(Command) (Response, error) { calls++; return Response{Completed: true, HostStatus: 3}, nil }, func() error { return nil })
	defer transport.Close()
	report, err := inspectTransport(context.Background(), Identity{Configured: "drive"}, transport)
	if report == nil || !errors.Is(err, ErrUncertain) || calls != 1 || report.Reason != entity.DeviceReason_DEVICE_REASON_OWNERSHIP_UNCERTAIN {
		t.Fatalf("report=%v error=%v calls=%d", report, err, calls)
	}
}

func TestPassiveAllowlistRejectsMotionMutationAndKeys(t *testing.T) {
	for _, opcode := range []byte{1, 4, 8, 0x0a, 0x10, 0x11, 0x15, 0x1b, 0x2b, 0x55, 0x92, 0xb5} {
		// Changing transfer direction cannot smuggle an active command into a passive SG open.
		if passiveCommand(Command{CDB: []byte{opcode, 0, 0, 0, 0, 0}, Direction: FromDevice, Data: make([]byte, 6)}) {
			t.Fatalf("active opcode allowed=%02x", opcode)
		}
	}
	if passiveCommand(Command{}) {
		t.Fatal("empty query accepted")
	}
}
