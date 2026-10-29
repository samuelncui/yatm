package tape

import (
	"context"
	"encoding/binary"
	"errors"
	"math"
	"testing"

	"github.com/samuelncui/yatm/entity"
)

// navigationFixtureDevice isolates command-form tests from the separately tested Configure envelope.
func navigationFixtureDevice(transport Transport) (*Device, error) {
	device, err := NewDevice(context.Background(), Identity{Configured: "fixture"}, transport)
	if device != nil {
		device.operation = Read
	}
	return device, err
}

func TestNavigationSelectsReportedPairBeforeMotion(t *testing.T) {
	for _, short := range []bool{false, true} {
		// Standard form usage proves the complete address field, while partition zero needs no mask bits.
		var calls, motions int
		var located []byte
		transport := newSerializedTransport(func(command Command) (Response, error) {
			calls++
			if command.CDB[0] == 0x92 || command.CDB[0] == 0x2b {
				motions++
				located = append([]byte(nil), command.CDB...)
				return Response{Completed: true}, nil
			}
			if !passiveCommand(command) {
				t.Fatalf("active inspection command: %x", command.CDB)
			}
			if short && command.CDB[0] == 0xa3 && command.CDB[3] == 0x92 {
				command.Data[1] = 1
				return Response{Completed: true, Residual: int32(len(command.Data) - 2)}, nil
			}
			response, err := inspectionFixture(command)
			if command.CDB[0] == 0xa3 && command.CDB[3] == 0x92 {
				command.Data[7] = 0 // LOCATE(16) partition field is not a required nonzero value.
			}
			if command.CDB[0] == 0xa3 && command.CDB[3] == 0x2b {
				command.Data[12] = 0 // LOCATE(10) likewise needs only partition zero.
			}
			return response, err
		}, func() error { return nil })
		defer transport.Close()
		device, err := navigationFixtureDevice(transport)
		if err != nil || motions != 0 {
			t.Fatalf("inspection=%v motions=%d", err, motions)
		}

		// Returning the selected navigator reuses observations instead of issuing a second probe.
		before := calls
		navigator, err := device.Navigation(context.Background())
		bits, object, opcode := uint32(64), uint64(math.MaxUint64), byte(0x92)
		if short {
			bits, object, opcode = 32, math.MaxUint32, 0x2b
		}
		if err != nil || navigator == nil || navigator.AddressBits() != bits || calls != before {
			t.Fatalf("navigator=%v err=%v calls=%d/%d", navigator, err, calls, before)
		}
		for _, kind := range []entity.DeviceFeatureKind{entity.DeviceFeatureKind_DEVICE_FEATURE_KIND_READ_POSITION, entity.DeviceFeatureKind_DEVICE_FEATURE_KIND_LOCATE} {
			f := feature(device.Report(), kind)
			if f.Support != entity.DeviceFeatureSupport_DEVICE_FEATURE_SUPPORT_SUPPORTED || f.Validation != entity.DeviceFeatureValidation_DEVICE_FEATURE_VALIDATION_UNVALIDATED ||
				f.Availability != entity.DeviceFeatureAvailability_DEVICE_FEATURE_AVAILABILITY_READY || f.GetNavigation().GetAddressBits() != bits {
				t.Fatalf("physical qualification invented or pair missing: %v", f)
			}
		}

		// Width/partition failures issue no command; the chosen complete-width form is sent exactly once.
		if short {
			if err := navigator.Locate(context.Background(), 0, math.MaxUint32+1); err == nil || calls != before {
				t.Fatal("32-bit overflow was issued or narrowed")
			}
		}
		for _, partition := range []uint32{1, 256, math.MaxUint32} {
			if err := navigator.Locate(context.Background(), partition, 1); !errors.Is(err, ErrUnsupported) || calls != before {
				t.Fatalf("partition=%d err=%v calls=%d", partition, err, calls)
			}
		}
		if err := navigator.Locate(context.Background(), 0, object); err != nil || motions != 1 || located[0] != opcode || located[1] != 2 {
			t.Fatalf("locate=%x err=%v motions=%d", located, err, motions)
		}
		if short && binary.BigEndian.Uint32(located[3:7]) != math.MaxUint32 || !short && binary.BigEndian.Uint64(located[4:12]) != math.MaxUint64 {
			t.Fatalf("address narrowed: %x", located)
		}
	}
}

