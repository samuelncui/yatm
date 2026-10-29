package executor

import (
	"compress/gzip"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"io"
	"math"
	"os"
	"testing"
	"time"

	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/internal/library"
	mediapkg "github.com/samuelncui/yatm/internal/media"
	"github.com/samuelncui/yatm/internal/media/tape"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

func tarWriteGeometrySignal(period, cycles int) []tarWriteGeometryBin {
	bins := make([]tarWriteGeometryBin, period*cycles+period/2)
	for i := range bins {
		bins[i] = tarWriteGeometryBin{seconds: .45, bytes: 64 << 20, low: uint64(i * 256), high: uint64((i + 1) * 256)}
		if i > 0 && i%period == 0 {
			bins[i].seconds += 6
		}
	}
	return bins
}

func TestTarWriteGeometryFitsRecurrenceAndRefusesUnidentifiableSignals(t *testing.T) {
	for _, test := range []struct {
		name string
		edit func([]tarWriteGeometryBin)
	}{
		{"constant", func(b []tarWriteGeometryBin) {
			for i := range b {
				b[i].seconds = .45
			}
		}},
		{"constant-slow", func(b []tarWriteGeometryBin) {
			for i := range b {
				b[i].seconds = 4
			}
		}},
		{"starved", func(b []tarWriteGeometryBin) {
			for i := range b {
				b[i].supplySeconds = 1
			}
		}},
		{"continuous-backhitch", func(b []tarWriteGeometryBin) {
			for i := range b {
				if i%3 == 0 {
					b[i].seconds += 6
				}
			}
		}},
		{"half-harmonic", func(b []tarWriteGeometryBin) {
			for i := range b {
				if i%32 == 0 {
					b[i].seconds += 6
				}
			}
		}},
		{"missing-boundary", func(b []tarWriteGeometryBin) { b[64*9].seconds = .45 }},
		{"phase-drift", func(b []tarWriteGeometryBin) {
			for i := range b {
				b[i].seconds = .45
			}
			for i, k := 1, 64; k < len(b); i, k = i+1, k+64+i%9 {
				b[k].seconds += 6
			}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			bins := tarWriteGeometrySignal(64, 20)
			test.edit(bins)
			fit, reason := fitTarWriteGeometry(bins, 64<<20, 64*(64<<20), 140e6)
			require.Nil(t, fit)
			require.NotEmpty(t, reason)
		})
	}
	fit, reason := fitTarWriteGeometry(tarWriteGeometrySignal(64, 20), 64<<20, 64*(64<<20), 140e6)
	require.NotNil(t, fit, reason)
	require.Len(t, fit.points, 20)
	require.InDelta(t, 64*(64<<20), fit.period, 64<<20)
}

func TestTarWriteGeometryCollectorLossBoundsAndAppend(t *testing.T) {
	_, report, profile := tarReadParametersFixture()
	report.GetTape().DensityCode = proto.Uint32(0x58)
	profile.InitializationId = "geometry-test"
	start := tape.Position{ObjectKnown: true}
	collector, reason := newTarWriteGeometryLearning(report, profile, start, nil)
	require.NotNil(t, collector, reason)
	stamp := time.Unix(1, 0)
	for bin := 0; bin < 288*10+144; bin++ {
		seconds := .45
		if bin > 0 && bin%288 == 0 {
			seconds += 6
		}
		for record := 0; record < 256; record++ {
			elapsed := time.Duration(seconds / 256 * float64(time.Second))
			collector.Observe(tape.CommandObservation{Sequence: uint64(bin*256 + record + 1), Opcode: 10,
				StartedAt: stamp, Elapsed: elapsed, Known: true, Bytes: 262144})
			stamp = stamp.Add(elapsed + 10*time.Microsecond)
		}
	}
	eod := tape.Position{ObjectKnown: true, Object: collector.cursor}
	value, reason := collector.Finish(eod, 0)
	require.NotNil(t, value, reason)
	require.Len(t, value.Wraps, 11)
	require.Equal(t, eod.Object, value.Partitions[0].GetEod())
	require.Equal(t, eod.Object, value.Wraps[10].GetEndpoint())
	require.False(t, value.Wraps[10].Full)
	require.NotZero(t, value.Wraps[0].GetEndpointUncertaintyObjects())
	require.Nil(t, collector.bins, "raw metrics are discarded after finalization")

	// The unchanged full prefix is cloned, while its old partial descriptor is replaced only on success.
	copy := proto.Clone(value).(*entity.TapePositionMap)
	appendCollector, reason := newTarWriteGeometryLearning(report, profile, eod, value)
	require.NotNil(t, appendCollector, reason)
	require.Equal(t, uint32(10), appendCollector.firstWrap)
	appendCollector.bins = tarWriteGeometrySignal(288, 10)[144:]
	for i := range appendCollector.bins {
		appendCollector.bins[i].low += eod.Object - 144*256
		appendCollector.bins[i].high += eod.Object - 144*256
	}
	appendCollector.bytes = uint64(len(appendCollector.bins)) * appendCollector.width
	appendCollector.cursor = eod.Object + uint64(len(appendCollector.bins)*256)
	newMap, reason := appendCollector.Finish(tape.Position{ObjectKnown: true, Object: appendCollector.cursor}, 0)
	require.NotNil(t, newMap, reason)
	for i := 0; i < 10; i++ {
		require.True(t, proto.Equal(value.Wraps[i], newMap.Wraps[i]))
	}
	require.True(t, proto.Equal(copy, value))

	for _, loss := range []string{"gap", "dropped", "budget", "unknown-middle"} {
		t.Run(loss, func(t *testing.T) {
			c, _ := newTarWriteGeometryLearning(report, profile, start, nil)
			observation := tape.CommandObservation{Sequence: 1, Opcode: 10, StartedAt: stamp,
				Elapsed: time.Millisecond, Known: true, Bytes: 262144}
			c.Observe(observation)
			dropped := uint64(0)
			switch loss {
			case "gap":
				observation.Sequence = 3
				c.Observe(observation)
			case "dropped":
				dropped = 1
			case "budget":
				c.bytes = c.width * tarWriteGeometryMaxBins
				observation.Sequence++
				c.Observe(observation)
			case "unknown-middle":
				observation.Sequence++
				observation.Known = false
				c.Observe(observation)
				observation.Sequence++
				observation.Known = true
				c.Observe(observation)
			}
			mapValue, why := c.Finish(tape.Position{ObjectKnown: true, Object: c.cursor}, dropped)
			require.Nil(t, mapValue)
			require.NotEmpty(t, why)
		})
	}
}

func TestTarWriteGeometryNativeReplay(t *testing.T) {
	path := os.Getenv("YATM_TAR_WRITE_REPLAY")
	if path == "" {
		t.Skip("set YATM_TAR_WRITE_REPLAY to the retained packed native WRITE facts")
	}
	file, err := os.Open(path)
	require.NoError(t, err)
	defer file.Close()
	compressed, err := gzip.NewReader(file)
	require.NoError(t, err)
	defer compressed.Close()
	_, report, profile := tarReadParametersFixture()
	report.GetTape().DensityCode = proto.Uint32(0x58)
	profile.InitializationId = "fa240e54-71be-4113-a050-6ef48e643bd8"
	collector, reason := newTarWriteGeometryLearning(report, profile, tape.Position{ObjectKnown: true, Object: 244}, nil)
	require.NotNil(t, collector, reason)
	sequence := uint64(0)
	var raw [21]byte
	for {
		_, err := io.ReadFull(compressed, raw[:])
		if err == io.EOF {
			break
		}
		require.NoError(t, err)
		sequence++
		collector.Observe(tape.CommandObservation{Sequence: sequence, Opcode: 10,
			StartedAt: time.Unix(0, int64(binary.LittleEndian.Uint64(raw[:8]))),
			Elapsed:   time.Duration(binary.LittleEndian.Uint64(raw[8:16])),
			Bytes:     int(binary.LittleEndian.Uint32(raw[16:20])), Known: raw[20] != 0})
	}
	value, reason := collector.Finish(tape.Position{ObjectKnown: true, Object: 5831508}, 0)
	require.NotNil(t, value, reason)
	encoded, err := json.Marshal(value)
	require.NoError(t, err)
	digest := sha256.Sum256(encoded)
	result := map[string]any{"writes": sequence, "derived_before_reference_sha256": hex.EncodeToString(digest[:]), "map": value}

	// Derive and freeze first. Only this evaluation boundary is allowed to open the withheld REOWP map.
	if truthPath := os.Getenv("YATM_TAR_WRITE_REPLAY_TRUTH"); truthPath != "" {
		rawTruth, err := os.ReadFile(truthPath)
		require.NoError(t, err)
		var truth struct {
			Geometry []struct {
				Low uint64 `json:"low"`
			} `json:"geometry"`
		}
		require.NoError(t, json.Unmarshal(rawTruth, &truth))
		require.Len(t, value.Wraps, len(truth.Geometry))
		maximum := uint64(0)
		for i := 0; i < len(value.Wraps)-1; i++ {
			actual, estimated := truth.Geometry[i+1].Low, value.Wraps[i].GetEndpoint()
			errorObjects := uint64(math.Abs(float64(estimated) - float64(actual)))
			maximum = max(maximum, errorObjects)
			require.LessOrEqual(t, errorObjects, value.Wraps[i].GetEndpointUncertaintyObjects(), "boundary %d", i)
		}
		result["maximum_reference_error_objects"] = maximum
	}
	stored := tarMapMediaFixture()
	stored.Profile.GetTape().Tar = profile
	require.NoError(t, library.ValidateTapePositionMap(stored, value))
	parameters := tarTestReadParameters(report, profile)
	stored.TapePositionMap = value
	model, captured, why := compileTarMediaReadModel(parameters, stored, nil, mediapkg.ReadEndpointInclusive, func() bool { return true })
	require.Nil(t, captured)
	require.NotNil(t, model, why)
	cost, known := model.AccessCost(&entity.TapeBytePosition{LogicalObject: value.Wraps[3].GetEndpoint() + 10000},
		&entity.TapeBytePosition{LogicalObject: value.Wraps[40].GetEndpoint() + 20000})
	require.True(t, known)
	result["consumer_cost"] = cost
	if output := os.Getenv("YATM_TAR_WRITE_REPLAY_REPORT"); output != "" {
		encoded, err := json.MarshalIndent(result, "", "  ")
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(output, append(encoded, '\n'), 0600))
	}
}

