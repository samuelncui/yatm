package executor

import (
	"encoding/json"
	"errors"
	"maps"
	"os"
	"testing"
	"time"

	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/internal/media/tape"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

func TestLTFSReadCompletionUsesBoundDataEODRatherThanIndexStart(t *testing.T) {
	values := ltfsAttributeFixture()
	facts, err := readLTFSReadFacts(t.Context(), func(name string, _ int) ([]byte, error) {
		return values[name], nil
	})
	require.NoError(t, err)
	value := &tapePositionMap{eod: map[uint32]uint64{0: 14, 1: 200}}
	reads := 0
	end, reason := ltfsReadDataEnd(facts, value, func(name string, limit int) ([]byte, error) {
		reads++
		require.Equal(t, "ltfs.indexLocation", name, "never read the mutating ltfs.sync attribute")
		require.Equal(t, 128, limit)
		return values[name], nil
	})
	require.Empty(t, reason)
	require.Equal(t, 1, reads)
	require.Equal(t, &entity.TapeBytePosition{Partition: 1, LogicalObject: 200}, end)

	for _, test := range []struct {
		name     string
		location string
		err      error
		edit     func(*tapePositionMap)
	}{
		{name: "missing attribute", err: errors.New("unsupported")},
		{name: "Index currently on DP", location: "b:180"},
		{name: "invalid Index pointer", location: "a:not-a-block"},
		{name: "zero Index pointer", location: "a:0"},
		{name: "unknown DP EOD", location: "a:8", edit: func(v *tapePositionMap) { delete(v.eod, 1) }},
	} {
		t.Run(test.name, func(t *testing.T) {
			copy := &tapePositionMap{eod: maps.Clone(value.eod)}
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
	facts := &ltfsReadFacts{uuid: "204d1c2f-c755-44cf-ac70-8541ded5f005", generation: 7}
	snapshot := &ltfsReadSnapshot{source: "A3/1F/45", observedAt: time.Now(),
		endpoints: []tape.WrapEndpoint{{Partition: 0, Object: 100}, {Partition: 0, Wrap: 1, Object: 180},
			{Partition: 1, Wrap: 4, Object: 8}}}
	value, reason := ltfsReadPositionMap(parameters, facts, snapshot)
	require.NotNil(t, value, reason)
	require.Equal(t, uint64(100), *value.wraps[1].EstimatedSpanObjects)
	require.Nil(t, value.wraps[2].EstimatedSpanObjects)
	require.Equal(t, uint64(180), value.eod[0])
	model, reason := compileTapePositionMap(parameters, 262144, value, func() bool { return true })
	require.NotNil(t, model, reason)
	_, known := model.AccessScore(&entity.TapeBytePosition{}, &entity.TapeBytePosition{LogicalObject: 180})
	require.True(t, known)
	_, known = model.AccessScore(&entity.TapeBytePosition{}, &entity.TapeBytePosition{LogicalObject: 181})
	require.False(t, known, "an estimated partial denominator cannot extend actual EOD")
	_, known = model.AccessScore(&entity.TapeBytePosition{Partition: 1}, &entity.TapeBytePosition{Partition: 1, LogicalObject: 1})
	require.False(t, known, "a partition with no complete wrap retains unknown coverage")

	// Without current reported wraps the Session has no map and keeps LOGICAL order.
	missing, reason := ltfsReadPositionMap(parameters, facts, &ltfsReadSnapshot{})
	require.Nil(t, missing)
	require.NotEmpty(t, reason)
}

func TestLTFSCartridgeDirectoryUsesTheSameConfirmedIntervals(t *testing.T) {
	// Acquire the captured CM through the actual Device fallback, with REOWP unavailable.
	data, err := os.ReadFile("../media/tape/testdata/cm/hp-lto5-directory-v4.bin")
	require.NoError(t, err)
	provider := &tarReadMapProvider{tarReportingFixture: &tarReportingFixture{tarDeviceFixture: newTarDeviceFixture()}, cm: data, partitions: 2}
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
	facts := &ltfsReadFacts{uuid: "204d1c2f-c755-44cf-ac70-8541ded5f005", generation: 7}
	snapshot := &ltfsReadSnapshot{source: geometry.Source, observedAt: geometry.ObservedAt,
		endpoints: geometry.Endpoints}
	value, reason := ltfsReadPositionMap(parameters, facts, snapshot)
	require.NotNil(t, value, reason)
	require.Equal(t, uint64(36765), *value.wraps[1].Endpoint)
	require.Equal(t, uint64(786468), value.eod[1])
	model, reason := compileTapePositionMap(parameters, 262144, value, geometry.Valid)
	require.NotNil(t, model, reason)

	// Proven identical intervals price identically regardless of source; no extra query occurs during costing.
	expectedData, err := os.ReadFile("../media/tape/testdata/cm/hp-lto5-directory-v4-endpoints.json")
	require.NoError(t, err)
	var expectedEndpoints []tape.WrapEndpoint
	require.NoError(t, json.Unmarshal(expectedData, &expectedEndpoints))
	confirmed, reason := ltfsReadPositionMap(parameters, facts, &ltfsReadSnapshot{
		endpoints: expectedEndpoints, source: "Independently captured endpoints with explicit count semantics",
		observedAt: geometry.ObservedAt,
	})
	require.NotNil(t, confirmed, reason)
	reference, reason := compileTapePositionMap(parameters, 262144, confirmed, geometry.Valid)
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
}

func TestLTFSReadOrderPositionMapRefusesUnequalFullWraps(t *testing.T) {
	// One full wrap holding 1% more objects than the others, as many short blocks do, refuses the map.
	parameters := tarNormalizedParameters()
	parameters.Geometry.NominalWraps = proto.Uint32(80)
	facts := &ltfsReadFacts{uuid: "204d1c2f-c755-44cf-ac70-8541ded5f005", generation: 7}
	endpoints := []tape.WrapEndpoint{{Partition: 1, Wrap: 4, Object: 10000}, {Partition: 1, Wrap: 5, Object: 20000},
		{Partition: 1, Wrap: 6, Object: 30100}, {Partition: 1, Wrap: 7, Object: 35000}}
	value, reason := ltfsReadPositionMap(parameters, facts, &ltfsReadSnapshot{source: "A3/1F/45", observedAt: time.Now(), endpoints: endpoints})
	require.Nil(t, value)
	require.Contains(t, reason, "unequal object counts")

	// Within half a percent of the median the map stays.
	endpoints[2].Object = 30049
	value, reason = ltfsReadPositionMap(parameters, facts, &ltfsReadSnapshot{source: "A3/1F/45", observedAt: time.Now(), endpoints: endpoints})
	require.NotNil(t, value, reason)

}
