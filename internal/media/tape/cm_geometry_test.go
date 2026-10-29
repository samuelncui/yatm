package tape

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/samuelncui/yatm/entity"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

func TestCartridgeDirectoryPreparationFallback(t *testing.T) {
	for _, mode := range []string{"REOWP", "unsupported", "short", "malformed"} {
		t.Run(mode, func(t *testing.T) {
			// Real inspection sees the selected REOWP outcome; CM is only available to explicit Geometry.
			data, err := os.ReadFile("testdata/cm/hp-lto5-directory-v4.bin")
			require.NoError(t, err)
			fixture := newGeometryFixture()
			fixture.vendor = "HP"
			fixture.actualPartitions = 2
			fixture.unsupported, fixture.shortOnly = mode == "unsupported", mode == "short"
			if mode == "malformed" {
				fixture.data = []byte{0, 3, 0, 0, 0}
			}
			calls, partitionCalls := 0, 0
			transport := newSerializedTransport(func(command Command) (Response, error) {
				if command.CDB[0] == 0x8c && command.CDB[1] == 3 {
					partitionCalls++
				}
				if command.CDB[0] != 0x3c {
					return fixture.execute(command)
				}
				calls++
				if command.CDB[1] == 3 {
					copy(command.Data, []byte{0, 0, 0x1f, 0xe0})
				} else {
					offset := int(command.CDB[3])<<16 | int(command.CDB[4])<<8 | int(command.CDB[5])
					copy(command.Data, data[offset:])
				}
				return Response{Completed: true}, nil
			}, func() error { return nil })
			device, err := NewDevice(t.Context(), Identity{}, transport)
			require.NoError(t, err)
			require.Zero(t, calls, "ordinary Inspect must not query CM")
			require.Zero(t, partitionCalls, "ordinary Inspect must not add a CM partition query")
			before := feature(device.Report(), entity.DeviceFeatureKind_DEVICE_FEATURE_KIND_END_OF_WRAP_POSITION)

			// Repeated requests reuse the same complete observation without changing REOWP support evidence.
			observed, err := device.Geometry(t.Context())
			require.NoError(t, err)
			require.True(t, observed.Valid())
			wantCalls := 0
			if mode != "REOWP" {
				wantCalls = 3
				require.Len(t, observed.Endpoints, 23)
				require.Equal(t, entity.TapePositionEndpointConvention_TAPE_POSITION_ENDPOINT_CONVENTION_EXCLUSIVE,
					observed.EndpointConvention)
				require.Contains(t, observed.Source, "LTO-CM")
			}
			require.Equal(t, wantCalls, calls)
			require.Equal(t, wantCalls/3, partitionCalls)
			require.True(t, proto.Equal(before, feature(device.Report(), entity.DeviceFeatureKind_DEVICE_FEATURE_KIND_END_OF_WRAP_POSITION)))
			first := observed.Endpoints[0]
			observed.Endpoints[0].Object = 999
			again, err := device.Geometry(t.Context())
			require.NoError(t, err)
			require.Equal(t, first, again.Endpoints[0], "callers cannot mutate the cached endpoints")
			require.Equal(t, wantCalls, calls)
			require.Equal(t, wantCalls/3, partitionCalls, "CM snapshot reuses its partition authority")

			// Ownership invalidation prevents both stale use and implicit reacquisition within this session.
			device.invalidateGeometry()
			require.Zero(t, device.actualPartitions)
			require.False(t, observed.Valid())
			require.False(t, again.Valid())
			withdrawn, err := device.Geometry(t.Context())
			require.NoError(t, err)
			require.Nil(t, withdrawn)
			require.Equal(t, wantCalls, calls)
			require.NoError(t, device.Cleanup(t.Context()))
		})
	}
}

