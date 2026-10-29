package tape

import (
	"bytes"
	"context"
	"errors"
	"io"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
	"unsafe"

	"github.com/samuelncui/yatm/entity"
	"google.golang.org/protobuf/proto"
)

type ltfsLabelCommand struct {
	cdb     []byte
	bytes   int
	timeout time.Duration
}

type ltfsLabelFixture struct {
	*serializedTransport
	base         *deviceFixture
	commands     []ltfsLabelCommand
	reserve      []int
	reserveErr   error
	onReserve    func()
	shortReserve bool
	limit        int
	partition    int
	step         int
	xml          [2][]byte
	edit         func(int, int, []byte) []byte
	fail         func(Command) (Response, error, bool)
}

func newLTFSLabelFixture(t *testing.T) (*Device, *ltfsLabelFixture) {
	t.Helper()
	// Reuse actual inspection and native response decoding; only the physical records are synthetic.
	f := &ltfsLabelFixture{base: newDeviceFixture(), limit: 32 << 10,
		xml: [2][]byte{[]byte("<label>zero</label>"), []byte("<label>one</label>")}}
	f.base.mode[51] = 1
	f.serializedTransport = newSerializedTransport(f.execute, func() error { f.base.closed++; return nil })
	d, err := NewDevice(t.Context(), Identity{Configured: "fixture"}, f)
	if err != nil {
		t.Fatal(err)
	}
	f.commands = nil
	t.Cleanup(func() { _ = d.Cleanup(context.Background()) })
	return d, f
}

func (f *ltfsLabelFixture) EnsureTransfer(ctx context.Context, size int) error {
	f.reserve = append(f.reserve, size)
	if err := ctx.Err(); err != nil {
		return err
	}
	if f.reserveErr == nil && !f.shortReserve {
		f.limit = max(f.limit, size)
	}
	if f.onReserve != nil {
		f.onReserve()
	}
	return f.reserveErr
}

func (f *ltfsLabelFixture) TransferLimit() int { return f.limit }

func (f *ltfsLabelFixture) execute(command Command) (Response, error) {
	// Capture command shapes without retaining the reusable read buffer or its contents.
	f.commands = append(f.commands, ltfsLabelCommand{cdb: bytes.Clone(command.CDB), bytes: len(command.Data), timeout: command.Timeout})
	if f.fail != nil {
		if response, err, handled := f.fail(command); handled {
			return response, err
		}
	}

	// Report exact variable READ and CP LOCATE forms, then serve the four framed partition objects.
	if command.CDB[0] == 0xa3 && command.CDB[3] == 8 {
		clear(command.Data)
		copy(command.Data, []byte{0, 3, 0, 6, 8, 0xff, 0xff, 0xff, 0xff, 0xff})
		return Response{Completed: true, Residual: int32(len(command.Data) - 10)}, nil
	}
	if command.CDB[0] == 0x92 {
		f.partition, f.step = int(command.CDB[3]), 0
		return Response{Completed: true}, nil
	}
	if command.CDB[0] != 8 {
		return f.base.execute(command)
	}
	var data []byte
	switch f.step {
	case 0:
		data = bytes.Repeat([]byte{' '}, 80)
		copy(data, "VOL1ABC001")
		copy(data[24:], "LTFS")
	case 2:
		data = f.xml[f.partition]
	}
	if f.edit != nil {
		data = f.edit(f.partition, f.step, data)
	}
	f.step++
	if data == nil {
		return Response{Completed: true, Status: 2, Residual: int32(len(command.Data)),
			Sense: fixedSense(0x70, 0, 0, 1, true, 0x80, int32(len(command.Data)))}, nil
	}
	copy(command.Data, data)
	remaining := len(command.Data) - len(data)
	if remaining == 0 {
		return Response{Completed: true}, nil
	}
	residual := max(remaining, 0)
	return Response{Completed: true, Status: 2, Residual: int32(residual),
		Sense: fixedSense(0x70, 0, 0, 0, true, 0x20, int32(remaining))}, nil
}

