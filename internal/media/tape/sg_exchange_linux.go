//go:build linux

package tape

import (
	"errors"
	"fmt"
	"io"
	"math"
	"unsafe"

	"golang.org/x/sys/unix"
)

const sgExchangeSenseBytes = 252

// Linux include/scsi/sg.h: SG_FLAG_Q_AT_TAIL requests tail placement, without a priority guarantee.
const sgFlagQueueAtTail = 0x10

// sgExchange owns address-stable buffers on one private, force-PackID SG-v3 descriptor.
// Its caller owns the command lane and must establish settlement before releasing the mapping.
type sgExchange struct {
	buffer  []byte
	packID  int32
	pending bool
	flags   uint32
	write   func([]byte) (int, error)
	read    func([]byte) (int, error)
	wait    func() error
}

func newSGExchange(fd int) *sgExchange {
	return &sgExchange{
		write: func(data []byte) (int, error) { return unix.Write(fd, data) },
		read:  func(data []byte) (int, error) { return unix.Read(fd, data) },
		wait: func() error {
			// Readiness only permits receiving the existing request; the kernel retains its command timeout.
			fds := []unix.PollFd{{Fd: int32(fd), Events: unix.POLLIN}}
			if _, err := unix.Poll(fds, -1); err != nil {
				return err
			}
			if fds[0].Revents&unix.POLLIN == 0 {
				return fmt.Errorf("SG receive poll has no readable response, events=%x: %w", fds[0].Revents, unix.EIO)
			}
			return nil
		},
	}
}

func (x *sgExchange) reserve(limit int) error {
	// Only a healthy idle lane can replace memory that a submitted request might still reference.
	if x.pending {
		return &UncertainError{Cause: errors.New("SG request still owns its native buffers")}
	}
	if limit <= 0 || limit > MaxTransferBytes {
		return fmt.Errorf("SG native transfer reservation is outside the supported envelope")
	}
	size := limit + sgExchangeSenseBytes + 16
	if len(x.buffer) == size {
		return nil
	}

	// Install a complete new mapping before clearing and releasing the previous idle reservation.
	buffer, err := unix.Mmap(-1, 0, size, unix.PROT_READ|unix.PROT_WRITE, unix.MAP_PRIVATE|unix.MAP_ANON)
	if err != nil {
		return fmt.Errorf("allocate SG native buffers failed, %w", err)
	}
	previous := x.buffer
	x.buffer = buffer
	if previous != nil {
		clear(previous)
		if err := unix.Munmap(previous); err != nil {
			return fmt.Errorf("release previous SG native buffers failed, %w", err)
		}
	}
	return nil
}

