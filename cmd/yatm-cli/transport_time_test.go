package main

import (
	"bytes"
	"encoding/json"
	"math"
	"strconv"
	"testing"

	"github.com/samuelncui/yatm/entity"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

func TestWriteProtoNanosecondsKeepExactStringsAndPresence(t *testing.T) {
	// The production JSONL writer must preserve every int64 bit and an explicitly known epoch zero.
	for _, stamp := range []int64{math.MinInt64, -1, 0, 1_234_567_890_123_456_789, math.MaxInt64} {
		t.Run(strconv.FormatInt(stamp, 10), func(t *testing.T) {
			message := &entity.ListFilesResponse{Entries: []*entity.FilesEntry{
				{Name: "known", MtimeNs: &stamp},
				{Name: "unknown", Error: "timestamp unavailable"},
			}}
			var output bytes.Buffer
			require.NoError(t, writeProto(&output, message))
			var decoded struct {
				Entries []map[string]any `json:"entries"`
			}
			require.NoError(t, json.Unmarshal(output.Bytes(), &decoded))
			require.Equal(t, strconv.FormatInt(stamp, 10), decoded.Entries[0]["mtime_ns"])
			require.NotContains(t, decoded.Entries[0], "mtime_seconds")
			require.NotContains(t, decoded.Entries[1], "mtime_ns")
			var roundtrip entity.ListFilesResponse
			require.NoError(t, protojson.Unmarshal(output.Bytes(), &roundtrip))
			require.True(t, proto.Equal(message, &roundtrip))
		})
	}
}

func TestWriteProtoNanosecondProgressKeepsDurationUnits(t *testing.T) {
	// Instants use ns while elapsed and remaining durations keep their existing wire units.
	message := &entity.Progress{StartedAtNs: 1_234_567_890_123_456_789, ElapsedMs: proto.Int64(1234),
		Stage: &entity.StageProgress{RemainingSeconds: proto.Int64(2)}}
	var output bytes.Buffer
	require.NoError(t, writeProto(&output, message))
	var decoded map[string]any
	require.NoError(t, json.Unmarshal(output.Bytes(), &decoded))
	require.Equal(t, "1234567890123456789", decoded["started_at_ns"])
	require.Equal(t, "1234", decoded["elapsed_ms"])
	require.Equal(t, "2", decoded["stage"].(map[string]any)["remaining_seconds"])
	require.NotContains(t, decoded, "started_at_seconds")
}
