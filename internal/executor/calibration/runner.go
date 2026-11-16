package calibration

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sync"
	"time"

	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/internal/executor"
	"github.com/samuelncui/yatm/internal/media/tape"
	settingspkg "github.com/samuelncui/yatm/internal/settings"
	"github.com/sirupsen/logrus"
	"google.golang.org/protobuf/proto"
	"gorm.io/gorm"
)

func init() {
	executor.RegisterJobType(entity.JobKind_JOB_KIND_CALIBRATION, newRunner, registerService)
}

// minimumMoveSeconds separates a LOCATE that moved the tape from one the drive answered from its
// position or buffer; the sequence never revisits a target, so a faster move is unexpected.
const minimumMoveSeconds = 0.3

// Config is the bundle's frozen request and its result.
type Config struct {
	ID   int64                      `gorm:"primaryKey;autoIncrement:false;check:id = 1"`
	Spec *entity.CalibrationJobSpec `gorm:"type:blob;not null"`
	// TotalMoves grows when the threshold probes insert a return to the origin.
	TotalMoves uint32
	Parameters *entity.TapeReadParameters `gorm:"type:blob"`
}

func (Config) TableName() string { return "config" }

// MoveRow is one measured LOCATE, persisted as it completes.
type MoveRow struct {
	ID       int64 `gorm:"primaryKey"`
	FromWrap uint32
	FromSpan float64
	ToWrap   uint32
	ToSpan   float64
	Seconds  float64
}

func (MoveRow) TableName() string { return "moves" }

func (m *MoveRow) toEntity() *entity.CalibrationMove {
	return &entity.CalibrationMove{FromWrap: m.FromWrap, FromSpan: m.FromSpan, ToWrap: m.ToWrap, ToSpan: m.ToSpan, Seconds: m.Seconds}
}

func prepareSchema(db *gorm.DB) error {
	return db.AutoMigrate(&Config{}, &MoveRow{})
}

// drive is the native session the calibration moves: LOCATE, READ POSITION and release.
type drive interface {
	Locate(ctx context.Context, object uint64) error
	Position(ctx context.Context) (uint64, error)
	Finish(ctx context.Context) error
	Cleanup(ctx context.Context) error
}

// session is an opened drive with the facts its calibration needs.
type session struct {
	drive
	serial      string
	density     *uint32
	endpoints   []tape.WrapEndpoint
	recordBytes uint32
	builtin     *entity.TapeReadParameters
}

// openSession and now are replaced by tests: the drive by a simulated one and the clock by its
// simulated time, so no test moves hardware or sleeps.
var (
	openSession = func(ctx context.Context, exe *executor.Executor, jobID int64, spec *entity.CalibrationJobSpec, logger *logrus.Logger) (*session, error) {
		var opened *executor.CalibrationDrive
		if spec.WriteTestData {
			var err error
			if opened, err = exe.OpenCalibrationTestDrive(ctx, jobID, spec.Device, logger); err != nil {
				return nil, err
			}
		} else {
			media, err := exe.Lib().GetMedia(ctx, spec.GetMediaId())
			if err != nil {
				return nil, err
			}
			if opened, err = exe.OpenCalibrationDrive(ctx, jobID, spec.Device, media); err != nil {
				return nil, err
			}
		}
		return &session{drive: opened, serial: opened.Serial(), density: opened.Report.GetTape().DensityCode,
			endpoints: opened.Endpoints, recordBytes: opened.RecordBytes, builtin: opened.Builtin}, nil
	}
	now = time.Now
)

type runner struct {
	exe     *executor.Executor
	job     *executor.Job
	db      *gorm.DB
	logFile *executor.JobLogWriter
	logger  *logrus.Logger

	lock  sync.Mutex
	phase entity.JobPhase
}

func newRunner(ctx context.Context, exe *executor.Executor, job *executor.Job) (executor.Runner, error) {
	files, err := exe.OpenRunnerFiles(ctx, job.ID)
	if err != nil {
		return nil, err
	}
	return &runner{exe: exe, job: job, db: files.DB, logFile: files.Log, logger: files.Logger}, nil
}

