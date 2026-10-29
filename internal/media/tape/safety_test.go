package tape

import (
	"context"
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/samuelncui/yatm/entity"
)

func TestVariableWriteCannotAcknowledgePartialRecord(t *testing.T) {
	// A partial variable transfer is not a shorter accepted physical record.
	response := Response{Completed: true, Status: 2, Residual: 1, Sense: fixedSense(0x70, 0, 0, 2, true, 0x40, 1)}
	calls := 0
	transport := newSerializedTransport(func(Command) (Response, error) { calls++; return response, nil }, func() error { return nil })
	defer transport.Close()
	records := Records{Transport: transport, Mode: RecordMode{MaximumBytes: 512, WarningCounts: true}, Timeout: time.Second}
	result, err := records.Write(context.Background(), make([]byte, 512))
	if !errors.Is(err, ErrUncertain) || result.AcceptedKnown || result.Bytes != 0 {
		t.Fatalf("partial record result=%+v, error=%v", result, err)
	}

	// Semantic uncertainty blocks the following command before it reaches the transport.
	if err := records.Sync(context.Background()); !errors.Is(err, ErrUncertain) || calls != 1 {
		t.Fatalf("partial record allowed sync: err=%v, calls=%d", err, calls)
	}
}

func TestCommandCancellationPreservesAcceptedRecord(t *testing.T) {
	// Cancellation after admission retains the accepted record when its command settles successfully.
	ctx, cancel := context.WithCancel(context.Background())
	transport := newSerializedTransport(func(Command) (Response, error) { cancel(); return Response{Completed: true}, nil }, func() error { return nil })
	defer transport.Close()
	records := Records{Transport: transport, Mode: RecordMode{MaximumBytes: 512}, Timeout: time.Second}
	result, err := records.Write(ctx, make([]byte, 512))
	if !errors.Is(err, context.Canceled) || !result.AcceptedKnown || result.Bytes != 512 {
		t.Fatalf("settled cancellation result=%+v, err=%v", result, err)
	}

	// Pre-command cancellation proves that zero records were physically accepted.
	result, err = records.Write(ctx, make([]byte, 512))
	if !errors.Is(err, context.Canceled) || !result.AcceptedKnown || result.Bytes != 0 {
		t.Fatalf("pre-command cancellation result=%+v, err=%v", result, err)
	}
}

func TestUnknownCompletionAndCursorBlockLaterCommands(t *testing.T) {
	for _, operation := range []string{"sync", "filemark", "rewind", "space", "locate", "read"} {
		t.Run(operation, func(t *testing.T) {
			// A real command failure is not a clean optional command rejection or a known cursor.
			calls := 0
			transport := newSerializedTransport(func(Command) (Response, error) {
				calls++
				return Response{Completed: true, Status: 2, Sense: fixedSense(0x70, 3, 0x11, 0, false, 0, 0)}, nil
			}, func() error { return nil })
			defer transport.Close()
			records := Records{Transport: transport, Mode: RecordMode{MaximumBytes: 512}, Timeout: time.Second}
			var err error
			switch operation {
			case "sync":
				err = records.Sync(context.Background())
			case "filemark":
				err = records.Filemarks(context.Background(), 1)
			case "rewind":
				err = records.Rewind(context.Background())
			case "space":
				err = records.Space(context.Background(), 1, 1)
			case "locate":
				err = records.Locate(context.Background(), 0, 123)
			case "read":
				_, err = records.Read(context.Background(), make([]byte, 512))
			}
			if !errors.Is(err, ErrUncertain) {
				t.Fatalf("physical failure=%v", err)
			}

			// No speculative navigation or completion follows an unknown outcome.
			if err := records.Rewind(context.Background()); !errors.Is(err, ErrUncertain) || calls != 1 {
				t.Fatalf("unknown outcome reused lane: err=%v, calls=%d", err, calls)
			}
		})
	}
}

