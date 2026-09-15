package restore

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/executor"
	"github.com/samuelncui/yatm/library"
	"google.golang.org/grpc"
	"gorm.io/gorm"
)

var _ entity.RestoreJobServiceServer = (*service)(nil)

type service struct {
	entity.UnimplementedRestoreJobServiceServer

	exe *executor.Executor
}

func registerService(server grpc.ServiceRegistrar, exe *executor.Executor) {
	entity.RegisterRestoreJobServiceServer(server, &service{exe: exe})
}

func (s *service) Create(ctx context.Context, req *entity.CreateRestoreJobRequest) (*entity.CreateRestoreJobReply, error) {
	if req == nil {
		return nil, fmt.Errorf("restore job request is missing")
	}
	job, err := Create(ctx, s.exe, req.Priority, req.Spec)
	if err != nil {
		return nil, err
	}
	return &entity.CreateRestoreJobReply{Job: job.ToEntity()}, nil
}

// Create validates the selection and freezes the destination before publishing a complete Job bundle.
func Create(ctx context.Context, exe *executor.Executor, priority int64, spec *entity.RestoreJobSpec) (*executor.Job, error) {
	// Reject malformed or unconsented policy combinations before creating a Job bundle.
	if spec == nil {
		return nil, fmt.Errorf("restore job spec is missing")
	}
	if len(spec.FileVersionIds)+len(spec.Selections) == 0 || len(spec.FileVersionIds)+len(spec.Selections) > 1000 {
		return nil, fmt.Errorf("Restore requires between 1 and 1000 selection roots or versions")
	}
	for _, id := range spec.FileVersionIds {
		if id <= 0 {
			return nil, fmt.Errorf("invalid FileVersion ID %d", id)
		}
	}
	if err := library.ValidateRestoreVersionSelection(spec.VersionPolicy, spec.SkipUnmatchedVersions); err != nil {
		return nil, err
	}

	// Freeze access and presentation inputs; selected content freezes during manifest preparation.
	release, err := exe.Lib().UseOnlineRead()
	if err != nil {
		return nil, err
	}
	defer release()
	destination, err := exe.FreezeRestoreDestination(ctx, spec.Destination)
	if err != nil {
		return nil, err
	}
	spec.Destination = destination
	if len(spec.Selections) != 0 {
		if err := exe.Lib().FreezeSelections(ctx, spec.Selections); err != nil {
			return nil, err
		}
	}

	// Create the common Job record and the complete Restore schema together.
	job, err := exe.CreateJob(ctx, entity.JobKind_RESTORE, priority, func(db *gorm.DB) error {
		if err := db.AutoMigrate(&Config{}, &Copy{}, &Output{}, &FileSelection{}); err != nil {
			return fmt.Errorf("create restore schema failed, %w", err)
		}
		if err := db.Create(&Config{ID: 1, Spec: spec, OperationID: uuid.NewString()}).Error; err != nil {
			return fmt.Errorf("create restore config failed, %w", err)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if err := exe.AddJobResources(ctx, job.ID, executor.JobResource{
		Kind: executor.JobResourceLocation, Role: executor.JobResourceDestination, ResourceID: destination.LocationId,
	}); err != nil {
		return nil, err
	}
	return job, nil
}

func (s *service) RestoreMedia(ctx context.Context, req *entity.RestoreMediaRequest) (*entity.RestoreMediaReply, error) {
	if err := validateRestoreMedia(req); err != nil {
		return nil, err
	}

	// Start one asynchronous Restore Media attempt through the common executor lifecycle.
	if err := s.exe.StartJob(ctx, req.Id, entity.JobKind_RESTORE, func(runCtx context.Context, value executor.Runner) error {
		runner, err := restoreRunner(value)
		if err != nil {
			return err
		}
		return runner.restore(runCtx, req)
	}); err != nil {
		return nil, err
	}
	return &entity.RestoreMediaReply{}, nil
}

func (s *service) GetProgress(ctx context.Context, req *entity.GetRestoreJobProgressRequest) (*entity.GetRestoreJobProgressReply, error) {
	var reply *entity.GetRestoreJobProgressReply
	err := s.useRunner(ctx, req.Id, func(runner *jobRestoreRunner) error {
		summary, err := runner.resultSummary(ctx)
		if err != nil {
			return err
		}
		reply = &entity.GetRestoreJobProgressReply{Progress: runner.getProgress().ToEntity(), Summary: summary}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return reply, nil
}

func (s *service) ListMedia(ctx context.Context, req *entity.ListRestoreJobMediaRequest) (*entity.ListRestoreJobMediaReply, error) {
	var reply *entity.ListRestoreJobMediaReply
	err := s.useRunner(ctx, req.Id, func(runner *jobRestoreRunner) error {
		var err error
		reply, err = runner.queryMedia(ctx, req)
		return err
	})
	if err != nil {
		return nil, err
	}
	return reply, nil
}

func (s *service) ListFiles(ctx context.Context, req *entity.ListRestoreJobFilesRequest) (*entity.ListRestoreJobFilesReply, error) {
	var reply *entity.ListRestoreJobFilesReply
	err := s.useRunner(ctx, req.Id, func(runner *jobRestoreRunner) error {
		var err error
		reply, err = runner.queryFiles(ctx, req)
		return err
	})
	if err != nil {
		return nil, err
	}
	return reply, nil
}

func (s *service) useRunner(ctx context.Context, id int64, use func(*jobRestoreRunner) error) error {
	return s.exe.UseJobRunner(ctx, id, entity.JobKind_RESTORE, func(value executor.Runner) error {
		runner, err := restoreRunner(value)
		if err != nil {
			return err
		}
		return use(runner)
	})
}

func restoreRunner(value executor.Runner) (*jobRestoreRunner, error) {
	runner, ok := value.(*jobRestoreRunner)
	if !ok {
		return nil, fmt.Errorf("unexpected restore runner, type=%T", value)
	}
	return runner, nil
}