// Index runs the Job's only attempt: prepare the drive, measure the sequence, fit and store.
func (r *runner) Index(ctx context.Context) (returnErr error) {
	r.setPhase(entity.JobPhase_JOB_PHASE_PREPARING_MEDIA)
	defer func() {
		if returnErr != nil {
			r.setPhase(entity.JobPhase_JOB_PHASE_UNSPECIFIED)
		}
	}()
	var config Config
	if err := r.db.WithContext(ctx).First(&config, 1).Error; err != nil {
		return err
	}
	spec := config.Spec

	// The device lease queues behind and excludes every other Job on this drive.
	err := r.exe.AcquireTapeDevice(ctx, r.job.ID, spec.Device, func() { r.setPhase(entity.JobPhase_JOB_PHASE_QUEUED) })
	r.setPhase(entity.JobPhase_JOB_PHASE_PREPARING_MEDIA)
	if err != nil {
		return err
	}
	opened, err := openSession(ctx, r.exe, r.job.ID, spec, r.logger)
	if err != nil {
		return err
	}
	released := false
	defer func() {
		if !released {
			returnErr = errors.Join(returnErr, opened.Cleanup(ctx))
		}
	}()
	layout, err := newPlan(opened)
	if err != nil {
		return err
	}

	// An idle drive repositions itself, so measuring starts right after preparation.
	r.setPhase(entity.JobPhase_JOB_PHASE_CALIBRATING)
	sequence, err := r.measure(ctx, opened, layout)
	if err != nil {
		return err
	}
	low, high := sequence.Threshold()
	result, err := Fit(sequence.Moves(), low, high)
	if err != nil {
		return err
	}
	r.logger.WithFields(logrus.Fields{
		"job_id": r.job.ID, "base_seconds": result.BaseSeconds, "seek_seconds_per_wrap": result.SeekSecondsPerWrap,
		"approach_reversal_seconds": result.ApproachReversalSeconds, "read_seconds_per_wrap": result.ReadSecondsPerWrap,
		"stream_spans": result.StreamSpans, "max_residual_seconds": result.MaxResidualSeconds,
	}).Info("Calibration fitted")

	// The timed unload prices the completion terms from the final position.
	started := now()
	released = true
	if err := opened.Finish(ctx); err != nil {
		return err
	}
	unloadSeconds := now().Sub(started).Seconds()
	parameters := layout.parameters(r.job.ID, result, unloadSeconds, sequence.at.U)
	r.logger.WithFields(logrus.Fields{"job_id": r.job.ID, "unload_seconds": unloadSeconds}).Info("Calibration unloaded the cartridge")

	if err := r.db.WithContext(ctx).Model(&Config{}).Where("id = ?", 1).Update("parameters", parameters).Error; err != nil {
		return err
	}
	if err := storeParameters(ctx, r.exe.Lib().Settings(), spec.Device, parameters); err != nil {
		return err
	}
	if err := r.exe.UpdateJobStatus(ctx, r.job.ID, entity.JobStatus_JOB_STATUS_PREPARING, entity.JobStatus_JOB_STATUS_COMPLETED); err != nil {
		return err
	}
	r.setPhase(entity.JobPhase_JOB_PHASE_COMPLETED)
	return nil
}

