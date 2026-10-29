//go:build linux

package tape

import (
	"bytes"
	"io"
	"math"
	"testing"
	"unsafe"

	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"
)

func sgExchangeTestHeader(data, sense, cdb []byte, direction Direction) sgHeader {
	header := sgHeader{InterfaceID: 'S', Direction: int32(direction), CommandLength: uint8(len(cdb)),
		SenseLength: uint8(len(sense)), DataLength: uint32(len(data)), Timeout: 600000, Command: &cdb[0]}
	if len(data) != 0 {
		header.Data = &data[0]
	}
	if len(sense) != 0 {
		header.Sense = &sense[0]
	}
	return header
}

func TestSGExchangeReceiveInterruptionsSubmitOnce(t *testing.T) {
	// The kernel can retain only native pointers, and the caller's buffers remain unchanged until matched receive.
	x := newSGExchange(-1)
	require.NoError(t, x.reserve(512))
	t.Cleanup(func() { require.NoError(t, x.closeBuffer()) })
	data, sense, cdb := bytes.Repeat([]byte{9}, 8), bytes.Repeat([]byte{9}, sgExchangeSenseBytes), []byte{8, 0, 0, 0, 8, 0}
	header := sgExchangeTestHeader(data, sense, cdb, FromDevice)
	original := header
	writes, reads, waits := 0, 0, 0
	var submitted sgHeader
	x.write = func(raw []byte) (int, error) {
		writes++
		submitted = *(*sgHeader)(unsafe.Pointer(&raw[0]))
		require.Equal(t, uint32(600000), submitted.Timeout)
		require.Equal(t, int32(1), submitted.PackID)
		require.NotSame(t, original.Data, submitted.Data)
		require.NotSame(t, original.Sense, submitted.Sense)
		require.NotSame(t, original.Command, submitted.Command)
		for _, pointer := range []*byte{submitted.Data, submitted.Sense, submitted.Command} {
			address, base := uintptr(unsafe.Pointer(pointer)), uintptr(unsafe.Pointer(&x.buffer[0]))
			require.True(t, address >= base && address < base+uintptr(len(x.buffer)))
		}
		require.Equal(t, cdb, unsafe.Slice(submitted.Command, int(submitted.CommandLength)))
		require.Equal(t, make([]byte, len(data)), unsafe.Slice(submitted.Data, len(data)))
		require.Equal(t, make([]byte, len(sense)), unsafe.Slice(submitted.Sense, len(sense)))
		return len(raw), nil
	}
	x.read = func(raw []byte) (int, error) {
		reads++
		selector := *(*sgHeader)(unsafe.Pointer(&raw[0]))
		require.Equal(t, int32('S'), selector.InterfaceID)
		require.Equal(t, submitted.Direction, selector.Direction)
		require.Negative(t, selector.Direction, "kernel must recognize the v3 receive selector")
		require.Equal(t, submitted.PackID, selector.PackID)
		legacyPackID := *(*int32)(unsafe.Pointer(&raw[8]))
		require.Zero(t, legacyPackID, "legacy pack_id overlaps the zero command/sense lengths and iovec count")
		if reads == 1 {
			return -1, unix.EINTR
		}
		if reads == 2 {
			return -1, unix.EAGAIN
		}
		copy(unsafe.Slice(submitted.Data, len(data)), "abcdeXYZ")
		copy(unsafe.Slice(submitted.Sense, len(sense)), []byte{0x70, 0, 5})
		reply := submitted
		reply.Status, reply.MaskedStatus, reply.DriverStatus = 2, 1, 8
		reply.SenseWritten, reply.Residual, reply.Duration, reply.Info = 3, 3, 17, 1
		copy(raw, sgHeaderBytes(&reply))
		return len(raw), nil
	}
	x.wait = func() error {
		waits++
		if waits == 1 {
			return unix.EINTR
		}
		return nil
	}

	// Repeated receives/poll preserve one submission, raw response fields and bounded copyback.
	require.NoError(t, x.execute(&header))
	require.Equal(t, 1, writes)
	require.Equal(t, 3, reads)
	require.Equal(t, 2, waits)
	require.False(t, x.pending)
	require.Equal(t, []byte{'a', 'b', 'c', 'd', 'e', 9, 9, 9}, data)
	require.Equal(t, []byte{0x70, 0, 5}, sense[:3])
	require.Equal(t, byte(9), sense[3])
	require.Same(t, original.Data, header.Data)
	require.Same(t, original.Sense, header.Sense)
	require.Same(t, original.Command, header.Command)
	require.Equal(t, original.User, header.User)
	require.Equal(t, uint8(2), header.Status)
	require.Equal(t, uint8(1), header.MaskedStatus)
	require.Equal(t, uint16(8), header.DriverStatus)
	require.Equal(t, int32(3), header.Residual)
	require.Equal(t, uint8(3), header.SenseWritten)
	require.Equal(t, uint32(17), header.Duration)
	require.Equal(t, uint32(1), header.Info)
	require.Equal(t, make([]byte, len(x.buffer)), x.buffer)
}

