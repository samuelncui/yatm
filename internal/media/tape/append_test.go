package tape

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"testing"
	"time"

	"github.com/samuelncui/yatm/entity"
)

type appendTailFixture struct {
	endObject, endFile uint64
	bits               uint32
	fixed              bool
	commands           [][]byte
	edit               func(int, Command, *Response) error
}

func (f *appendTailFixture) device(t *testing.T) *Device {
	t.Helper()
	// Isolate this tail invariant from Configure's separately covered record/crypto admission.
	mode := RecordMode{MaximumBytes: 512 << 10}
	if f.fixed {
		mode.FixedBytes = mode.MaximumBytes
	}
	transport := newSerializedTransport(func(command Command) (Response, error) {
		step := len(f.commands)
		f.commands = append(f.commands, bytes.Clone(command.CDB))
		response := Response{Completed: true}
		switch step {
		case 0:
			if !bytes.Equal(command.CDB, []byte{0x11, 3, 0, 0, 0, 0}) || command.Direction != NoData {
				t.Fatalf("unexpected EOD motion %x", command.CDB)
			}
		case 1, 3, 7:
			if !bytes.Equal(command.CDB, []byte{0x34, 6, 0, 0, 0, 0, 0, 0, 0, 0}) || len(command.Data) != 32 {
				t.Fatalf("expected exact long position, got %x", command.CDB)
			}
			object, file := f.endObject, f.endFile
			if step == 3 {
				object--
				file--
			}
			binary.BigEndian.PutUint64(command.Data[8:16], object)
			binary.BigEndian.PutUint64(command.Data[16:24], file)
		case 2, 6:
			object := f.endObject
			if step == 2 {
				object--
			}
			opcode := byte(0x92)
			if f.bits == 32 {
				opcode = 0x2b
			}
			want, err := locateCDB(opcode, 0, object)
			if err != nil || !bytes.Equal(command.CDB, want) || command.Direction != NoData {
				t.Fatalf("unexpected LOCATE %x want %x err=%v", command.CDB, want, err)
			}
		case 4, 5:
			want := []byte{8, 0, 8, 0, 0, 0}
			information := int32(mode.MaximumBytes)
			if f.fixed {
				want = []byte{8, 1, 0, 0, 1, 0}
				information = 1
			}
			if !bytes.Equal(command.CDB, want) || len(command.Data) != mode.MaximumBytes {
				t.Fatalf("unexpected tail READ %x bytes=%d", command.CDB, len(command.Data))
			}
			response.Status, response.Residual = 2, int32(len(command.Data))
			if step == 4 {
				response.Sense = fixedSense(0x70, 0, 0, 1, true, 0x80, information)
			} else {
				response.Sense = fixedSense(0x70, 8, 0, 5, true, 0, information)
			}
		default:
			t.Fatalf("extra command after bounded tail: %x", command.CDB)
		}
		if f.edit != nil {
			err := f.edit(step, command, &response)
			return response, err
		}
		return response, nil
	}, func() error { return nil })
	t.Cleanup(func() { _ = transport.Close() })
	records := Records{Transport: transport, Mode: mode, Timeout: 10 * time.Minute}
	opcode := byte(0x92)
	if f.bits == 32 {
		opcode = 0x2b
	}
	device := &Device{transport: transport, operation: Read, records: records,
		report: &entity.InspectDeviceResponse{
			Report: &entity.InspectDeviceResponse_Tape{Tape: &entity.TapeDeviceReport{}},
		},
		navigation: &Navigation{records: records, long: true, bits: f.bits, opcode: opcode}}
	for _, kind := range []entity.DeviceFeatureKind{
		entity.DeviceFeatureKind_DEVICE_FEATURE_KIND_READ_POSITION, entity.DeviceFeatureKind_DEVICE_FEATURE_KIND_LOCATE,
	} {
		device.report.GetTape().Features = append(device.report.GetTape().Features, &entity.DeviceFeature{
			Feature: kind, Support: entity.DeviceFeatureSupport_DEVICE_FEATURE_SUPPORT_SUPPORTED,
		})
	}
	return device
}

