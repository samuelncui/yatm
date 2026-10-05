package restore

import (
	"context"
	"fmt"

	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/internal/executor"
	"github.com/samuelncui/yatm/internal/library"
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

func (s *service) Estimate(ctx context.Context, req *entity.EstimateRestoreJobRequest) (*entity.EstimateRestoreJobResponse, error) {
	if req == nil {
		return nil, fmt.Errorf("restore selection is required")
	}
	result, err := s.exe.InspectSelections(ctx, &entity.SelectionInspection{Restore: true, Selections: req.Selections,
		FileVersionIds: req.FileVersionIds, Destination: req.Destination, AllowDamagedCopies: req.AllowDamagedCopies,
		VersionPolicy: req.VersionPolicy, SkipUnmatchedVersions: req.SkipUnmatchedVersions})
	if err != nil {
		return nil, err
	}
	return &entity.EstimateRestoreJobResponse{Result: result}, nil
}

func (s *service) Create(ctx context.Context, req *entity.CreateRestoreJobRequest) (*entity.CreateRestoreJobResponse, error) {
	if req == nil {
		return nil, fmt.Errorf("restore job request is missing")
	}
	job, err := Create(ctx, s.exe, req.Priority, req.Spec)
	if err != nil {
		return nil, err
	}
	return &entity.CreateRestoreJobResponse{Job: job.ToEntity()}, nil
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
	job, err := exe.CreateJob(ctx, entity.JobKind_JOB_KIND_RESTORE, priority, func(db *gorm.DB) error {
		if err := db.AutoMigrate(&Config{}, &Copy{}, &File{}); err != nil {
			return fmt.Errorf("create restore schema failed, %w", err)
		}
		if err := db.Create(&Config{ID: 1, Spec: spec}).Error; err != nil {
			return fmt.Errorf("create restore config failed, %w", err)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if err := exe.AddJobProperties(ctx, job.ID, executor.JobProperty{
		Key: executor.JobPropertyLocation, Value: destination.LocationId,
	}); err != nil {
		return nil, err
	}
	return job, nil
}

func (s *service) RestoreMedia(ctx context.Context, req *entity.RestoreMediaRequest) (*entity.RestoreMediaResponse, error) {
	if err := validateRestoreMedia(req); err != nil {
		return nil, err
	}

	// Start one asynchronous Restore Media attempt through the common executor lifecycle.
	if err := s.exe.StartJob(ctx, req.Id, entity.JobKind_JOB_KIND_RESTORE, func(runCtx context.Context, value executor.Runner) error {
		runner, err := restoreRunner(value)
		if err != nil {
			return err
		}
		return runner.restore(runCtx, req)
	}); err != nil {
		return nil, err
	}
	return &entity.RestoreMediaResponse{}, nil
}

func (s *service) GetProgress(ctx context.Context, req *entity.GetRestoreJobProgressRequest) (*entity.GetRestoreJobProgressResponse, error) {
	// Read typed state and shared timing while excluding deletion and a new attempt.
	var reply *entity.GetRestoreJobProgressResponse
	err := s.useRunner(ctx, req.Id, func(runner *jobRestoreRunner) error {
		summary, err := runner.resultSummary(ctx)
		if err != nil {
			return err
		}
		reply = &entity.GetRestoreJobProgressResponse{Progress: runner.progressSnapshot(), Summary: summary}
		return s.exe.ApplyAttemptProgress(ctx, req.Id, reply.Progress)
	})
	if err != nil {
		return nil, err
	}
	return reply, nil
}

func (s *service) ListMedia(ctx context.Context, req *entity.ListRestoreJobMediaRequest) (*entity.ListRestoreJobMediaResponse, error) {
	var reply *entity.ListRestoreJobMediaResponse
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

func (s *service) ListFiles(ctx context.Context, req *entity.ListRestoreJobFilesRequest) (*entity.ListRestoreJobFilesResponse, error) {
	var reply *entity.ListRestoreJobFilesResponse
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
	return s.exe.UseJobRunner(ctx, id, entity.JobKind_JOB_KIND_RESTORE, func(value executor.Runner) error {
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
