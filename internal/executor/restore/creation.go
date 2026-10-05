package restore

import (
	"context"
	"strings"

	"github.com/samuelncui/yatm/entity"
)

func (s *service) GetCreation(
	ctx context.Context, req *entity.GetRestoreJobCreationRequest,
) (*entity.GetRestoreJobCreationResponse, error) {
	// Keep every recorded selection, destination and version policy, including cutoff presence.
	var config Config
	priority, err := s.exe.ReadJobConfig(ctx, req.GetId(), entity.JobKind_JOB_KIND_RESTORE, &config)
	if err != nil {
		return nil, err
	}

	// Legacy output roots cannot identify a public Location choice or the original selections.
	reply := &entity.GetRestoreJobCreationResponse{
		Request: &entity.CreateRestoreJobRequest{Priority: priority, Spec: config.Spec},
	}
	var missing []string
	if len(config.Spec.GetSelections())+len(config.Spec.GetFileVersionIds()) == 0 {
		missing = append(missing, "The original Restore selections and version choices are unavailable; select them again.")
	}
	if config.Spec.GetDestination().GetLocationId() <= 0 {
		missing = append(missing, "The original Restore Location is unavailable; choose a destination.")
	}
	if config.LegacyRoot != "" {
		missing = append(missing, "This imported v0.1.x Job retains a legacy output root, not a public creation destination.")
	}
	reply.UnavailableReason = strings.Join(missing, " ")
	return reply, nil
}