func TestLTFSLabelsReadBothPrefixesWithoutControlOrCleanupOwnership(t *testing.T) {
	// A nonzero existing block descriptor and captured cipher state do not authorize control changes.
	d, f := newLTFSLabelFixture(t)
	if !d.report.GetTape().GetFixedRecords() || d.report.GetTape().GetRecordBytes() == 0 {
		t.Fatal("fixture must retain its existing nonzero block descriptor")
	}
	originalMode := bytes.Clone(f.base.mode)
	originalSecurity := f.base.security
	labels, err := d.ReadLTFSLabels(t.Context())
	if err != nil || !reflect.DeepEqual(labels, f.xml) {
		t.Fatalf("labels=%q err=%v", labels, err)
	}
	if !reflect.DeepEqual(f.reserve, []int{ltfsLabelMaximumBytes}) {
		t.Fatalf("reservation=%v", f.reserve)
	}

	// Qualification precedes exactly two CP LOCATEs and eight variable READs, with no extra motion.
	var opcodes []byte
	for _, command := range f.commands {
		opcodes = append(opcodes, command.cdb[0])
		switch command.cdb[0] {
		case 0xa3:
		case 0x92:
			if command.timeout != 20*time.Minute || command.cdb[1] != 2 || !bytes.Equal(command.cdb[4:12], make([]byte, 8)) {
				t.Fatalf("LOCATE=%x timeout=%s", command.cdb, command.timeout)
			}
		case 8:
			if command.timeout != 20*time.Minute || command.cdb[1] != 0 || command.bytes != ltfsLabelMaximumBytes {
				t.Fatalf("READ=%x bytes=%d timeout=%s", command.cdb, command.bytes, command.timeout)
			}
		default:
			t.Fatalf("unexpected command after inspection: %x", command.cdb)
		}
	}
	if !reflect.DeepEqual(opcodes, []byte{0xa3, 0xa3, 0x92, 8, 8, 8, 8, 0x92, 8, 8, 8, 8}) ||
		f.commands[2].cdb[3] != 0 || f.commands[7].cdb[3] != 1 {
		t.Fatalf("commands=%x", opcodes)
	}

	// Both records are independent copies and cleanup remains with the existing Device owner.
	f.xml[0][0] = '!'
	labels[1][0] = '!'
	if labels[0][0] != '<' || f.xml[1][0] != '<' || f.base.closed != 0 || f.base.security != originalSecurity ||
		!bytes.Equal(originalMode, f.base.mode) || f.base.selects != 0 || f.base.formats != 0 || f.base.writes != 0 {
		t.Fatal("borrowed records, changed controls, or premature close")
	}
	if err := d.Cleanup(t.Context()); err != nil || f.base.closed != 1 || f.base.security != originalSecurity || !bytes.Equal(originalMode, f.base.mode) {
		t.Fatalf("cleanup=%v closed=%d", err, f.base.closed)
	}
}

func TestLTFSLabelsRejectBoundsAndUnsupportedCPBeforeMotion(t *testing.T) {
	for _, test := range []struct {
		name string
		edit func(*Device, *ltfsLabelFixture)
	}{
		{"unknown maximum", func(d *Device, _ *ltfsLabelFixture) {
			feature(d.report, entity.DeviceFeatureKind_DEVICE_FEATURE_KIND_SEQUENTIAL_READ).GetRecords().MaximumBytes = nil
		}},
		{"ANSI outside minimum", func(d *Device, _ *ltfsLabelFixture) {
			feature(d.report, entity.DeviceFeatureKind_DEVICE_FEATURE_KIND_SEQUENTIAL_READ).GetRecords().MinimumBytes = proto.Uint32(512)
		}},
		{"fixed only", func(d *Device, _ *ltfsLabelFixture) {
			feature(d.report, entity.DeviceFeatureKind_DEVICE_FEATURE_KIND_SEQUENTIAL_READ).GetRecords().Variable = proto.Bool(false)
		}},
		{"unknown presence", func(d *Device, _ *ltfsLabelFixture) { d.report.GetTape().MediumPresent = nil }},
		{"reservation insufficient", func(_ *Device, f *ltfsLabelFixture) { f.shortReserve = true }},
		{"reservation rejected", func(_ *Device, f *ltfsLabelFixture) { f.reserveErr = ErrUnsupported }},
		{"native reservation cap", func(_ *Device, f *ltfsLabelFixture) {
			f.reserveErr = errors.New("SG reserved buffer 262144 is smaller than selected record 524288")
		}},
		{"fixed transport insufficient", func(d *Device, f *ltfsLabelFixture) {
			d.transport = &fixedTransferTransport{serializedTransport: f.serializedTransport, limit: 79}
		}},
		{"unknown transport bound", func(d *Device, f *ltfsLabelFixture) { d.transport = f.serializedTransport }},
		{"partition byte unsupported", func(_ *Device, f *ltfsLabelFixture) {
			f.fail = func(c Command) (Response, error, bool) {
				if c.CDB[0] != 0xa3 {
					return Response{}, nil, false
				}
				r, err := f.base.execute(c)
				c.Data[7] = 0 // The partition-one bit is not supported by this usage map.
				return r, err, true
			}
		}},
		{"unknown command form", func(_ *Device, f *ltfsLabelFixture) {
			f.fail = func(c Command) (Response, error, bool) {
				if c.CDB[0] != 0xa3 {
					return Response{}, nil, false
				}
				clear(c.Data)
				return Response{Completed: true, Residual: int32(len(c.Data) - 2)}, nil, true
			}
		}},
		{"malformed command usage", func(_ *Device, f *ltfsLabelFixture) {
			f.fail = func(c Command) (Response, error, bool) {
				if c.CDB[0] != 0xa3 {
					return Response{}, nil, false
				}
				r, err := f.base.execute(c)
				c.Data[8] = 0x0f // A partial logical-object field cannot establish a command form.
				return r, err, true
			}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			// Invalid bounds and an unsupported CP partition field cannot enter the physical read lane.
			d, f := newLTFSLabelFixture(t)
			test.edit(d, f)
			labels, err := d.ReadLTFSLabels(t.Context())
			if !CleanUnsupported(err) || labels[0] != nil || labels[1] != nil {
				t.Fatalf("labels=%q err=%v", labels, err)
			}
			for _, command := range f.commands {
				if command.cdb[0] != 0xa3 {
					t.Fatalf("active command=%x", command.cdb)
				}
			}
		})
	}
}

