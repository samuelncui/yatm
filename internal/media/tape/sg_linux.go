//go:build linux

package tape

import (
	"errors"
	"fmt"
	"unsafe"

	"golang.org/x/sys/unix"
)

// OpenTransport opens a leased SG character device. Read-only opens accept passive query forms
// only. Its current kernel reserve bounds transfer buffers conservatively; it is not drive capacity.
func OpenTransport(path string, readOnly bool) (Transport, error) {
	t, err := openSGTransport(path, readOnly, nil)
	if err != nil {
		return nil, err
	}
	if !readOnly {
		t.capacity = newCapacityTransport(t.serializedTransport, func() (*serializedTransport, error) {
			// Associate the sibling with the still-owned original node, without another device probe.
			var original unix.Stat_t
			if err := unix.Fstat(t.fd, &original); err != nil {
				return nil, fmt.Errorf("stat original SG transport failed, %w", err)
			}
			query, err := openSGTransport(path, false, &original)
			if err != nil {
				return nil, err
			}
			return query.serializedTransport, nil
		})
	}
	return t, nil
}

func openSGTransport(path string, readOnly bool, original *unix.Stat_t) (_ *sgTransport, resultErr error) {
	// Validate the native access path without issuing a command or opening an st alias.
	flags := unix.O_RDWR | unix.O_CLOEXEC | unix.O_NONBLOCK
	if readOnly {
		flags = unix.O_RDONLY | unix.O_CLOEXEC | unix.O_NONBLOCK
	}
	fd, err := unix.Open(path, flags, 0)
	if err != nil {
		return nil, fmt.Errorf("open SG transport failed, %w", err)
	}
	owned := true
	var exchange *sgExchange
	defer func() {
		if owned {
			cleanupErr := unix.Close(fd)
			if exchange != nil {
				cleanupErr = errors.Join(cleanupErr, exchange.closeBuffer())
			}
			if cleanupErr != nil {
				resultErr = &UncertainError{Cause: errors.Join(resultErr, cleanupErr)}
			}
		}
	}()
	var info unix.Stat_t
	if err := unix.Fstat(fd, &info); err != nil {
		return nil, fmt.Errorf("stat SG transport failed, %w", err)
	}
	if original != nil && (info.Dev != original.Dev || info.Rdev != original.Rdev || info.Ino != original.Ino) {
		return nil, fmt.Errorf("capacity attachment SG identity differs from primary")
	}
	if info.Mode&unix.S_IFMT != unix.S_IFCHR {
		return nil, fmt.Errorf("SG transport is not a character device: %w", ErrUnsupported)
	}
	version, err := unix.IoctlGetInt(fd, sgVersion)
	if err != nil {
		if errors.Is(err, unix.ENOTTY) {
			if original == nil {
				return nil, fmt.Errorf("SG v3 transport unavailable: %w", errors.Join(ErrUnsupported, err))
			}
			return nil, fmt.Errorf("SG v3 transport unavailable (%v): %w", err, ErrUnsupported)
		}
		return nil, fmt.Errorf("query SG version failed, %w", err)
	}
	if version < 30000 {
		return nil, fmt.Errorf("SG v3 transport unavailable: %w", ErrUnsupported)
	}
	limit, err := unix.IoctlGetInt(fd, sgReservedSize)
	if err != nil {
		return nil, fmt.Errorf("query SG buffer limit failed, %w", err)
	}
	if limit < MaxQueryBytes {
		return nil, fmt.Errorf("SG buffer does not fit bounded queries: %w", ErrUnsupported)
	}

	// Passive inspection keeps read-only access; its allowlist contains idempotent queries only.
	t := &sgTransport{fd: fd, limit: min(limit, MaxTransferBytes)}
	t.ioctl = func(command uintptr, pointer unsafe.Pointer) error {
		_, _, errno := unix.Syscall(unix.SYS_IOCTL, uintptr(t.fd), command, uintptr(pointer))
		if errno != 0 {
			return errno
		}
		return nil
	}

	// State-changing commands separate submission from receipt so signal restart cannot replay them.
	if original != nil {
		t.limit = MaxQueryBytes
	}
	if !readOnly {
		if err := unix.IoctlSetPointerInt(fd, sgForcePackID, 1); err != nil {
			return nil, fmt.Errorf("enable SG response matching failed, %w", err)
		}
		exchange = newSGExchange(fd)
		if original != nil {
			exchange.flags = sgFlagQueueAtTail
		}
		if err := exchange.reserve(t.limit); err != nil {
			return nil, fmt.Errorf("reserve native command buffers failed, %w", err)
		}
		t.reserve = exchange.reserve
		t.command = func(header *sgHeader) (bool, error) {
			err := exchange.execute(header)
			return exchange.pending, err
		}
	}

	// The command owner also owns settlement, native buffers and final descriptor release.
	t.serializedTransport = newSerializedTransport(func(command Command) (Response, error) {
		if original != nil && !exactCapacityCommand(command) {
			return Response{}, fmt.Errorf("command is outside the private capacity allowlist")
		}
		if readOnly && !passiveCommand(command) {
			return Response{}, fmt.Errorf("command is not a passive Tape query")
		}
		return t.executeCommand(command)
	}, func() error {
		if t.unsettled {
			return &UncertainError{Cause: fmt.Errorf("SG descriptor retained while command settlement is unknown")}
		}
		err := unix.Close(t.fd)
		if exchange != nil {
			err = errors.Join(err, exchange.closeBuffer())
		}
		return err
	})
	owned = false
	return t, nil
}
