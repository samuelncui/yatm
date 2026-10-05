package scan

import (
	"context"
	"fmt"

	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/internal/executor"
	"github.com/samuelncui/yatm/internal/library"
	mediapkg "github.com/samuelncui/yatm/internal/media"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/proto"
	"gorm.io/gorm"
)

type service struct {
	entity.UnimplementedScanJobServiceServer
	exe *executor.Executor
}

func previewEnabled(policy entity.PreviewPolicy) bool {
	return policy == entity.PreviewPolicy_PREVIEW_POLICY_MISSING_ONLY ||
		policy == entity.PreviewPolicy_PREVIEW_POLICY_REGENERATE_ALL
}

func registerService(server grpc.ServiceRegistrar, exe *executor.Executor) {
	entity.RegisterScanJobServiceServer(server, &service{exe: exe})
}

// Create uses the public Scan contract for automatic collection and explicit user work alike.
func Create(ctx context.Context, exe *executor.Executor, req *entity.CreateScanJobRequest) (*entity.CreateScanJobResponse, error) {
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
	if spec.MediaId < 0 {
		return fmt.Errorf("Scan source identifiers cannot be negative")
	}

	// Select either physical Media or a bounded set of Location/Library roots.
	if (spec.MediaId > 0) == (len(spec.Selections) > 0) {
		return fmt.Errorf("Scan requires Media or selections")
	}
	if len(spec.Selections) > 1000 {
		return fmt.Errorf("Scan supports at most 1000 selection roots")
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
		}
		if logical := selection.GetLibrary(); logical != nil && logical.FileId < 0 {
			return fmt.Errorf("Scan Library File ID is invalid")
		}
	}

	// Publication and verification policies must match their source and acquisition guarantees.
	if spec.ResultPolicy == entity.ScanResultPolicy_SCAN_RESULT_POLICY_PUBLISH_ORIGINALS && spec.MediaId > 0 {
		return fmt.Errorf("Media cannot publish originals")
	}
	if spec.ResultPolicy == entity.ScanResultPolicy_SCAN_RESULT_POLICY_PUBLISH_INVENTORY || spec.ResultPolicy == entity.ScanResultPolicy_SCAN_RESULT_POLICY_VERIFY_COPIES {
		if spec.MediaId <= 0 {
			return fmt.Errorf("inventory and copy checks require Media")
		}
	}
	if spec.ResultPolicy == entity.ScanResultPolicy_SCAN_RESULT_POLICY_VERIFY_COPIES && spec.SignaturePolicy != entity.ScanSignaturePolicy_SCAN_SIGNATURE_POLICY_FORCE_READ {
		return fmt.Errorf("copy checks require FORCE_READ")
	}
	return nil
}

