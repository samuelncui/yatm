package archive

import (
	"context"

	"github.com/samuelncui/yatm/entity"
)

func (s *service) GetCreation(
	ctx context.Context, req *entity.GetArchiveJobCreationRequest,
) (*entity.GetArchiveJobCreationResponse, error) {
	// Read stored inputs without opening the runner or touching its manifest and Preview Job.
	var config Config
	priority, err := s.exe.ReadJobConfig(ctx, req.GetId(), entity.JobKind_JOB_KIND_ARCHIVE, &config)
	if err != nil {
		return nil, err
	}

	// Preview is stored as the effective companion Scan policy, not a second Create payload.
	request := &entity.CreateArchiveJobRequest{Priority: priority, Spec: config.Spec}
	if config.Preview != nil {
		request.PreviewPolicy = config.Preview.PreviewPolicy
		request.ForceRehash = config.Preview.SignaturePolicy == entity.ScanSignaturePolicy_SCAN_SIGNATURE_POLICY_FORCE_READ
	} else if len(config.Spec.GetSelections()) > 0 {
		request.PreviewPolicy = entity.PreviewPolicy_PREVIEW_POLICY_NONE
	}
	reply := &entity.GetArchiveJobCreationResponse{Request: request}
	if len(config.Spec.GetSelections()) == 0 {
		reply.UnavailableReason = "The original Archive selections are unavailable. " +
			"Imported v0.1.x Jobs do not retain their public creation inputs; select files and review the options."
	}
	return reply, nil
}
