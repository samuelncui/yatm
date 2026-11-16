package apis

import (
	"math"
	"testing"

	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/internal/library"
	"github.com/stretchr/testify/require"
)

func TestConvertStoredTimestampsNanoseconds(t *testing.T) {
	// Persisted Position instants are already ns; neither rounding nor time conversion is needed.
	positions := convertPositions(&library.Position{MtimeNS: math.MinInt64, WrittenAtNS: math.MaxInt64, CheckedAtNS: -1})
	require.EqualValues(t, math.MinInt64, positions[0].MtimeNs)
	require.EqualValues(t, math.MaxInt64, positions[0].WrittenAtNs)
	require.EqualValues(t, -1, positions[0].CheckedAtNs)

	// Media creation keeps its exact nanosecond value.
	stored := &library.Media{CreatedAtNS: 1_234_567_890_123_456_789,
		Profile: (&entity.VolumeMediaProfile{Type: entity.VolumeType_VOLUME_TYPE_HDD}).Pack()}
	converted, err := convertMedia(stored)
	require.NoError(t, err)
	require.Equal(t, stored.CreatedAtNS, converted.CreatedAtNs)
}