func TestLTFSLabelsRejectUnusableFramingAndOversizeWithoutRetry(t *testing.T) {
	for _, test := range []struct {
		name  string
		edit  func(*ltfsLabelFixture)
		reads int
	}{
		{"bad VOL1", func(f *ltfsLabelFixture) {
			f.edit = func(_ int, step int, data []byte) []byte {
				if step == 0 {
					data[0] = 'X'
				}
				return data
			}
		}, 1},
		{"missing first mark", func(f *ltfsLabelFixture) {
			f.edit = func(_ int, step int, data []byte) []byte {
				if step == 1 {
					return []byte("data")
				}
				return data
			}
		}, 2},
		{"missing XML", func(f *ltfsLabelFixture) { f.xml[0] = nil }, 3},
		{"missing final mark", func(f *ltfsLabelFixture) {
			f.edit = func(_ int, step int, data []byte) []byte {
				if step == 3 {
					return []byte("data")
				}
				return data
			}
		}, 4},
		{"oversized XML", func(f *ltfsLabelFixture) { f.xml[0] = make([]byte, ltfsLabelMaximumBytes+1) }, 3},
	} {
		t.Run(test.name, func(t *testing.T) {
			// A settled unusable prefix withdraws the entire pair; it cannot trigger another partition or retry.
			d, f := newLTFSLabelFixture(t)
			test.edit(f)
			labels, err := d.ReadLTFSLabels(t.Context())
			if !CleanUnsupported(err) || labels[0] != nil || labels[1] != nil {
				t.Fatalf("labels=%q err=%v", labels, err)
			}
			reads, moves := 0, 0
			for _, command := range f.commands {
				if command.cdb[0] == 8 {
					reads++
				}
				if command.cdb[0] == 0x92 {
					moves++
				}
			}
			if reads != test.reads || moves != 1 || f.base.closed != 0 {
				t.Fatalf("reads=%d moves=%d closed=%d", reads, moves, f.base.closed)
			}
		})
	}
}

func TestLTFSLabelsNativeReservationCapKeepsOrdinaryMountAvailable(t *testing.T) {
	// Exercise the native reservation implementation, with only the host ioctl and physical transport stubbed.
	d, f := newLTFSLabelFixture(t)
	selected, readback, allocated := 0, 0, 0
	transport := &sgTransport{serializedTransport: f.serializedTransport, limit: 64 << 10}
	transport.ioctl = func(command uintptr, data unsafe.Pointer) error {
		switch command {
		case sgSetReservedSize:
			selected = int(*(*int32)(data))
		case sgReservedSize:
			readback++
			*(*int32)(data) = 64 << 10
		default:
			t.Fatalf("unexpected ioctl=%x", command)
		}
		return nil
	}
	transport.reserve = func(size int) error { allocated = size; return nil }
	d.transport = transport
	labels, err := d.ReadLTFSLabels(t.Context())
	if !CleanUnsupported(err) || labels[0] != nil || labels[1] != nil || len(f.commands) != 0 ||
		selected != ltfsLabelMaximumBytes || readback != 1 || allocated != 64<<10 || f.base.closed != 0 {
		t.Fatalf("labels=%q commands=%v selected=%d readback=%d allocated=%d closed=%d err=%v",
			labels, f.commands, selected, readback, allocated, f.base.closed, err)
	}

	// No command owns the capped descriptor; successful ordinary cleanup releases it exactly once.
	if err := d.Cleanup(t.Context()); err != nil || f.base.closed != 1 {
		t.Fatalf("closed=%d err=%v", f.base.closed, err)
	}
}

