package scan

import (
	"context"

	"github.com/samuelncui/yatm/entity"
)

// latestScanCandidates bounds how many companion Scans a latest read skips; starting options are
// optional, so an older submitted Scan beyond them is not searched for.
const latestScanCandidates = 16

func (s *service) GetCreation(
	ctx context.Context, req *entity.GetScanJobCreationRequest,
) (*entity.GetScanJobCreationResponse, error) {
	// Copy the retained specification without enumerating sources or replaying indexed inputs.
	id, err := s.exe.CreationJobID(ctx, entity.JobKind_JOB_KIND_SCAN, req.GetId(), req.GetLatest())
	if err != nil || req.GetLatest() && id == 0 {
		return &entity.GetScanJobCreationResponse{}, err
	}
	var config Config
	priority, err := s.exe.ReadJobConfig(ctx, id, entity.JobKind_JOB_KIND_SCAN, &config)
	if err != nil {
		return nil, err
	}

	// The latest submitted Scan is one a caller created: an Archive's companion Scan carries the
	// Archive's choices, which the Archive read already returns. The walk past companions is
	// bounded, and it ends without a latest Scan when it is exhausted or meets an unreadable one.
	for skipped := 0; req.GetLatest() && config.IndexedInput; skipped++ {
		if skipped == latestScanCandidates {
			return &entity.GetScanJobCreationResponse{}, nil
		}
		if id, err = s.exe.LatestJobID(ctx, entity.JobKind_JOB_KIND_SCAN, id); err != nil || id == 0 {
			return &entity.GetScanJobCreationResponse{}, err
		}
		config = Config{}
		if priority, err = s.exe.ReadJobConfig(ctx, id, entity.JobKind_JOB_KIND_SCAN, &config); err != nil {
			return &entity.GetScanJobCreationResponse{}, nil
		}
	}

	// An indexed companion's item manifest cannot recover its original public selection roots.
	reply := &entity.GetScanJobCreationResponse{
		Request: &entity.CreateScanJobRequest{Priority: priority, Spec: config.Spec},
	}
	if config.IndexedInput {
		reply.UnavailableReason = "This Scan used indexed Archive inputs and did not retain public selection roots. " +
			"Select the sources again and review the recorded options."
	} else if config.Spec.GetMediaId() == 0 && len(config.Spec.GetSelections()) == 0 {
		reply.UnavailableReason = "The original Scan source selections are unavailable; " +
			"select the sources again and review the options."
	}
	return reply, nil
}
