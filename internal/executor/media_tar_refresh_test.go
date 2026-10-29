package executor

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/samuelncui/acp"
	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/internal/library"
	mediapkg "github.com/samuelncui/yatm/internal/media"
	"github.com/samuelncui/yatm/internal/media/tape"
	"github.com/stretchr/testify/require"
)

// tarReloadFixture adds only the documented cycle/map observations to synthetic SSC storage.
type tarReloadFixture struct {
	*tarDeviceFixture
	barcodeAfterLoad string
}

func (f *tarReloadFixture) Open(identity tape.Identity) (tape.Transport, error) {
	transport, err := f.tarDeviceFixture.Open(identity)
	if err != nil {
		return nil, err
	}
	return &tarReloadHandle{Transport: transport, fixture: f}, nil
}

type tarReloadHandle struct {
	tape.Transport
	fixture *tarReloadFixture
	held    bool
}

func (h *tarReloadHandle) Execute(ctx context.Context, command tape.Command) (tape.Response, error) {
	// Only an exact synchronous non-ejecting HOLD followed by LOAD changes readiness.
	if err := ctx.Err(); err != nil {
		return tape.Response{}, err
	}
	f := h.fixture
	if bytes.Equal(command.CDB, []byte{0x1b, 0, 0, 0, 0, 0}) {
		return h.Transport.Execute(ctx, command)
	}
	if command.CDB[0] == 0x1b {
		if command.Direction != tape.NoData ||
			!bytes.Equal(command.CDB, []byte{0x1b, 0, 0, 0, command.CDB[4], 0}) ||
			command.CDB[4] != 8 && command.CDB[4] != 1 {
			return tape.Response{}, fmt.Errorf("invalid fixture HOLD/LOAD form")
		}
		if command.CDB[4] == 8 {
			h.held = true
			f.securityEvents = append(f.securityEvents, "hold")
		} else {
			if !h.held {
				return tape.Response{}, fmt.Errorf("fixture LOAD requires held Tape")
			}
			h.held, f.cursor = false, 0
			f.securityEvents = append(f.securityEvents, "load")
			if f.barcodeAfterLoad != "" {
				f.barcode = f.barcodeAfterLoad
			}
		}
		return tape.Response{Completed: true}, nil
	}
	if command.CDB[0] == 0 && h.held {
		sense := tarFixtureSense(2, 0, 4, 0)
		sense[12] = 0x3a
		return tape.Response{Completed: true, Status: 2, Sense: sense}, nil
	}

	// The reported endpoint is a fixture observation, never a physical freshness assertion.
	if command.CDB[0] == 0xa3 && command.CDB[1] == 0x1f {
		data := make([]byte, 16)
		binary.BigEndian.PutUint16(data, 14)
		object := uint64(len(f.records))
		for i := 15; i >= 10; i-- {
			data[i], object = byte(object), object>>8
		}
		copy(command.Data, data)
		f.securityEvents = append(f.securityEvents, "map")
		return tape.Response{Completed: true, Residual: int32(len(command.Data) - len(data))}, nil
	}
	if command.CDB[0] == 0x8c && command.CDB[1] == 0 {
		f.securityEvents = append(f.securityEvents, "barcode")
	}

	// Reuse ordinary records, mode/key controls and positive form reports from the shared fixture.
	response, err := h.Transport.Execute(ctx, command)
	if command.CDB[0] == 0x12 && command.CDB[1] == 0 && err == nil {
		copy(command.Data[8:16], "HP      ")
		copy(command.Data[16:32], "Ultrium fixture ")
	}
	if command.CDB[0] == 0xa3 && command.CDB[1] == 0x0c && command.CDB[3] == 0xa3 && err == nil {
		clear(command.Data)
		command.Data[1], command.Data[3], command.Data[4], command.Data[5] = 3, 12, 0xa3, 0x1f
		for i := 6; i < 16; i++ {
			command.Data[i] = 0xff
		}
		response.Residual = int32(len(command.Data) - 16)
	}
	return response, err
}