func TestInspectionResponseBoundaries(t *testing.T) {
	// An INQUIRY response may advertise more bytes than a bounded allocation retrieves.
	report := newReport(Identity{})
	data := make([]byte, 36)
	data[0], data[4] = 1, 255
	copy(data[8:36], "OTHER   GENERIC MODEL   1234")
	if err := decodeInquiry(data, report); err != nil {
		t.Fatalf("bounded inquiry=%v", err)
	}

	// A zero maximum remains absent, while actual equality identifies a fixed-only envelope.
	for _, tc := range []struct {
		data     []byte
		maximum  uint32
		variable bool
	}{{[]byte{0, 0, 0, 0, 0, 1}, 0, false}, {[]byte{0, 0, 2, 0, 2, 0}, 512, false}} {
		report := newReport(Identity{})
		if err := decodeBlockLimits(tc.data, report, nil); err != nil {
			t.Fatal(err)
		}
		limits := feature(report, entity.DeviceFeatureKind_DEVICE_FEATURE_KIND_SEQUENTIAL_READ).GetRecords()
		if (limits.MaximumBytes == nil) != (tc.maximum == 0) || limits.GetVariable() != tc.variable {
			t.Fatalf("limits=%v", limits)
		}
		if limits.GetMaximumBytes() != tc.maximum {
			t.Fatalf("maximum=%v", limits.MaximumBytes)
		}
	}
}

func TestPositionValidityFlags(t *testing.T) {
	// Short overflow and location-unknown differ from long object/file unknown flags.
	for _, tc := range []struct {
		long         bool
		flags        byte
		object, file bool
	}{{false, 0, true, false}, {false, 2, false, false}, {false, 4, false, false},
		{true, 0, true, true}, {true, 4, false, true}, {true, 8, true, false}} {
		length := 20
		if tc.long {
			length = 32
		}
		data := make([]byte, length)
		data[0] = tc.flags | 0x40
		position, err := decodePosition(data, tc.long)
		if err != nil || position.ObjectKnown != tc.object || position.FileKnown != tc.file || !position.BeyondEarlyWarning {
			t.Fatalf("flags=%02x long=%t position=%+v err=%v", tc.flags, tc.long, position, err)
		}
	}
}

func TestPartialInspectionFailureOverridesEarlierOptionalReason(t *testing.T) {
	// An optional serial body failure cannot hide a later unknown physical outcome.
	transport := newSerializedTransport(func(command Command) (Response, error) {
		if command.CDB[0] == 0x12 && command.CDB[1] == 1 {
			return Response{Completed: true, Residual: int32(len(command.Data))}, nil
		}
		if command.CDB[0] == 0x34 {
			return Response{Completed: true, HostStatus: 3}, nil
		}
		return inspectionFixture(command)
	}, func() error { return nil })
	defer transport.Close()
	report, err := inspectTransport(context.Background(), Identity{}, transport)
	if !errors.Is(err, ErrUncertain) || report.Reason != entity.DeviceReason_DEVICE_REASON_OWNERSHIP_UNCERTAIN || report.ObservedAtNs == nil {
		t.Fatalf("report=%v, err=%v", report, err)
	}
	if feature(report, entity.DeviceFeatureKind_DEVICE_FEATURE_KIND_SEQUENTIAL_READ).Scope.GetModel() != "ULT3580-TD8" {
		t.Fatal("partial report lost observed drive scope")
	}
}

func TestHolderVisibilityRejectsHiddenOrUnreadableTables(t *testing.T) {
	// Process exit races leave no holder; hidden directories and broken fd observations stay unknown.
	root := t.TempDir()
	for _, path := range []string{filepath.Join(root, "1", "fd"), filepath.Join(root, "2")} {
		if err := os.MkdirAll(path, 0700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink("/dev/null", filepath.Join(root, "1", "fd", "0")); err != nil {
		t.Fatal(err)
	}
	if err := checkVisibility(context.Background(), root, ""); err != nil {
		t.Fatal(err)
	}

	// Even apparently clean fuser absence cannot authorize an invisible process table.
	if err := checkVisibility(context.Background(), root, "1 2 0:1 / "+root+" rw - proc proc rw,hidepid=2"); !errors.Is(err, ErrOwnershipUnknown) {
		t.Fatalf("hidden process table=%v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "1", "fd", "1"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	if err := checkVisibility(context.Background(), root, ""); !errors.Is(err, ErrOwnershipUnknown) {
		t.Fatalf("unreadable fd observation=%v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := checkVisibility(ctx, root, ""); !errors.Is(err, context.Canceled) {
		t.Fatalf("visibility cancellation=%v", err)
	}
}

func TestDescriptorSenseRejectsDuplicateEvidence(t *testing.T) {
	// Conflicting repeated information descriptors cannot choose an accepted count.
	data := make([]byte, 32)
	data[0], data[7] = 0x72, 24
	for _, start := range []int{8, 20} {
		data[start+1], data[start+2] = 10, 0x80
		binary.BigEndian.PutUint64(data[start+4:start+12], uint64(start))
	}
	if _, err := DecodeSense(data); !errors.Is(err, ErrMalformed) {
		t.Fatalf("duplicate descriptors=%v", err)
	}
}
