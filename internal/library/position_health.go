package library

import (
	"bytes"
	"context"
	"fmt"
	"slices"

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

// RestoreCandidateHealth lists the Position health a Restore may read: unverified and healthy
// copies, plus known damaged or unreadable copies only when the request allows them.
func RestoreCandidateHealth(allowDamaged bool) []entity.PositionHealth {
	health := []entity.PositionHealth{entity.PositionHealth_POSITION_HEALTH_UNKNOWN, entity.PositionHealth_POSITION_HEALTH_HEALTHY}
	if allowDamaged {
		health = append(health, entity.PositionHealth_POSITION_HEALTH_DAMAGED, entity.PositionHealth_POSITION_HEALTH_UNREADABLE)
	}
	return health
}

// PositionRestoreEligible keeps unverified inventory usable while excluding known unusable copies.
func PositionRestoreEligible(health entity.PositionHealth) bool {
	return slices.Contains(RestoreCandidateHealth(false), health)
}

// ContentMedia lists, for each signature, the distinct Media holding a regular Position of that
// content that Restore may read; the list length is the content's Media count. Several copies on one
// Media count once, and damaged, unreadable or missing copies do not count. Callers pass a bounded
// batch of known signatures.
func (l *Library) ContentMedia(ctx context.Context, signatures [][]byte) (map[string][]int64, error) {
	result := make(map[string][]int64, len(signatures))
	if len(signatures) == 0 {
		return result, nil
	}
	var rows []struct {
		Signature []byte
		MediaID   int64
	}
	if err := l.readDB().WithContext(ctx).Model(ModelPosition).Distinct("signature", "media_id").
		Where("signature IN ? AND is_dir = ? AND health IN ?", signatures, false, RestoreCandidateHealth(false)).
		Order("media_id").Scan(&rows).Error; err != nil {
		return nil, fmt.Errorf("count content Media failed, %w", err)
	}
	for _, row := range rows {
		key := string(row.Signature)
		result[key] = append(result[key], row.MediaID)
	}
	return result, nil
}

// CountReachedContent counts the originals whose recorded content signature is already held by at
// least target Media. Missing originals and unknown content never count.
func (l *Library) CountReachedContent(ctx context.Context, originals []*FileLocation, target uint32) (int64, error) {
	signatures := make([][]byte, 0, len(originals))
	for _, original := range originals {
		if original != nil && len(original.Signature) > 0 {
			signatures = append(signatures, original.Signature)
		}
	}
	media, err := l.ContentMedia(ctx, signatures)
	if err != nil {
		return 0, err
	}
	var count int64
	for _, signature := range signatures {
		if len(media[string(signature)]) >= int(target) {
			count++
		}
	}
	return count, nil
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