func TestVerifyAppendTailBoundedProof(t *testing.T) {
	for _, test := range []struct {
		bits   uint32
		object uint64
		fixed  bool
	}{
		{32, 5, false}, {32, math.MaxUint32, false}, {64, 1<<40 + 9, false}, {64, 12, true},
	} {
		t.Run(fmt.Sprintf("%d/%d/fixed=%t", test.bits, test.object, test.fixed), func(t *testing.T) {
			// Prefix length never changes the two boundary READs or the fresh filemark ordinal.
			fixture := &appendTailFixture{bits: test.bits, endObject: test.object, endFile: 2, fixed: test.fixed}
			file, err := fixture.device(t).VerifyAppendTail(t.Context())
			if err != nil || file != 2 || len(fixture.commands) != 8 {
				t.Fatalf("file=%d calls=%d err=%v", file, len(fixture.commands), err)
			}
		})
	}
}

func TestVerifyAppendTailMissingEvidenceRefusesAppend(t *testing.T) {
	for _, test := range []struct {
		name  string
		calls int
		setup func(*Device)
		edit  func(int, Command, *Response) error
	}{
		{name: "unconfigured", setup: func(d *Device) { d.operation = 0 }},
		{name: "no navigator", setup: func(d *Device) { d.navigation = nil }},
		{name: "short position only", setup: func(d *Device) { d.navigation.long = false }},
		{name: "unsupported EOD position", calls: 2, edit: func(step int, _ Command, r *Response) error {
			if step == 1 {
				*r = Response{Completed: true, Status: 2, Sense: fixedSense(0x70, 5, 0x24, 0, false, 0, 0)}
			}
			return nil
		}},
		{name: "unknown EOD object", calls: 2, edit: func(step int, c Command, _ *Response) error {
			if step == 1 {
				c.Data[0] = 4
			}
			return nil
		}},
		{name: "unknown EOD file", calls: 2, edit: func(step int, c Command, _ *Response) error {
			if step == 1 {
				c.Data[0] = 8
			}
			return nil
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			// Missing evidence fails APPEND without a file ordinal, alternate motion or sequential walk.
			fixture := &appendTailFixture{bits: 64, endObject: 20, endFile: 2, edit: test.edit}
			device := fixture.device(t)
			if test.setup != nil {
				test.setup(device)
			}
			file, err := device.VerifyAppendTail(t.Context())
			if err == nil || file != 0 || len(fixture.commands) != test.calls {
				t.Fatalf("file=%d calls=%d err=%v", file, len(fixture.commands), err)
			}
		})
	}
}

