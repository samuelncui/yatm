package executor

import (
	"context"
	"testing"
	"time"

	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/internal/library"
	mediapkg "github.com/samuelncui/yatm/internal/media"
	"github.com/samuelncui/yatm/internal/media/tape"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

func cmCacheReport(firmware string) *entity.InspectDeviceResponse {
	return &entity.InspectDeviceResponse{Vendor: proto.String("HP"), Model: proto.String("Ultrium 5-SCSI"),
		Firmware: proto.String(firmware), Report: &entity.InspectDeviceResponse_Tape{
			Tape: &entity.TapeDeviceReport{DensityCode: proto.Uint32(0x58)},
		}}
}

func TestCMCacheLTFSRetainedScope(t *testing.T) {
	// Build a valid previous report exactly as the old LTFS source persisted it.
	parameters := tarNormalizedParameters()
	stored := &library.Media{Kind: entity.MediaKind_MEDIA_KIND_TAPE,
		Profile: (&entity.TapeMediaProfile{Format: library.TapeFormatLTFSV1}).Pack()}
	facts := &ltfsReadFacts{uuid: "204d1c2f-c755-44cf-ac70-8541ded5f005", generation: 7,
		partitionSource: "pre-mount SG LTFS Labels"}
	endpoints := []tape.WrapEndpoint{{Object: 99}, {Wrap: 1, Object: 180}}
	previous, _, reason := ltfsReadPositionMap(parameters, stored, facts, &ltfsReadSnapshot{
		endpoints: endpoints, source: "A3/1F/45", observedAt: time.Now(),
		boundary: entity.TapePositionEndpointConvention_TAPE_POSITION_ENDPOINT_CONVENTION_INCLUSIVE,
	})
	require.NotNil(t, previous, reason)

	// Only the qualified legacy derived interpretation is refused; explicit user sources keep their contract.
	for _, test := range []struct {
		name, firmware, source string
		boundary               entity.TapePositionEndpointConvention
		accepted               bool
	}{
		{"old qualified report", "Z6ED", previous.Source, previous.EndpointConvention, false},
		{"user inclusive", "Z6ED", "user layout", previous.EndpointConvention, true},
		{"already exclusive", "Z6ED", previous.Source, entity.TapePositionEndpointConvention_TAPE_POSITION_ENDPOINT_CONVENTION_EXCLUSIVE, true},
		{"other firmware", "0001", previous.Source, previous.EndpointConvention, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			value := proto.Clone(previous).(*entity.TapePositionMap)
			value.Source, value.EndpointConvention = test.source, test.boundary
			stored.TapePositionMap = value
			before := proto.Clone(value)
			got, observed, reason := ltfsReadPositionMap(parameters, stored, facts,
				&ltfsReadSnapshot{report: cmCacheReport(test.firmware)})
			require.Equal(t, test.accepted, got != nil, reason)
			require.False(t, observed)
			if !test.accepted {
				require.Contains(t, reason, "endpoint refresh")
			}
			require.True(t, proto.Equal(before, stored.TapePositionMap), "refusal does not rewrite a cached Media")
		})
	}

	// A current qualified report derives exclusive geometry while leaving the old input snapshot untouched.
	stored.TapePositionMap = previous
	before := proto.Clone(previous)
	current, observed, reason := ltfsReadPositionMap(parameters, stored, facts, &ltfsReadSnapshot{
		endpoints: endpoints, source: "A3/1F/45", observedAt: time.Now(), report: cmCacheReport("Z6ED"),
		boundary: tape.REOWPEndpointConvention(cmCacheReport("Z6ED")),
	})
	require.NotNil(t, current, reason)
	require.True(t, observed)
	require.Equal(t, entity.TapePositionEndpointConvention_TAPE_POSITION_ENDPOINT_CONVENTION_EXCLUSIVE, current.EndpointConvention)
	require.Equal(t, uint64(99), current.Wraps[0].GetEndpoint())
	require.True(t, proto.Equal(before, previous))
}

type cmCacheProvider struct {
	*tarReadMapProvider
	firmware string
}

func (p *cmCacheProvider) Open(identity tape.Identity) (tape.Transport, error) {
	transport, err := p.tarReadMapProvider.Open(identity)
	if err != nil {
		return nil, err
	}
	return &fakeTransport{Transport: transport, execute: func(ctx context.Context, command tape.Command) (tape.Response, error) {
		// The cache policy uses real inspection; synthetic hardware changes only the identified firmware/density.
		if command.CDB[0] == 0x3c {
			clear(command.Data)
			return tape.Response{Completed: true}, nil
		}
		response, err := transport.Execute(ctx, command)
		if err != nil {
			return response, err
		}
		if command.CDB[0] == 0x12 && command.CDB[1] == 0 {
			copy(command.Data[8:16], "HP      ")
			copy(command.Data[16:32], "Ultrium 5-SCSI   ")
			copy(command.Data[32:36], p.firmware)
		}
		return response, nil
	}}, nil
}