func TestLTFSLabelsReservationRetainsCancellationAndUncertainty(t *testing.T) {
	for _, failure := range []error{context.Canceled, context.DeadlineExceeded,
		&UncertainError{Cause: io.ErrUnexpectedEOF}, errors.Join(io.ErrShortBuffer, context.Canceled)} {
		// A local allocation miss is optional; loss of ownership or request cancellation is not.
		d, f := newLTFSLabelFixture(t)
		f.reserveErr = failure
		labels, err := d.ReadLTFSLabels(t.Context())
		if !errors.Is(err, failure) || CleanUnsupported(err) || labels[0] != nil || labels[1] != nil || len(f.commands) != 0 {
			t.Fatalf("failure=%v labels=%q commands=%v err=%v", failure, labels, f.commands, err)
		}
	}

	// Cancellation observed while a plain host allocation fails keeps both real causes.
	d, f := newLTFSLabelFixture(t)
	ctx, cancel := context.WithCancel(t.Context())
	f.reserveErr, f.onReserve = io.ErrShortBuffer, cancel
	_, err := d.ReadLTFSLabels(ctx)
	if !errors.Is(err, context.Canceled) || !errors.Is(err, io.ErrShortBuffer) || CleanUnsupported(err) || len(f.commands) != 0 {
		t.Fatalf("commands=%v err=%v", f.commands, err)
	}
}

func TestLTFSLabelsPreserveRealErrorsAndJoinedOversizeCauses(t *testing.T) {
	for _, failure := range []error{context.Canceled, &UncertainError{Cause: io.ErrUnexpectedEOF}, io.ErrUnexpectedEOF, io.ErrShortBuffer} {
		// A real native/transport failure must remain distinguishable from optional observation absence.
		d, f := newLTFSLabelFixture(t)
		f.fail = func(c Command) (Response, error, bool) {
			if c.CDB[0] == 8 {
				return Response{}, failure, true
			}
			return Response{}, nil, false
		}
		_, err := d.ReadLTFSLabels(t.Context())
		if !errors.Is(err, failure) || CleanUnsupported(err) {
			t.Fatalf("failure=%v err=%v", failure, err)
		}
	}

	// Oversize is optional only when every cause belongs to the exact settled overlong-record response.
	oversize := errors.Join(io.ErrShortBuffer, &CommandError{Response: Response{Completed: true, Status: 2}, Sense: Sense{ILI: true, InformationValid: true, Information: -1}})
	for _, failure := range []error{context.Canceled, &UncertainError{Cause: io.ErrUnexpectedEOF}, io.ErrUnexpectedEOF} {
		if onlyLTFSLabelOversize(errors.Join(oversize, failure)) {
			t.Fatalf("joined real error was hidden: %v", failure)
		}
	}
}

func TestLTFSLabelsCancellationWaitsForIssuedReadAndDoesNotClose(t *testing.T) {
	// A submitted native READ retains its command/buffer owner until the transport returns.
	d, f := newLTFSLabelFixture(t)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	issued, release := make(chan struct{}), make(chan struct{})
	releaseNative := sync.OnceFunc(func() { close(release) })
	defer releaseNative()
	f.fail = func(c Command) (Response, error, bool) {
		if c.CDB[0] == 8 {
			close(issued)
			<-release
			return Response{Completed: true}, nil, true
		}
		return Response{}, nil, false
	}
	done := make(chan error, 1)
	go func() { _, err := d.ReadLTFSLabels(ctx); done <- err }()
	select {
	case <-issued:
	case err := <-done:
		t.Fatalf("probe returned before issuing READ: %v", err)
	}
	cancel()
	select {
	case err := <-done:
		t.Fatalf("issued READ returned early: %v", err)
	default:
	}
	releaseNative()
	if err := <-done; !errors.Is(err, context.Canceled) || CleanUnsupported(err) {
		t.Fatalf("err=%v", err)
	}

	// Cancellation ends the probe without another read, compensating motion or descriptor close.
	reads, moves := 0, 0
	for _, command := range f.commands {
		if command.cdb[0] == 8 {
			reads++
		}
		if command.cdb[0] == 0x92 {
			moves++
		}
	}
	if reads != 1 || moves != 1 || f.base.closed != 0 {
		t.Fatalf("reads=%d moves=%d closed=%d", reads, moves, f.base.closed)
	}
}

