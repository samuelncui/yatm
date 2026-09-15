package scan

import (
	"context"
	"fmt"

	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/executor"
	"github.com/samuelncui/yatm/library"
	mediapkg "github.com/samuelncui/yatm/media"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/proto"
	"gorm.io/gorm"
)

type service struct {
	entity.UnimplementedScanJobServiceServer
	exe *executor.Executor
}

const maxListEntries = 1000

func registerService(server grpc.ServiceRegistrar, exe *executor.Executor) {
	entity.RegisterScanJobServiceServer(server, &service{exe: exe})
}

// Create uses the public Scan contract for automatic collection and explicit user work alike.
func Create(ctx context.Context, exe *executor.Executor, req *entity.CreateScanJobRequest) (*entity.CreateScanJobReply, error) {
	return (&service{exe: exe}).Create(ctx, req)
}

func validateSpec(spec *entity.ScanJobSpec) error {
	// Reject unknown policy values before source-specific validation or Job creation.
	if spec == nil {
		return fmt.Errorf("Scan specification is missing")
	}
	if _, ok := entity.ScanSignaturePolicy_name[int32(spec.SignaturePolicy)]; !ok {
		return fmt.Errorf("invalid Scan signature policy")
	}
	if _, ok := entity.ScanResultPolicy_name[int32(spec.ResultPolicy)]; !ok {
		return fmt.Errorf("invalid Scan result policy")
	}
	if _, ok := entity.PreviewPolicy_name[int32(spec.PreviewPolicy)]; !ok {
		return fmt.Errorf("invalid Scan Preview policy")
	}
	if spec.LocationId < 0 || spec.MediaId < 0 {
		return fmt.Errorf("Scan source identifiers cannot be negative")
	}

	// Exactly one bounded input namespace owns every relative selection.
	sources := 0
	if spec.LocationId > 0 {
		sources++
	}
	if spec.MediaId > 0 {
		sources++
	}
	if len(spec.Selections) > 0 {
		sources++
	}
	if sources != 1 {
		return fmt.Errorf("Scan requires exactly one Location, Media or selection source")
	}
	if len(spec.Paths) > 1000 || len(spec.Selections) > 1000 {
		return fmt.Errorf("Scan supports at most 1000 selection roots")
	}
	if len(spec.Paths) > 0 && spec.LocationId == 0 {
		return fmt.Errorf("Scan paths require a Location")
	}
	for _, path := range spec.Paths {
		if path == "" {
			continue
		}
		if err := entity.ValidateRelativePath(path); err != nil {
			return err
		}
	}
	for _, selection := range spec.Selections {
		if selection == nil || selection.Target == nil {
			return fmt.Errorf("Scan selection is missing")
		}
		if local := selection.GetLocation(); local != nil {
			if local.LocationId <= 0 {
				return fmt.Errorf("Scan Location is missing")
			}
			if local.Path != "" {
				if err := entity.ValidateRelativePath(local.Path); err != nil {
					return err
				}
			}
			if ref := local.Reference; ref != nil && (ref.LocationId != local.LocationId || ref.Path != local.Path) {
				return fmt.Errorf("Scan selection and observation refer to different paths")
			}
		}
		if logical := selection.GetLibrary(); logical != nil && logical.FileId < 0 {
			return fmt.Errorf("Scan Library File ID is invalid")
		}
	}

	// Publication and verification policies must match their source and acquisition guarantees.
	if spec.ResultPolicy == entity.ScanResultPolicy_PUBLISH_ORIGINALS && spec.MediaId > 0 {
		return fmt.Errorf("Media cannot publish originals")
	}
	if spec.ResultPolicy == entity.ScanResultPolicy_PUBLISH_INVENTORY || spec.ResultPolicy == entity.ScanResultPolicy_VERIFY_COPIES {
		if spec.MediaId <= 0 {
			return fmt.Errorf("inventory and copy checks require Media")
		}
	}
	if spec.ResultPolicy == entity.ScanResultPolicy_VERIFY_COPIES && spec.SignaturePolicy != entity.ScanSignaturePolicy_FORCE_READ {
		return fmt.Errorf("copy checks require FORCE_READ")
	}
	return nil
}

