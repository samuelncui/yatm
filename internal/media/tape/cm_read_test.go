package tape

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestReadCartridgeMemoryChunks(t *testing.T) {
	// The documented 8,160-byte image requires a full query buffer and a 4,064-byte tail.
	want := make([]byte, 8160)
	for i := range want {
		want[i] = byte(i*31 + 7)
	}
	cdbs := [][]byte{
		{0x3c, 3, 0x10, 0, 0, 0, 0, 0, 4, 0},
		{0x3c, 2, 0x10, 0, 0, 0, 0, 0x10, 0, 0},
		{0x3c, 2, 0x10, 0, 0x10, 0, 0, 0x0f, 0xe0, 0},
	}
	responses := [][]byte{{0, 0, 0x1f, 0xe0}, want[:4096], want[4096:]}
	calls := 0
	transport := newSerializedTransport(func(command Command) (Response, error) {
		if calls >= len(cdbs) || !bytes.Equal(command.CDB, cdbs[calls]) ||
			len(command.Data) != len(responses[calls]) || command.Direction != FromDevice ||
			command.Timeout != 2*time.Second || !passiveCommand(command) {
			t.Fatalf("command %d: CDB=%x bytes=%d timeout=%s", calls, command.CDB, len(command.Data), command.Timeout)
		}
		copy(command.Data, responses[calls])
		calls++
		return Response{Completed: true}, nil
	}, func() error { return nil })
	defer transport.Close()

	// Real query validation and transport serialization produce one complete image, without motion.
	got, err := readCartridgeMemory(t.Context(), transport)
	if err != nil || calls != 3 || !bytes.Equal(got, want) {
		t.Fatalf("bytes=%d calls=%d err=%v", len(got), calls, err)
	}
}

func TestReadCartridgeMemoryCapacity(t *testing.T) {
	for _, test := range []struct {
		capacity, calls int
		malformed       bool
	}{
		{0, 1, false}, {1, 2, false}, {4096, 2, false}, {4097, 3, false}, {8192, 3, false},
		{8193, 1, true}, {65536, 1, true}, {0xffffff, 1, true},
	} {
		t.Run(fmt.Sprint(test.capacity), func(t *testing.T) {
			// Capacity occupies all three descriptor bytes; rejected sizes never request image data.
			calls := 0
			transport := newSerializedTransport(func(command Command) (Response, error) {
				calls++
				if calls == 1 {
					copy(command.Data, []byte{0, byte(test.capacity >> 16), byte(test.capacity >> 8), byte(test.capacity)})
				}
				return Response{Completed: true}, nil
			}, func() error { return nil })
			defer transport.Close()

			// Unavailable memory remains absent; supported sizes include the exact 8 KiB ceiling.
			data, err := readCartridgeMemory(t.Context(), transport)
			if calls != test.calls || errors.Is(err, ErrMalformed) != test.malformed {
				t.Fatalf("calls=%d err=%v", calls, err)
			}
			if test.malformed || test.capacity == 0 {
				if data != nil || test.capacity == 0 && err != nil {
					t.Fatalf("unavailable image returned %d bytes, err=%v", len(data), err)
				}
			} else if err != nil || len(data) != test.capacity {
				t.Fatalf("bytes=%d err=%v", len(data), err)
			}
		})
	}
}