func TestLTFSLabelsUseSmallerObservedAndTransportBounds(t *testing.T) {
	for _, fixed := range []bool{false, true} {
		// A declared smaller envelope bounds all requests; no optional partition-list attribute is needed.
		d, f := newLTFSLabelFixture(t)
		d.report.GetTape().PartitionCount = nil
		maximum := 64 << 10
		feature(d.report, entity.DeviceFeatureKind_DEVICE_FEATURE_KIND_SEQUENTIAL_READ).GetRecords().MaximumBytes = proto.Uint32(uint32(maximum))
		if fixed {
			maximum = 1024
			d.transport = &fixedTransferTransport{serializedTransport: f.serializedTransport, limit: maximum}
		}
		labels, err := d.ReadLTFSLabels(t.Context())
		if err != nil || !reflect.DeepEqual(labels, f.xml) {
			t.Fatalf("fixed=%t labels=%q err=%v", fixed, labels, err)
		}
		for _, command := range f.commands {
			if command.cdb[0] == 8 && command.bytes != maximum {
				t.Fatalf("requested=%d bound=%d", command.bytes, maximum)
			}
		}
	}
}

func TestLTFSLabelsObservedNotReadyAndPreIssueCancellationDoNotMove(t *testing.T) {
	for _, reason := range []entity.DeviceReason{entity.DeviceReason_DEVICE_REASON_NO_MEDIUM, entity.DeviceReason_DEVICE_REASON_NOT_READY} {
		// An observed readiness failure is a real attempt error, not optional missing Label evidence.
		d, f := newLTFSLabelFixture(t)
		d.report.Reason = reason
		_, err := d.ReadLTFSLabels(t.Context())
		if err == nil || CleanUnsupported(err) || len(f.commands) != 0 || len(f.reserve) != 0 {
			t.Fatalf("reason=%s err=%v commands=%v", reason, err, f.commands)
		}
	}

	// Canceled admission and host reservation failures cannot issue even a qualification query.
	d, f := newLTFSLabelFixture(t)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := d.ReadLTFSLabels(ctx); !errors.Is(err, context.Canceled) || len(f.commands) != 0 || len(f.reserve) != 0 {
		t.Fatalf("err=%v commands=%v", err, f.commands)
	}
	f.reserveErr = io.ErrUnexpectedEOF
	if _, err := d.ReadLTFSLabels(t.Context()); !CleanUnsupported(err) || !strings.Contains(err.Error(), io.ErrUnexpectedEOF.Error()) || len(f.commands) != 0 {
		t.Fatalf("reservation err=%v", err)
	}
}

func TestLTFSLabelsNativeFailureClassification(t *testing.T) {
	for _, test := range []struct {
		name                string
		opcode              byte
		response            func(int) Response
		optional, uncertain bool
	}{
		{"LOCATE unsupported", 0x92, func(_ int) Response {
			return Response{Completed: true, Status: 2, Sense: fixedSense(0x70, 5, 0x20, 0, false, 0, 0)}
		}, true, false},
		{"READ unsupported", 8, func(n int) Response {
			return Response{Completed: true, Status: 2, Residual: int32(n), Sense: fixedSense(0x70, 5, 0x20, 0, false, 0, 0)}
		}, true, false},
		{"EOD prefix", 8, func(n int) Response {
			return Response{Completed: true, Status: 2, Residual: int32(n), Sense: fixedSense(0x70, 8, 0, 5, true, 0, int32(n))}
		}, true, false},
		{"medium error", 8, func(_ int) Response {
			return Response{Completed: true, Status: 2, Sense: fixedSense(0x70, 3, 0x11, 0, false, 0, 0)}
		}, false, true},
		{"medium removed", 8, func(_ int) Response {
			return Response{Completed: true, Status: 2, Sense: fixedSense(0x70, 2, 0x3a, 0, false, 0, 0)}
		}, false, false},
		{"malformed GOOD residual", 8, func(_ int) Response { return Response{Completed: true, Residual: 1} }, false, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			// Only known settled unsupported commands or unusable prefixes can become LOGICAL fallback.
			d, f := newLTFSLabelFixture(t)
			f.fail = func(c Command) (Response, error, bool) {
				if c.CDB[0] == test.opcode {
					return test.response(len(c.Data)), nil, true
				}
				return Response{}, nil, false
			}
			labels, err := d.ReadLTFSLabels(t.Context())
			if err == nil || CleanUnsupported(err) != test.optional || errors.Is(err, ErrUncertain) != test.uncertain || labels[0] != nil || labels[1] != nil {
				t.Fatalf("labels=%q err=%v optional=%t uncertain=%t", labels, err, CleanUnsupported(err), errors.Is(err, ErrUncertain))
			}
		})
	}
}