func TestNavigationReportingKeepsUnsupportedUnknownAndMalformedDistinct(t *testing.T) {
	for _, tc := range []struct {
		name    string
		code    byte
		reject  bool
		support entity.DeviceFeatureSupport
		reason  entity.DeviceReason
	}{
		{"unsupported forms", 1, false, entity.DeviceFeatureSupport_DEVICE_FEATURE_SUPPORT_UNSUPPORTED, entity.DeviceReason_DEVICE_REASON_UNSUPPORTED},
		{"unknown report", 0, false, entity.DeviceFeatureSupport_DEVICE_FEATURE_SUPPORT_UNKNOWN, entity.DeviceReason_DEVICE_REASON_NOT_READY},
		{"vendor support", 5, false, entity.DeviceFeatureSupport_DEVICE_FEATURE_SUPPORT_UNKNOWN, entity.DeviceReason_DEVICE_REASON_NOT_READY},
		{"reserved report", 2, false, entity.DeviceFeatureSupport_DEVICE_FEATURE_SUPPORT_UNKNOWN, entity.DeviceReason_DEVICE_REASON_MALFORMED_RESPONSE},
		{"reporting unsupported", 0, true, entity.DeviceFeatureSupport_DEVICE_FEATURE_SUPPORT_UNKNOWN, entity.DeviceReason_DEVICE_REASON_UNSUPPORTED},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Optional reporting rejection never claims that the drive lacks the underlying operation.
			transport := newSerializedTransport(func(command Command) (Response, error) {
				if command.CDB[0] != 0xa3 {
					return inspectionFixture(command)
				}
				if tc.reject {
					return Response{Completed: true, Status: 2, Sense: fixedSense(0x70, 5, 0x20, 0, false, 0, 0)}, nil
				}
				command.Data[1] = tc.code
				return Response{Completed: true, Residual: int32(len(command.Data) - 2)}, nil
			}, func() error { return nil })
			defer transport.Close()
			device, err := navigationFixtureDevice(transport)
			if err != nil {
				t.Fatal(err)
			}
			f := feature(device.Report(), entity.DeviceFeatureKind_DEVICE_FEATURE_KIND_LOCATE)
			if f.Support != tc.support || f.Reason != tc.reason || f.GetNavigation() != nil || f.Validation != entity.DeviceFeatureValidation_DEVICE_FEATURE_VALIDATION_UNVALIDATED {
				t.Fatalf("report=%v", f)
			}
			navigator, err := device.Navigation(context.Background())
			if navigator != nil || err != nil {
				t.Fatalf("navigator=%v err=%v", navigator, err)
			}
		})
	}
}

func TestNavigationDoesNotRetryActuallyRejectedLongPosition(t *testing.T) {
	// A usage report cannot erase an observed clean long-form rejection or cause another long query.
	var longQueries, shortQueries, longReports int
	transport := newSerializedTransport(func(command Command) (Response, error) {
		if command.CDB[0] == 0x34 {
			if command.CDB[1] == 6 {
				longQueries++
				return Response{Completed: true, Status: 2, Sense: fixedSense(0x70, 5, 0x24, 0, false, 0, 0)}, nil
			}
			shortQueries++
		}
		if command.CDB[0] == 0xa3 && command.CDB[3] == 0x34 && command.CDB[5] == 6 {
			longReports++
		}
		return inspectionFixture(command)
	}, func() error { return nil })
	defer transport.Close()
	device, err := navigationFixtureDevice(transport)
	if err != nil {
		t.Fatal(err)
	}
	navigator, err := device.Navigation(context.Background())
	if err != nil || navigator == nil || navigator.AddressBits() != 32 || longQueries != 1 || shortQueries != 1 || longReports != 0 {
		t.Fatalf("navigator=%v err=%v long=%d short=%d reports=%d", navigator, err, longQueries, shortQueries, longReports)
	}
}

