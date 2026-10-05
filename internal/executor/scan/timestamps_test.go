package scan

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"testing"
	"time"

	"github.com/samuelncui/acp"
	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/internal/library"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protojson"
)

func TestScanNanosecondsRoundTrip(t *testing.T) {
	// The manifest owns distinct expected, read and observation instants without floating-point conversion.
	r := newProgressRunner(t)
	for i, stamp := range []int64{math.MinInt64, 1780000000000123456, 1780000000000123457, math.MaxInt64} {
		row := &Entry{LocationID: 1, Path: fmt.Sprint(i), MtimeNS: stamp, ReadMtimeNS: stamp, CheckedAtNS: stamp,
			Expected: &entity.ExpectedFile{MtimeNs: stamp}, Before: &entity.ObservedEntry{MtimeNs: stamp}}
		require.NoError(t, r.db.Create(row).Error)
		var stored Entry
		require.NoError(t, r.db.First(&stored, row.ID).Error)
		require.Equal(t, stamp, stored.MtimeNS)
		require.Equal(t, stamp, stored.ReadMtimeNS)
		require.Equal(t, stamp, stored.CheckedAtNS)
		require.Equal(t, stamp, stored.Expected.MtimeNs)
		require.Equal(t, stamp, stored.Before.MtimeNs)
		data, err := json.Marshal(&stored)
		require.NoError(t, err)
		var decoded Entry
		require.NoError(t, json.Unmarshal(data, &decoded))
		require.Equal(t, stamp, decoded.MtimeNS)
		require.Equal(t, stamp, decoded.CheckedAtNS)
		wire, err := protojson.Marshal(stored.ToEntity())
		require.NoError(t, err)
		var entry entity.ScanEntry
		require.NoError(t, protojson.Unmarshal(wire, &entry))
		require.Equal(t, stamp, entry.CheckedAtNs)
	}
	var paths []string
	require.NoError(t, r.db.Model(&Entry{}).Where("checked_at_ns > ? AND checked_at_ns < ?", int64(1780000000000123456), int64(1780000000001000000)).Pluck("path", &paths).Error)
	require.Equal(t, []string{"2"}, paths, "same-millisecond observations remain distinguishable")
}

type scanTimestampInfo struct{ stamp time.Time }

func (i scanTimestampInfo) Name() string       { return "file" }
func (i scanTimestampInfo) Size() int64        { return 7 }
func (i scanTimestampInfo) Mode() os.FileMode  { return 0644 }
func (i scanTimestampInfo) ModTime() time.Time { return i.stamp }
func (i scanTimestampInfo) IsDir() bool        { return false }
func (i scanTimestampInfo) Sys() any           { return nil }

func TestScanRejectsUnrepresentableObservedAndReadMtime(t *testing.T) {
	for _, year := range []int{1600, 2400} {
		t.Run(fmt.Sprint(year), func(t *testing.T) {
			// Physical roots and logical leaves share this preparation boundary before any manifest write.
			stamp := time.Date(year, 1, 1, 0, 0, 0, 123, time.UTC)
			local := &locationStage{source: &library.Location{ID: 1}}
			row, err := local.observation("file", "", scanTimestampInfo{stamp: stamp}, entity.ScanSignaturePolicy_SCAN_SIGNATURE_POLICY_KNOWN_ONLY)
			require.ErrorContains(t, err, "outside the signed Unix nanosecond range")
			require.Nil(t, row)
			// ACP completion must not wrap an actual mtime or overwrite the frozen Verify baseline.
			stream := &contentStream{config: &Config{Spec: &entity.ScanJobSpec{ResultPolicy: entity.ScanResultPolicy_SCAN_RESULT_POLICY_VERIFY_COPIES}}}
			baseline := &Entry{MtimeNS: 123, Expected: &entity.ExpectedFile{MtimeNs: 123}}
			write, keep := stream.accept(baseline, acp.Result{Size: 7, Mode: 0644, ModTime: stamp, SHA256: make([]byte, 32)})
			require.True(t, keep)
			require.ErrorContains(t, write.cause, "outside the signed Unix nanosecond range")
			require.EqualValues(t, 123, baseline.MtimeNS)
			require.EqualValues(t, 123, baseline.Expected.MtimeNs)
			require.Zero(t, baseline.CheckedAtNS)
		})
	}
}
