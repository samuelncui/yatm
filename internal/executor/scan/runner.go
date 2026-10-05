package scan

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/internal/executor"
	"github.com/samuelncui/yatm/internal/library"
	mediapkg "github.com/samuelncui/yatm/internal/media"
	"github.com/sirupsen/logrus"
	"google.golang.org/protobuf/proto"
	"gorm.io/gorm"
)

func init() { executor.RegisterJobType(entity.JobKind_JOB_KIND_SCAN, newRunner, registerService) }

type runner struct {
	scopes       []*Scope
	locations    []int64
	sources      map[int64]*library.Location
	lock         sync.Mutex
	phase        entity.JobPhase
	phaseStarted time.Time
	contentScope *Scope
	// content is the attempt whose durable snapshot and counters the content phases report; it is
	// published before those phases become observable and replaced for the next source scope.
	content       *contentStream
	previewTiming previewTiming
	activeScope   *Scope
	exe           *executor.Executor
	job           *executor.Job
	db            *gorm.DB
	progress      *executor.Progress
	// previewOutcomes is the current attempt's Preview classification; it survives the phase
	// that produced it so the Job reports it after the phase ends.
	previewOutcomes previewOutcomes
	// previewContent is the content this attempt already generated, so identical files share one
	// decode and one bundle instead of one per entry.
	previewContent map[string]*previewGeneration
	inflightBytes  atomic.Int64
	logFile        *executor.JobLogWriter
	logger         *logrus.Logger
}

func prepareSchema(db *gorm.DB) error {
	return db.AutoMigrate(&Config{}, &Entry{})
}

func newRunner(ctx context.Context, exe *executor.Executor, job *executor.Job) (executor.Runner, error) {
	// Open all bundle resources before exposing the runner, closing acquired handles on failure.
	logFile, err := exe.NewLogWriter(ctx, job.ID)
	if err != nil {
		return nil, err
	}
	db, err := exe.NewStateDB(ctx, job.ID)
	if err != nil {
		_ = logFile.Close()
		return nil, err
	}
	// A new runner has no live stage of its own; an attached attempt observes one.
	phase := entity.JobPhase_JOB_PHASE_INDEXING
	if job.CheckpointState() == entity.JobStatus_JOB_STATUS_READY {
		phase = entity.JobPhase_JOB_PHASE_UNSPECIFIED
	}
	if job.Status == entity.JobStatus_JOB_STATUS_COMPLETED {
		phase = entity.JobPhase_JOB_PHASE_COMPLETED
	}
	logger := logrus.New()
	logger.SetOutput(io.MultiWriter(os.Stderr, logFile))
	return &runner{phase: phase, exe: exe, job: job, db: db, progress: executor.NewProgress(), logFile: logFile, logger: logger}, nil
}

func (r *runner) Index(ctx context.Context) (returnErr error) {
	// Initial creation may transfer a Location reservation; this is the Scan's only preparation.
	r.resetAttemptProgress()
	r.setPhase(entity.JobPhase_JOB_PHASE_INDEXING)
	defer func() {
		if returnErr != nil {
			r.setPhase(entity.JobPhase_JOB_PHASE_UNSPECIFIED)
		}
	}()
	var config Config
	if err := r.db.WithContext(ctx).First(&config, 1).Error; err != nil {
		return err
	}
	if config.Spec == nil {
		return fmt.Errorf("Scan specification is missing")
	}

	// Sequential Media require explicit drive selection after their expectation has been frozen.
	if config.MediaKind == entity.MediaKind_MEDIA_KIND_TAPE {
		if err := r.freezeMedia(ctx, &config); err != nil {
			return err
		}
		if err := r.exe.UpdateJobStatus(ctx, r.job.ID, entity.JobStatus_JOB_STATUS_PREPARING, entity.JobStatus_JOB_STATUS_READY); err != nil {
			return err
		}
		r.setPhase(entity.JobPhase_JOB_PHASE_UNSPECIFIED)
		return nil
	}
	return r.execute(ctx, &config, nil, entity.JobStatus_JOB_STATUS_PREPARING)
}

func (r *runner) readMedia(ctx context.Context, target *entity.ReadMediaTarget) (returnErr error) {
	// A prepared Tape Scan accepts one explicit Media operation; failure ends the Job.
	r.resetAttemptProgress()
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
	return r.execute(ctx, &config, target, entity.JobStatus_JOB_STATUS_READY)
}