func TestNavigationFormsHaveAnIndependentEffectiveAddressWidth(t *testing.T) {
	for _, long := range []bool{false, true} {
		for _, opcode := range []byte{0x2b, 0x92} {
			// Both SSC observation forms identify the same implicit logical object used by both LOCATE forms.
			var positionAction, locatedOpcode byte
			transport := newSerializedTransport(func(command Command) (Response, error) {
				if command.CDB[0] == 0x34 {
					positionAction = command.CDB[1]
					if !long && command.CDB[1] == 6 {
						return Response{Completed: true, Status: 2, Sense: fixedSense(0x70, 5, 0x24, 0, false, 0, 0)}, nil
					}
				}
				if command.CDB[0] == 0xa3 && (command.CDB[3] == 0x92 || command.CDB[3] == 0x2b) && command.CDB[3] != opcode {
					command.Data[1] = 1
					return Response{Completed: true, Residual: int32(len(command.Data) - 2)}, nil
				}
				if command.CDB[0] == 0x92 || command.CDB[0] == 0x2b {
					locatedOpcode = command.CDB[0]
					return Response{Completed: true}, nil
				}
				return inspectionFixture(command)
			}, func() error { return nil })
			defer transport.Close()
			device, err := navigationFixtureDevice(transport)
			if err != nil {
				t.Fatal(err)
			}
			navigator, err := device.Navigation(context.Background())
			if err != nil || navigator == nil {
				t.Fatalf("long=%t opcode=%02x navigator=%v err=%v", long, opcode, navigator, err)
			}

			// Effective width is the intersection, while the selected observation decoder stays unchanged.
			bits, action := uint32(32), byte(0)
			if long {
				action = 6
				if opcode == 0x92 {
					bits = 64
				}
			}
			if navigator.AddressBits() != bits {
				t.Fatalf("long=%t opcode=%02x width=%d", long, opcode, navigator.AddressBits())
			}
			if _, err := navigator.Position(context.Background()); err != nil || positionAction != action {
				t.Fatalf("position form changed: action=%d err=%v", positionAction, err)
			}
			if err := navigator.Locate(context.Background(), 0, math.MaxUint32); err != nil || locatedOpcode != opcode {
				t.Fatalf("motion form changed: opcode=%02x err=%v", locatedOpcode, err)
			}
			if bits == 32 && navigator.Locate(context.Background(), 0, math.MaxUint32+1) == nil {
				t.Fatal("effective observation/motion range exceeded")
			}
			f := feature(device.Report(), entity.DeviceFeatureKind_DEVICE_FEATURE_KIND_READ_POSITION)
			if f.GetNavigation().GetReadPositionLong() != long || f.GetNavigation().GetAddressBits() != bits {
				t.Fatalf("effective range or native observation form misreported: %v", f)
			}
		}
	}
}

func TestReadPositionReportRequiresTheLiteralServiceAction(t *testing.T) {
	for _, action := range []byte{0, 6} {
		// SPC reports the literal action at its CDB position; it is not a field support mask.
		form := []byte{0x34, action, 0, 0, 0, 0, 0, 0, 0, 0}
		data := make([]byte, 14)
		data[1], data[3], data[4], data[5] = 3, 10, 0x34, action
		if support, err := commandFormSupport(data, form); err != nil || support != entity.DeviceFeatureSupport_DEVICE_FEATURE_SUPPORT_SUPPORTED {
			t.Fatalf("literal action=%d support=%v err=%v", action, support, err)
		}
		for _, invalid := range []byte{0, 6, 8, 0xff} {
			if invalid == action {
				continue
			}
			data[5] = invalid
			if _, err := commandFormSupport(data, form); !errors.Is(err, ErrMalformed) {
				t.Fatalf("requested=%d descriptor=%d accepted: %v", action, invalid, err)
			}
		}
	}
}

