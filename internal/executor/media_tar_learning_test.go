package executor

import (
	"bytes"
	"context"
	"encoding/json"
	"math/rand/v2"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/samuelncui/yatm/entity"
	mediapkg "github.com/samuelncui/yatm/internal/media"
	"github.com/samuelncui/yatm/internal/media/tape"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

func tapeObservationReplay(values ...tape.CommandObservation) <-chan tape.CommandObservation {
	// Only completed native facts enter the collector; no replay issues hardware I/O.
	replay := make(chan tape.CommandObservation, len(values))
	started := time.Unix(1792581049, 0)
	for i, value := range values {
		value.Sequence, value.Known = uint64(i+1), true
		value.StartedAt = started
		started = started.Add(value.Elapsed + 10*time.Millisecond)
		replay <- value
	}
	close(replay)
	return replay
}

func tapeReadLearningRows(job int64, rate, first, mark float64, segments int) []tapeReadObservation {
	var values []tapeReadObservation
	for i := range segments {
		bytes := uint64(1+i%7) << 22
		values = append(values, tapeReadObservation{JobID: job, Action: observationRead,
			Bytes: bytes, DeviceSeconds: float64(bytes) / rate})
	}
	for i := range 3 {
		bytes := uint64(1+i) << 18
		values = append(values,
			tapeReadObservation{JobID: job, Action: observationFirstRead,
				Bytes: bytes, DeviceSeconds: first + float64(bytes)/rate},
			tapeReadObservation{JobID: job, Action: observationFilemarkRead,
				Bytes: bytes, DeviceSeconds: mark + float64(bytes)/rate})
	}
	return values
}

func TestTapeReadObservationsResolveToAccessFeatures(t *testing.T) {
	// Retained wrap and offset, with band and direction derived from the generation layout,
	// reproduce the features the cost model computes from the live position map.
	geometry, cleanup := tarReadModelMap(t, []tape.WrapEndpoint{{Object: 99}, {Wrap: 1, Object: 199}, {Wrap: 2, Object: 250}})
	defer func() { require.NoError(t, cleanup()) }()
	parameters := tarNormalizedParameters()
	_, _, profile := tarReadParametersFixture()
	model, reason := tarTestReadModel(parameters, profile, geometry, mediapkg.ReadEndpointInclusive)
	require.NotNil(t, model, reason)

	targets := []uint64{150, 120, 30, 60, 220, 180, 5}
	values := []tape.CommandObservation{{Opcode: 0x34, Position: tape.Position{Object: 10, File: 1, FileKnown: true}}}
	for _, target := range targets {
		values = append(values, tape.CommandObservation{Opcode: 0x92, Elapsed: time.Second, Position: tape.Position{Object: target}})
	}
	values = append(values,
		tape.CommandObservation{Opcode: 0x08, Elapsed: time.Millisecond, Bytes: 262144},
		tape.CommandObservation{Opcode: 0x08, Elapsed: time.Millisecond, Bytes: 262144},
		tape.CommandObservation{Opcode: 0x08, Elapsed: time.Millisecond, Bytes: 262144},
		tape.CommandObservation{Opcode: 0x1b, Elapsed: 30 * time.Second})
	records := collectTapeReadObservations(tapeObservationReplay(values...), tapeReadObservation{JobID: 3}, model, nil)

	// One LOCATE per motion, a first READ, a two-command segment and the final UNLOAD.
	require.Len(t, records, len(targets)+3)
	refit := newTapeReadRefit(parameters)
	from := uint64(10)
	for i, target := range targets {
		record := records[i]
		require.Equal(t, observationLocate, record.Action)
		require.Equal(t, from, *record.From.Object)
		require.Equal(t, target, *record.To.Object)
		want, known := model.AccessFeatures(&entity.TapeBytePosition{LogicalObject: from}, &entity.TapeBytePosition{LogicalObject: target})
		require.True(t, known)
		got, known := refit.features(&record)
		require.True(t, known)
		require.Equal(t, want, got, "LOCATE %d -> %d", from, target)
		from = target
	}
	require.EqualValues(t, 1, *records[0].From.File)
	require.Nil(t, records[1].From.File, "LOCATE does not identify a file number")
	first, segment, unload := records[len(targets)], records[len(targets)+1], records[len(targets)+2]
	require.Equal(t, observationFirstRead, first.Action)
	require.Equal(t, observationLocate, first.PreviousAction)
	require.Equal(t, observationRead, segment.Action)
	require.Equal(t, observationFirstRead, segment.PreviousAction)
	require.EqualValues(t, 2, segment.Commands)
	require.EqualValues(t, 2*262144, segment.Bytes)
	require.InDelta(t, .01, segment.MaxHostGapSeconds, 1e-9)
	require.Equal(t, observationUnload, unload.Action)
	require.EqualValues(t, 8, *unload.From.Object)
	require.NotNil(t, unload.From.Offset)
}

func TestTapeReadObservationsKeepOnlyKnownEndpoints(t *testing.T) {
	// A sequence gap withdraws the cursor; the next LOCATE reestablishes it without a record.
	values := tapeObservationReplay(
		tape.CommandObservation{Opcode: 0x34, Position: tape.Position{Object: 10}},
		tape.CommandObservation{Opcode: 0x92, Elapsed: time.Second, Position: tape.Position{Object: 40}},
	)
	gapped := make(chan tape.CommandObservation, 4)
	for value := range values {
		gapped <- value
	}
	gapped <- tape.CommandObservation{Sequence: 9, Opcode: 0x92, Known: true, Elapsed: time.Second, Position: tape.Position{Object: 80}}
	gapped <- tape.CommandObservation{Sequence: 10, Opcode: 0x92, Known: true, Elapsed: time.Second, Position: tape.Position{Object: 90}}
	close(gapped)
	records := collectTapeReadObservations(gapped, tapeReadObservation{}, nil, nil)
	require.Len(t, records, 2)
	require.EqualValues(t, 10, *records[0].From.Object)
	require.EqualValues(t, 80, *records[1].From.Object)
	require.Equal(t, observationLocate, records[1].PreviousAction)
}

func TestTapeFitL1WeighsJobsEqually(t *testing.T) {
	// Nine accurate Jobs and one long abnormal Job: the abnormal Job's 600 records weigh as one Job.
	want := [7]float64{3, 6, -.8, .6, 1.2, 6, 90}
	random := rand.New(rand.NewPCG(1, 2))
	var rows []tapeFitRow
	for job := range int64(10) {
		count := 40
		if job == 9 {
			count = 600
		}
		for range count {
			row := tapeFitRow{job: job, action: observationLocate}
			row.x[0], row.x[6] = 1, random.Float64()
			for column := 1; column < 6; column++ {
				row.x[column] = float64(random.IntN(2))
			}
			for column, value := range row.x {
				row.y += value * want[column]
			}
			if job == 9 {
				row.y += 30 + 20*random.Float64()
			}
			rows = append(rows, row)
		}
	}
	fitted, identified := tapeFitL1(rows, 7, [7]float64{})
	for column := range 7 {
		require.True(t, identified[column])
		require.InDelta(t, want[column], fitted[column], .05, "column %d", column)
	}

	// The same rows always produce the same coefficients.
	again, _ := tapeFitL1(rows, 7, [7]float64{})
	require.Equal(t, fitted, again)
}

func TestTapeFitL1KeepsUnidentifiableCoefficients(t *testing.T) {
	// An absent feature and a duplicated feature cannot be separated; both keep their prior values.
	current := [7]float64{0, 7, 11}
	var rows []tapeFitRow
	for i := range 20 {
		row := tapeFitRow{job: int64(i % 4), x: [7]float64{float64(i + 1), 0, 0}}
		row.y = 2 * row.x[0]
		rows = append(rows, row)
	}
	fitted, identified := tapeFitL1(rows, 3, current)
	require.Equal(t, [7]bool{true}, identified)
	require.InDelta(t, 2, fitted[0], 1e-9)
	require.Equal(t, 7.0, fitted[1])
	require.Equal(t, 11.0, fitted[2])

	for i := range rows {
		rows[i].x[1] = rows[i].x[0]
		rows[i].y = 2*rows[i].x[0] + 7*rows[i].x[1]
	}
	fitted, identified = tapeFitL1(rows, 2, current)
	require.Equal(t, [7]bool{true}, identified, "a collinear later column is not identifiable")
	require.InDelta(t, 2, fitted[0], 1e-9)
	require.Equal(t, 7.0, fitted[1])
}

func recordTapeReadJob(t *testing.T, exe *Executor, base *entity.TapeReadParameters,
	values []tapeReadObservation,
) int64 {
	t.Helper()
	job := createTestJob(t, exe, entity.JobKind_JOB_KIND_RESTORE, 0)
	encoded, err := protojson.Marshal(base.AppliesTo)
	require.NoError(t, err)
	var scope bytes.Buffer
	require.NoError(t, json.Compact(&scope, encoded)) // JSONL lines store the compact form.
	for i := range values {
		values[i].Device, values[i].Scope, values[i].JobID = "drive", scope.Bytes(), job.ID
	}
	require.NoError(t, exe.appendTapeReadObservations(context.Background(), job.ID, "TAPE01", values))
	return job.ID
}

func TestTapeReadRefitUsesExistingJobBundles(t *testing.T) {
	// Each Job's raw READ facts live in its bundle; the refit over all existing Jobs is published.
	exe, _, _, _ := tarFactoryFixture(t)
	ctx, logger := context.Background(), logrus.New()
	base := tarNormalizedParameters()
	base.Reading.PostLocateReadOverheadSeconds = proto.Float64(.2)
	for job := int64(1); job <= 9; job++ {
		recordTapeReadJob(t, exe, base, tapeReadLearningRows(job, 300e6, .01, .5, 20))
		exe.refitTapeReadModel(ctx, logger, "drive", base)
	}
	learned := func() *entity.TapeLearnedReadParameters {
		settings, err := exe.settings.Device.Current(ctx)
		require.NoError(t, err)
		require.Len(t, settings.LearnedReadParameters, 1)
		return settings.LearnedReadParameters[0]
	}
	model := learned()
	require.EqualValues(t, 9*26, model.TrainingObservations)
	require.InDelta(t, 300e6, model.Parameters.Reading.GetBytesPerSecond(), 300e6*1e-6)
	require.InDelta(t, .01, model.Parameters.Reading.GetPostLocateReadOverheadSeconds(), 1e-6)
	require.InDelta(t, .5, model.Parameters.Reading.GetFilemarkOverheadSeconds(), 1e-6)
	require.Less(t, model.MaxErrorSeconds, 1e-6)
	require.True(t, proto.Equal(base.Positioning, model.Parameters.Positioning), "no LOCATE observations")
	require.True(t, proto.Equal(base.Completion, model.Parameters.Completion), "no UNLOAD observations")

	// Observations of another device or applicability never enter this refit.
	other := proto.Clone(base).(*entity.TapeReadParameters)
	other.AppliesTo.Firmware = proto.String("other")
	recordTapeReadJob(t, exe, other, tapeReadLearningRows(0, 1e6, 3, 3, 20))

	// One abnormal later Job moves the published rate by about one tenth at most.
	outlier := recordTapeReadJob(t, exe, base, tapeReadLearningRows(0, 30e6, 2, 5, 500))
	exe.refitTapeReadModel(ctx, logger, "drive", base)
	require.InDelta(t, 300e6, learned().Parameters.Reading.GetBytesPerSecond(), 300e6*.1)
	require.EqualValues(t, 9*26+506, learned().TrainingObservations)

	// Refitting the same files twice gives the same model.
	refit := func() *entity.TapeLearnedReadParameters {
		value := newTapeReadRefit(base)
		require.NoError(t, exe.visitTapeReadObservations(ctx, logger, "drive", base.AppliesTo, value.add))
		return value.model("drive", time.Unix(1, 0))
	}
	require.True(t, proto.Equal(refit(), refit()))

	// Deleting a Job removes its bundle and therefore its observations from later refits.
	path := filepath.Join(exe.jobWorkPath(outlier), "tapes", "TAPE01", tapeReadObservationsFile)
	require.FileExists(t, path)
	_, err := exe.DeleteJobs(ctx, false, outlier)
	require.NoError(t, err)
	require.NoFileExists(t, path)
	exe.refitTapeReadModel(ctx, logger, "drive", base)
	require.EqualValues(t, 9*26, learned().TrainingObservations)
	require.InDelta(t, 300e6, learned().Parameters.Reading.GetBytesPerSecond(), 300e6*1e-6)
}

func TestTapeReadObservationsRoundTrip(t *testing.T) {
	// The bundle file keeps every raw field and appends later attempts; a truncated tail is skipped.
	exe, _, _, _ := tarFactoryFixture(t)
	ctx, logger := context.Background(), logrus.New()
	base := tarNormalizedParameters()
	values := []tapeReadObservation{
		{Barcode: "TAPE01", MapSource: "A3/1F/45", MapObservedAtNS: proto.Int64(1792581049000000001), Opcode: 0x92,
			Action: observationLocate, StartedAtNS: 1792581049000000002, IdleSeconds: proto.Float64(.25),
			From: observationPoint{Partition: proto.Uint32(0), Object: proto.Uint64(3), File: proto.Uint64(1),
				Wrap: proto.Uint32(0), Offset: proto.Uint64(3), Span: proto.Uint64(100)},
			To: observationPoint{Partition: proto.Uint32(0), Object: proto.Uint64(150),
				Wrap: proto.Uint32(1), Offset: proto.Uint64(50), Span: proto.Uint64(100)}, Commands: 1, DeviceSeconds: 12.5},
		{Barcode: "TAPE01", Opcode: 0x08, Action: observationRead, PreviousAction: observationFirstRead,
			StartedAtNS: 1792581050000000000, MinLPOS: proto.Uint64(10), MaxLPOS: proto.Uint64(9000),
			From:     observationPoint{Partition: proto.Uint32(0), Object: proto.Uint64(151)},
			To:       observationPoint{Partition: proto.Uint32(0), Object: proto.Uint64(160)},
			Commands: 9, Bytes: 9 << 18, DeviceSeconds: .03, HostSeconds: .002, MaxHostGapSeconds: .001, Discard: true},
	}
	job := recordTapeReadJob(t, exe, base, values[:1])
	values[1].Device, values[1].Scope, values[1].JobID = values[0].Device, values[0].Scope, job
	require.NoError(t, exe.appendTapeReadObservations(ctx, job, "TAPE01", values[1:]))
	path := filepath.Join(exe.jobWorkPath(job), "tapes", "TAPE01", tapeReadObservationsFile)
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0)
	require.NoError(t, err)
	_, err = file.WriteString(`{"device":"drive","sc`)
	require.NoError(t, err)
	require.NoError(t, file.Close())

	var stored []tapeReadObservation
	require.NoError(t, exe.visitTapeReadObservations(ctx, logger, "drive", base.AppliesTo, func(value *tapeReadObservation) {
		stored = append(stored, *value)
	}))
	require.Equal(t, values, stored)
}

func TestTapeReadLearningOffWritesNothing(t *testing.T) {
	// The Off preference returns before observing commands, so nothing is buffered or stored.
	exe, backend, _, _ := tarFactoryFixture(t)
	ctx := context.Background()
	base := tarNormalizedParameters()
	settings := &entity.DeviceSettings{Devices: []*entity.DeviceConfig{{Device: "drive", Options: &entity.DeviceConfig_Tape{
		Tape: &entity.TapeDeviceSettings{PassiveReadLearning: proto.Bool(false)}}}}}
	finish := backend.startTarReadLearning(ctx, nil, "drive", "TAPE01", settings, base, nil, nil, &entity.TapeBytePosition{})
	finish()
	paths, err := filepath.Glob(filepath.Join(exe.paths.Work, "jobs", "*", "tapes", "*", tapeReadObservationsFile))
	require.NoError(t, err)
	require.Empty(t, paths)
	current, err := exe.settings.Device.Current(ctx)
	require.NoError(t, err)
	require.Empty(t, current.LearnedReadParameters)
}
