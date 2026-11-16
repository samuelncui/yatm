package executor

import (
	"context"
	"encoding/binary"

	"github.com/samuelncui/yatm/internal/media/tape"
)

// tarReportingFixture adds an HP Ultrium identity and one complete REOWP report to synthetic SSC storage.
type tarReportingFixture struct {
	*tarDeviceFixture
}

func (f *tarReportingFixture) Open(identity tape.Identity) (tape.Transport, error) {
	transport, err := f.tarDeviceFixture.Open(identity)
	if err != nil {
		return nil, err
	}
	return &tarReportingHandle{Transport: transport, fixture: f}, nil
}

type tarReportingHandle struct {
	tape.Transport
	fixture *tarReportingFixture
}

func (h *tarReportingHandle) Execute(ctx context.Context, command tape.Command) (tape.Response, error) {
	if err := ctx.Err(); err != nil {
		return tape.Response{}, err
	}
	f := h.fixture

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
