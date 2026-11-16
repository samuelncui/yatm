package scan

import (
	"context"

	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/internal/executor/observation"
)

func (r *locationStage) matchRelocations(ctx context.Context) error {
	// Preparation settled exact paths first; only unassigned observations need relocation candidates.
	var pending []int64
	if err := r.db.WithContext(ctx).Model(&Entry{}).
		Where("location_id = ? AND file_id = 0 AND change != ?", r.source.ID, entity.ScanChange_SCAN_CHANGE_REMOVED).
		Limit(1).Pluck("id", &pending).Error; err != nil {
		return err
	}
	if len(pending) == 0 {
		return nil
	}

	store := observation.NewMatchStore(r.db.Model(&Entry{}).Where("location_id = ?", r.source.ID))
	return r.exe.MatchLocationObservations(ctx, r.source, store, r.logger)
}