func (s *service) Create(ctx context.Context, req *entity.CreateScanJobRequest) (*entity.CreateScanJobReply, error) {
	// Validate all public option combinations before creating a durable Job or reserving resources.
	if req == nil {
		return nil, fmt.Errorf("Scan request is missing")
	}
	if err := validateSpec(req.Spec); err != nil {
		return nil, err
	}
	if req.Spec.PreviewPolicy != entity.PreviewPolicy_PREVIEW_NONE && s.exe.Previews() == nil {
		return nil, fmt.Errorf("Preview module is not configured")
	}
	release, err := s.exe.Lib().UseOnlineRead()
	if err != nil {
		return nil, err
	}
	defer release()
	config := &Config{ID: 1, Spec: proto.Clone(req.Spec).(*entity.ScanJobSpec)}
	target := executor.JobTarget{}
	var reservation func()
	if req.Spec.LocationId > 0 {
		var err error
		reservation, err = s.exe.Lib().UseOnlineSource(req.Spec.LocationId)
		if err != nil {
			return nil, err
		}
		defer func() {
			if reservation != nil {
				reservation()
			}
		}()
		location, err := s.exe.Lib().GetOnlineSource(ctx, req.Spec.LocationId)
		if err != nil {
			return nil, err
		}
		if location.Binding != entity.OnlineBinding_CONFIRMED {
			return nil, library.ErrOnlineUnverified
		}
		target = executor.JobTarget{LocationID: location.ID, TargetName: location.Name}
	}
	if req.Spec.MediaId > 0 {
		stored, err := s.exe.Lib().GetMedia(ctx, req.Spec.MediaId)
		if err != nil {
			return nil, err
		}
		capabilities, err := mediapkg.CapabilitiesForProfile(stored.Profile)
		if err != nil {
			return nil, err
		}
		if req.Spec.PreviewPolicy != entity.PreviewPolicy_PREVIEW_NONE && capabilities.Read == mediapkg.AccessSequential {
			return nil, fmt.Errorf("sequential Media does not support Preview generation")
		}
		config.MediaKind, config.MediaIdentity, config.MediaProfile = stored.Kind, stored.Identity, proto.Clone(stored.Profile).(*entity.MediaProfile)
		target = executor.JobTarget{MediaID: stored.ID, TargetName: stored.Name}
	}

	// The complete typed bundle exists before asynchronous indexing starts or Create returns.
	job, err := s.exe.CreateTargetJob(ctx, entity.JobKind_SCAN, req.Priority, target, func(db *gorm.DB) error {
		if err := prepareSchema(db); err != nil {
			return err
		}
		return db.Create(config).Error
	}, func(value executor.Runner) error {
		r, ok := value.(*runner)
		if !ok {
			return fmt.Errorf("unexpected Scan runner %T", value)
		}
		r.reservation = reservation
		reservation = nil
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &entity.CreateScanJobReply{Job: job.ToEntity()}, nil
}

func (s *service) ReadMedia(ctx context.Context, req *entity.ReadScanMediaRequest) (*entity.ReadScanMediaReply, error) {
	if req == nil || req.Target == nil {
		return nil, fmt.Errorf("Scan Media target is missing")
	}
	target := proto.Clone(req.Target).(*entity.ReadMediaTarget)
	err := s.exe.StartJob(ctx, req.Id, entity.JobKind_SCAN, func(ctx context.Context, value executor.Runner) error {
		r, ok := value.(*runner)
		if !ok {
			return fmt.Errorf("unexpected Scan runner %T", value)
		}
		return r.readMedia(ctx, target)
	})
	return &entity.ReadScanMediaReply{}, err
}

func (s *service) GetProgress(ctx context.Context, req *entity.GetScanJobProgressRequest) (*entity.GetScanJobProgressReply, error) {
	if req == nil {
		return nil, fmt.Errorf("Scan progress request is missing")
	}
	reply := &entity.GetScanJobProgressReply{}
	err := s.useRunner(ctx, req.Id, func(r *runner) error {
		// Aggregate one manifest without loading result rows or traversing filesystem state.
		var groups []struct {
			Change       entity.ScanChange
			Finding      entity.ScanFinding
			Preview      entity.ScanPreviewOutcome
			Count, Bytes int64
		}
		if err := r.db.WithContext(ctx).Model(&Entry{}).Select("change, finding, preview, COUNT(*) AS count, COALESCE(SUM(size),0) AS bytes").Group("change,finding,preview").Find(&groups).Error; err != nil {
			return err
		}
		for _, g := range groups {
			switch g.Change {
			case entity.ScanChange_SCAN_CHANGE_ADDED:
				reply.Added += g.Count
			case entity.ScanChange_SCAN_CHANGE_CHANGED:
				reply.Changed += g.Count
			case entity.ScanChange_SCAN_CHANGE_REMOVED:
				reply.Removed += g.Count
			case entity.ScanChange_SCAN_CHANGE_UNCHANGED:
				reply.Unchanged += g.Count
			}
			if g.Change != entity.ScanChange_SCAN_CHANGE_REMOVED {
				reply.Bytes += g.Bytes
			}
			switch g.Finding {
			case entity.ScanFinding_MATCH:
				reply.Matched += g.Count
			case entity.ScanFinding_MISMATCH:
				reply.Damaged += g.Count
			case entity.ScanFinding_MISSING:
				reply.Missing += g.Count
			case entity.ScanFinding_UNREADABLE:
				reply.Unreadable += g.Count
			case entity.ScanFinding_UNVERIFIABLE:
				reply.Unverifiable += g.Count
			}
			switch g.Preview {
			case entity.ScanPreviewOutcome_PREVIEW_READY:
				reply.PreviewsReady += g.Count
			case entity.ScanPreviewOutcome_PREVIEW_SKIPPED:
				reply.PreviewsSkipped += g.Count
			case entity.ScanPreviewOutcome_PREVIEW_FAILED:
				reply.PreviewsFailed += g.Count
			}
		}
		reply.Progress = r.progress.ToEntity()
		reply.Progress.TotalFiles = reply.Added + reply.Changed + reply.Unchanged
		reply.Progress.TotalBytes = reply.Bytes
		if r.Phase() == entity.JobPhase_JOB_PHASE_COMPLETED {
			reply.Progress.CopiedFiles = reply.Progress.TotalFiles
			reply.Progress.CopiedBytes = reply.Progress.TotalBytes
		}
		var scopes []*Scope
		if err := r.db.WithContext(ctx).Order("id").Limit(101).Find(&scopes).Error; err != nil {
			return err
		}
		reply.ScopesHasMore = len(scopes) > 100
		if reply.ScopesHasMore {
			scopes = scopes[:100]
		}
		for _, scope := range scopes {
			reply.Scopes = append(reply.Scopes, scope.ToEntity())
		}
		return nil
	})
	return reply, err
}

func (s *service) ListScopes(ctx context.Context, req *entity.ListScanJobScopesRequest) (*entity.ListScanJobScopesReply, error) {
	if req == nil {
		return nil, fmt.Errorf("Scan scopes request is missing")
	}
	limit := int(req.Limit)
	if limit == 0 {
		limit = 100
	}
	if limit < 0 || limit > maxListEntries {
		return nil, fmt.Errorf("invalid Scan scope page size")
	}
	reply := &entity.ListScanJobScopesReply{}
	err := s.useRunner(ctx, req.Id, func(r *runner) error {
		var scopes []*Scope
		if err := r.db.WithContext(ctx).Where("id > ?", req.GetAfterId()).Order("id").Limit(limit + 1).Find(&scopes).Error; err != nil {
			return err
		}
		reply.HasMore = len(scopes) > limit
		if reply.HasMore {
			scopes = scopes[:limit]
		}
		for _, scope := range scopes {
			reply.Scopes = append(reply.Scopes, scope.ToEntity())
		}
		return nil
	})
	return reply, err
}

func (s *service) ListEntries(ctx context.Context, req *entity.ListScanJobEntriesRequest) (*entity.ListScanJobEntriesReply, error) {
	if req == nil {
		return nil, fmt.Errorf("Scan entries request is missing")
	}
	limit := int(req.Limit)
	if limit == 0 {
		limit = 100
	}
	if limit < 0 || limit > 1000 {
		return nil, fmt.Errorf("invalid Scan result page size")
	}
	reply := &entity.ListScanJobEntriesReply{}
	err := s.useRunner(ctx, req.Id, func(r *runner) error {
		var entries []*Entry
		if err := r.db.WithContext(ctx).Where("id > ?", req.GetAfterId()).Order("id").Limit(limit + 1).Find(&entries).Error; err != nil {
			return err
		}
		reply.HasMore = len(entries) > limit
		if reply.HasMore {
			entries = entries[:limit]
		}
		for _, entry := range entries {
			reply.Entries = append(reply.Entries, entry.ToEntity())
		}
		return nil
	})
	return reply, err
}

func (s *service) useRunner(ctx context.Context, id int64, use func(*runner) error) error {
	return s.exe.UseJobRunner(ctx, id, entity.JobKind_SCAN, func(value executor.Runner) error {
		r, ok := value.(*runner)
		if !ok {
			return fmt.Errorf("unexpected Scan runner %T", value)
		}
		return use(r)
	})
}
