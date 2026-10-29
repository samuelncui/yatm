package executor

import (
	"bytes"
	"context"
	"sync"
	"testing"

	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/internal/media/tape"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

type tarRecordDefaultsProvider struct {
	*tarDeviceFixture
	limits   [6]byte
	maximum  int
	lock     sync.Mutex
	commands []byte
	writes   []int
	fixed    []bool
}

func (p *tarRecordDefaultsProvider) Open(identity tape.Identity) (tape.Transport, error) {
	transport, err := p.tarDeviceFixture.Open(identity)
	if err != nil {
		return nil, err
	}
	return &fakeBoundedTransport{limit: p.maximum, Transport: &fakeTransport{Transport: transport, execute: func(ctx context.Context, command tape.Command) (tape.Response, error) {
		return p.execute(ctx, command, transport)
	}}}, nil
}

func (p *tarRecordDefaultsProvider) execute(ctx context.Context, command tape.Command, base tape.Transport) (tape.Response, error) {
	// Retain actual factory commands while varying only passive block limits and transport capacity.
	p.lock.Lock()
	p.commands = append(p.commands, command.CDB[0])
	if command.CDB[0] == 0x0a {
		p.writes = append(p.writes, len(command.Data))
		p.fixed = append(p.fixed, command.CDB[1]&1 != 0)
	}
	p.lock.Unlock()
	if command.CDB[0] == 5 {
		copy(command.Data, p.limits[:])
		return tape.Response{Completed: true, Residual: int32(len(command.Data) - len(p.limits))}, nil
	}
	return base.Execute(ctx, command)
}

func TestTarFactoryFormatRecordDefaultsAndOverrides(t *testing.T) {
	for _, test := range []struct {
		name            string
		saved, explicit *entity.TarWriteOptions
		mode            entity.TapeRecordMode
		bytes           uint32
	}{
		{name: "suite omission", mode: entity.TapeRecordMode_TAPE_RECORD_MODE_VARIABLE, bytes: 512 << 10},
		{name: "Device defaults", saved: &entity.TarWriteOptions{
			RecordMode: entity.TapeRecordMode_TAPE_RECORD_MODE_FIXED.Enum(), RecordBytes: proto.Uint32(8192),
		}, mode: entity.TapeRecordMode_TAPE_RECORD_MODE_FIXED, bytes: 8192},
		{name: "explicit overrides Device", saved: &entity.TarWriteOptions{
			RecordMode: entity.TapeRecordMode_TAPE_RECORD_MODE_FIXED.Enum(), RecordBytes: proto.Uint32(8192),
		}, explicit: &entity.TarWriteOptions{
			RecordMode: entity.TapeRecordMode_TAPE_RECORD_MODE_VARIABLE.Enum(), RecordBytes: proto.Uint32(1 << 20),
		}, mode: entity.TapeRecordMode_TAPE_RECORD_MODE_VARIABLE, bytes: 1 << 20},
	} {
		t.Run(test.name, func(t *testing.T) {
			// Use the real factory and passive SSC parser with a supported two-MiB transfer envelope.
			ctx := t.Context()
			exe, backend, fixture, _ := tarFactoryFixture(t)
			provider := &tarRecordDefaultsProvider{tarDeviceFixture: fixture, limits: [6]byte{0, 0x20, 0, 0, 0, 0}, maximum: 2 << 20}
			exe.tapeProvider = provider
			if test.saved != nil {
				_, err := exe.settings.Device.Save(ctx, &entity.DeviceSettings{Devices: []*entity.DeviceConfig{{
					Device: "/dev/nst0", Options: &entity.DeviceConfig_Tape{Tape: &entity.TapeDeviceSettings{TarWriteOptions: test.saved}},
				}}})
				require.NoError(t, err)
			} else {
				defaults, err := backend.tapeDefaults(ctx, "/dev/nst0")
				require.NoError(t, err)
				require.Nil(t, defaults.GetTarWriteOptions())
			}
			target := tarFormatTarget(entity.TapeRecordMode_TAPE_RECORD_MODE_VARIABLE)
			target.GetTape().TarOptions = test.explicit
			originalTarget := proto.Clone(target)
			originalMode := bytes.Clone(fixture.mode)

			// Omitted Job options must reach a usable FORMAT Session, not stop at structural resolution.
			session, err := backend.NewWriteSession(ctx, target)
			require.NoError(t, err)
			tarWriteFixtureMember(t, session, "default-record.bin", bytes.Repeat([]byte{0x5a}, int(test.bytes)+512))
			result, err := session.Finalize(ctx, nil)
			require.NoError(t, err)
			require.Equal(t, test.mode, result.Media.Profile.GetTape().Tar.RecordMode)
			require.Equal(t, test.bytes, result.Media.Profile.GetTape().Tar.RecordBytes)
			require.False(t, result.Media.Profile.GetTape().Tar.HardwareCompression)

			// Native WRITE frames use the chosen record envelope and cleanup restores original controls.
			require.Contains(t, provider.writes, int(test.bytes))
			for index, size := range provider.writes {
				require.LessOrEqual(t, size, int(test.bytes))
				require.Equal(t, test.mode == entity.TapeRecordMode_TAPE_RECORD_MODE_FIXED, provider.fixed[index])
			}
			require.True(t, proto.Equal(originalTarget, target), "resolution must not mutate the submitted Job options")
			require.Equal(t, originalMode, fixture.mode)
			require.Equal(t, fixture.opens, fixture.closes)
		})
	}
}

func TestTarFactoryFormatRecordDefaultsRejectUnsupportedBeforeMutation(t *testing.T) {
	for _, test := range []struct {
		name    string
		limits  [6]byte
		maximum int
		options *entity.TarWriteOptions
		error   string
	}{
		{name: "default exceeds block limit", limits: [6]byte{0, 4, 0, 0, 0, 0}, maximum: 2 << 20,
			error: "selected record is outside observed block limits"},
		{name: "fixed-only device rejects omitted variable mode", limits: [6]byte{0, 0, 0x10, 0, 0x10, 0}, maximum: 2 << 20,
			options: &entity.TarWriteOptions{RecordBytes: proto.Uint32(4096)}, error: "variable tar records are outside observed block limits"},
		{name: "default exceeds transport buffer", limits: [6]byte{0, 0x20, 0, 0, 0, 0}, maximum: 256 << 10,
			error: "selected record exceeds the transport buffer"},
	} {
		t.Run(test.name, func(t *testing.T) {
			// Device reports remain authoritative even when the suite fills omitted record choices.
			exe, backend, fixture, _ := tarFactoryFixture(t)
			provider := &tarRecordDefaultsProvider{tarDeviceFixture: fixture, limits: test.limits, maximum: test.maximum}
			exe.tapeProvider = provider
			target := tarFormatTarget(entity.TapeRecordMode_TAPE_RECORD_MODE_VARIABLE)
			target.GetTape().TarOptions = test.options
			originalTarget := proto.Clone(target)
			originalMode := bytes.Clone(fixture.mode)
			session, err := backend.NewWriteSession(t.Context(), target)
			require.Nil(t, session)
			require.ErrorContains(t, err, test.error)

			// Rejection must precede drive controls, movement, initialization and any content publication.
			for _, opcode := range provider.commands {
				require.NotContains(t, []byte{0x01, 0x04, 0x0a, 0x10, 0x11, 0x15, 0x19, 0x2b, 0x55, 0x92, 0xb5}, opcode)
			}
			require.Empty(t, fixture.records)
			require.Zero(t, fixture.writes)
			require.Zero(t, fixture.filemarks)
			require.Equal(t, originalMode, fixture.mode)
			require.True(t, proto.Equal(originalTarget, target))
			require.Equal(t, fixture.opens, fixture.closes)

			// A failed preparation cannot publish the proposed Media into Library.
			stored, err := exe.Lib().GetMediaByIdentity(t.Context(), entity.MediaKind_MEDIA_KIND_TAPE, target.GetTape().Barcode)
			require.NoError(t, err)
			require.Nil(t, stored)
		})
	}
}