func TestNavigationReportsKnownPositionOutsideTheEffectiveRangeUnavailable(t *testing.T) {
	// A long observation may be exact but outside a supported LOCATE(10)'s range; never call it READY.
	transport := newSerializedTransport(func(command Command) (Response, error) {
		if command.CDB[0] == 0xa3 && command.CDB[3] == 0x92 {
			command.Data[1] = 1
			return Response{Completed: true, Residual: int32(len(command.Data) - 2)}, nil
		}
		response, err := inspectionFixture(command)
		if command.CDB[0] == 0x34 {
			binary.BigEndian.PutUint64(command.Data[8:16], math.MaxUint32+1)
		}
		return response, err
	}, func() error { return nil })
	defer transport.Close()
	device, err := navigationFixtureDevice(transport)
	if err != nil {
		t.Fatal(err)
	}
	for _, kind := range []entity.DeviceFeatureKind{entity.DeviceFeatureKind_DEVICE_FEATURE_KIND_READ_POSITION, entity.DeviceFeatureKind_DEVICE_FEATURE_KIND_LOCATE} {
		f := feature(device.Report(), kind)
		if f.Support != entity.DeviceFeatureSupport_DEVICE_FEATURE_SUPPORT_SUPPORTED || f.Availability != entity.DeviceFeatureAvailability_DEVICE_FEATURE_AVAILABILITY_UNAVAILABLE ||
			f.GetNavigation().GetAddressBits() != 32 || !f.GetNavigation().GetReadPositionLong() {
			t.Fatalf("exact observation outside shared range=%v", f)
		}
	}
}

func TestNavigationPositionUnknownDoesNotBecomeAnExactAddress(t *testing.T) {
	// LONU makes both object and partition undefined even when the file ordinal remains exact.
	transport := newSerializedTransport(func(command Command) (Response, error) {
		response, err := inspectionFixture(command)
		if command.CDB[0] == 0x34 {
			command.Data[0] = 4
			binary.BigEndian.PutUint32(command.Data[4:8], math.MaxUint32)
		}
		return response, err
	}, func() error { return nil })
	defer transport.Close()
	device, err := navigationFixtureDevice(transport)
	if err != nil {
		t.Fatal(err)
	}
	navigator, err := device.Navigation(context.Background())
	if err != nil || navigator == nil {
		t.Fatal(err)
	}
	position, err := navigator.Position(context.Background())
	if err != nil || position.ObjectKnown || !position.FileKnown || position.File != 4 {
		t.Fatalf("position=%+v err=%v", position, err)
	}
	for _, kind := range []entity.DeviceFeatureKind{entity.DeviceFeatureKind_DEVICE_FEATURE_KIND_READ_POSITION, entity.DeviceFeatureKind_DEVICE_FEATURE_KIND_LOCATE} {
		if feature(device.Report(), kind).Availability != entity.DeviceFeatureAvailability_DEVICE_FEATURE_AVAILABILITY_UNKNOWN {
			t.Fatal("unknown logical address reported ready")
		}
	}
}