func TestCartridgeDirectoryReusesConfiguredPartitionCount(t *testing.T) {
	// tar Configure already queries actual partitions; lazy geometry must not add another MAM command.
	data, err := os.ReadFile("testdata/cm/hp-lto5-single-partition-v4.bin")
	require.NoError(t, err)
	fixture := newGeometryFixture()
	fixture.vendor, fixture.unsupported, fixture.actualPartitions = "HP", true, 1
	cmCalls, partitionCalls := 0, 0
	transport := newSerializedTransport(func(command Command) (Response, error) {
		if command.CDB[0] == 0x8c && command.CDB[1] == 3 {
			partitionCalls++
		}
		if command.CDB[0] != 0x3c {
			return fixture.execute(command)
		}
		cmCalls++
		if command.CDB[1] == 3 {
			copy(command.Data, []byte{0, 0, 0x1f, 0xe0})
		} else {
			offset := int(command.CDB[3])<<16 | int(command.CDB[4])<<8 | int(command.CDB[5])
			copy(command.Data, data[offset:])
		}
		return Response{Completed: true}, nil
	}, func() error { return nil })
	device, err := NewDevice(t.Context(), Identity{}, transport)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, device.Cleanup(context.Background())) })
	_, err = device.Configure(t.Context(), &entity.TarMediaProfile{
		RecordMode: entity.TapeRecordMode_TAPE_RECORD_MODE_VARIABLE, RecordBytes: 512,
	}, Read)
	require.NoError(t, err)
	require.Equal(t, uint32(1), device.actualPartitions)
	require.Equal(t, 1, partitionCalls)

	// Inactive EOD page presence cannot revive a second partition or request another source observation.
	observed, err := device.Geometry(t.Context())
	require.NoError(t, err)
	require.Len(t, observed.Endpoints, 35)
	require.Equal(t, 1, partitionCalls)
	require.Equal(t, 3, cmCalls)
	require.Equal(t, uint32(1), device.actualPartitions)
	device.invalidateGeometry()
	require.False(t, observed.Valid())
	require.Zero(t, device.actualPartitions)
	withdrawn, err := device.Geometry(t.Context())
	require.NoError(t, err)
	require.Nil(t, withdrawn)
	require.Equal(t, 1, partitionCalls)
	require.Equal(t, 3, cmCalls)
}

func TestCartridgeDirectoryPartitionQueryFailure(t *testing.T) {
	for _, test := range []struct {
		name     string
		response Response
		data     []byte
		err      error
		fatal    bool
	}{
		{name: "unsupported", response: Response{Completed: true, Status: 2, Sense: fixedSense(0x70, 5, 0x24, 0, false, 0, 0)}},
		{name: "unknown count", response: Response{Completed: true}, data: []byte{0, 2, 0, 0}},
		{name: "too many partitions", response: Response{Completed: true}, data: []byte{0, 2, 0, 3}},
		{name: "short response", response: Response{Completed: true, Residual: 1}, data: []byte{0, 2, 0, 1}},
		{name: "unknown completion", fatal: true},
		{name: "medium changed", response: Response{Completed: true, Status: 2, Sense: fixedSense(0x70, 6, 0x28, 0, false, 0, 0)}, fatal: true},
		{name: "medium absent", response: Response{Completed: true, Status: 2, Sense: fixedSense(0x70, 2, 0x3a, 0, false, 0, 0)}, fatal: true},
		{name: "canceled", err: context.Canceled, fatal: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			// Unknown actual partition count cannot be supplied by MODE page or inferred from CM page presence.
			fixture := newGeometryFixture()
			fixture.vendor, fixture.unsupported = "HP", true
			queries, cmCalls := 0, 0
			transport := newSerializedTransport(func(command Command) (Response, error) {
				if command.CDB[0] == 0x8c && command.CDB[1] == 3 {
					queries++
					copy(command.Data, test.data)
					return test.response, test.err
				}
				if command.CDB[0] == 0x3c {
					cmCalls++
				}
				return fixture.execute(command)
			}, func() error { return nil })
			device, err := NewDevice(t.Context(), Identity{}, transport)
			require.NoError(t, err)
			value, err := device.Geometry(t.Context())
			require.Nil(t, value)
			require.Equal(t, test.fatal, err != nil)
			require.Equal(t, 1, queries)
			require.Zero(t, cmCalls)

			// Failed preparation does not retry the count or start an image read; terminal cleanup keeps uncertainty.
			_, _ = device.Geometry(t.Context())
			require.Equal(t, 1, queries)
			require.Zero(t, cmCalls)
			cleanup := device.Cleanup(t.Context())
			require.Equal(t, errors.Is(err, ErrUncertain), errors.Is(cleanup, ErrUncertain))
		})
	}
}

