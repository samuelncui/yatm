package scan

import (
	"context"

	"github.com/samuelncui/yatm/entity"
)

func (s *service) GetCreation(
	ctx context.Context, req *entity.GetScanJobCreationRequest,
) (*entity.GetScanJobCreationResponse, error) {
	// Copy the retained specification without enumerating sources or replaying indexed inputs.
	var config Config
	priority, err := s.exe.ReadJobConfig(ctx, req.GetId(), entity.JobKind_JOB_KIND_SCAN, &config)
	if err != nil {
		return nil, err
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
