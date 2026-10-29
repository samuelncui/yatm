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

func TestTarWriteGeometryObservationUsesExistingPositionChecks(t *testing.T) {
	_, report, profile := tarReadParametersFixture()
	report.GetTape().DensityCode = proto.Uint32(0x58)
	profile.InitializationId = "geometry-observation"
	profile.RecordBytes = 1 << 20
	for _, completed := range []bool{false, true} {
		// Feed scalar observations only; this test sends no command and allocates no payload data.
		values := make(chan tape.CommandObservation, 256)
		go func() {
			defer close(values)
			sequence, object := uint64(1), uint64(3)
			values <- tape.CommandObservation{Sequence: sequence, Opcode: 0x34, Known: true,
				Position: tape.Position{ObjectKnown: true, Object: object}}
			stamp := time.Unix(1, 0)
			for bin := range 288*10 + 144 {
				seconds := .45
				if bin > 0 && bin%288 == 0 {
					seconds += 6
				}
				for range 64 {
					sequence++
					elapsed := time.Duration(seconds / 64 * float64(time.Second))
					values <- tape.CommandObservation{Sequence: sequence, Opcode: 10, Known: true,
						StartedAt: stamp, Elapsed: elapsed, Bytes: int(profile.RecordBytes)}
					stamp = stamp.Add(elapsed + 10*time.Microsecond)
					object++
				}
			}
			// A final FILEMARK alone does not establish an EOD address for the optional map.
			sequence++
			values <- tape.CommandObservation{Sequence: sequence, Opcode: 0x10}
			if completed {
				values <- tape.CommandObservation{Sequence: sequence + 1, Opcode: 0x34, Known: true,
					Position: tape.Position{ObjectKnown: true, Object: object + 1}}
			}
		}()
		value, reason := collectTarWriteGeometry(values, report, profile, nil)
		if !completed {
			require.Nil(t, value)
			require.NotEmpty(t, reason)
			continue
		}
		require.NotNil(t, value, reason)
		require.Len(t, value.Wraps, 11)
		require.Equal(t, uint64(3+(288*10+144)*64+1), value.Partitions[0].GetEod())
	}
}

func TestTarWriteGeometryRetainsOnlyValidatedOwnedMap(t *testing.T) {
	ctx := context.Background()
	exe, backend, _, _ := tarFactoryFixture(t)
	stored := tarMapMediaFixture()
	geometry, cleanup := tarReadModelMap(t, []tape.WrapEndpoint{{Object: 9}, {Wrap: 1, Object: 19}, {Wrap: 2, Object: 24}})
	defer func() { require.NoError(t, cleanup()) }()
	value, reason := deriveTarPositionMap(tarNormalizedParameters(), stored.Profile.GetTape().Tar, geometry, mediapkg.ReadEndpointInclusive)
	require.NotNil(t, value, reason)
	descriptor := describeMedia(stored)
	backend.retainTarWriteGeometry(ctx, stored, descriptor, value)
	require.True(t, proto.Equal(value, descriptor.TapePositionMap), "new FORMAT map accompanies its ordinary first publication")
	stored, err := exe.Lib().CreateMedia(ctx, stored)
	require.NoError(t, err)
	backend.retainTarWriteGeometry(ctx, stored, descriptor, value)
	reloaded, err := exe.Lib().GetMedia(ctx, stored.ID)
	require.NoError(t, err)
	require.True(t, proto.Equal(value, reloaded.TapePositionMap))
	require.True(t, proto.Equal(stored.Profile, reloaded.Profile))
	invalid := proto.Clone(value).(*entity.TapePositionMap)
	invalid.InitializationId = "another initialization"
	backend.retainTarWriteGeometry(ctx, reloaded, descriptor, invalid)
	after, err := exe.Lib().GetMedia(ctx, stored.ID)
	require.NoError(t, err)
	require.True(t, proto.Equal(value, after.TapePositionMap))

	// An absent uncertainty is not an exact boundary; retained data remains valid but cannot price reads.
	unknown := proto.Clone(value).(*entity.TapePositionMap)
	unknown.Wraps[0].EndpointUncertaintyObjects = nil
	require.NoError(t, library.ValidateTapePositionMap(stored, unknown))
	model, why := compileTapePositionMap(tarNormalizedParameters(), unknown, func() bool { return true })
	require.Nil(t, model)
	require.Contains(t, why, "uncertainty is unknown")
}
