package calibration

import (
	"context"
	"errors"
	"math"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/internal/executor"
	"github.com/samuelncui/yatm/internal/library"
	"github.com/samuelncui/yatm/internal/media/tape"
	previewpkg "github.com/samuelncui/yatm/internal/preview"
	"github.com/samuelncui/yatm/internal/resource"
	settingspkg "github.com/samuelncui/yatm/internal/settings"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

const testDevice = "/dev/nst0"

// simulatedDrive answers LOCATE with the times of the read cost model and advances a simulated
// clock instead of sleeping. Objects are placed linearly in wraps 0 and 1 of its endpoints.
type simulatedDrive struct {
	lock      sync.Mutex
	clock     time.Time
	endpoints []tape.WrapEndpoint
	at        uint64
	locates   int
	// extra adds seconds to the LOCATE with this index; offset shifts the reported position.
	extra    map[int]float64
	offset   map[int]uint64
	onLocate func(index int)
	finished int
	// testData counts sessions opened by writing test data.
	testData int
}

func (d *simulatedDrive) now() time.Time {
	d.lock.Lock()
	defer d.lock.Unlock()
	return d.clock
}

func (d *simulatedDrive) point(object uint64) Point {
	first, second := float64(d.endpoints[0].Object), float64(d.endpoints[1].Object)
	if float64(object) < first {
		return Point{0, float64(object) / first}
	}
	return Point{1, 1 - (float64(object)-first)/(second-first)}
}

// modelSeconds follows the positioning model the fit assumes, with terms near the HP LTO-5 values.
func modelSeconds(from, to Point) float64 {
	const base, seek, reversal, read, stream = 6.2, 93.5, 4.6, 131.0, 0.046
	direction := 1.0
	if to.Wrap%2 == 1 {
		direction = -1
	}
	delta := (to.U - from.U) * direction
	switch {
	case delta < 0:
		return base + seek*math.Abs(to.U-from.U) + reversal
	case from.Wrap == to.Wrap && delta <= stream:
		return read * delta
	case from.Wrap == to.Wrap && (from.U-0.5)*(to.U-0.5) >= 0:
		return base + read*delta
	}
	return base + seek*math.Abs(to.U-from.U)
}

func (d *simulatedDrive) Locate(_ context.Context, object uint64) error {
	d.lock.Lock()
	index := d.locates
	d.locates++
	seconds := modelSeconds(d.point(d.at), d.point(object)) + d.extra[index]
	d.clock = d.clock.Add(time.Duration(seconds * float64(time.Second)))
	d.at = object
	hook := d.onLocate
	d.lock.Unlock()
	if hook != nil {
		hook(index)
	}
	return nil
}

func (d *simulatedDrive) Position(context.Context) (uint64, error) {
	d.lock.Lock()
	defer d.lock.Unlock()
	return d.at + d.offset[d.locates-1], nil
}

func (d *simulatedDrive) Finish(context.Context) error {
	d.lock.Lock()
	defer d.lock.Unlock()
	d.finished++
	d.clock = d.clock.Add(time.Duration((29 + 88*d.point(d.at).U) * float64(time.Second)))
	return nil
}

// LTO-5 holds about 18.4 GB per wrap in 512 KiB records.
var testEndpoints = []tape.WrapEndpoint{{Wrap: 0, Object: 35000}, {Wrap: 1, Object: 70010}, {Wrap: 2, Object: 71000}}

func setupCalibration(t *testing.T, drive *simulatedDrive) (*executor.Executor, *library.Media) {
	t.Helper()
	root := t.TempDir()
	executorDB, err := resource.OpenSQLite(filepath.Join(root, "executor.db"))
	require.NoError(t, err)
	libraryDB, err := resource.OpenSQLite(filepath.Join(root, "library.db"))
	require.NoError(t, err)
	settings := settingspkg.New(libraryDB, settingspkg.PreviewDefinition{
		Default:  func() (*entity.PreviewSettings, error) { return previewpkg.SettingsFromConfig(previewpkg.Config{}) },
		Validate: previewpkg.ValidateSettings,
	})
	lib := library.NewWithSettings(libraryDB, settings)
	require.NoError(t, lib.AutoMigrate())
	media, err := lib.CreateMedia(context.Background(), &library.Media{
		Kind: entity.MediaKind_MEDIA_KIND_TAPE, Identity: "CAL001", Name: "calibration",
		Profile: (&entity.TapeMediaProfile{Format: library.TapeFormatTarPAXV1, Tar: &entity.TarMediaProfile{
			InitializationId: "11111111-1111-4111-8111-111111111111", RecordMode: entity.TapeRecordMode_TAPE_RECORD_MODE_VARIABLE, RecordBytes: 524288,
		}}).Pack(),
	})
	require.NoError(t, err)
	exe := executor.New(executorDB, lib, []string{testDevice}, executor.Paths{Work: filepath.Join(root, "work")}, executor.Scripts{}, nil)
	require.NoError(t, exe.AutoMigrate())

	// The drive replaces native access and its simulated time replaces the clock.
	drive.endpoints = append([]tape.WrapEndpoint(nil), drive.endpoints...)
	if drive.endpoints == nil {
		drive.endpoints = testEndpoints
	}
	previousOpen, previousNow := openSession, now
	t.Cleanup(func() { openSession, now = previousOpen, previousNow })
	now = drive.now
	openSession = func(_ context.Context, _ *executor.Executor, _ int64, spec *entity.CalibrationJobSpec, _ *logrus.Logger) (*session, error) {
		switch {
		case spec.Device != testDevice:
			return nil, errors.New("unexpected calibration device")
		case spec.WriteTestData:
			drive.testData++
		}
		builtin := &entity.TapeReadParameters{Geometry: &entity.TapeReadGeometry{NominalWraps: proto.Uint32(80)},
			Completion: &entity.TapeReadCompletion{LtfsUnmountSeconds: proto.Float64(12.61)}}
		return &session{drive: drive, serial: "SN1", density: proto.Uint32(0x58), endpoints: drive.endpoints, wrapBytes: float64(testEndpoints[0].Object) * 524288, builtin: builtin}, nil
	}
	return exe, media
}

func createCalibration(t *testing.T, exe *executor.Executor, media *library.Media) *entity.Job {
	t.Helper()
	reply, err := (&service{exe: exe}).Create(context.Background(), &entity.CreateCalibrationJobRequest{
		Spec: &entity.CalibrationJobSpec{Device: " " + testDevice + " "},
	})
	require.NoError(t, err)
	return reply.Job
}

func waitSettled(t *testing.T, exe *executor.Executor, id int64) *entity.GetCalibrationJobProgressResponse {
	t.Helper()
	require.Eventually(t, func() bool {
		job, err := exe.GetJob(context.Background(), id)
		return err == nil && job.Status != entity.JobStatus_JOB_STATUS_PREPARING && !exe.IsRunning(id)
	}, 15*time.Second, 5*time.Millisecond)
	reply, err := (&service{exe: exe}).GetProgress(context.Background(), &entity.GetCalibrationJobProgressRequest{Id: id})
	require.NoError(t, err)
	return reply
}

func storedParameters(t *testing.T, exe *executor.Executor) []*entity.TapeReadParameters {
	t.Helper()
	value, err := exe.Lib().Settings().Device.Current(context.Background())
	require.NoError(t, err)
	return settingspkg.TapeDefaults(value, testDevice).GetReadParameters()
}

func TestCalibrationStoresFittedParameters(t *testing.T) {
	drive := &simulatedDrive{}
	exe, media := setupCalibration(t, drive)

	// An existing set for another density stays; the set for this density is replaced.
	other := &entity.TapeReadParameters{AppliesTo: &entity.TapeReadApplicability{DensityCode: proto.Uint32(0x5a)}, SourceReference: "kept"}
	old := &entity.TapeReadParameters{AppliesTo: &entity.TapeReadApplicability{DensityCode: proto.Uint32(0x58)}, SourceReference: "replaced"}
	_, err := exe.Lib().Settings().Device.Save(context.Background(), &entity.DeviceSettings{Devices: []*entity.DeviceConfig{{
		Device: testDevice, Options: &entity.DeviceConfig_Tape{Tape: &entity.TapeDeviceSettings{
			ReadOrder: entity.TapeReadOrder_TAPE_READ_ORDER_AUTO, ReadParameters: []*entity.TapeReadParameters{other, old}}},
	}}})
	require.NoError(t, err)

	job := createCalibration(t, exe, media)
	require.Equal(t, entity.JobKind_JOB_KIND_CALIBRATION, job.Kind)
	require.Equal(t, testDevice, job.GetTargetName())
	require.Nil(t, job.MediaId)
	reply := waitSettled(t, exe, job.Id)
	require.Equal(t, entity.JobStatus_JOB_STATUS_COMPLETED, reply.Job.Status, reply.Job.Error)
	require.Equal(t, testDevice, reply.Spec.Device)
	require.Len(t, reply.Moves, int(reply.TotalMoves))
	require.Len(t, reply.Moves, 21)
	require.Equal(t, 1, drive.finished)
	require.NotNil(t, reply.ElapsedMs)

	// The fit recovers the simulated terms and derives bytes and completion from the wrap length.
	parameters := reply.Parameters
	require.NotNil(t, parameters)
	positioning := parameters.Positioning
	require.InDelta(t, 6.2, positioning.GetBaseSeconds(), 0.3)
	require.InDelta(t, 93.5, positioning.GetSeekSecondsPerWrap(), 1)
	require.InDelta(t, 4.6, positioning.GetApproachReversalSeconds(), 0.5)
	wrapBytes := 35000.0 * 524288
	require.InDelta(t, 0.046*wrapBytes, float64(positioning.GetStreamingBytes()), 0.004*wrapBytes)
	require.InDelta(t, wrapBytes/131, parameters.Reading.GetBytesPerSecond(), wrapBytes/131*0.02)
	require.Equal(t, math.Round(parameters.Reading.GetBytesPerSecond()), parameters.Reading.GetBytesPerSecond())
	require.Equal(t, positioning.GetSeekSecondsPerWrap(), parameters.Completion.GetFullSpanSeconds())
	require.InDelta(t, 29+88*0.03-positioning.GetSeekSecondsPerWrap()*0.03, parameters.Completion.GetFixedSeconds(), 0.02)
	require.Equal(t, 12.61, parameters.Completion.GetLtfsUnmountSeconds())
	require.Equal(t, uint32(80), parameters.Geometry.GetNominalWraps())
	require.Equal(t, "SN1", parameters.AppliesTo.GetDriveSerial())
	require.Equal(t, uint32(0x58), parameters.AppliesTo.GetDensityCode())
	require.Equal(t, "Calibration Job "+strconv.FormatInt(job.Id, 10), parameters.SourceReference)
	require.Equal(t, math.Round(positioning.GetBaseSeconds()*100)/100, positioning.GetBaseSeconds())

	stored := storedParameters(t, exe)
	require.Len(t, stored, 2)
	require.Equal(t, "kept", stored[0].SourceReference)
	require.True(t, proto.Equal(parameters, stored[1]))
	value, err := exe.Lib().Settings().Device.Current(context.Background())
	require.NoError(t, err)
	require.Equal(t, entity.TapeReadOrder_TAPE_READ_ORDER_AUTO, settingspkg.TapeDefaults(value, testDevice).GetReadOrder())

	// Deleting the Job removes only its bundle.
	_, err = exe.DeleteJobs(context.Background(), false, job.Id)
	require.NoError(t, err)
	require.Len(t, storedParameters(t, exe), 2)
}

func TestCalibrationFailures(t *testing.T) {
	for name, test := range map[string]struct {
		drive  *simulatedDrive
		cancel bool
		reason string
	}{
		"irregular moves": {drive: &simulatedDrive{extra: map[int]float64{18: 8}}, reason: ErrIrregular.Error()},
		"too little data": {
			drive:  &simulatedDrive{endpoints: []tape.WrapEndpoint{{Wrap: 0, Object: 35000}, {Wrap: 1, Object: 40000}}},
			reason: "Calibration needs two full wraps of data, about 37 GB on this cartridge, and it holds less; load a cartridge holding that much",
		},
		"position mismatch": {drive: &simulatedDrive{offset: map[int]uint64{3: 1}}, reason: "not the target"},
		"unmoved tape":      {drive: &simulatedDrive{extra: map[int]float64{4: -100}}, reason: "did not move the tape"},
		"cancellation":      {drive: &simulatedDrive{}, cancel: true, reason: "Cancelled by the operator"},
	} {
		t.Run(name, func(t *testing.T) {
			exe, media := setupCalibration(t, test.drive)
			var job *entity.Job
			started := make(chan struct{})
			if test.cancel {
				test.drive.onLocate = func(index int) {
					if index == 5 {
						<-started
						require.NoError(t, exe.Cancel(job.Id))
					}
				}
			}
			job = createCalibration(t, exe, media)
			close(started)
			reply := waitSettled(t, exe, job.Id)
			require.Equal(t, entity.JobStatus_JOB_STATUS_FAILED, reply.Job.Status)
			require.Contains(t, reply.Job.Error, test.reason)
			require.Nil(t, reply.Parameters)
			require.Empty(t, storedParameters(t, exe))
			require.Equal(t, 1, test.drive.finished, "a failed or cancelled Calibration unloads the cartridge")
			if test.cancel {
				require.Len(t, reply.Moves, 5)
			}
		})
	}
}

func TestCalibrationOnTestDataNeedsNoMedia(t *testing.T) {
	drive := &simulatedDrive{}
	exe, _ := setupCalibration(t, drive)
	reply, err := (&service{exe: exe}).Create(context.Background(), &entity.CreateCalibrationJobRequest{
		Spec: &entity.CalibrationJobSpec{Device: testDevice, WriteTestData: true},
	})
	require.NoError(t, err)
	require.Equal(t, testDevice, reply.Job.GetTargetName())
	require.Nil(t, reply.Job.MediaId)
	progress := waitSettled(t, exe, reply.Job.Id)
	require.Equal(t, entity.JobStatus_JOB_STATUS_COMPLETED, progress.Job.Status, progress.Job.Error)
	require.Equal(t, 1, drive.testData)
	require.Equal(t, 1, drive.finished)
	require.Len(t, storedParameters(t, exe), 1)
}

func TestCalibrationRejectsInvalidSpecs(t *testing.T) {
	exe, _ := setupCalibration(t, &simulatedDrive{})
	s := &service{exe: exe}
	for _, spec := range []*entity.CalibrationJobSpec{nil, {}, {WriteTestData: true}, {Device: "  "}} {
		_, err := s.Create(context.Background(), &entity.CreateCalibrationJobRequest{Spec: spec})
		require.Error(t, err)
	}
}

func TestPlanInterpolatesWrapsZeroAndOne(t *testing.T) {
	layout, err := newPlan(&session{density: proto.Uint32(0x58), endpoints: testEndpoints, wrapBytes: float64(testEndpoints[0].Object) * 524288,
		builtin: &entity.TapeReadParameters{Geometry: &entity.TapeReadGeometry{NominalWraps: proto.Uint32(80)}}})
	require.NoError(t, err)
	for _, test := range []struct {
		point  Point
		object uint64
	}{
		{Point{0, 0}, 0}, {Point{0, 0.04}, 1400}, {Point{0, 0.5}, 17500}, {Point{0, 1}, 34999},
		{Point{1, 1}, 35000}, {Point{1, 0.5}, 52505}, {Point{1, 0.03}, 68960}, {Point{1, 0}, 70009},
	} {
		require.Equal(t, test.object, layout.object(test.point), "%+v", test.point)
	}
}