func TestCMCacheTarFactoryRetainedScope(t *testing.T) {
	for _, test := range []struct {
		name, firmware, source string
		boundary               entity.TapePositionEndpointConvention
		current, accepted      bool
	}{
		{"old qualified report", "Z6ED", "A3/1F/45", entity.TapePositionEndpointConvention_TAPE_POSITION_ENDPOINT_CONVENTION_INCLUSIVE, false, false},
		{"user inclusive", "Z6ED", "user layout", entity.TapePositionEndpointConvention_TAPE_POSITION_ENDPOINT_CONVENTION_INCLUSIVE, false, true},
		{"already exclusive", "Z6ED", "A3/1F/45", entity.TapePositionEndpointConvention_TAPE_POSITION_ENDPOINT_CONVENTION_EXCLUSIVE, false, true},
		{"other firmware", "0001", "A3/1F/45", entity.TapePositionEndpointConvention_TAPE_POSITION_ENDPOINT_CONVENTION_INCLUSIVE, false, true},
		{"current replaces legacy", "Z6ED", "A3/1F/45", entity.TapePositionEndpointConvention_TAPE_POSITION_ENDPOINT_CONVENTION_INCLUSIVE, true, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			// Produce a real tar label/member over synthetic records before exercising the public Session factory.
			exe, backend, fixture, _ := tarFactoryFixture(t)
			write, err := newTarFixtureWriteSession(backend.NewWriteSession(t.Context(), tarFormatTarget(entity.TapeRecordMode_TAPE_RECORD_MODE_VARIABLE)))
			require.NoError(t, err)
			tarWriteFixtureMember(t, write, "file", make([]byte, 32<<10))
			result, err := write.Finalize(t.Context(), nil)
			require.NoError(t, err)
			stored, err := exe.Lib().CreateMedia(t.Context(), &library.Media{Kind: result.Media.Kind,
				Identity: result.Media.Identity, Profile: result.Media.Profile})
			require.NoError(t, err)
			require.NotEmpty(t, stored.Profile.GetTape().Encryption)
			require.Equal(t, "AES-256-GCM-128", stored.Profile.GetTape().Tar.GetEncryptionAlgorithm())
			parameters := tarNormalizedParameters()
			parameters.AppliesTo.EncryptionAlgorithm = proto.String("AES-256-GCM-128")
			parameters.AppliesTo.RecordBytes = proto.Uint32(stored.Profile.GetTape().Tar.RecordBytes)
			parameters.AppliesTo.DensityCode, parameters.AppliesTo.Firmware = proto.Uint32(0x58), proto.String(test.firmware)
			endpoints := []tape.WrapEndpoint{{Object: 5}, {Wrap: 1, Object: uint64(len(fixture.records))}}
			geometry, closeGeometry := tarReadModelMap(t, endpoints)
			cached, reason := deriveTarPositionMap(parameters, stored.Profile.GetTape().Tar, geometry,
				mediapkg.ReadEndpointConvention(test.boundary))
			require.NotNil(t, cached, reason)
			require.NoError(t, closeGeometry())
			cached.Source = test.source
			stored, err = exe.Lib().UpdateMediaTapePositionMap(t.Context(), stored, cached)
			require.NoError(t, err)
			before := proto.Clone(stored.TapePositionMap)

			// No current directory leaves the cache policy visible; complete current endpoints permit replacement.
			fixture.mode[8] = 0x58
			mapped := &tarReadMapProvider{tarReloadFixture: &tarReloadFixture{tarDeviceFixture: fixture}}
			if test.current {
				mapped.endpoints = endpoints
			}
			exe.tapeProvider = &cmCacheProvider{tarReadMapProvider: mapped, firmware: test.firmware}
			_, err = exe.settings.Device.Save(t.Context(), &entity.DeviceSettings{Devices: []*entity.DeviceConfig{{Device: "/dev/nst0",
				Options: &entity.DeviceConfig_Tape{Tape: &entity.TapeDeviceSettings{ReadOrder: entity.TapeReadOrder_TAPE_READ_ORDER_AUTO,
					ReadParameters: parameters}},
			}}})
			require.NoError(t, err)
			ctx, err := exe.withDeviceSettings(t.Context())
			require.NoError(t, err)
			read, err := backend.NewReadSession(ctx, tarEncryptedReadTarget(stored))
			require.NoError(t, err)
			provider, reason, err := read.(mediapkg.OrderedReadSession).ReadOrder(ctx)
			require.NoError(t, err)
			require.Equal(t, test.accepted, provider != nil, reason)
			require.NoError(t, read.Finalize(ctx, nil))

			// Refusal is session-local; publication happens only for a real current replacement.
			reloaded, err := exe.Lib().GetMedia(t.Context(), stored.ID)
			require.NoError(t, err)
			require.True(t, proto.Equal(before, stored.TapePositionMap))
			if test.current {
				require.Equal(t, entity.TapePositionEndpointConvention_TAPE_POSITION_ENDPOINT_CONVENTION_EXCLUSIVE,
					reloaded.TapePositionMap.EndpointConvention)
			} else {
				require.True(t, proto.Equal(before, reloaded.TapePositionMap))
			}
		})
	}
}
