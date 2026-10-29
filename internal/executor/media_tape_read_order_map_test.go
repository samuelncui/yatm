package executor

import (
	"encoding/json"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/internal/library"
	"github.com/samuelncui/yatm/internal/media/tape"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

func TestLTFSReadCompletionUsesBoundDataEODRatherThanIndexStart(t *testing.T) {
	values := ltfsAttributeFixture()
	facts, err := readLTFSReadFacts(t.Context(), nil, func(name string, _ int) ([]byte, error) {
		return values[name], nil
	})
	require.NoError(t, err)
	value := &entity.TapePositionMap{LtfsUuid: facts.uuid, LtfsIndexGeneration: proto.Uint64(facts.generation),
		Partitions: []*entity.TapePositionPartition{{Partition: 0, Eod: proto.Uint64(200)}, {Partition: 1, Eod: proto.Uint64(14)}}}
	reads := 0
	end, reason := ltfsReadDataEnd(facts, value, func(name string, limit int) ([]byte, error) {
		reads++
		require.Equal(t, "ltfs.indexLocation", name, "never read the mutating ltfs.sync attribute")
		require.Equal(t, 128, limit)
		return values[name], nil
	})
	require.Empty(t, reason)
	require.Equal(t, 1, reads)
	require.Equal(t, &entity.TapeBytePosition{Partition: 0, LogicalObject: 200}, end)

	for _, test := range []struct {
		name     string
		location string
		err      error
		edit     func(*entity.TapePositionMap)
	}{
		{name: "missing attribute", err: errors.New("unsupported")},
		{name: "Index currently on DP", location: "q:180"},
		{name: "invalid Index pointer", location: "z:not-a-block"},
		{name: "zero Index pointer", location: "z:0"},
		{name: "another UUID", location: "z:8", edit: func(v *entity.TapePositionMap) { v.LtfsUuid = "another" }},
		{name: "another generation", location: "z:8", edit: func(v *entity.TapePositionMap) { v.LtfsIndexGeneration = proto.Uint64(8) }},
		{name: "unknown DP EOD", location: "z:8", edit: func(v *entity.TapePositionMap) { v.Partitions[0].Eod = nil }},
	} {
		t.Run(test.name, func(t *testing.T) {
			copy := proto.Clone(value).(*entity.TapePositionMap)
			if test.edit != nil {
				test.edit(copy)
			}
			end, reason := ltfsReadDataEnd(facts, copy, func(string, int) ([]byte, error) {
				return []byte(test.location), test.err
			})
			require.Nil(t, end)
			require.NotEmpty(t, reason)
		})
	}
}

func TestLTFSReadOrderPositionMapKeepsPartitionCoverageAndIndexOwner(t *testing.T) {
	// The data partition supplies a full-wrap scale; a short Index partition has no invented denominator.
	parameters := tarNormalizedParameters()
	stored := &library.Media{Kind: entity.MediaKind_MEDIA_KIND_TAPE,
		Profile: (&entity.TapeMediaProfile{Format: library.TapeFormatLTFSV1}).Pack()}
	facts := &ltfsReadFacts{uuid: "204d1c2f-c755-44cf-ac70-8541ded5f005", generation: 7}
	snapshot := &ltfsReadSnapshot{source: "A3/1F/45", observedAt: time.Now(),
		boundary: entity.TapePositionEndpointConvention_TAPE_POSITION_ENDPOINT_CONVENTION_INCLUSIVE,
		endpoints: []tape.WrapEndpoint{{Partition: 0, Object: 99}, {Partition: 0, Wrap: 1, Object: 180},
			{Partition: 1, Wrap: 4, Object: 8}}}
	value, observed, reason := ltfsReadPositionMap(parameters, stored, facts, snapshot)
	require.NotNil(t, value, reason)
	require.True(t, observed)
	require.Equal(t, uint64(100), value.Wraps[1].GetEstimatedSpanObjects())
	require.Nil(t, value.Wraps[2].EstimatedSpanObjects)
	require.Equal(t, uint64(180), value.Partitions[0].GetEod())
	model, reason := compileTapePositionMap(parameters, value, func() bool { return true })
	require.NotNil(t, model, reason)
	_, known := model.AccessScore(&entity.TapeBytePosition{}, &entity.TapeBytePosition{LogicalObject: 180})
	require.True(t, known)
	_, known = model.AccessScore(&entity.TapeBytePosition{}, &entity.TapeBytePosition{LogicalObject: 181})
	require.False(t, known, "an estimated partial denominator cannot extend actual EOD")
	_, known = model.AccessScore(&entity.TapeBytePosition{Partition: 1}, &entity.TapeBytePosition{Partition: 1, LogicalObject: 1})
	require.False(t, known, "a partition with no complete wrap retains unknown coverage")

	// Retained geometry can be reused only by the exact mounted UUID and captured Index generation.
	stored.TapePositionMap = value
	retained, observed, reason := ltfsReadPositionMap(parameters, stored, facts, &ltfsReadSnapshot{})
	require.NotNil(t, retained, reason)
	require.False(t, observed)
	require.True(t, proto.Equal(value, retained))
	for _, changed := range []*ltfsReadFacts{
		{uuid: "e20588e2-9df7-4a45-99c8-229de7b08796", generation: facts.generation},
		{uuid: facts.uuid, generation: facts.generation + 1},
	} {
		retained, _, reason := ltfsReadPositionMap(parameters, stored, changed, &ltfsReadSnapshot{})
		require.Nil(t, retained)
		require.Contains(t, reason, "matching retained")
	}
}

func TestLTFSCartridgeDirectoryUsesTheSameConfirmedIntervals(t *testing.T) {
	// Acquire the captured CM through the actual Device fallback, with REOWP unavailable.
	data, err := os.ReadFile("../media/tape/testdata/cm/hp-lto5-directory-v4.bin")
	require.NoError(t, err)
	provider := &tarReadMapProvider{tarReloadFixture: &tarReloadFixture{tarDeviceFixture: newTarDeviceFixture()}, cm: data, partitions: 2}
	identity, err := provider.Resolve("/dev/nst0")
	require.NoError(t, err)
	transport, err := provider.Open(identity)
	require.NoError(t, err)
	device, err := tape.NewDevice(t.Context(), identity, transport)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, device.Cleanup(t.Context())) })
	geometry, err := device.Geometry(t.Context())
	require.NoError(t, err)
	require.Len(t, geometry.Endpoints, 23)
	require.Equal(t, 3, provider.cmCalls)

	// A count of N objects ends at N, preserving the last partial wrap and partition binding.
	parameters := tarNormalizedParameters()
	parameters.Geometry.NominalWraps = proto.Uint32(80)
	stored := &library.Media{Kind: entity.MediaKind_MEDIA_KIND_TAPE,
		Profile: (&entity.TapeMediaProfile{Format: library.TapeFormatLTFSV1}).Pack()}
	facts := &ltfsReadFacts{uuid: "204d1c2f-c755-44cf-ac70-8541ded5f005", generation: 7}
	snapshot := &ltfsReadSnapshot{source: geometry.Source, observedAt: geometry.ObservedAt,
		endpoints: geometry.Endpoints, boundary: geometry.EndpointConvention}
	value, observed, reason := ltfsReadPositionMap(parameters, stored, facts, snapshot)
	require.NotNil(t, value, reason)
	require.True(t, observed)
	require.Equal(t, uint64(36765), value.Wraps[1].GetEndpoint())
	require.Equal(t, uint64(786468), value.Partitions[1].GetEod())
	model, reason := compileTapePositionMap(parameters, value, geometry.Valid)
	require.NotNil(t, model, reason)

	// Proven identical intervals price identically regardless of source; no extra query occurs during costing.
	expectedData, err := os.ReadFile("../media/tape/testdata/cm/hp-lto5-directory-v4-endpoints.json")
	require.NoError(t, err)
	var expectedEndpoints []tape.WrapEndpoint
	require.NoError(t, json.Unmarshal(expectedData, &expectedEndpoints))
	confirmed, _, reason := ltfsReadPositionMap(parameters, stored, facts, &ltfsReadSnapshot{
		endpoints: expectedEndpoints, source: "Independently captured endpoints with explicit count semantics",
		observedAt: geometry.ObservedAt, boundary: entity.TapePositionEndpointConvention_TAPE_POSITION_ENDPOINT_CONVENTION_EXCLUSIVE,
	})
	require.NotNil(t, confirmed, reason)
	reference, reason := compileTapePositionMap(parameters, confirmed, geometry.Valid)
	require.NotNil(t, reference, reason)
	for _, object := range []uint64{1, 36764, 36765, 36766, 73538, 73539, 772278, 786468} {
		from := &entity.TapeBytePosition{Partition: 1, LogicalObject: 1}
		to := &entity.TapeBytePosition{Partition: 1, LogicalObject: object}
		actual, known := model.AccessScore(from, to)
		expected, referenceKnown := reference.AccessScore(from, to)
		require.True(t, known)
		require.True(t, referenceKnown)
		require.Equal(t, expected, actual)
	}
	require.Equal(t, 3, provider.cmCalls)
	stored.TapePositionMap = value
	retained, fresh, reason := ltfsReadPositionMap(parameters, stored, facts, &ltfsReadSnapshot{})
	require.NotNil(t, retained, reason)
	require.False(t, fresh)
	require.True(t, proto.Equal(value, retained))
}
