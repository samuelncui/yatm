package library

import (
	"bytes"
	"context"
	"fmt"

	"github.com/samuelncui/yatm/entity"
)

// PositionHealthObservation is a completed physical observation of archive inventory.
type PositionHealthObservation struct {
	PositionID  int64
	Health      entity.PositionHealth
	CheckedAtNS int64
	JobID       int64
}

// PublishPositionHealth records one completed physical check for a regular Position.
func (l *Library) PublishPositionHealth(ctx context.Context, observation *PositionHealthObservation) error {
	// Inventory edits and imports cannot manufacture verification.
	if observation == nil || observation.PositionID <= 0 {
		return fmt.Errorf("Position health observation is invalid")
	}
	if observation.CheckedAtNS == 0 {
		return fmt.Errorf("Position health check time is missing")
	}
	switch observation.Health {
	case entity.PositionHealth_POSITION_HEALTH_HEALTHY, entity.PositionHealth_POSITION_HEALTH_DAMAGED,
		entity.PositionHealth_POSITION_HEALTH_MISSING, entity.PositionHealth_POSITION_HEALTH_UNREADABLE:
	default:
		return fmt.Errorf("Position health observation is unsupported, health=%s", observation.Health)
	}

	// Update only observation columns; the expected content facts remain untouched.
	result := l.db.WithContext(ctx).Model(&Position{}).Where("id = ? AND is_dir = ?", observation.PositionID, false).
		Updates(map[string]any{
			"health": observation.Health, "checked_at_ns": observation.CheckedAtNS, "health_job_id": observation.JobID,
		})
	if result.Error != nil {
		return fmt.Errorf("publish Position health failed, %w", result.Error)
	}
	if result.RowsAffected != 1 {
		return fmt.Errorf("publish Position health failed, regular Position %d is missing", observation.PositionID)
	}
	return nil
}

// PositionRestoreEligible keeps unverified inventory usable while excluding known unusable copies.
func PositionRestoreEligible(health entity.PositionHealth) bool {
	return health == entity.PositionHealth_POSITION_HEALTH_UNKNOWN || health == entity.PositionHealth_POSITION_HEALTH_HEALTHY
}

func preservePositionHealth(updated, previous *Position) {
	updated.Health = entity.PositionHealth_POSITION_HEALTH_UNKNOWN
	updated.CheckedAtNS, updated.HealthJobID = 0, 0
	if updated.MediaID != previous.MediaID || updated.Path != previous.Path || updated.IsDir != previous.IsDir {
		return
	}
	if updated.Size != previous.Size || !bytes.Equal(updated.Hash, previous.Hash) || !bytes.Equal(updated.Signature, previous.Signature) {
		return
	}
	updated.Health, updated.CheckedAtNS, updated.HealthJobID = previous.Health, previous.CheckedAtNS, previous.HealthJobID
}