func BenchmarkTarWriteGeometryFit(b *testing.B) {
	bins := tarWriteGeometrySignal(288, 79)
	b.ReportAllocs()
	for b.Loop() {
		fit, _ := fitTarWriteGeometry(bins, 64<<20, 1500e9/80, 140e6)
		if fit == nil {
			b.Fatal("fit unavailable")
		}
	}
}

func BenchmarkTarWriteGeometryObserve(b *testing.B) {
	_, report, profile := tarReadParametersFixture()
	profile.InitializationId = "geometry-bench"
	report.GetTape().DensityCode = proto.Uint32(0x58)
	collector, _ := newTarWriteGeometryLearning(report, profile, tape.Position{ObjectKnown: true}, nil)
	observation := tape.CommandObservation{Opcode: 10, StartedAt: time.Unix(1, 0), Elapsed: time.Millisecond, Known: true, Bytes: 262144}
	b.ReportAllocs()
	for b.Loop() {
		observation.Sequence++
		observation.StartedAt = observation.StartedAt.Add(time.Millisecond)
		collector.Observe(observation)
		if collector.bytes > 1<<40 {
			collector.bytes = 0
			collector.cursor = 0
		}
	}
}

func TestTarWriteGeometryCollectorConservesNativeAndSupplyFacts(t *testing.T) {
	_, report, profile := tarReadParametersFixture()
	profile.InitializationId = "geometry-byte-accounting"
	report.GetTape().DensityCode = proto.Uint32(0x58)
	collector, reason := newTarWriteGeometryLearning(report, profile, tape.Position{ObjectKnown: true}, nil)
	require.NotNil(t, collector, reason)
	collector.width = 262144
	stamp := time.Unix(1, 0)
	collector.Observe(tape.CommandObservation{Sequence: 1, Opcode: 10, StartedAt: stamp,
		Elapsed: 100 * time.Millisecond, Known: true, Bytes: 262144})
	collector.Observe(tape.CommandObservation{Sequence: 2, Opcode: 10, StartedAt: stamp.Add(150 * time.Millisecond),
		Elapsed: 200 * time.Millisecond, Known: true, Bytes: 131072})
	collector.Observe(tape.CommandObservation{Sequence: 3, Opcode: 10, Known: false, Bytes: 262144})
	require.Equal(t, uint64(393216), collector.bytes)
	require.Equal(t, uint64(2), collector.cursor)
	require.InDelta(t, .3, collector.bins[0].seconds+collector.bins[1].seconds, 1e-12)
	require.InDelta(t, .05, collector.bins[0].supplySeconds+collector.bins[1].supplySeconds, 1e-12)
	require.Equal(t, uint64(131072), collector.bins[1].bytes)
	_, reason = newTarWriteGeometryLearning(report, profile, tape.Position{ObjectKnown: true, Object: 500000}, nil)
	require.Contains(t, reason, "prefix geometry")
	report.GetTape().DensityCode = proto.Uint32(0xee)
	_, reason = newTarWriteGeometryLearning(report, profile, tape.Position{ObjectKnown: true}, nil)
	require.Contains(t, reason, "sourced")
}