func TestCartridgeDirectoryFailureBoundary(t *testing.T) {
	for _, test := range []struct {
		name     string
		response Response
		err      error
		fatal    bool
	}{
		{name: "unsupported", response: Response{Completed: true, Status: 2, Sense: fixedSense(0x70, 5, 0x24, 0, false, 0, 0)}},
		{name: "short", response: Response{Completed: true, Residual: 1}},
		{name: "no memory", response: Response{Completed: true}},
		{name: "unknown completion", fatal: true},
		{name: "medium lost", response: Response{Completed: true, Status: 2, Sense: fixedSense(0x70, 2, 0x3a, 0, false, 0, 0)}, fatal: true},
		{name: "medium changed", response: Response{Completed: true, Status: 2, Sense: fixedSense(0x70, 6, 0x28, 0, false, 0, 0)}, fatal: true},
		{name: "canceled", err: context.Canceled, fatal: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			// Failure happens only at the optional descriptor; no extra query or device action follows it.
			fixture := newGeometryFixture()
			fixture.vendor, fixture.unsupported = "HP", true
			calls := 0
			transport := newSerializedTransport(func(command Command) (Response, error) {
				if command.CDB[0] != 0x3c {
					return fixture.execute(command)
				}
				calls++
				return test.response, test.err
			}, func() error { return nil })
			device, err := NewDevice(t.Context(), Identity{}, transport)
			require.NoError(t, err)
			observed, err := device.Geometry(t.Context())
			require.Nil(t, observed)
			require.Equal(t, test.fatal, err != nil)
			require.Equal(t, 1, calls)
			require.Contains(t, feature(device.Report(), entity.DeviceFeatureKind_DEVICE_FEATURE_KIND_SOFTWARE_READ_ORDER).Detail,
				"Cartridge memory directory is unavailable")

			// Uncertain completion is retained by the real transport through cleanup; optional absence is not.
			uncertain := errors.Is(err, ErrUncertain)
			_, _ = device.Geometry(t.Context())
			require.Equal(t, 1, calls)
			cleanup := device.Cleanup(t.Context())
			require.Equal(t, uncertain, errors.Is(cleanup, ErrUncertain))
		})
	}
}

func TestREOWPEndpointConventionUsesOnlyQualifiedHP5Counts(t *testing.T) {
	// Count semantics are qualified for this observed response, not all HP or IBM generations.
	report := &entity.InspectDeviceResponse{Vendor: proto.String("HP"), Model: proto.String("Ultrium 5-SCSI"),
		Firmware: proto.String("Z6ED"), Report: &entity.InspectDeviceResponse_Tape{Tape: &entity.TapeDeviceReport{DensityCode: proto.Uint32(0x58)}}}
	require.Equal(t, entity.TapePositionEndpointConvention_TAPE_POSITION_ENDPOINT_CONVENTION_EXCLUSIVE, REOWPEndpointConvention(report))
	for _, change := range []func(*entity.InspectDeviceResponse){
		func(r *entity.InspectDeviceResponse) { r.Vendor = proto.String("IBM") },
		func(r *entity.InspectDeviceResponse) { r.Model = proto.String("Ultrium 6-SCSI") },
		func(r *entity.InspectDeviceResponse) { r.Firmware = proto.String("J38W") },
		func(r *entity.InspectDeviceResponse) { r.GetTape().DensityCode = nil },
	} {
		other := proto.Clone(report).(*entity.InspectDeviceResponse)
		change(other)
		require.Equal(t, entity.TapePositionEndpointConvention_TAPE_POSITION_ENDPOINT_CONVENTION_INCLUSIVE, REOWPEndpointConvention(other))
	}
}