func (x *sgExchange) execute(header *sgHeader) error {
	// Keep all kernel-retained pointers outside Go memory, without replacing the caller's owned buffers.
	if x.pending {
		return &UncertainError{Cause: errors.New("SG request has not been received")}
	}
	limit := len(x.buffer) - sgExchangeSenseBytes - 16
	if limit <= 0 || header.InterfaceID != 'S' || header.CommandLength < 6 || header.CommandLength > 16 ||
		header.IovecCount != 0 || header.Flags != 0 || header.User != nil || header.Command == nil ||
		int(header.SenseLength) > sgExchangeSenseBytes || header.SenseLength != 0 && header.Sense == nil ||
		uint64(header.DataLength) > uint64(limit) || header.DataLength != 0 && header.Data == nil ||
		header.Direction != int32(NoData) && header.Direction != int32(FromDevice) && header.Direction != int32(ToDevice) ||
		header.Direction == int32(NoData) && header.DataLength != 0 {
		return fmt.Errorf("SG native header or buffer envelope is invalid: %w", ErrMalformed)
	}
	original := *header
	data := x.buffer[:int(original.DataLength)]
	sense := x.buffer[limit : limit+sgExchangeSenseBytes]
	cdb := x.buffer[limit+sgExchangeSenseBytes:]
	clear(data)
	clear(sense)
	clear(cdb)
	copy(cdb, unsafe.Slice(original.Command, int(original.CommandLength)))
	if original.Direction == int32(ToDevice) {
		copy(data, unsafe.Slice(original.Data, len(data)))
	}
	defer func() {
		if !x.pending {
			clear(data)
			clear(sense)
			clear(cdb)
		}
	}()

	// PackID is bounded correlation for one request, never a token that permits command resubmission.
	if x.packID == math.MaxInt32 {
		x.packID = 0
	}
	x.packID++
	submitted := sgHeader{InterfaceID: original.InterfaceID, Direction: original.Direction,
		CommandLength: original.CommandLength, SenseLength: original.SenseLength, DataLength: original.DataLength,
		Command: &cdb[0], Timeout: original.Timeout, PackID: x.packID, Flags: x.flags}
	if len(data) != 0 {
		submitted.Data = &data[0]
	}
	if original.SenseLength != 0 {
		submitted.Sense = &sense[0]
	}
	x.pending = true
	count, err := x.write(sgHeaderBytes(&submitted))
	if err != nil {
		if count <= 0 && (errors.Is(err, unix.EINVAL) || errors.Is(err, unix.ENOTTY) ||
			errors.Is(err, unix.EACCES) || errors.Is(err, unix.EPERM)) {
			x.pending = false
		}
		return fmt.Errorf("submit SG header failed, %w", err)
	}
	if count != int(unsafe.Sizeof(submitted)) {
		return fmt.Errorf("submit SG header count=%d: %w", count, io.ErrShortWrite)
	}

	// Only receives and readiness waits repeat; there is no second write after any submission outcome.
	for {
		received := sgHeader{InterfaceID: 'S', Direction: submitted.Direction, PackID: submitted.PackID}
		count, err := x.read(sgHeaderBytes(&received))
		if count <= 0 && errors.Is(err, unix.EINTR) {
			continue
		}
		if count <= 0 && errors.Is(err, unix.EAGAIN) {
			for {
				err := x.wait()
				if errors.Is(err, unix.EINTR) {
					continue
				}
				if err != nil {
					return fmt.Errorf("wait for SG response failed, %w", err)
				}
				break
			}
			continue
		}
		if err != nil {
			return fmt.Errorf("receive SG header failed, %w", err)
		}
		if count != int(unsafe.Sizeof(received)) {
			return fmt.Errorf("receive SG header count=%d: %w", count, ErrMalformed)
		}

		// A full response must preserve every submitted input field, including its native pointer identities.
		input := received
		input.Status, input.MaskedStatus, input.MessageStatus, input.SenseWritten = 0, 0, 0, 0
		input.HostStatus, input.DriverStatus, input.Residual, input.Duration, input.Info = 0, 0, 0, 0, 0
		if input != submitted {
			return fmt.Errorf("received SG header differs from submitted request: %w", ErrMalformed)
		}
		x.pending = false
		*header = received
		header.Data, header.Command, header.Sense, header.User = original.Data, original.Command, original.Sense, original.User
		if received.Residual < 0 || uint64(received.Residual) > uint64(original.DataLength) ||
			received.SenseWritten > original.SenseLength {
			return fmt.Errorf("received SG residual or sense count is invalid: %w", ErrMalformed)
		}
		if original.Direction == int32(FromDevice) {
			count := int(original.DataLength) - int(received.Residual)
			copy(unsafe.Slice(original.Data, count), data[:count])
		}
		copy(unsafe.Slice(original.Sense, int(received.SenseWritten)), sense[:received.SenseWritten])
		return nil
	}
}

func (x *sgExchange) closeBuffer() error {
	// The owner has already established settlement; pending alone cannot disprove that external evidence.
	if x.buffer == nil {
		return nil
	}
	clear(x.buffer)
	if err := unix.Munmap(x.buffer); err != nil {
		return fmt.Errorf("release SG native buffers failed, %w", err)
	}
	x.buffer = nil
	x.pending = false
	return nil
}

func sgHeaderBytes(header *sgHeader) []byte {
	return unsafe.Slice((*byte)(unsafe.Pointer(header)), int(unsafe.Sizeof(*header)))
}