// measure drives the sequence, timing exactly each LOCATE and checking where it arrived.
func (r *runner) measure(ctx context.Context, d drive, layout *plan) (*Sequence, error) {
	sequence := NewSequence(Point{})
	for first := true; ; first = false {
		target, ok := sequence.Next()
		if !ok {
			return sequence, nil
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		object := layout.object(target)
		started := now()
		if err := d.Locate(ctx, object); err != nil {
			return nil, fmt.Errorf("LOCATE to wrap %d at span %.3f failed, %w", target.Wrap, target.U, err)
		}
		seconds := now().Sub(started).Seconds()

		// A completed LOCATE is checked and recorded even when cancellation arrived during it.
		settle := context.WithoutCancel(ctx)
		position, err := d.Position(settle)
		if err != nil {
			return nil, fmt.Errorf("READ POSITION after LOCATE failed, %w", err)
		}
		if position != object {
			return nil, fmt.Errorf("Tape position after LOCATE is logical object %d, not the target %d", position, object)
		}
		if seconds < minimumMoveSeconds {
			return nil, fmt.Errorf("LOCATE to wrap %d at span %.3f took %.2f s and did not move the tape; this drive does not position as the calibration expects",
				target.Wrap, target.U, seconds)
		}

		// The first LOCATE only leaves the load point and is not fitted.
		from := sequence.at
		sequence.Record(target, seconds)
		if first {
			r.logger.WithFields(logrus.Fields{"job_id": r.job.ID, "to_wrap": target.Wrap, "to_span": target.U, "seconds": seconds}).
				Info("Calibration reached its origin")
			continue
		}
		move := &MoveRow{FromWrap: from.Wrap, FromSpan: from.U, ToWrap: target.Wrap, ToSpan: target.U, Seconds: seconds}
		if err := r.recordMove(settle, move, sequence.total()); err != nil {
			return nil, err
		}
	}
}

func (r *runner) recordMove(ctx context.Context, move *MoveRow, total int) error {
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(move).Error; err != nil {
			return err
		}
		return tx.Model(&Config{}).Where("id = ?", 1).Update("total_moves", total).Error
	})
	if err != nil {
		return fmt.Errorf("record calibration move failed, %w", err)
	}
	r.logger.WithFields(logrus.Fields{
		"job_id": r.job.ID, "from_wrap": move.FromWrap, "from_span": move.FromSpan,
		"to_wrap": move.ToWrap, "to_span": move.ToSpan, "seconds": move.Seconds, "total_moves": total,
	}).Info("Calibration move")
	return nil
}

// total is the number of fitted moves of the run, including the returns to the origin inserted
// between threshold probes so far.
func (s *Sequence) total() int {
	return len(forward) + len(back) + probes + s.returns + 1 + len(cross)
}

// plan converts sequence targets to logical objects of the loaded cartridge and its results to read
// parameters.
type plan struct {
	// low and high bound the logical objects of wraps 0 and 1.
	low, high   [2]uint64
	recordBytes uint32
	density     uint32
	serial      string
	builtin     *entity.TapeReadParameters
}

func newPlan(s *session) (*plan, error) {
	if s.density == nil {
		return nil, errors.New("Calibration needs the cartridge's recorded density, which the drive does not report")
	}
	if s.builtin.GetGeometry().GetNominalWraps() == 0 {
		return nil, fmt.Errorf("Calibration has no built-in layout for density code %d", *s.density)
	}
	if len(s.endpoints) == 0 {
		return nil, errors.New("Calibration needs the drive's current wrap endpoint map, which is unavailable")
	}

	// The last endpoint is EOD inside a partial wrap, so wraps 0 and 1 are complete only before a third.
	if len(s.endpoints) < 3 {
		message := "Calibration needs the first two wraps written"
		if len(s.endpoints) == 2 {
			message += fmt.Sprintf(" (about %.0f GB on this cartridge)", 2*float64(s.endpoints[0].Object)*float64(s.recordBytes)/1e9)
		}
		return nil, errors.New(message)
	}
	return &plan{
		low: [2]uint64{0, s.endpoints[0].Object}, high: [2]uint64{s.endpoints[0].Object, s.endpoints[1].Object},
		recordBytes: s.recordBytes, density: *s.density, serial: s.serial, builtin: s.builtin,
	}, nil
}

// object interpolates a target linearly inside its wrap's endpoint interval; even wraps run toward
// increasing span, odd wraps back toward the beginning of tape.
func (p *plan) object(target Point) uint64 {
	low, high := p.low[target.Wrap], p.high[target.Wrap]
	fraction := target.U
	if target.Wrap%2 == 1 {
		fraction = 1 - fraction
	}
	offset := uint64(math.Round(fraction * float64(high-low)))
	return min(low+offset, high-1)
}

