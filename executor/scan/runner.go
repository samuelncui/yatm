package scan

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"sync"

	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/executor"
	"github.com/samuelncui/yatm/library"
	mediapkg "github.com/samuelncui/yatm/media"
	"github.com/samuelncui/yatm/tools"
	"github.com/sirupsen/logrus"
	"google.golang.org/protobuf/proto"
	"gorm.io/gorm"
)

func init() { executor.RegisterJobType(entity.JobKind_SCAN, newRunner, registerService) }

type runner struct {
	lock        sync.Mutex
	phase       entity.JobPhase
	reservation func()
	exe         *executor.Executor
	job         *executor.Job
	db          *gorm.DB
	progress    *executor.Progress
	logFile     *os.File
	logger      *logrus.Logger
}

func prepareSchema(db *gorm.DB) error {
	return db.AutoMigrate(&Config{}, &Scope{}, &Entry{}, &Item{}, &Original{})
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
	if err := prepareSchema(db.WithContext(ctx)); err != nil {
		if sqlDB, openErr := db.DB(); openErr == nil {
			_ = sqlDB.Close()
		}
		_ = logFile.Close()
		return nil, err
	}

	// Reconstruct the durable retry boundary rather than resuming an interrupted in-memory phase.
	phase := entity.JobPhase_JOB_PHASE_WAITING_FOR_INDEX_RETRY
	if job.Status == entity.JobStatus_PENDING {
		phase = entity.JobPhase_JOB_PHASE_WAITING_FOR_MEDIA
	}
	if job.Status == entity.JobStatus_COMPLETED {
		phase = entity.JobPhase_JOB_PHASE_COMPLETED
	}
	logger := logrus.New()
	logger.SetOutput(io.MultiWriter(os.Stderr, logFile))
	return &runner{phase: phase, exe: exe, job: job, db: db, progress: executor.NewProgress(), logFile: logFile, logger: logger}, nil
}

func (r *runner) Index(ctx context.Context) (returnErr error) {
	// Initial creation may transfer a Location reservation; every retry owns the same operation boundary.
	if r.Phase() != entity.JobPhase_JOB_PHASE_WAITING_FOR_INDEX_RETRY {
		return fmt.Errorf("Scan is not waiting for indexing")
	}
	r.setPhase(entity.JobPhase_JOB_PHASE_INDEXING)
	defer func() {
		if returnErr != nil {
			r.setPhase(entity.JobPhase_JOB_PHASE_WAITING_FOR_INDEX_RETRY)
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
		if err := r.exe.UpdateJobStatus(ctx, r.job.ID, entity.JobStatus_INDEXING, entity.JobStatus_PENDING); err != nil {
			return err
		}
		r.setPhase(entity.JobPhase_JOB_PHASE_WAITING_FOR_MEDIA)
		return nil
	}
	return r.execute(ctx, &config, nil, entity.JobStatus_INDEXING)
}

func (r *runner) readMedia(ctx context.Context, target *entity.ReadMediaTarget) (returnErr error) {
	// Media selection is a resumable execution attempt, not a second Job or independent pipeline.
	if r.Phase() != entity.JobPhase_JOB_PHASE_WAITING_FOR_MEDIA {
		return fmt.Errorf("Scan is not waiting for Media")
	}
	r.setPhase(entity.JobPhase_JOB_PHASE_PREPARING_MEDIA)
	defer func() {
		if returnErr != nil {
			r.setPhase(entity.JobPhase_JOB_PHASE_WAITING_FOR_MEDIA)
		}
	}()
	var config Config
	if err := r.db.WithContext(ctx).First(&config, 1).Error; err != nil {
		return err
	}
	return r.execute(ctx, &config, target, entity.JobStatus_PENDING)
}

func (r *runner) execute(ctx context.Context, config *Config, target *entity.ReadMediaTarget, status entity.JobStatus) error {
	// Keep imported identity replacement outside the complete observation and publication boundary.
	release, err := r.exe.Lib().UseOnlineRead()
	if err != nil {
		return err
	}
	defer release()
	if err := r.prepareInputs(ctx, config, target); err != nil {
		return err
	}
	var firstFailure error
	var failed int64
	var after int64 = -1
	for {
		var scope Scope
		result := r.db.WithContext(ctx).Model(&Scope{}).Select("location_id").Where("location_id > ?", after).Group("location_id").Order("location_id").Limit(1).Scan(&scope)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			break
		}
		after = scope.LocationID
		if err := r.runScope(ctx, config, &scope, target); err != nil {
			failed++
			if firstFailure == nil {
				firstFailure = err
			}
			if saveErr := r.db.WithContext(tools.WithoutTimeout(ctx)).Model(&Scope{}).Where("location_id = ? AND published_at = 0 AND error = ?", scope.LocationID, "").Update("error", err.Error()).Error; saveErr != nil {
				return errors.Join(err, saveErr)
			}
			if ctx.Err() != nil {
				return errors.Join(ctx.Err(), firstFailure)
			}
		}
	}
	if firstFailure != nil {
		return fmt.Errorf("%d Scan sources incomplete: %w", failed, firstFailure)
	}

	// Metadata commits remain authoritative if the separate Job checkpoint fails.
	if err := r.exe.UpdateJobStatus(ctx, r.job.ID, status, entity.JobStatus_COMPLETED); err != nil {
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
		return nil, fmt.Errorf("Scan Media binding changed, media_id=%d", stored.ID)
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
	return r.exe.NewMediaBackend(r.job.ID, r.logger).NewReadSession(ctx, r.db, target)
}

func (r *runner) setPhase(phase entity.JobPhase) {
	r.lock.Lock()
	r.phase = phase
	r.lock.Unlock()
	r.exe.TouchJob(r.job.ID)
}
func (r *runner) Phase() entity.JobPhase          { r.lock.Lock(); defer r.lock.Unlock(); return r.phase }
func (r *runner) Logger() *logrus.Logger          { return r.logger }
func (r *runner) getProgress() *executor.Progress { return r.progress }
func (r *runner) Close() error {
	// Release admission and both independently owned bundle handles.
	if r.reservation != nil {
		r.reservation()
		r.reservation = nil
	}
	db, err := r.db.DB()
	if err != nil {
		return errors.Join(err, r.logFile.Close())
	}
	return errors.Join(db.Close(), r.logFile.Close())
}