func TestReadCartridgeMemoryFailures(t *testing.T) {
	transportErr := errors.New("fixture transport error")
	for _, test := range []struct {
		name        string
		failAt      int
		response    Response
		err, want   error
		message     string
		unsupported bool
	}{
		{name: "empty descriptor", failAt: 1, response: Response{Completed: true, Residual: 4},
			want: ErrMalformed, message: "has 0 bytes, want 4"},
		{name: "short descriptor", failAt: 1, response: Response{Completed: true, Residual: 1},
			want: ErrMalformed, message: "has 3 bytes, want 4"},
		{name: "short first chunk", failAt: 2, response: Response{Completed: true, Residual: 1},
			want: ErrMalformed, message: "offset 0 has 4095 bytes, want 4096"},
		{name: "short last chunk", failAt: 3, response: Response{Completed: true, Residual: 1},
			want: ErrMalformed, message: "offset 4096 has 4063 bytes, want 4064"},
		{name: "absent descriptor completion", failAt: 1, want: ErrUncertain},
		{name: "absent first chunk completion", failAt: 2, want: ErrUncertain},
		{name: "absent last chunk completion", failAt: 3, want: ErrUncertain},
		{name: "host completion error", failAt: 2, response: Response{Completed: true, HostStatus: 3}, want: ErrUncertain},
		{name: "invalid residual", failAt: 2, response: Response{Completed: true, Residual: -1}, want: ErrUncertain},
		{name: "transport error", failAt: 2, response: Response{Completed: true}, err: transportErr, want: transportErr},
		{name: "unsupported descriptor", failAt: 1, unsupported: true,
			response: Response{Completed: true, Status: 2, Sense: fixedSense(0x70, 5, 0x20, 0, false, 0, 0)}},
	} {
		t.Run(test.name, func(t *testing.T) {
			// Faults at any boundary must return no partial image and must not retry another form.
			calls := 0
			transport := newSerializedTransport(func(command Command) (Response, error) {
				calls++
				if calls == test.failAt {
					return test.response, test.err
				}
				if calls == 1 {
					copy(command.Data, []byte{0, 0, 0x1f, 0xe0})
				}
				return Response{Completed: true}, nil
			}, func() error { return nil })
			defer transport.Close()
			data, err := readCartridgeMemory(t.Context(), transport)
			if data != nil || err == nil || calls != test.failAt ||
				test.want != nil && !errors.Is(err, test.want) || !strings.Contains(err.Error(), test.message) ||
				CleanUnsupported(err) != test.unsupported {
				t.Fatalf("bytes=%d calls=%d err=%v", len(data), calls, err)
			}

			// Unknown completion retains the transport's existing refusal of later device commands.
			if errors.Is(err, ErrUncertain) {
				_, nextErr := readCartridgeMemory(t.Context(), transport)
				if !errors.Is(nextErr, ErrUncertain) || calls != test.failAt {
					t.Fatalf("later read err=%v calls=%d", nextErr, calls)
				}
			}
		})
	}
}

func TestReadCartridgeMemoryCancellation(t *testing.T) {
	for _, at := range []int{0, 1, 2} {
		t.Run(fmt.Sprint(at), func(t *testing.T) {
			// Cancellation before admission or during a settled command never starts the next chunk.
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			if at == 0 {
				cancel()
			}
			calls := 0
			transport := newSerializedTransport(func(command Command) (Response, error) {
				calls++
				if calls == at {
					cancel()
				}
				if calls == 1 {
					copy(command.Data, []byte{0, 0, 0x1f, 0xe0})
				}
				return Response{Completed: true}, nil
			}, func() error { return nil })
			defer transport.Close()
			data, err := readCartridgeMemory(ctx, transport)
			if data != nil || !errors.Is(err, context.Canceled) || calls != at {
				t.Fatalf("bytes=%d calls=%d err=%v", len(data), calls, err)
			}
		})
	}
}

