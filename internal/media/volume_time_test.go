package media

import (
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/samuelncui/yatm/entity"
	"github.com/stretchr/testify/require"
)

func TestVolumeMarkerRetainsExactNanoseconds(t *testing.T) {
	// Markers persist signed decimal strings, including the explicit zero sentinel.
	for _, stamp := range []int64{0, -1, 1_700_000_000_123_456_789, math.MinInt64, math.MaxInt64} {
		t.Run(strconv.FormatInt(stamp, 10), func(t *testing.T) {
			// Exercise the same file decoder used by discovery and write admission.
			root := t.TempDir()
			marker := VolumeMarker{Version: volumeMarkerVersion, UUID: "9ef04c80-e054-41fc-8d7f-ed195eab9b47",
				CreatedAtNS: stamp, Profile: &entity.VolumeMediaProfile{Type: entity.VolumeType_VOLUME_TYPE_HDD}}
			data, err := json.Marshal(marker)
			require.NoError(t, err)
			var fields map[string]json.RawMessage
			require.NoError(t, json.Unmarshal(data, &fields))
			require.Equal(t, `"`+strconv.FormatInt(stamp, 10)+`"`, string(fields["created_at_ns"]))
			require.NotContains(t, fields, "created_at")
			require.NoError(t, os.WriteFile(filepath.Join(root, VolumeMarkerName), data, 0o600))
			volume, err := OpenVolume(root)
			require.NoError(t, err)
			require.Equal(t, stamp, volume.Marker.CreatedAtNS)
		})
	}
}

func TestVolumeMarkerRejectsOldOrMissingTimeBeforeWrites(t *testing.T) {
	// Pre-stable shapes cannot be interpreted as a current marker with an unknown creation time.
	for _, field := range []string{
		``, `,"created_at":"2026-10-04T12:00:00Z"`, `,"created_at_ns":null`,
		`,"created_at_ns":0`, `,"created_at_ns":"2026-10-04T12:00:00Z"`,
		`,"created_at_ns":"9223372036854775808"`, `,"created_at_ns":"-9223372036854775809"`,
	} {
		t.Run(field, func(t *testing.T) {
			// Keep the rejected marker and every directory entry unchanged.
			root := t.TempDir()
			data := []byte(`{"version":1,"uuid":"9ef04c80-e054-41fc-8d7f-ed195eab9b47","profile":{"type":1}` +
				field + `}`)
			filename := filepath.Join(root, VolumeMarkerName)
			require.NoError(t, os.WriteFile(filename, data, 0o600))
			_, err := OpenVolume(root)
			require.Error(t, err)
			_, err = InitializeVolume(root, &entity.VolumeMediaProfile{Type: entity.VolumeType_VOLUME_TYPE_HDD})
			require.ErrorContains(t, err, "marker already exists")
			actual, err := os.ReadFile(filename)
			require.NoError(t, err)
			require.Equal(t, data, actual)
			entries, err := os.ReadDir(root)
			require.NoError(t, err)
			require.Len(t, entries, 1)
		})
	}
}

func TestVolumeIdentityDistinguishesSameMillisecond(t *testing.T) {
	// Creation times one nanosecond apart must not compare equal after a millisecond round trip.
	first := &Volume{Root: "/volume", Marker: VolumeMarker{UUID: "volume", CreatedAtNS: 1_700_000_000_123_456_789,
		Profile: &entity.VolumeMediaProfile{Type: entity.VolumeType_VOLUME_TYPE_HDD}}}
	second := *first
	require.True(t, SameVolume(first, &second))
	second.Marker.CreatedAtNS++
	require.False(t, SameVolume(first, &second))
}