func TestNavigationIssuedFailureNeverTriesAnotherForm(t *testing.T) {
	for _, uncertain := range []bool{false, true} {
		// A failure on the already selected long form cannot trigger a short-form retry.
		var motions, queries int
		transport := newSerializedTransport(func(command Command) (Response, error) {
			if command.CDB[0] == 0x92 || command.CDB[0] == 0x2b {
				motions++
				if command.CDB[0] != 0x92 {
					t.Fatal("alternate form issued")
				}
				if uncertain {
					return Response{Completed: true, HostStatus: 3}, nil
				}
				return Response{Completed: true, Status: 2, Sense: fixedSense(0x70, 5, 0x24, 0, false, 0, 0)}, nil
			}
			queries++
			return inspectionFixture(command)
		}, func() error { return nil })
		defer transport.Close()
		device, err := navigationFixtureDevice(transport)
		if err != nil {
			t.Fatal(err)
		}
		navigator, err := device.Navigation(context.Background())
		if err != nil {
			t.Fatal(err)
		}

		// Pre-command cancellation leaves the device untouched; an issued failure preserves its cause.
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if err := navigator.Locate(ctx, 0, 1); !errors.Is(err, context.Canceled) || motions != 0 {
			t.Fatalf("canceled locate=%v motions=%d", err, motions)
		}
		err = navigator.Locate(context.Background(), 0, 1)
		if err == nil || errors.Is(err, ErrUncertain) != uncertain || motions != 1 {
			t.Fatalf("locate=%v motions=%d", err, motions)
		}
		if !uncertain && Reason(err) != entity.DeviceReason_DEVICE_REASON_UNSUPPORTED {
			t.Fatalf("rejection cause lost: %v", err)
		}
		if uncertain {
			before := queries
			if _, err := navigator.Position(context.Background()); !errors.Is(err, ErrUncertain) || queries != before {
				t.Fatal("uncertain transport admitted another command")
			}
		}
	}
}

func TestPositionContradictionDiffersFromGoodUnknown(t *testing.T) {
	for _, tc := range []struct {
		flags byte
		obj   uint64
		file  uint64
		bad   bool
	}{{0x80, 1, 0, true}, {0x80, 0, 1, true}, {0, 2, 3, true}, {4, 2, 3, false}, {8, 2, 3, false}, {0x80, 0, 0, false}} {
		// Only counters whose validity bits permit interpretation participate in consistency checks.
		data := make([]byte, 32)
		data[0] = tc.flags
		binary.BigEndian.PutUint64(data[8:16], tc.obj)
		binary.BigEndian.PutUint64(data[16:24], tc.file)
		_, err := decodePosition(data, true)
		if errors.Is(err, ErrMalformed) != tc.bad {
			t.Fatalf("flags=%02x object=%d file=%d err=%v", tc.flags, tc.obj, tc.file, err)
		}
	}
}

func TestPassiveNavigationReportingIsServiceActionBounded(t *testing.T) {
	for _, action := range []byte{0, 6} {
		// Only the two consumed READ POSITION service actions may use reporting option two.
		cdb := []byte{0xa3, 0x0c, 2, 0x34, 0, action, 0, 0, 0, 64, 0, 0}
		if !passiveCommand(Command{CDB: cdb, Direction: FromDevice, Data: make([]byte, 64)}) {
			t.Fatalf("required report rejected: %x", cdb)
		}
		for _, change := range []struct{ at, value int }{{2, 3}, {3, 0x92}, {4, 1}, {5, 8}, {10, 1}, {11, 1}} {
			altered := append([]byte(nil), cdb...)
			altered[change.at] = byte(change.value)
			if passiveCommand(Command{CDB: altered, Direction: FromDevice, Data: make([]byte, 64)}) {
				t.Fatalf("unused/reserved reporting form admitted: %x", altered)
			}
		}
	}
}