func TestSGExchangeSubmissionNeverRetries(t *testing.T) {
	for _, tc := range []struct {
		name    string
		count   int
		err     error
		pending bool
	}{
		{"short", int(unsafe.Sizeof(sgHeader{})) - 1, nil, true},
		{"zero", 0, nil, true},
		{"interrupted", -1, unix.EINTR, true},
		{"busy", -1, unix.EAGAIN, true},
		{"I/O error", -1, unix.EIO, true},
		{"full count plus error", int(unsafe.Sizeof(sgHeader{})), unix.EIO, true},
		{"partial plus rejection", 1, unix.EINVAL, true},
		{"invalid before submit", -1, unix.EINVAL, false},
		{"unsupported before submit", -1, unix.ENOTTY, false},
		{"access before submit", -1, unix.EACCES, false},
		{"permission before submit", -1, unix.EPERM, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// A failed submission never appends or receives; unknown outcomes retain the native WRITE bytes.
			x := newSGExchange(-1)
			require.NoError(t, x.reserve(512))
			t.Cleanup(func() { require.NoError(t, x.closeBuffer()) }) // The mock has no live kernel request.
			data, sense, cdb := []byte("owned secret"), make([]byte, sgExchangeSenseBytes), []byte{0x0a, 0, 0, 0, 12, 0}
			header := sgExchangeTestHeader(data, sense, cdb, ToDevice)
			original := header
			writes := 0
			x.write = func(raw []byte) (int, error) {
				writes++
				native := (*sgHeader)(unsafe.Pointer(&raw[0]))
				require.Equal(t, data, unsafe.Slice(native.Data, len(data)))
				return tc.count, tc.err
			}
			x.read = func([]byte) (int, error) { t.Fatal("submission retried through receive"); return 0, nil }
			x.wait = func() error { t.Fatal("failed submission entered poll"); return nil }
			err := x.execute(&header)
			if tc.err == nil {
				require.ErrorIs(t, err, io.ErrShortWrite)
			} else {
				require.ErrorIs(t, err, tc.err)
			}
			require.Equal(t, 1, writes)
			require.Equal(t, tc.pending, x.pending)
			require.Equal(t, original, header)

			// Pending ownership forbids reuse or replacement; proven pre-submit failure clears copied secrets.
			if tc.pending {
				require.Equal(t, data, x.buffer[:len(data)])
				require.ErrorIs(t, x.execute(&header), ErrUncertain)
				require.ErrorIs(t, x.reserve(1024), ErrUncertain)
				require.Equal(t, 1, writes)
			} else {
				require.Equal(t, make([]byte, len(x.buffer)), x.buffer)
			}
		})
	}
}

