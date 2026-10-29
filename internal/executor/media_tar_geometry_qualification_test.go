package executor

import (
	"testing"
	"time"

	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/internal/library"
	"github.com/samuelncui/yatm/internal/media/tape"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

func TestTarWriteGeometrySeparatesNativeStallsFromPeriodicSupplyPauses(t *testing.T) {
	for _, native := range []bool{false, true} {
		name := "periodic-supply-pauses"
		if native {
			name = "native-stalls-with-buffer-lag"
		}
		t.Run(name, func(t *testing.T) {
			// Mix full 512-KiB and short records while keeping the byte-period fixture fixed.
			_, report, profile := tarReadParametersFixture()
			report.GetTape().DensityCode = proto.Uint32(0x58)
			profile.RecordBytes = 512 << 10
			stored := tarMapMediaFixture()
			profile.InitializationId = stored.Profile.GetTape().GetTar().InitializationId
			stored.Profile.GetTape().Tar = profile
			collector, reason := newTarWriteGeometryLearning(report, profile,
				tape.Position{ObjectKnown: true, Object: 244}, nil)
			require.NotNil(t, collector, reason)
			stamp := time.Unix(1, 0)
			var boundaries []uint64
			for bin := range 288*10 + 144 {
				if bin != 0 && bin%288 == 0 {
					boundaries = append(boundaries, collector.cursor)
				}
				seconds := .45
				if bin > 288 && bin%288 == 4 {
					if native {
						seconds += 6
					} else {
						stamp = stamp.Add(6 * time.Second)
					}
				}
				for remaining := collector.width; remaining != 0; {
					bytes := min(remaining, uint64(profile.RecordBytes))
					if collector.cursor%3 == 0 {
						bytes = min(bytes, uint64(128<<10))
					}
					elapsed := time.Duration(seconds * float64(bytes) / float64(collector.width) * float64(time.Second))
					collector.Observe(tape.CommandObservation{Sequence: collector.sequence + 1, Opcode: 0x0a,
						StartedAt: stamp, Elapsed: elapsed, Known: true, Bytes: int(bytes)})
					stamp = stamp.Add(elapsed + time.Microsecond)
					remaining -= bytes
				}
			}

			// Host pauses alone cannot create a wrap map, even with a strong byte-period recurrence.
			eod := collector.cursor + 1
			value, reason := collector.Finish(tape.Position{ObjectKnown: true, Object: eod}, 0)
			require.Nil(t, collector.bins, "settlement releases the raw observation bins")
			if !native {
				require.Nil(t, value)
				require.NotEmpty(t, reason)
				return
			}

			// This small injected lag must fit the empirical allowance, including object/byte conversion.
			require.NotNil(t, value, reason)
			require.Len(t, value.Wraps, len(boundaries)+1)
			for i, actual := range boundaries {
				estimated := value.Wraps[i].GetEndpoint()
				errorObjects := max(actual, estimated) - min(actual, estimated)
				require.NotZero(t, errorObjects, "the fixture must retain its injected buffering delay")
				require.LessOrEqual(t, errorObjects, value.Wraps[i].GetEndpointUncertaintyObjects())
			}
			last := value.Wraps[len(value.Wraps)-1]
			require.False(t, last.Full)
			require.Equal(t, eod, last.GetEndpoint())
			require.Equal(t, eod, value.Partitions[0].GetEod())
			require.GreaterOrEqual(t, last.GetEstimatedSpanObjects(), eod-value.Wraps[len(value.Wraps)-2].GetEndpoint())
			require.NoError(t, library.ValidateTapePositionMap(stored, value))
		})
	}
}

func TestTarWriteGeometryAppendRequiresBoundInitializationAndPartialTail(t *testing.T) {
	// The same source layout may be resumed only from the retained actual EOD of this initialization.
	_, report, profile := tarReadParametersFixture()
	report.GetTape().DensityCode = proto.Uint32(0x58)
	profile.InitializationId = "geometry-append"
	prefix := &entity.TapePositionMap{InitializationId: profile.InitializationId,
		EndpointConvention: entity.TapePositionEndpointConvention_TAPE_POSITION_ENDPOINT_CONVENTION_EXCLUSIVE,
		Partitions:         []*entity.TapePositionPartition{{Eod: proto.Uint64(110592)}},
		Wraps: []*entity.TapePositionWrap{
			{Wrap: 0, Full: true, Endpoint: proto.Uint64(73728),
				Direction: entity.TapePositionDirection_TAPE_POSITION_DIRECTION_FORWARD},
			{Wrap: 1, Endpoint: proto.Uint64(110592), EstimatedSpanObjects: proto.Uint64(73728),
				Direction: entity.TapePositionDirection_TAPE_POSITION_DIRECTION_REVERSE},
		}}
	start := tape.Position{ObjectKnown: true, Object: 110592}
	collector, reason := newTarWriteGeometryLearning(report, profile, start, prefix)
	require.NotNil(t, collector, reason)
	require.Equal(t, uint32(1), collector.firstWrap)
	before := proto.Clone(prefix)

	// A stale or incomplete descriptor must not manufacture the starting wrap ordinal.
	for name, change := range map[string]func(*entity.TapePositionMap){
		"different initialization": func(value *entity.TapePositionMap) { value.InitializationId = "replacement" },
		"different EOD":            func(value *entity.TapePositionMap) { value.Partitions[0].Eod = proto.Uint64(110593) },
		"full final wrap":          func(value *entity.TapePositionMap) { value.Wraps[1].Full = true },
		"unknown partial scale":    func(value *entity.TapePositionMap) { value.Wraps[1].EstimatedSpanObjects = nil },
		"missing earlier endpoint": func(value *entity.TapePositionMap) { value.Wraps[0].Endpoint = nil },
		"wrong direction":          func(value *entity.TapePositionMap) { value.Wraps[1].Direction = value.Wraps[0].Direction },
		"wrong band":               func(value *entity.TapePositionMap) { value.Wraps[1].Band = 1 },
	} {
		t.Run(name, func(t *testing.T) {
			value := proto.Clone(prefix).(*entity.TapePositionMap)
			change(value)
			next, reason := newTarWriteGeometryLearning(report, profile, start, value)
			require.Nil(t, next)
			require.NotEmpty(t, reason)
			require.True(t, proto.Equal(before, prefix))
		})
	}

	// An incomplete new attempt leaves the old map intact, rather than replacing its tail with a guess.
	value, reason := collector.Finish(start, 0)
	require.Nil(t, value)
	require.NotEmpty(t, reason)
	require.Nil(t, collector.bins)
	require.True(t, proto.Equal(before, prefix))
}