func TestNavigationFormRequiresTheWholeAddressAndExactCDBLength(t *testing.T) {
	for _, short := range []bool{false, true} {
		// An ignored object field cannot authorize navigation, and partial field masks are malformed.
		opcode, object := byte(0x92), uint64(math.MaxUint64)
		if short {
			opcode, object = 0x2b, math.MaxUint32
		}
		form, err := locateCDB(opcode, 0, object)
		if err != nil {
			t.Fatal(err)
		}
		data := make([]byte, 4+len(form))
		data[1], data[3], data[4] = 3, byte(len(form)), opcode
		for i := 5; i < len(data); i++ {
			data[i] = 0xff
		}
		if support, err := commandFormSupport(data, form); err != nil || support != entity.DeviceFeatureSupport_DEVICE_FEATURE_SUPPORT_SUPPORTED {
			t.Fatalf("complete form support=%v err=%v", support, err)
		}

		// A partial address field is not a smaller supported range; truncation also invalidates its evidence.
		data[8] &^= 0x80
		if short {
			data[7] &^= 0x80
		}
		if _, err := commandFormSupport(data, form); !errors.Is(err, ErrMalformed) {
			t.Fatalf("partial address field=%v", err)
		}
		start, end := 8, 16
		if short {
			start, end = 7, 11
		}
		clear(data[start:end])
		if support, err := commandFormSupport(data, form); err != nil || support != entity.DeviceFeatureSupport_DEVICE_FEATURE_SUPPORT_UNSUPPORTED {
			t.Fatalf("ignored address support=%v err=%v", support, err)
		}
		if _, err := commandFormSupport(data[:len(data)-1], form); !errors.Is(err, ErrMalformed) {
			t.Fatalf("truncated usage descriptor=%v", err)
		}
	}
}

func TestNavigationPositionPreservesCommandAndSemanticFailures(t *testing.T) {
	for _, tc := range []struct {
		name      string
		sense     []byte
		truncated bool
		badBOP    bool
		uncertain bool
		reason    entity.DeviceReason
	}{
		{"clean unsupported", fixedSense(0x70, 5, 0x24, 0, false, 0, 0), false, false, false, entity.DeviceReason_DEVICE_REASON_UNSUPPORTED},
		{"reset", fixedSense(0x70, 6, 0x29, 0, false, 0, 0), false, false, true, entity.DeviceReason_DEVICE_REASON_OWNERSHIP_UNCERTAIN},
		{"deferred", fixedSense(0x71, 5, 0x24, 0, false, 0, 0), false, false, true, entity.DeviceReason_DEVICE_REASON_OWNERSHIP_UNCERTAIN},
		{"truncated GOOD", nil, true, false, false, entity.DeviceReason_DEVICE_REASON_MALFORMED_RESPONSE},
		{"contradictory GOOD", nil, false, true, false, entity.DeviceReason_DEVICE_REASON_MALFORMED_RESPONSE},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Discovery succeeds first; the later query must preserve the real failure without form fallback.
			failed, queries := false, 0
			transport := newSerializedTransport(func(command Command) (Response, error) {
				if failed && command.CDB[0] == 0x34 {
					queries++
					if command.CDB[1] != 6 {
						t.Fatal("position failure triggered another form")
					}
					if tc.sense != nil {
						return Response{Completed: true, Status: 2, Sense: tc.sense}, nil
					}
					response, err := inspectionFixture(command)
					if tc.truncated {
						response.Residual = 1
					}
					if tc.badBOP {
						command.Data[0] = 0x80
					}
					return response, err
				}
				return inspectionFixture(command)
			}, func() error { return nil })
			defer transport.Close()
			device, err := navigationFixtureDevice(transport)
			if err != nil {
				t.Fatal(err)
			}
			navigator, err := device.Navigation(context.Background())
			if err != nil {
				t.Fatal(err)
			}

			// Reset/deferred uncertainty poisons admission; a clean typed rejection remains distinguishable.
			failed = true
			_, err = navigator.Position(context.Background())
			if err == nil || errors.Is(err, ErrUncertain) != tc.uncertain || Reason(err) != tc.reason || queries != 1 {
				t.Fatalf("position=%v queries=%d", err, queries)
			}
			if tc.uncertain {
				if _, err := navigator.Position(context.Background()); !errors.Is(err, ErrUncertain) || queries != 1 {
					t.Fatal("uncertain position admitted another command")
				}
			}
		})
	}
}