func TestSGExchangeReceiveRequiresMatchingFullHeader(t *testing.T) {
	for _, tc := range []struct {
		name    string
		change  func(*sgHeader)
		short   bool
		readErr error
		pollErr error
		pending bool
	}{
		{name: "short header", short: true, pending: true},
		{name: "wrong PackID", change: func(h *sgHeader) { h.PackID++ }, pending: true},
		{name: "wrong layout", change: func(h *sgHeader) { h.InterfaceID = 0 }, pending: true},
		{name: "wrong pointer", change: func(h *sgHeader) { h.Data = h.Command }, pending: true},
		{name: "wrong timeout", change: func(h *sgHeader) { h.Timeout++ }, pending: true},
		{name: "wrong length", change: func(h *sgHeader) { h.DataLength++ }, pending: true},
		{name: "receive error", readErr: unix.EIO, pending: true},
		{name: "receive validation errno", readErr: unix.EINVAL, pending: true},
		{name: "receive permission errno", readErr: unix.EPERM, pending: true},
		{name: "poll failure", pollErr: unix.EIO, pending: true},
		{name: "negative residual", change: func(h *sgHeader) { h.Residual = -1 }},
		{name: "oversized residual", change: func(h *sgHeader) { h.Residual = int32(h.DataLength) + 1 }},
		{name: "oversized sense", change: func(h *sgHeader) { h.SenseWritten = h.SenseLength + 1 }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Invalid correlation retains ownership; matched dequeue settles even when response counts are malformed.
			x := newSGExchange(-1)
			require.NoError(t, x.reserve(512))
			t.Cleanup(func() { require.NoError(t, x.closeBuffer()) }) // All mock kernel work is settled here.
			data, sense, cdb := bytes.Repeat([]byte{9}, 8), bytes.Repeat([]byte{9}, sgExchangeSenseBytes), []byte{8, 0, 0, 0, 8, 0}
			header := sgExchangeTestHeader(data, sense, cdb, FromDevice)
			original := header
			writes, reads := 0, 0
			var submitted sgHeader
			x.write = func(raw []byte) (int, error) {
				writes++
				submitted = *(*sgHeader)(unsafe.Pointer(&raw[0]))
				copy(unsafe.Slice(submitted.Data, len(data)), "new data")
				copy(unsafe.Slice(submitted.Sense, len(sense)), "sense")
				return len(raw), nil
			}
			x.read = func(raw []byte) (int, error) {
				reads++
				if tc.readErr != nil {
					return -1, tc.readErr
				}
				if tc.pollErr != nil {
					return -1, unix.EAGAIN
				}
				reply := submitted
				reply.SenseWritten = 5
				if tc.change != nil {
					tc.change(&reply)
				}
				copy(raw, sgHeaderBytes(&reply))
				if tc.short {
					return len(raw) - 1, nil
				}
				return len(raw), nil
			}
			x.wait = func() error { return tc.pollErr }
			err := x.execute(&header)
			if tc.readErr != nil {
				require.ErrorIs(t, err, tc.readErr)
			} else if tc.pollErr != nil {
				require.ErrorIs(t, err, tc.pollErr)
			} else {
				require.ErrorIs(t, err, ErrMalformed)
			}
			require.Equal(t, 1, writes)
			require.Equal(t, 1, reads)
			require.Equal(t, tc.pending, x.pending)
			require.Equal(t, bytes.Repeat([]byte{9}, len(data)), data)
			require.Equal(t, bytes.Repeat([]byte{9}, len(sense)), sense)
			require.Same(t, original.Data, header.Data)
			require.Same(t, original.Command, header.Command)
			require.Same(t, original.Sense, header.Sense)
			if tc.pending {
				require.Equal(t, []byte("new data"), x.buffer[:len(data)])
			} else {
				require.Equal(t, make([]byte, len(x.buffer)), x.buffer)
			}
		})
	}
}

func TestSGExchangeReservationAndNativeReuse(t *testing.T) {
	// Reservations replace only idle native mappings and retain their original full slices for unmap.
	x := newSGExchange(-1)
	require.NoError(t, x.reserve(512))
	t.Cleanup(func() { require.NoError(t, x.closeBuffer()) })
	first := uintptr(unsafe.Pointer(&x.buffer[0]))
	require.NoError(t, x.reserve(512))
	require.Equal(t, first, uintptr(unsafe.Pointer(&x.buffer[0])))
	x.buffer[0] = 9
	require.NoError(t, x.reserve(1024))
	require.NotEqual(t, first, uintptr(unsafe.Pointer(&x.buffer[0])), "new mapping is installed while the old address is still reserved")
	require.Len(t, x.buffer, 1024+sgExchangeSenseBytes+16)
	require.Equal(t, make([]byte, len(x.buffer)), x.buffer)
	for _, invalid := range []int{0, -1, MaxTransferBytes + 1} {
		require.Error(t, x.reserve(invalid))
		require.Len(t, x.buffer, 1024+sgExchangeSenseBytes+16)
	}

	// Native CDB/sense state is cleared between commands and bounded PackIDs wrap only after settled dequeue.
	x.packID = math.MaxInt32 - 1
	var submitted sgHeader
	var ids []int32
	x.write = func(raw []byte) (int, error) {
		submitted = *(*sgHeader)(unsafe.Pointer(&raw[0]))
		ids = append(ids, submitted.PackID)
		require.Equal(t, uint32(600000), submitted.Timeout)
		require.Equal(t, make([]byte, sgExchangeSenseBytes), unsafe.Slice(submitted.Sense, sgExchangeSenseBytes))
		native := unsafe.Slice(submitted.Command, 16)
		require.Equal(t, make([]byte, 16-int(submitted.CommandLength)), native[submitted.CommandLength:])
		return len(raw), nil
	}
	x.read = func(raw []byte) (int, error) { copy(raw, sgHeaderBytes(&submitted)); return len(raw), nil }
	for _, length := range []int{16, 6} {
		cdb, sense := bytes.Repeat([]byte{9}, length), make([]byte, sgExchangeSenseBytes)
		header := sgExchangeTestHeader(nil, sense, cdb, NoData)
		require.NoError(t, x.execute(&header))
		require.Same(t, &cdb[0], header.Command)
		require.Equal(t, make([]byte, len(x.buffer)), x.buffer)
	}
	require.Equal(t, []int32{math.MaxInt32, 1}, ids)
	require.NoError(t, x.closeBuffer())
	require.Nil(t, x.buffer)
	require.NoError(t, x.closeBuffer())
}

