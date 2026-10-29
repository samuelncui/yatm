package tape

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"syscall"
	"time"
	"unsafe"
)

// These structures follow Linux v6.12 include/scsi/sg.h with native pointer alignment.
// https://github.com/torvalds/linux/blob/v6.12/include/scsi/sg.h
// No SG direct or device-mapped I/O is requested.
type sgHeader struct {
	InterfaceID, Direction                            int32
	CommandLength, SenseLength                        uint8
	IovecCount                                        uint16
	DataLength                                        uint32
	Data, Command, Sense                              *byte
	Timeout, Flags                                    uint32
	PackID                                            int32
	User                                              *byte
	Status, MaskedStatus, MessageStatus, SenseWritten uint8
	HostStatus, DriverStatus                          uint16
	Residual                                          int32
	Duration, Info                                    uint32
}

type sgRequest struct {
	State, Orphan, Owned, Problem byte
	PackID                        int32
	User                          *byte
	Duration                      uint32
	Unused                        int32
}

const (
	sgIO              = 0x2285
	sgVersion         = 0x2282
	sgReservedSize    = 0x2272
	sgSetReservedSize = 0x2275
	sgForcePackID     = 0x227b
	sgRequestTable    = 0x2286
)

type sgTransport struct {
	*serializedTransport
	fd        int
	limit     int
	unsettled bool
	ioctl     func(uintptr, unsafe.Pointer) error
	command   func(*sgHeader) (bool, error)
	reserve   func(int) error
	capacity  *capacityTransport
}

// TryExecute keeps optional observation visible through the ordinary transport decorators.
// Only the exact capacity command uses the independently owned native query lane.
func (t *sgTransport) TryExecute(ctx context.Context, command Command, onIssue func() error) (Response, bool, error) {
	if t.capacity != nil && exactCapacityCommand(command) {
		return t.capacity.tryExecute(ctx, command, onIssue)
	}
	return t.serializedTransport.TryExecute(ctx, command, onIssue)
}

func (t *sgTransport) Close() error {
	// Stop optional admission before waiting for either native owner to settle and release resources.
	var queryErr error
	if t.capacity != nil {
		queryErr = t.capacity.Close()
	}
	return errors.Join(queryErr, t.serializedTransport.Close())
}

// TransferLimit is this descriptor's current reserved buffer, not the hardware's maximum transfer.
func (t *sgTransport) TransferLimit() int { return t.limit }

// EnsureTransfer reserves the explicitly selected size and refuses an insufficient readback.
// Linux caps this per-descriptor buffer by the adapter; Close releases it without device controls.
func (t *sgTransport) EnsureTransfer(ctx context.Context, size int) error {
	// Buffer changes share command admission and never run beside an active command.
	if size <= 0 || size > MaxTransferBytes {
		return fmt.Errorf("Tape transfer reservation is outside the supported envelope")
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.lane:
	}
	defer func() { t.lane <- struct{}{} }()
	if err := ctx.Err(); err != nil {
		return err
	}
	if t.closed || t.uncertain != nil {
		return errors.Join(t.uncertain, fmt.Errorf("SG transport is unavailable"))
	}
	if size <= t.limit {
		return nil
	}

	// SG_SET_RESERVED_SIZE may round or cap allocation; actual size remains the only usable bound.
	value := int32(size)
	if err := t.ioctl(sgSetReservedSize, unsafe.Pointer(&value)); err != nil {
		return fmt.Errorf("reserve selected SG transfer failed, %w", err)
	}
	if err := t.ioctl(sgReservedSize, unsafe.Pointer(&value)); err != nil {
		return fmt.Errorf("read SG reservation failed, %w", err)
	}
	limit := min(int(value), MaxTransferBytes)
	if t.reserve != nil {
		if err := t.reserve(limit); err != nil {
			return fmt.Errorf("reserve native command buffers failed, %w", err)
		}
	}
	t.limit = limit
	if t.limit < size {
		return fmt.Errorf("SG reserved buffer %d is smaller than selected record %d", t.limit, size)
	}
	return nil
}