func TestVerifyAppendTailContradictionsRefuseWrite(t *testing.T) {
	for _, test := range []struct {
		name string
		step int
		edit func(Command, *Response)
	}{
		{"unsupported SPACE", 0, func(_ Command, r *Response) {
			*r = Response{Completed: true, Status: 2, Sense: fixedSense(0x70, 5, 0x20, 0, false, 0, 0)}
		}},
		{"SPACE unknown completion", 0, func(_ Command, r *Response) { *r = Response{} }},
		{"SPACE residual", 0, func(_ Command, r *Response) { r.Residual = 1 }},
		{"medium changed", 1, func(_ Command, r *Response) {
			*r = Response{Completed: true, Status: 2, Sense: fixedSense(0x70, 6, 0x28, 0, false, 0, 0)}
		}},
		{"wrong partition", 1, func(c Command, _ *Response) { c.Data[7] = 1 }},
		{"zero object", 1, func(c Command, _ *Response) { clear(c.Data[8:16]) }},
		{"zero file", 1, func(c Command, _ *Response) { clear(c.Data[16:24]) }},
		{"file greater than object", 1, func(c Command, _ *Response) { binary.BigEndian.PutUint64(c.Data[16:24], 31) }},
		{"maximum object", 1, func(c Command, _ *Response) { binary.BigEndian.PutUint64(c.Data[8:16], math.MaxUint64) }},
		{"both maximum counters", 1, func(c Command, _ *Response) {
			binary.BigEndian.PutUint64(c.Data[8:16], math.MaxUint64)
			binary.BigEndian.PutUint64(c.Data[16:24], math.MaxUint64)
		}},
		{"last-object mismatch", 3, func(c Command, _ *Response) { c.Data[15]-- }},
		{"last-file mismatch", 3, func(c Command, _ *Response) { c.Data[23]++ }},
		{"unknown last-file", 3, func(c Command, _ *Response) { c.Data[0] = 8 }},
		{"data instead of filemark", 4, func(_ Command, r *Response) { *r = Response{Completed: true} }},
		{"EOD instead of filemark", 4, func(_ Command, r *Response) {
			r.Sense = fixedSense(0x70, 8, 0, 5, true, 0, 512<<10)
		}},
		{"filemark payload contradiction", 4, func(_ Command, r *Response) { r.Residual-- }},
		{"filemark instead of EOD", 5, func(_ Command, r *Response) {
			r.Sense = fixedSense(0x70, 0, 0, 1, true, 0x80, 512<<10)
		}},
		{"data instead of EOD", 5, func(_ Command, r *Response) { *r = Response{Completed: true} }},
		{"restore object mismatch", 7, func(c Command, _ *Response) { c.Data[15]++ }},
		{"restore file mismatch", 7, func(c Command, _ *Response) { c.Data[23]-- }},
		{"restore unknown", 7, func(c Command, _ *Response) { c.Data[0] = 4 }},
	} {
		t.Run(test.name, func(t *testing.T) {
			// A changed native fact stops at its first offending command; no fallback or later WRITE is admitted here.
			fixture := &appendTailFixture{bits: 64, endObject: 30, endFile: 2,
				edit: func(step int, c Command, r *Response) error {
					if step == test.step {
						test.edit(c, r)
					}
					return nil
				}}
			file, err := fixture.device(t).VerifyAppendTail(t.Context())
			if err == nil || file != 0 || len(fixture.commands) != test.step+1 {
				t.Fatalf("file=%d calls=%d err=%v", file, len(fixture.commands), err)
			}
		})
	}
}

func TestVerifyAppendTailFailuresAndCancellation(t *testing.T) {
	for _, step := range []int{0, 1, 2, 3, 4, 5, 6, 7} {
		t.Run(fmt.Sprint(step), func(t *testing.T) {
			// Every completed boundary must propagate transport failures rather than silently choose another route.
			failure := errors.New("injected append transport failure")
			fixture := &appendTailFixture{bits: 64, endObject: 30, endFile: 2,
				edit: func(at int, _ Command, _ *Response) error {
					if at == step {
						return failure
					}
					return nil
				}}
			file, err := fixture.device(t).VerifyAppendTail(t.Context())
			if !errors.Is(err, failure) || file != 0 || len(fixture.commands) != step+1 {
				t.Fatalf("file=%d calls=%d err=%v", file, len(fixture.commands), err)
			}
		})
	}

	// Cancellation before admission or after successful SPACE cannot issue another physical command.
	for _, afterSpace := range []bool{false, true} {
		ctx, cancel := context.WithCancel(t.Context())
		fixture := &appendTailFixture{bits: 64, endObject: 30, endFile: 2}
		if afterSpace {
			fixture.edit = func(step int, _ Command, _ *Response) error {
				if step == 0 {
					cancel()
				}
				return nil
			}
		} else {
			cancel()
		}
		_, err := fixture.device(t).VerifyAppendTail(ctx)
		cancel()
		wantCalls := 0
		if afterSpace {
			wantCalls = 1
		}
		if !errors.Is(err, context.Canceled) || len(fixture.commands) != wantCalls {
			t.Fatalf("cancel afterSpace=%t calls=%d err=%v", afterSpace, len(fixture.commands), err)
		}
	}

	// Long READ POSITION cannot make a LOCATE(10) target wider than its native field.
	fixture := &appendTailFixture{bits: 32, endObject: math.MaxUint32 + 1, endFile: 2}
	_, err := fixture.device(t).VerifyAppendTail(t.Context())
	if err == nil || len(fixture.commands) != 2 {
		t.Fatalf("32-bit range check calls=%d err=%v", len(fixture.commands), err)
	}
}