func BenchmarkSGExchangeReadCopy(b *testing.B) {
	for _, size := range []int{256 << 10, 1 << 20} {
		b.Run(map[int]string{256 << 10: "256KiB", 1 << 20: "1MiB"}[size], func(b *testing.B) {
			// This measures absolute host copying/clearing with mocked native fill, not hardware throughput.
			x := newSGExchange(-1)
			if err := x.reserve(size); err != nil {
				b.Fatal(err)
			}
			b.Cleanup(func() {
				if err := x.closeBuffer(); err != nil {
					b.Fatal(err)
				}
			})
			payload, data, sense := bytes.Repeat([]byte{7}, size), make([]byte, size), make([]byte, sgExchangeSenseBytes)
			cdb := []byte{8, 0, byte(size >> 16), byte(size >> 8), byte(size), 0}
			header := sgExchangeTestHeader(data, sense, cdb, FromDevice)
			var submitted sgHeader
			x.write = func(raw []byte) (int, error) {
				submitted = *(*sgHeader)(unsafe.Pointer(&raw[0]))
				return len(raw), nil
			}
			x.read = func(raw []byte) (int, error) {
				copy(unsafe.Slice(submitted.Data, size), payload)
				copy(raw, sgHeaderBytes(&submitted))
				return len(raw), nil
			}
			b.SetBytes(int64(size))
			b.ReportAllocs()
			b.ResetTimer()
			for b.Loop() {
				if err := x.execute(&header); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func TestSGCapacityExchangeOwnsBuffersAndRequestsTailPlacement(t *testing.T) {
	// A held tail-placed query cannot own or overwrite the primary exchange's mapping/PackID.
	query, primary := newSGExchange(-1), newSGExchange(-1)
	require.NoError(t, query.reserve(MaxQueryBytes))
	require.NoError(t, primary.reserve(512))
	t.Cleanup(func() { require.NoError(t, query.closeBuffer()); require.NoError(t, primary.closeBuffer()) })
	query.flags = sgFlagQueueAtTail
	query.packID, primary.packID = 40, 8
	queryEntered, queryRelease := make(chan struct{}), make(chan struct{})
	var queryHeader, primaryHeader sgHeader
	query.write = func(raw []byte) (int, error) {
		queryHeader = *(*sgHeader)(unsafe.Pointer(&raw[0]))
		require.Equal(t, uint32(0x10), queryHeader.Flags)
		require.Equal(t, int32(41), queryHeader.PackID)
		require.Equal(t, capacityCommand().CDB, unsafe.Slice(queryHeader.Command, int(queryHeader.CommandLength)))
		close(queryEntered)
		return len(raw), nil
	}
	query.read = func(raw []byte) (int, error) {
		<-queryRelease
		copy(unsafe.Slice(queryHeader.Data, int(queryHeader.DataLength)), capacityPage(1, 2))
		queryHeader.Residual = MaxQueryBytes - 20
		copy(raw, sgHeaderBytes(&queryHeader))
		return len(raw), nil
	}
	primary.write = func(raw []byte) (int, error) {
		primaryHeader = *(*sgHeader)(unsafe.Pointer(&raw[0]))
		require.Zero(t, primaryHeader.Flags)
		require.Equal(t, int32(9), primaryHeader.PackID)
		require.NotSame(t, queryHeader.Command, primaryHeader.Command)
		require.NotSame(t, queryHeader.Data, primaryHeader.Data)
		require.NotSame(t, queryHeader.Sense, primaryHeader.Sense)
		require.Equal(t, []byte{7}, unsafe.Slice(primaryHeader.Data, 1))
		return len(raw), nil
	}
	primary.read = func(raw []byte) (int, error) { copy(raw, sgHeaderBytes(&primaryHeader)); return len(raw), nil }
	command := capacityCommand()
	header := sgExchangeTestHeader(command.Data, make([]byte, sgExchangeSenseBytes), command.CDB, FromDevice)
	queryDone := make(chan error, 1)
	go func() { queryDone <- query.execute(&header) }()
	<-queryEntered

	// WRITE submits/receives while query is still pending, and query input correlation stays intact.
	write := capacityTestWrite()
	writeHeader := sgExchangeTestHeader(write.Data, make([]byte, sgExchangeSenseBytes), write.CDB, ToDevice)
	require.NoError(t, primary.execute(&writeHeader))
	require.True(t, query.pending)
	require.Equal(t, command.CDB, unsafe.Slice(queryHeader.Command, int(queryHeader.CommandLength)))
	close(queryRelease)
	require.NoError(t, capacityTestWait(t, queryDone))
	require.Equal(t, capacityPage(1, 2), command.Data[:20])
	require.False(t, query.pending)
	require.Equal(t, make([]byte, len(primary.buffer)), primary.buffer)
	require.Equal(t, make([]byte, len(query.buffer)), query.buffer)
}