func (t *sgTransport) executeCommand(command Command) (Response, error) {
	// Keep caller buffers alive until the synchronous exchange and any settlement finish.
	if len(command.Data) > t.limit {
		return Response{}, fmt.Errorf("Tape transfer exceeds SG buffer limit")
	}
	sense := make([]byte, 252)
	header := sgHeader{
		InterfaceID: 'S', Direction: int32(command.Direction), CommandLength: uint8(len(command.CDB)),
		SenseLength: uint8(len(sense)), DataLength: uint32(len(command.Data)),
		Command: &command.CDB[0], Sense: &sense[0], Timeout: uint32(command.Timeout.Milliseconds()),
	}
	if len(command.Data) != 0 {
		header.Data = &command.Data[0]
	}
	deadline := time.Now().Add(command.Timeout)
	var err error
	var pending bool
	if t.command != nil {
		pending, err = t.command(&header)
	} else {
		err = t.ioctl(sgIO, unsafe.Pointer(&header))
		pending = errors.Is(err, syscall.EINTR)
	}
	defer runtime.KeepAlive(command)
	defer runtime.KeepAlive(sense)
	defer runtime.KeepAlive(header)
	if err != nil {
		// A failed submission or receive cannot authorize reuse before the existing request settles.
		if pending {
			if settleErr := t.settlePending(deadline); settleErr != nil {
				t.unsettled = true
				return Response{}, &UncertainError{Cause: errors.Join(err, settleErr)}
			}
			return Response{}, &UncertainError{Cause: err}
		}
		if exactCapacityCommand(command) && capacitySubmissionRejected(err) {
			// A proven pre-submission rejection disables only the optional query.
			return Response{}, fmt.Errorf("capacity query rejected before submission (%v): %w", err, ErrUnsupported)
		}
		if errors.Is(err, syscall.EINVAL) || errors.Is(err, syscall.ENOTTY) || errors.Is(err, syscall.EACCES) || errors.Is(err, syscall.EPERM) {
			return Response{}, fmt.Errorf("SG command rejected, %w", err)
		}
		return Response{}, &UncertainError{Cause: err}
	}

	// Native completion statuses remain evidence even when command acceptance is unknown.
	if int(header.SenseWritten) > len(sense) {
		return Response{}, &UncertainError{Cause: ErrMalformed}
	}
	response := Response{
		Completed: true,
		Status:    header.Status, MessageStatus: header.MessageStatus, HostStatus: header.HostStatus, DriverStatus: header.DriverStatus,
		Residual: header.Residual, Sense: append([]byte(nil), sense[:header.SenseWritten]...),
	}
	if header.Info&6 != 0 {
		return response, &UncertainError{Cause: fmt.Errorf("SG completion used an unexpected transfer mode, info=%x", header.Info)}
	}
	if response.MessageStatus != 0 || response.HostStatus != 0 || response.DriverStatus&0xf != 0 && response.DriverStatus&0xf != 8 {
		if err := checkCommandResponse(command, response); onlyCapacityQueryError(err) {
			return response, err
		}
		return response, &UncertainError{Cause: fmt.Errorf("SG host/driver completion failed, host=%x driver=%x", response.HostStatus, response.DriverStatus)}
	}
	return response, nil
}

func (t *sgTransport) settlePending(deadline time.Time) error {
	// Linux v6.12 sg.c exposes active requests without submitting another command.
	// https://github.com/torvalds/linux/blob/v6.12/drivers/scsi/sg.c
	for {
		var requests [16]sgRequest
		if err := t.ioctl(sgRequestTable, unsafe.Pointer(&requests[0])); err != nil {
			return fmt.Errorf("observe pending SG command failed, %w", err)
		}
		active := false
		for _, request := range requests {
			if request.State > 3 {
				return ErrMalformed
			}
			active = active || request.State == 1
		}
		if !active {
			return nil
		}
		if !time.Now().Before(deadline) {
			return fmt.Errorf("pending SG command did not settle before its timeout")
		}
		time.Sleep(min(10*time.Millisecond, time.Until(deadline)))
	}
}