func TestTarFactoryRefreshRechecksCartridgeBeforeReadControls(t *testing.T) {
	for _, tc := range []struct {
		name       string
		missingMAM bool
		mismatch   bool
	}{
		{name: "encrypted"},
		{name: "missing MAM", missingMAM: true},
		{name: "changed MAM", mismatch: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Establish an ordinary recorded member and frozen Library expectation through the real factory.
			ctx := context.Background()
			exe, backend, fixture, jobID := tarFactoryFixture(t)
			exe.tapeProvider = &tarReloadFixture{tarDeviceFixture: fixture}
			original := append([]byte(nil), fixture.mode...)
			target := tarFormatTarget(entity.TapeRecordMode_TAPE_RECORD_MODE_VARIABLE)
			write, err := newTarFixtureWriteSession(backend.NewWriteSession(ctx, target))
			require.NoError(t, err)
			data := bytes.Repeat([]byte("refresh preserves content\n"), 500)
			tarWriteFixtureMember(t, write, "member.bin", data)
			result, err := write.Finalize(ctx, nil)
			require.NoError(t, err)
			var member *mediapkg.CompletedFile
			require.NoError(t, tarFixtureInventory(write, result, ctx, func(value *mediapkg.CompletedFile) error { member = value; return nil }))
			require.NotNil(t, member)
			stored, err := exe.Lib().CreateMedia(ctx, &library.Media{
				Kind: result.Media.Kind, Identity: result.Media.Identity, Profile: result.Media.Profile,
			})
			require.NoError(t, err)

			// A new leased read explicitly requests the cycle before keys, record settings or label reads.
			exe.endAttempt(jobID)
			require.NoError(t, exe.beginAttempt(jobID, func(error) {}))
			fixture.securityEvents = nil
			if tc.missingMAM {
				fixture.barcode = ""
			}
			if tc.mismatch {
				exe.tapeProvider.(*tarReloadFixture).barcodeAfterLoad = "OTHER1L5"
			}
			readTarget := tarEncryptedReadTarget(stored)
			readTarget.GetTape().RefreshPositionMap = true
			read, err := backend.NewReadSession(ctx, readTarget)
			if tc.missingMAM || tc.mismatch {
				require.ErrorContains(t, err, map[bool]string{true: "tar Tape identity is unavailable", false: "tar Tape barcode differs"}[tc.missingMAM])
				require.Nil(t, read)
				require.NotContains(t, fixture.securityEvents, "mode")
				require.NotContains(t, fixture.securityEvents, "script")
				require.NotContains(t, fixture.securityEvents, "rewind")
				require.NotContains(t, fixture.securityEvents, "read")
				require.Equal(t, fixture.opens, fixture.closes)
				return
			}
			require.NoError(t, err)
			load, barcode, firstControl := -1, -1, len(fixture.securityEvents)
			holds, loads := 0, 0
			for i, event := range fixture.securityEvents {
				switch event {
				case "hold":
					holds++
				case "load":
					load = i
					loads++
				case "barcode":
					if load >= 0 && barcode < 0 {
						barcode = i
					}
				case "mode", "script", "rewind", "read":
					firstControl = min(firstControl, i)
				}
			}
			require.Equal(t, 1, holds)
			require.Equal(t, 1, loads)
			require.Greater(t, load, 0)
			require.Greater(t, barcode, load)
			require.Greater(t, firstControl, barcode)

			// ACP verifies content after the cycle; normal unload applies the configured device key policy.
			source, err := read.Source(mediapkg.ReadRequest{Path: member.Path, Storage: member.Storage})
			require.NoError(t, err)
			output := filepath.Join(t.TempDir(), "restored")
			runTarFixtureStream(t, &tarFixtureItem{
				SimpleJob: &acp.SimpleJob{Path: member.Path, Dsts: []string{output}}, source: source.Content,
			})
			actual, err := os.ReadFile(output)
			require.NoError(t, err)
			require.Equal(t, data, actual)
			require.NoError(t, read.Finalize(ctx, nil))
			require.Equal(t, original, fixture.mode)
			require.Nil(t, fixture.cipherKey)
			require.Equal(t, fixture.opens, fixture.closes)
		})
	}
}