// parameters builds a complete read parameter set: measured terms, the geometry and LTFS unmount
// time of the built-in values for this density, and completion from the timed unload.
func (p *plan) parameters(jobID int64, result *Result, unloadSeconds, finalSpan float64) *entity.TapeReadParameters {
	wrapBytes := float64(p.high[0]-p.low[0]) * float64(p.recordBytes)
	applies := &entity.TapeReadApplicability{DensityCode: proto.Uint32(p.density)}
	if p.serial != "" {
		applies.DriveSerial = proto.String(p.serial)
	}
	completion := &entity.TapeReadCompletion{
		FullSpanSeconds: seconds(result.SeekSecondsPerWrap),
		FixedSeconds:    seconds(max(0, unloadSeconds-result.SeekSecondsPerWrap*finalSpan)),
	}
	if value := p.builtin.GetCompletion().LtfsUnmountSeconds; value != nil {
		completion.LtfsUnmountSeconds = proto.Float64(*value)
	}
	return &entity.TapeReadParameters{
		AppliesTo: applies,
		Geometry:  proto.Clone(p.builtin.GetGeometry()).(*entity.TapeReadGeometry),
		Positioning: &entity.TapeReadPositioning{
			BaseSeconds: seconds(result.BaseSeconds), SeekSecondsPerWrap: seconds(result.SeekSecondsPerWrap),
			ApproachReversalSeconds: seconds(result.ApproachReversalSeconds),
			BufferBytes:             proto.Uint64(uint64(math.Round(result.StreamSpans * wrapBytes))),
		},
		Reading:         &entity.TapeReadTiming{BytesPerSecond: proto.Float64(math.Round(wrapBytes / result.ReadSecondsPerWrap))},
		Completion:      completion,
		SourceReference: fmt.Sprintf("Calibration Job %d", jobID),
	}
}

func seconds(value float64) *float64 { return proto.Float64(math.Round(value*100) / 100) }

// storeParameters replaces this device's set for the same density code, or adds it, through the
// Device settings group's ordinary validated save.
func storeParameters(ctx context.Context, settings *settingspkg.Module, device string, parameters *entity.TapeReadParameters) error {
	value, err := settings.Device.Current(ctx)
	if err != nil {
		return err
	}
	var tapeSettings *entity.TapeDeviceSettings
	for _, config := range value.Devices {
		if config.Device == device && config.GetTape() != nil {
			tapeSettings = config.GetTape()
			break
		}
	}
	if tapeSettings == nil {
		tapeSettings = &entity.TapeDeviceSettings{}
		value.Devices = append(value.Devices, &entity.DeviceConfig{Device: device, Options: &entity.DeviceConfig_Tape{Tape: tapeSettings}})
	}
	density := parameters.GetAppliesTo().GetDensityCode()
	replaced := false
	for index, existing := range tapeSettings.ReadParameters {
		if existing.GetAppliesTo().GetDensityCode() == density {
			tapeSettings.ReadParameters[index], replaced = parameters, true
			break
		}
	}
	if !replaced {
		tapeSettings.ReadParameters = append(tapeSettings.ReadParameters, parameters)
	}
	_, err = settings.Device.Save(ctx, value)
	return err
}

func (r *runner) setPhase(phase entity.JobPhase) {
	r.lock.Lock()
	previous := r.phase
	r.phase = phase
	r.lock.Unlock()
	if previous == phase {
		return
	}
	r.logger.WithFields(logrus.Fields{"job_id": r.job.ID, "phase": phase.String()}).Info("Calibration phase started")
	r.exe.TouchJob(r.job.ID)
}

func (r *runner) Phase() entity.JobPhase { r.lock.Lock(); defer r.lock.Unlock(); return r.phase }
func (r *runner) Logger() *logrus.Logger { return r.logger }
func (r *runner) SetActive(active bool) error {
	return executor.SetRunnerFilesActive(r.db, r.logFile, active)
}
func (r *runner) Close() error { return executor.CloseRunnerFiles(r.db, r.logFile) }