func (r *runner) execute(ctx context.Context, config *Config, target *entity.ReadMediaTarget, status entity.JobStatus) error {
	// Freeze every selected input before any Location starts content processing or publication.
	if err := r.prepareInputs(ctx, config, target); err != nil {
		return err
	}
	// Stop on the first failed Location; earlier Library publications remain committed.
	for _, id := range r.locations {
		if err := r.runScope(ctx, config, &Scope{LocationID: id}, target); err != nil {
			return err
		}
	}

	// Metadata commits remain authoritative if the separate Job checkpoint fails.
	if err := r.exe.UpdateJobStatus(ctx, r.job.ID, status, entity.JobStatus_JOB_STATUS_COMPLETED); err != nil {
		return err
	}
	r.setPhase(entity.JobPhase_JOB_PHASE_COMPLETED)
	return nil
}

func (r *runner) validateMedia(ctx context.Context, config *Config) (*library.Media, error) {
	stored, err := r.exe.Lib().GetMedia(ctx, config.Spec.MediaId)
	if err != nil {
		return nil, err
	}
	if stored.Kind != config.MediaKind || stored.Identity != config.MediaIdentity || !proto.Equal(stored.Profile, config.MediaProfile) {
		return nil, fmt.Errorf("Scan Media identity changed, media_id=%d", stored.ID)
	}
	return stored, nil
}

func (r *runner) openSession(ctx context.Context, config *Config, requested *entity.ReadMediaTarget) (mediapkg.ReadSession, error) {
	// The physical selector cannot change the immutable Media kind, identity or profile.
	stored, err := r.validateMedia(ctx, config)
	if err != nil {
		return nil, err
	}
	target := requested
	if target == nil && stored.Kind == entity.MediaKind_MEDIA_KIND_VOLUME {
		target = (&entity.ReadVolumeTarget{Uuid: stored.Identity}).Pack()
	}
	if target == nil {
		return nil, fmt.Errorf("select a Tape device to scan")
	}
	if stored.Kind == entity.MediaKind_MEDIA_KIND_TAPE && target.GetTape() == nil {
		return nil, fmt.Errorf("Scan requires Tape Media")
	}
	if stored.Kind == entity.MediaKind_MEDIA_KIND_VOLUME && target.GetVolume().GetUuid() != stored.Identity {
		return nil, fmt.Errorf("Scan requires Volume %q", stored.Identity)
	}
	target = proto.Clone(target).(*entity.ReadMediaTarget)
	target.ExpectedMediaId, target.ExpectedIdentity, target.ExpectedProfile = stored.ID, config.MediaIdentity, config.MediaProfile
	return r.exe.NewMediaBackend(r.job.ID, r.logger, r.mediaWait).NewReadSession(ctx, target)
}

// mediaWait reports waiting for a shared Media resource while the session is prepared.
func (r *runner) mediaWait(waiting bool) {
	if waiting {
		r.setPhase(entity.JobPhase_JOB_PHASE_QUEUED)
		return
	}
	r.setPhase(entity.JobPhase_JOB_PHASE_PREPARING_MEDIA)
}

func (r *runner) setPhase(phase entity.JobPhase) {
	// Close the previous phase's timing before exposing the next stage.
	r.lock.Lock()
	previous, started := r.phase, r.phaseStarted
	if previous == phase && !started.IsZero() {
		r.lock.Unlock()
		return
	}
	r.phase = phase
	r.phaseStarted = time.Now()
	r.contentScope = nil
	r.previewTiming = previewTiming{}
	r.lock.Unlock()
	if !started.IsZero() {
		r.logResult("Scan phase finished", started, nil, logrus.Fields{"phase": previous.String()})
	}
	r.logInfo("Scan phase started", logrus.Fields{"phase": phase.String()})

	// Publish only phase transitions, keeping runtime samples out of catalog revisions.
	if r.exe != nil && r.job != nil {
		r.exe.TouchJob(r.job.ID)
	}
}
func (r *runner) Phase() entity.JobPhase { r.lock.Lock(); defer r.lock.Unlock(); return r.phase }
func (r *runner) Logger() *logrus.Logger { return r.logger }

func (r *runner) SetActive(active bool) error {
	return executor.SetRunnerFilesActive(r.db, r.logFile, active)
}
func (r *runner) getProgress() *executor.Progress {
	r.lock.Lock()
	defer r.lock.Unlock()
	return r.progress
}
func (r *runner) resetAttemptProgress() {
	// Attempts cannot reuse timing or throughput from the previous execution.
	r.lock.Lock()
	r.progress = executor.NewProgress()
	r.phaseStarted = time.Time{}
	r.contentScope = nil
	r.previewTiming = previewTiming{}
	r.previewOutcomes = previewOutcomes{}
	r.previewContent = nil
	r.lock.Unlock()
	r.inflightBytes.Store(0)
}
func (r *runner) Close() error {
	// Close both independently owned bundle handles.
	db, err := r.db.DB()
	if err != nil {
		return errors.Join(err, r.logFile.Close())
	}
	return errors.Join(db.Close(), r.logFile.Close())
}