func TestPassiveCartridgeMemoryCommands(t *testing.T) {
	// Valid descriptor and data forms retain the existing 4 KiB individual query ceiling.
	for _, test := range []struct {
		cdb   []byte
		bytes int
	}{
		{[]byte{0x3c, 3, 0x10, 0, 0, 0, 0, 0, 4, 0}, 4},
		{[]byte{0x3c, 2, 0x10, 0, 0, 0, 0, 0x10, 0, 0}, 4096},
		{[]byte{0x3c, 2, 0x10, 0, 0x10, 0, 0, 0x0f, 0xe0, 0}, 4064},
		{[]byte{0x3c, 2, 0x10, 0, 0x10, 0, 0, 0x10, 0, 0}, 4096},
		{[]byte{0x3c, 2, 0x10, 0, 0x1f, 0xff, 0, 0, 1, 0}, 1},
	} {
		if !passiveCommand(Command{CDB: test.cdb, Data: make([]byte, test.bytes), Direction: FromDevice}) {
			t.Fatalf("valid READ BUFFER rejected: %x", test.cdb)
		}
	}

	// Unsupported modes, reserved fields and mismatched or out-of-bounds ranges stay forbidden.
	for _, test := range []struct {
		name  string
		cdb   []byte
		bytes int
	}{
		{"write buffer", []byte{0x3b, 2, 0x10, 0, 0, 0, 0, 0, 4, 0}, 4},
		{"combined mode", []byte{0x3c, 0, 0x10, 0, 0, 0, 0, 0, 4, 0}, 4},
		{"echo mode", []byte{0x3c, 0x0a, 0x10, 0, 0, 0, 0, 0, 4, 0}, 4},
		{"mode reserved bit", []byte{0x3c, 0x22, 0x10, 0, 0, 0, 0, 0, 4, 0}, 4},
		{"descriptor reserved bit", []byte{0x3c, 0x23, 0x10, 0, 0, 0, 0, 0, 4, 0}, 4},
		{"other buffer", []byte{0x3c, 2, 0x11, 0, 0, 0, 0, 0, 4, 0}, 4},
		{"control", []byte{0x3c, 2, 0x10, 0, 0, 0, 0, 0, 4, 1}, 4},
		{"descriptor high offset", []byte{0x3c, 3, 0x10, 1, 0, 0, 0, 0, 4, 0}, 4},
		{"descriptor middle offset", []byte{0x3c, 3, 0x10, 0, 1, 0, 0, 0, 4, 0}, 4},
		{"descriptor low offset", []byte{0x3c, 3, 0x10, 0, 0, 1, 0, 0, 4, 0}, 4},
		{"short descriptor", []byte{0x3c, 3, 0x10, 0, 0, 0, 0, 0, 3, 0}, 3},
		{"long descriptor", []byte{0x3c, 3, 0x10, 0, 0, 0, 0, 0, 5, 0}, 5},
		{"short CDB", []byte{0x3c, 3, 0x10, 0, 0, 0, 0, 0, 4}, 4},
		{"long CDB", []byte{0x3c, 3, 0x10, 0, 0, 0, 0, 0, 4, 0, 0}, 4},
		{"allocation mismatch", []byte{0x3c, 2, 0x10, 0, 0, 0, 0, 0, 4, 0}, 3},
		{"empty allocation", []byte{0x3c, 2, 0x10, 0, 0, 0, 0, 0, 0, 0}, 0},
		{"high allocation", []byte{0x3c, 2, 0x10, 0, 0, 0, 1, 0, 4, 0}, 4},
		{"transfer limit", []byte{0x3c, 2, 0x10, 0, 0, 0, 0, 0x10, 1, 0}, 4097},
		{"high offset", []byte{0x3c, 2, 0x10, 1, 0, 0, 0, 0, 4, 0}, 4},
		{"offset at end", []byte{0x3c, 2, 0x10, 0, 0x20, 0, 0, 0, 1, 0}, 1},
		{"range past end", []byte{0x3c, 2, 0x10, 0, 0x10, 1, 0, 0x10, 0, 0}, 4096},
	} {
		t.Run(test.name, func(t *testing.T) {
			if passiveCommand(Command{CDB: test.cdb, Data: make([]byte, test.bytes), Direction: FromDevice}) {
				t.Fatalf("unsafe READ BUFFER admitted: %x", test.cdb)
			}
		})
	}

	// Changing direction cannot turn the same allowlisted fields into a write or no-data command.
	for _, direction := range []Direction{ToDevice, NoData} {
		command := Command{CDB: []byte{0x3c, 3, 0x10, 0, 0, 0, 0, 0, 4, 0}, Data: make([]byte, 4), Direction: direction}
		if passiveCommand(command) {
			t.Fatalf("unsafe READ BUFFER direction admitted: %d", direction)
		}
	}
}
