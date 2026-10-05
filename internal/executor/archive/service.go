package archive

import (
	"context"
	"fmt"

	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/internal/executor"
	"google.golang.org/grpc"
	"gorm.io/gorm"
)

var _ entity.ArchiveJobServiceServer = (*service)(nil)

type service struct {
	entity.UnimplementedArchiveJobServiceServer

	exe *executor.Executor
}

func registerService(server grpc.ServiceRegistrar, exe *executor.Executor) {
	entity.RegisterArchiveJobServiceServer(server, &service{exe: exe})
}

func (s *service) Estimate(ctx context.Context, req *entity.EstimateArchiveJobRequest) (*entity.EstimateArchiveJobResponse, error) {
	if req == nil {
		return nil, fmt.Errorf("archive selection is required")
	}
	result, err := s.exe.InspectSelections(ctx, &entity.SelectionInspection{Selections: req.Selections})
	if err != nil {
		return nil, err
	}
	return &entity.EstimateArchiveJobResponse{Result: result}, nil
}

func (s *service) Create(ctx context.Context, req *entity.CreateArchiveJobRequest) (*entity.CreateArchiveJobResponse, error) {
	// Reject incomplete requests and force policies that have no Preview consumer.
	if req == nil || req.Spec == nil {
		return nil, fmt.Errorf("archive job spec is missing")
	}
	policy := req.PreviewPolicy
	if policy == entity.PreviewPolicy_PREVIEW_POLICY_UNSPECIFIED {
		policy = entity.PreviewPolicy_PREVIEW_POLICY_NONE
	}
	if _, ok := entity.PreviewPolicy_name[int32(policy)]; !ok {
		return nil, fmt.Errorf("invalid Preview policy")
	}
	if policy == entity.PreviewPolicy_PREVIEW_POLICY_NONE && req.ForceRehash {
		return nil, fmt.Errorf("force rehash requires Preview generation")
	}
	if policy != entity.PreviewPolicy_PREVIEW_POLICY_NONE {
		if s.exe.Previews() == nil {
			return nil, fmt.Errorf("Preview module is not configured")
		}
		if err := s.exe.Previews().CheckGeneration(ctx); err != nil {
			return nil, err
		}
	}
	// Preserve selected identities until the created Job is visible to catalog maintenance.
	if err := s.exe.Lib().FreezeSelections(ctx, req.Spec.Selections); err != nil {
		return nil, err
	}
	if err := s.exe.Lib().ValidateOriginalSelections(ctx, req.Spec.Selections); err != nil {
		return nil, err
	}

	// Create the common Job record and the complete Archive schema together.
	config := &Config{ID: 1, Spec: req.Spec}
	if policy != entity.PreviewPolicy_PREVIEW_POLICY_NONE {
		config.Preview = &entity.ScanJobSpec{SignaturePolicy: entity.ScanSignaturePolicy_SCAN_SIGNATURE_POLICY_KNOWN_ONLY,
			ResultPolicy: entity.ScanResultPolicy_SCAN_RESULT_POLICY_REPORT_ONLY, PreviewPolicy: policy}
		if req.ForceRehash {
			config.Preview.SignaturePolicy = entity.ScanSignaturePolicy_SCAN_SIGNATURE_POLICY_FORCE_READ
		}
	}
	job, err := s.exe.CreateJob(ctx, entity.JobKind_JOB_KIND_ARCHIVE, req.Priority, func(db *gorm.DB) error {
		if err := ensureSchema(ctx, db); err != nil {
			return fmt.Errorf("create archive schema failed, %w", err)
		}
		if err := db.Create(config).Error; err != nil {
			return fmt.Errorf("create archive config failed, %w", err)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &entity.CreateArchiveJobResponse{Job: job.ToEntity()}, nil
}

func (s *service) WriteMedia(ctx context.Context, req *entity.WriteArchiveMediaRequest) (*entity.WriteArchiveMediaResponse, error) {
	if err := validateWriteMedia(req); err != nil {
		return nil, err
	}

	// Start one asynchronous Archive Media attempt through the common executor lifecycle.
	if err := s.exe.StartJob(ctx, req.Id, entity.JobKind_JOB_KIND_ARCHIVE, func(runCtx context.Context, value executor.Runner) error {
		runner, err := archiveRunner(value)
		if err != nil {
			return err
		}
		return runner.writeMedia(runCtx, req)
	}); err != nil {
		return nil, err
	}
	return &entity.WriteArchiveMediaResponse{}, nil
}

func (s *service) GetProgress(ctx context.Context, req *entity.GetArchiveJobProgressRequest) (*entity.GetArchiveJobProgressResponse, error) {
	// Read typed state and shared timing while excluding deletion and a new attempt.
	var reply *entity.GetArchiveJobProgressResponse
	err := s.useRunner(ctx, req.Id, func(runner *jobArchiveRunner) error {
		var config Config
		if err := runner.db.WithContext(ctx).First(&config, 1).Error; err != nil {
			return err
		}
		reply = &entity.GetArchiveJobProgressResponse{Progress: runner.progressSnapshot(), PreviewJobId: config.PreviewJobID, PreviewError: config.PreviewError}
		return s.exe.ApplyAttemptProgress(ctx, req.Id, reply.Progress)
	})
	if err != nil {
		return nil, err
	}
	return reply, nil
}

func (s *service) ListFiles(ctx context.Context, req *entity.ListArchiveJobFilesRequest) (*entity.ListArchiveJobFilesResponse, error) {
	var reply *entity.ListArchiveJobFilesResponse
	err := s.useRunner(ctx, req.Id, func(runner *jobArchiveRunner) error {
		var err error
		reply, err = runner.queryFiles(ctx, req)
		return err
	})
	if err != nil {
		return nil, err
	}
	return reply, nil
}

func (s *service) useRunner(ctx context.Context, id int64, use func(*jobArchiveRunner) error) error {
	return s.exe.UseJobRunner(ctx, id, entity.JobKind_JOB_KIND_ARCHIVE, func(value executor.Runner) error {
		runner, err := archiveRunner(value)
		if err != nil {
			return err
		}
		return use(runner)
	})
}

func archiveRunner(value executor.Runner) (*jobArchiveRunner, error) {
	runner, ok := value.(*jobArchiveRunner)
	if !ok {
		return nil, fmt.Errorf("unexpected archive runner, type=%T", value)
	}
	return runner, nil
}
