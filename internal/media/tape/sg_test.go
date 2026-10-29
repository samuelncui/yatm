package tape

import (
	"context"
	"errors"
	"io"
	"syscall"
	"testing"
	"time"
	"unsafe"
)

func TestSGHeaderAndRequestLayout(t *testing.T) {
	// Native pointer alignment must match Linux sg.h on both 32-bit and 64-bit targets.
	header, request := sgHeader{}, sgRequest{}
	wantHeader, wantRequest, wantStatus := uintptr(64), uintptr(20), uintptr(44)
	if unsafe.Sizeof(uintptr(0)) == 8 {
		wantHeader, wantRequest, wantStatus = 88, 24, 64
	}
	if unsafe.Sizeof(header) != wantHeader || unsafe.Sizeof(request) != wantRequest || unsafe.Offsetof(header.Status) != wantStatus {
		t.Fatalf("SG layout: header=%d request=%d status=%d", unsafe.Sizeof(header), unsafe.Sizeof(request), unsafe.Offsetof(header.Status))
	}
}

func TestSGInterruptedCommandSettlesWithoutResubmission(t *testing.T) {
	// A simulated ioctl signal admits one command and observes its existing request until settlement.
	commands, queries := 0, 0
	transport := &sgTransport{limit: MaxQueryBytes}
	transport.ioctl = func(operation uintptr, pointer unsafe.Pointer) error {
		switch operation {
		case sgIO:
			commands++
			header := (*sgHeader)(pointer)
			if header.Flags != 0 || header.Direction != int32(NoData) || header.Timeout != 1000 || *header.Command != 0 {
				t.Errorf("unexpected SG request=%+v", header)
			}
			return syscall.EINTR
		case sgRequestTable:
			queries++
			if queries == 1 {
				(*[16]sgRequest)(pointer)[0].State = 1
			}
			return nil
		default:
			t.Fatalf("unexpected ioctl=%x", operation)
			return nil
		}
	}
	transport.serializedTransport = newSerializedTransport(transport.executeCommand, func() error { return nil })
	defer transport.Close()
	command := Command{CDB: make([]byte, 6), Direction: NoData, Timeout: time.Second}
	response, err := transport.Execute(context.Background(), command)
	if !errors.Is(err, ErrUncertain) || response.Completed || commands != 1 || queries != 2 || transport.unsettled {
		t.Fatalf("interruption response=%v err=%v commands=%d queries=%d unsettled=%t", response, err, commands, queries, transport.unsettled)
	}

	// Even a settled orphan has no accepted-count evidence and cannot authorize another command.
	if _, err := transport.Execute(context.Background(), command); !errors.Is(err, ErrUncertain) || commands != 1 {
		t.Fatalf("interrupted command resubmitted: err=%v commands=%d", err, commands)
	}
}

func TestSGInterruptedSettlementBoundAndFailure(t *testing.T) {
	for _, tc := range []struct {
		name  string
		state byte
		err   error
	}{{"deadline", 1, nil}, {"query failure", 0, syscall.EIO}, {"malformed table", 4, nil}} {
		t.Run(tc.name, func(t *testing.T) {
			// Missing settlement evidence preserves unavailability rather than closing an active fd.
			queries := 0
			transport := &sgTransport{limit: MaxQueryBytes}
			transport.ioctl = func(operation uintptr, pointer unsafe.Pointer) error {
				if operation == sgIO {
					return syscall.EINTR
				}
				queries++
				(*[16]sgRequest)(pointer)[0].State = tc.state
				return tc.err
			}
			transport.serializedTransport = newSerializedTransport(transport.executeCommand, func() error {
				if !transport.unsettled {
					t.Error("unsettled descriptor was releasable")
				}
				return &UncertainError{Cause: errors.New("descriptor retained")}
			})
			command := Command{CDB: make([]byte, 6), Direction: NoData, Timeout: time.Millisecond}
			if _, err := transport.Execute(context.Background(), command); !errors.Is(err, ErrUncertain) || !transport.unsettled {
				t.Fatalf("settlement error=%v unsettled=%t", err, transport.unsettled)
			}

			// The observation loop is bounded; Close returns a truthful error without releasing ownership.
			if err := transport.Close(); !errors.Is(err, ErrUncertain) || queries > 3 {
				t.Fatalf("close=%v, queries=%d", err, queries)
			}
		})
	}
}

func TestSGResponseEvidenceAndPreCommandRejection(t *testing.T) {
	// Native completion keeps sense/status/residual fields for the shared decoder.
	transport := &sgTransport{limit: MaxQueryBytes}
	transport.ioctl = func(_ uintptr, pointer unsafe.Pointer) error {
		header := (*sgHeader)(pointer)
		header.Status, header.DriverStatus, header.Residual = 2, 8, 5
		data := unsafe.Slice(header.Sense, int(header.SenseLength))
		sense := fixedSense(0x70, 5, 0x20, 0, false, 0, 0)
		copy(data, sense)
		header.SenseWritten = uint8(len(sense))
		return nil
	}
	command := Command{CDB: []byte{0x12, 0, 0, 0, 96, 0}, Direction: FromDevice, Data: make([]byte, 96), Timeout: time.Second}
	response, err := transport.executeCommand(command)
	if err != nil || !response.Completed || response.Residual != 5 || Reason(CheckResponse(response)).String() != "DEVICE_REASON_UNSUPPORTED" {
		t.Fatalf("SG evidence=%v, err=%v", response, err)
	}

	// A clean ioctl validation/permission rejection leaves no accepted physical command to settle.
	for _, rejection := range []error{syscall.EINVAL, syscall.ENOTTY, syscall.EACCES, syscall.EPERM} {
		transport.ioctl = func(uintptr, unsafe.Pointer) error { return rejection }
		response, err := transport.executeCommand(command)
		if !errors.Is(err, rejection) || errors.Is(err, ErrUncertain) || response.Completed {
			t.Fatalf("pre-command rejection: response=%v err=%v", response, err)
		}
	}
}

