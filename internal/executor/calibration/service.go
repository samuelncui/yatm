package calibration

import (
	"context"
	"fmt"
	"strings"

	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/internal/executor"
	"github.com/samuelncui/yatm/internal/library"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/proto"
	"gorm.io/gorm"
)

type service struct {
	entity.UnimplementedCalibrationJobServiceServer
	exe *executor.Executor
}

func registerService(server grpc.ServiceRegistrar, exe *executor.Executor) {
	entity.RegisterCalibrationJobServiceServer(server, &service{exe: exe})
}

// Create validates the request, builds the bundle and runs the Job's only attempt immediately.
func (s *service) Create(ctx context.Context, req *entity.CreateCalibrationJobRequest) (*entity.CreateCalibrationJobResponse, error) {
	if req.GetSpec() == nil {
		return nil, fmt.Errorf("Calibration specification is missing")
	}
	spec := proto.Clone(req.Spec).(*entity.CalibrationJobSpec)
	spec.Device = strings.TrimSpace(spec.Device)
	if spec.Device == "" {
		return nil, fmt.Errorf("Calibration requires a Tape device")
	}
	if (spec.MediaId != nil) == spec.WriteTestData {
		return nil, fmt.Errorf("Calibration requires exactly one of a Media ID and writing test data")
	}
	target := executor.JobTarget{TargetName: spec.Device}
	if spec.MediaId != nil {
		media, err := s.exe.Lib().GetMedia(ctx, spec.GetMediaId())
		if err != nil {
			return nil, err
		}
		if media.Kind != entity.MediaKind_MEDIA_KIND_TAPE || media.Profile.GetTape().GetFormat() != library.TapeFormatTarPAXV1 {
			return nil, fmt.Errorf("Calibration requires tar Tape Media, media_id=%d", media.ID)
		}
		target.MediaID = media.ID
	}

	config := &Config{ID: 1, Spec: spec, TotalMoves: uint32(NewSequence(Point{}).total())}
	job, err := s.exe.CreateTargetJob(ctx, entity.JobKind_JOB_KIND_CALIBRATION, 0, target, func(db *gorm.DB) error {
		if err := prepareSchema(db); err != nil {
			return err
		}
		return db.Create(config).Error
	})
	if err != nil {
		return nil, err
	}
	return &entity.CreateCalibrationJobResponse{Job: job.ToEntity()}, nil
}

// GetProgress reports the measured moves and, once fitted, the parameters.
func (s *service) GetProgress(ctx context.Context, req *entity.GetCalibrationJobProgressRequest) (*entity.GetCalibrationJobProgressResponse, error) {
	if req == nil {
		return nil, fmt.Errorf("Calibration progress request is missing")
	}
	reply := &entity.GetCalibrationJobProgressResponse{}
	err := s.exe.UseJobRunner(ctx, req.Id, entity.JobKind_JOB_KIND_CALIBRATION, func(value executor.Runner) error {
		r, ok := value.(*runner)
		if !ok {
			return fmt.Errorf("unexpected Calibration runner %T", value)
		}
		var config Config
		if err := r.db.WithContext(ctx).First(&config, 1).Error; err != nil {
			return err
		}
		var moves []*MoveRow
		if err := r.db.WithContext(ctx).Order("id").Find(&moves).Error; err != nil {
			return err
		}
		job, err := s.exe.GetJob(ctx, req.Id)
		if err != nil {
			return err
		}
		reply.Job, reply.Spec, reply.TotalMoves, reply.Parameters = job.ToEntity(), config.Spec, config.TotalMoves, config.Parameters
		for _, move := range moves {
			reply.Moves = append(reply.Moves, move.toEntity())
		}

		// The shared attempt timing supplies the live or frozen duration.
		progress := &entity.Progress{}
		if err := s.exe.ApplyAttemptProgress(ctx, req.Id, progress); err != nil {
			return err
		}
		reply.ElapsedMs = progress.ElapsedMs
		return nil
	})
	return reply, err
}
