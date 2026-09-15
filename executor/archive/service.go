package archive

import (
	"context"
	"fmt"

	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/executor"
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

func (s *service) Create(ctx context.Context, req *entity.CreateArchiveJobRequest) (*entity.CreateArchiveJobReply, error) {
	// Reject incomplete requests and force policies that have no Preview consumer.
	if req == nil || req.Spec == nil {
		return nil, fmt.Errorf("archive job spec is missing")
	}
	if _, ok := entity.PreviewPolicy_name[int32(req.PreviewPolicy)]; !ok {
		return nil, fmt.Errorf("invalid Preview policy")
	}
	if req.PreviewPolicy == entity.PreviewPolicy_PREVIEW_NONE && req.ForceRehash {
		return nil, fmt.Errorf("force rehash requires Preview generation")
	}
	if len(req.Spec.Sources) != 0 {
		return nil, fmt.Errorf("Backup requires Library or registered Location selections")
	}
	if len(req.Spec.FileIds) != 0 && len(req.Spec.Selections) != 0 {
		return nil, fmt.Errorf("use File IDs or selections, not both")
	}
	for _, id := range req.Spec.FileIds {
		req.Spec.Selections = append(req.Spec.Selections, &entity.FileSelection{Target: &entity.FileSelection_Library{Library: &entity.LibrarySelection{FileId: id}}, Scope: entity.FileScope_FILE_SCOPE_ALL})
	}
	req.Spec.FileIds = nil
	if err := s.exe.Lib().FreezeSelections(ctx, req.Spec.Selections); err != nil {
		return nil, err
	}

	// Create the common Job record and the complete Archive schema together.
	config := &Config{ID: 1, Spec: req.Spec}
	if req.PreviewPolicy != entity.PreviewPolicy_PREVIEW_NONE {
		config.Preview = &entity.ScanJobSpec{SignaturePolicy: entity.ScanSignaturePolicy_KNOWN_ONLY,
			ResultPolicy: entity.ScanResultPolicy_REPORT_ONLY, PreviewPolicy: req.PreviewPolicy}
		if req.ForceRehash {
			config.Preview.SignaturePolicy = entity.ScanSignaturePolicy_FORCE_READ
		}
	}
	job, err := s.exe.CreateJob(ctx, entity.JobKind_ARCHIVE, req.Priority, func(db *gorm.DB) error {
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
	return &entity.CreateArchiveJobReply{Job: job.ToEntity()}, nil
}

func (s *service) WriteMedia(ctx context.Context, req *entity.WriteArchiveMediaRequest) (*entity.WriteArchiveMediaReply, error) {
	if err := validateWriteMedia(req); err != nil {
		return nil, err
	}

	// Start one asynchronous Archive Media attempt through the common executor lifecycle.
	if err := s.exe.StartJob(ctx, req.Id, entity.JobKind_ARCHIVE, func(runCtx context.Context, value executor.Runner) error {
		runner, err := archiveRunner(value)
		if err != nil {
			return err
		}
		return runner.writeMedia(runCtx, req)
	}); err != nil {
		return nil, err
	}
	return &entity.WriteArchiveMediaReply{}, nil
}

func (s *service) GetProgress(ctx context.Context, req *entity.GetArchiveJobProgressRequest) (*entity.GetArchiveJobProgressReply, error) {
	var reply *entity.GetArchiveJobProgressReply
	err := s.useRunner(ctx, req.Id, func(runner *jobArchiveRunner) error {
		var config Config
		if err := runner.db.WithContext(ctx).First(&config, 1).Error; err != nil {
			return err
		}
		reply = &entity.GetArchiveJobProgressReply{Progress: runner.getProgress().ToEntity(), PreviewJobId: config.PreviewJobID, PreviewError: config.PreviewError}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return reply, nil
}

func (s *service) ListFiles(ctx context.Context, req *entity.ListArchiveJobFilesRequest) (*entity.ListArchiveJobFilesReply, error) {
	var reply *entity.ListArchiveJobFilesReply
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
	return s.exe.UseJobRunner(ctx, id, entity.JobKind_ARCHIVE, func(value executor.Runner) error {
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