func (s *service) Create(ctx context.Context, req *entity.CreateScanJobRequest) (*entity.CreateScanJobResponse, error) {
	// Validate all public option combinations before creating a durable Job or reserving resources.
	if req == nil {
		return nil, fmt.Errorf("Scan request is missing")
	}
	if req.Spec == nil {
		return nil, fmt.Errorf("Scan specification is missing")
	}
	spec := proto.Clone(req.Spec).(*entity.ScanJobSpec)
	if spec.SignaturePolicy == entity.ScanSignaturePolicy_SCAN_SIGNATURE_POLICY_UNSPECIFIED {
		spec.SignaturePolicy = entity.ScanSignaturePolicy_SCAN_SIGNATURE_POLICY_FILL_MISSING
	}
	if spec.ResultPolicy == entity.ScanResultPolicy_SCAN_RESULT_POLICY_UNSPECIFIED {
		spec.ResultPolicy = entity.ScanResultPolicy_SCAN_RESULT_POLICY_REPORT_ONLY
	}
	if spec.PreviewPolicy == entity.PreviewPolicy_PREVIEW_POLICY_UNSPECIFIED {
		spec.PreviewPolicy = entity.PreviewPolicy_PREVIEW_POLICY_NONE
	}
	if err := validateSpec(spec); err != nil {
		return nil, err
	}
	if previewEnabled(spec.PreviewPolicy) && s.exe.Previews() == nil {
		return nil, fmt.Errorf("Preview module is not configured")
	}
	if previewEnabled(spec.PreviewPolicy) {
		if err := s.exe.Previews().CheckGeneration(ctx); err != nil {
			return nil, err
		}
	}
	// Keep selection validation and Job publication within the identity admission boundary.
	config := &Config{ID: 1, Spec: spec}
	if len(config.Spec.Selections) > 0 {
		if err := s.exe.Lib().FreezeSelections(ctx, config.Spec.Selections); err != nil {
			return nil, err
		}
	}
	if err := s.exe.Lib().ValidateOriginalSelections(ctx, config.Spec.Selections); err != nil {
		return nil, err
	}
	if previewEnabled(spec.PreviewPolicy) {
		previewSettings, err := freezePreviewSettings(ctx, s.exe)
		if err != nil {
			return nil, err
		}
		config.PreviewJobSettings = previewSettings
	}
	target := executor.JobTarget{}
	// Only explicit roots of one Location give this Job a single primary target.
	var locationID int64
	for _, selection := range config.Spec.Selections {
		local := selection.GetLocation()
		if local == nil {
			locationID = 0
			break
		}
		if locationID != 0 && locationID != local.LocationId {
			locationID = 0
			break
		}
		locationID = local.LocationId
	}
	if locationID > 0 {
		location, err := s.exe.Lib().GetLocation(ctx, locationID)
		if err != nil {
			return nil, err
		}
		target = executor.JobTarget{LocationID: location.ID, TargetName: location.Name}
	}
	if spec.MediaId > 0 {
		stored, err := s.exe.Lib().GetMedia(ctx, spec.MediaId)
		if err != nil {
			return nil, err
		}
		capabilities, err := mediapkg.CapabilitiesForProfile(stored.Profile)
		if err != nil {
			return nil, err
		}
		if previewEnabled(spec.PreviewPolicy) && capabilities.Read == mediapkg.AccessSequential {
			return nil, fmt.Errorf("sequential Media does not support Preview generation")
		}
		config.MediaKind, config.MediaIdentity, config.MediaProfile = stored.Kind, stored.Identity, proto.Clone(stored.Profile).(*entity.MediaProfile)
		target = executor.JobTarget{MediaID: stored.ID, TargetName: stored.Name}
	}

	// The complete typed bundle exists before asynchronous indexing starts or Create returns.
	job, err := s.exe.CreateTargetJob(ctx, entity.JobKind_JOB_KIND_SCAN, req.Priority, target, func(db *gorm.DB) error {
		if err := prepareSchema(db); err != nil {
			return err
		}
		return db.Create(config).Error
	}, func(value executor.Runner) error {
		if _, ok := value.(*runner); !ok {
			return fmt.Errorf("unexpected Scan runner %T", value)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &entity.CreateScanJobResponse{Job: job.ToEntity()}, nil
}

func (s *service) ReadMedia(ctx context.Context, req *entity.ReadScanMediaRequest) (*entity.ReadScanMediaResponse, error) {
	if req == nil || req.Target == nil {
		return nil, fmt.Errorf("Scan Media target is missing")
	}
	target := proto.Clone(req.Target).(*entity.ReadMediaTarget)
	err := s.exe.StartJob(ctx, req.Id, entity.JobKind_JOB_KIND_SCAN, func(ctx context.Context, value executor.Runner) error {
		r, ok := value.(*runner)
		if !ok {
			return fmt.Errorf("unexpected Scan runner %T", value)
		}
		return r.readMedia(ctx, target)
	})
	return &entity.ReadScanMediaResponse{}, err
}

func (s *service) GetProgress(ctx context.Context, req *entity.GetScanJobProgressRequest) (*entity.GetScanJobProgressResponse, error) {
	// Validate the request before locking its typed runner.
	if req == nil {
		return nil, fmt.Errorf("Scan progress request is missing")
	}

	// Read a consistent typed snapshot while excluding deletion and a new attempt.
	reply := &entity.GetScanJobProgressResponse{}
	err := s.useRunner(ctx, req.Id, func(r *runner) error {
		// Aggregate one manifest without loading result rows or traversing filesystem state.
		var groups []struct {
			Change       entity.ScanChange
			Finding      entity.ScanFinding
			Count, Bytes int64
		}
		if err := r.db.WithContext(ctx).Model(&Entry{}).Select("change, finding, COUNT(*) AS count, COALESCE(SUM(size),0) AS bytes").Group("change,finding").Find(&groups).Error; err != nil {
			return err
		}
		for _, g := range groups {
			switch g.Change {
			case entity.ScanChange_SCAN_CHANGE_ADDED:
				reply.AddedCount += g.Count
			case entity.ScanChange_SCAN_CHANGE_CHANGED:
				reply.ChangedCount += g.Count
			case entity.ScanChange_SCAN_CHANGE_REMOVED:
				reply.RemovedCount += g.Count
			case entity.ScanChange_SCAN_CHANGE_UNCHANGED:
				reply.UnchangedCount += g.Count
			}
			if g.Change != entity.ScanChange_SCAN_CHANGE_REMOVED {
				reply.ProcessedBytes += g.Bytes
			}
			switch g.Finding {
			case entity.ScanFinding_SCAN_FINDING_MATCH:
				reply.MatchedCount += g.Count
			case entity.ScanFinding_SCAN_FINDING_MISMATCH:
				reply.DamagedCount += g.Count
			case entity.ScanFinding_SCAN_FINDING_MISSING:
				reply.MissingCount += g.Count
			case entity.ScanFinding_SCAN_FINDING_UNREADABLE:
				reply.UnreadableCount += g.Count
			case entity.ScanFinding_SCAN_FINDING_UNVERIFIABLE:
				reply.UnverifiableCount += g.Count
			}
		}

		// Preview outcomes are the attempt's own, like its speed: a content's assets are the
		// store's answer, so the manifest holds only the entries whose Preview failed and why.
		var failedPreviews int64
		if err := r.db.WithContext(ctx).Model(&Entry{}).Where("preview_error <> ''").Count(&failedPreviews).Error; err != nil {
			return err
		}
		r.lock.Lock()
		reply.PreviewsReadyCount, reply.PreviewsSkippedCount, reply.PreviewsFailedCount = r.previewOutcomes.ready, r.previewOutcomes.skipped, failedPreviews
		r.lock.Unlock()

		// Combine phase-local progress with the shared latest-attempt duration.
		progress, err := r.progressSnapshot(ctx)
		if err != nil {
			return err
		}
		reply.Progress = progress
		if err := s.exe.ApplyAttemptProgress(ctx, req.Id, reply.Progress); err != nil {
			return err
		}

		return nil
	})
	return reply, err
}

func (s *service) ListEntries(ctx context.Context, req *entity.ListScanJobEntriesRequest) (*entity.ListScanJobEntriesResponse, error) {
	if req == nil {
		return nil, fmt.Errorf("Scan entries request is missing")
	}
	page, err := executor.NewJobResultPage(req.Limit, req.Cursor, req.Order, req.Offset, req.IncludeTotal)
	if err != nil {
		return nil, err
	}
	after, hasCursor, err := executor.JobResultCursorID(page.Cursor)
	if err != nil {
		return nil, err
	}
	reply := &entity.ListScanJobEntriesResponse{}
	err = s.useRunner(ctx, req.Id, func(r *runner) error {
		// The total spans the complete manifest, independent of this page position.
		if page.IncludeTotal {
			var total int64
			if err := r.db.WithContext(ctx).Model(&Entry{}).Count(&total).Error; err != nil {
				return err
			}
			reply.TotalEntryCount = proto.Int64(total)
		}
		query := r.db.WithContext(ctx)
		if hasCursor {
			query = query.Where("id "+page.Comparison()+" ?", after)
		}
		var entries []*Entry
		if err := query.Order("id " + page.Direction()).Limit(page.SentinelLimit()).Offset(int(page.Offset)).Find(&entries).Error; err != nil {
			return err
		}
		reply.HasMore = len(entries) > page.Limit
		if reply.HasMore {
			entries = entries[:page.Limit]
		}
		for _, entry := range entries {
			reply.Entries = append(reply.Entries, entry.ToEntity())
		}
		return r.describePreviews(ctx, reply.Entries)
	})
	return reply, err
}

// describePreviews states each returned row's Preview outcome without storing one. A recorded
// failure is the Job's own finding; otherwise the content store answers whether assets exist, and
// a requested Preview that has none was skipped. Only the returned page is asked, so the cost
// follows what is displayed rather than the size of the manifest.
func (r *runner) describePreviews(ctx context.Context, entries []*entity.ScanEntry) error {
	if len(entries) == 0 {
		return nil
	}
	var config Config
	if err := r.db.WithContext(ctx).First(&config, 1).Error; err != nil {
		return err
	}
	if !previewEnabled(config.Spec.GetPreviewPolicy()) {
		for _, entry := range entries {
			entry.Preview = entity.ScanPreviewOutcome_SCAN_PREVIEW_OUTCOME_NOT_REQUESTED
		}
		return nil
	}
	for _, entry := range entries {
		if entry.PreviewError != "" {
			entry.Preview = entity.ScanPreviewOutcome_SCAN_PREVIEW_OUTCOME_FAILED
			continue
		}
		entry.Preview = entity.ScanPreviewOutcome_SCAN_PREVIEW_OUTCOME_SKIPPED
		if len(entry.Sha256) != 32 {
			continue
		}
		signature, err := library.NewFileSignature(entry.Sha256, entry.SizeBytes)
		if err != nil {
			continue
		}
		exists, err := r.exe.Previews().Exists(signature)
		if err != nil {
			return err
		}
		if exists {
			entry.Preview = entity.ScanPreviewOutcome_SCAN_PREVIEW_OUTCOME_READY
		}
	}
	return nil
}

func (s *service) useRunner(ctx context.Context, id int64, use func(*runner) error) error {
	return s.exe.UseJobRunner(ctx, id, entity.JobKind_JOB_KIND_SCAN, func(value executor.Runner) error {
		r, ok := value.(*runner)
		if !ok {
			return fmt.Errorf("unexpected Scan runner %T", value)
		}
		return use(r)
	})
}
