package entity

import (
	"encoding/json"
	"math"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

func TestLocationReferenceJSONPreservesNanoseconds(t *testing.T) {
	for _, stamp := range []int64{math.MinInt64, -1, 0, 1791000000123456781, math.MaxInt64} {
		// Real observation manifests encode a reference containing these protobuf facts with encoding/json.
		want := &LocationEntryRef{LocationId: 3, Path: "literal\\name", Facts: &LocationFileFacts{
			MtimeNs: stamp, SizeBytes: 123, Mode: 0o644,
		}}
		encoded, err := json.Marshal(want)
		require.NoError(t, err)
		var raw struct {
			Facts map[string]json.RawMessage `json:"facts"`
		}
		require.NoError(t, json.Unmarshal(encoded, &raw))
		if stamp != 0 {
			var decimal string
			require.NoError(t, json.Unmarshal(raw.Facts["mtime_ns"], &decimal))
			require.NotEmpty(t, decimal)
		}

		// Decoding preserves all reference facts, not just the timestamp under test.
		var actual LocationEntryRef
		require.NoError(t, json.Unmarshal(encoded, &actual))
		require.True(t, proto.Equal(want, &actual))
	}
}