func TestSGReservationReadsActualBoundWithoutClampingRequest(t *testing.T) {
	for _, actual := range []int{512 << 10, 64 << 10} {
		// The initial reserve is not a hardware maximum; larger selected records request per-fd allocation.
		calls := 0
		transport := &sgTransport{limit: 32 << 10}
		transport.serializedTransport = newSerializedTransport(nil, func() error { return nil })
		reserved := 0
		transport.reserve = func(limit int) error {
			reserved = limit
			return nil
		}
		transport.ioctl = func(operation uintptr, pointer unsafe.Pointer) error {
			calls++
			value := (*int32)(pointer)
			switch operation {
			case sgSetReservedSize:
				if *value != 512<<10 {
					t.Fatalf("selected reservation was clamped: %d", *value)
				}
			case sgReservedSize:
				*value = int32(actual)
			default:
				t.Fatalf("unexpected reservation ioctl=%x", operation)
			}
			return nil
		}
		err := transport.EnsureTransfer(context.Background(), 512<<10)
		if (err == nil) != (actual == 512<<10) || calls != 2 || transport.TransferLimit() != actual || reserved != actual {
			t.Fatalf("reservation=%v actual=%d native=%d", err, transport.TransferLimit(), reserved)
		}
		if err := transport.Close(); err != nil {
			t.Fatal(err)
		}
	}
}

func TestSGSplitFailureSettlesBeforeReuse(t *testing.T) {
	for _, cause := range []error{syscall.EINTR, syscall.EINVAL, syscall.EPERM, io.ErrShortWrite} {
		t.Run(cause.Error(), func(t *testing.T) {
			// Receive errno cannot be mistaken for a clean pre-submission rejection.
			calls, queries, closes := 0, 0, 0
			transport := &sgTransport{limit: MaxQueryBytes}
			transport.command = func(*sgHeader) (bool, error) {
				calls++
				return true, cause
			}
			transport.ioctl = func(operation uintptr, pointer unsafe.Pointer) error {
				if operation != sgRequestTable {
					t.Fatalf("unexpected native operation=%x", operation)
				}
				queries++
				if queries == 1 {
					(*[16]sgRequest)(pointer)[0].State = 1
				}
				return nil
			}
			transport.serializedTransport = newSerializedTransport(transport.executeCommand, func() error {
				closes++
				return nil
			})

			// Settlement observes the existing request; it cannot replay or publish the command.
			command := Command{CDB: make([]byte, 6), Direction: NoData, Timeout: time.Second}
			response, err := transport.Execute(context.Background(), command)
			if !errors.Is(err, ErrUncertain) || !errors.Is(err, cause) || response.Completed ||
				calls != 1 || queries != 2 || transport.unsettled {
				t.Fatalf("response=%v err=%v calls=%d queries=%d unsettled=%t",
					response, err, calls, queries, transport.unsettled)
			}
			if _, err := transport.Execute(context.Background(), command); !errors.Is(err, ErrUncertain) || calls != 1 {
				t.Fatalf("uncertain command replayed: err=%v calls=%d", err, calls)
			}
			if err := transport.Close(); !errors.Is(err, ErrUncertain) || closes != 1 {
				t.Fatalf("settled close=%v closes=%d", err, closes)
			}
		})
	}
}

func TestSGSplitUnknownSettlementRetainsOwner(t *testing.T) {
	// A native submission error keeps its buffers and descriptor when settlement cannot be observed.
	transport := &sgTransport{limit: MaxQueryBytes}
	transport.command = func(*sgHeader) (bool, error) { return true, syscall.EIO }
	transport.ioctl = func(operation uintptr, _ unsafe.Pointer) error {
		if operation != sgRequestTable {
			t.Fatalf("unexpected native operation=%x", operation)
		}
		return syscall.EIO
	}
	released := false
	transport.serializedTransport = newSerializedTransport(transport.executeCommand, func() error {
		if transport.unsettled {
			return &UncertainError{Cause: errors.New("native owner retained")}
		}
		released = true
		return nil
	})
	command := Command{CDB: make([]byte, 6), Direction: NoData, Timeout: time.Second}
	if _, err := transport.Execute(context.Background(), command); !errors.Is(err, ErrUncertain) || !transport.unsettled {
		t.Fatalf("execute=%v unsettled=%t", err, transport.unsettled)
	}
	if err := transport.Close(); !errors.Is(err, ErrUncertain) || released {
		t.Fatalf("unknown close=%v released=%t", err, released)
	}
}
