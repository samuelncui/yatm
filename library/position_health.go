package library

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/samuelncui/yatm/entity"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// PositionHealthObservation is a completed physical observation of frozen inventory facts.
type PositionHealthObservation struct {
	PositionID   int64
	ContentToken []byte
	Health       entity.PositionHealth
	CheckedAt    int64
	JobID        int64
}

// PositionContentToken binds a check to inventory facts, not mutable check results.
// This hashes metadata only; file content is read and hashed exclusively by ACP.
func PositionContentToken(position *Position) ([]byte, error) {
	if position == nil {
		return nil, fmt.Errorf("Position expectation is missing")
	}

	// Exclude health so the same frozen observation can be safely replayed after a Job checkpoint failure.
	data, err := json.Marshal(struct {
		ID, MediaID        int64
		Path               string
		IsDir              bool
		Signature, Hash    []byte
		Size               int64
		Mode               uint32
		ModTime, WriteTime int64
		StorageOrder       []byte
		StorageMetadata    *entity.StorageMetadata
	}{position.ID, position.MediaID, position.Path, position.IsDir, position.Signature, position.Hash,
		position.Size, position.Mode, position.ModTime.UnixNano(), position.WriteTime.UnixNano(),
		position.StorageOrder, position.StorageMetadata})
	if err != nil {
		return nil, fmt.Errorf("encode Position expectation failed, %w", err)
	}
	token := sha256.Sum256(data)
	return token[:], nil
}

// PublishPositionHealth records a physical check only while its expected inventory is unchanged.
// A false result means the check is stale; the caller should retain it only in the Job result.
func (l *Library) PublishPositionHealth(ctx context.Context, observation *PositionHealthObservation) (bool, error) {
	// Accept completed checks only; inventory edits and imports cannot manufacture verification.
	if observation == nil || observation.PositionID <= 0 || len(observation.ContentToken) != sha256.Size {
		return false, fmt.Errorf("Position health expectation is invalid")
	}
	if observation.CheckedAt <= 0 {
		return false, fmt.Errorf("Position health check time is missing")
	}
	switch observation.Health {
	case entity.PositionHealth_HEALTHY, entity.PositionHealth_DAMAGED,
		entity.PositionHealth_MISSING, entity.PositionHealth_UNREADABLE:
	default:
		return false, fmt.Errorf("Position health observation is unsupported, health=%s", observation.Health)
	}

	// Compare and publish inside one metadata transaction, excluding concurrent inventory replacements.
	var published bool
	err := l.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var stored Position
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&stored, observation.PositionID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return nil
			}
			return fmt.Errorf("read checked Position failed, %w", err)
		}
		token, err := PositionContentToken(&stored)
		if err != nil {
			return err
		}
		if stored.IsDir || !bytes.Equal(token, observation.ContentToken) {
			return nil
		}
		if stored.CheckedAt > observation.CheckedAt {
			return nil
		}

		// Update only observation columns: the expected signature/hash/size remain immutable to this operation.
		if err := tx.Model(&stored).Updates(map[string]any{
			"health": observation.Health, "checked_at": observation.CheckedAt, "health_job_id": observation.JobID,
		}).Error; err != nil {
			return fmt.Errorf("publish Position health failed, %w", err)
		}
		published = true
		return nil
	})
	return published, err
}

// PositionRestoreEligible keeps unverified inventory usable while excluding known unusable copies.
func PositionRestoreEligible(health entity.PositionHealth) bool {
	return health == entity.PositionHealth_POSITION_HEALTH_UNKNOWN || health == entity.PositionHealth_HEALTHY
}

func preservePositionHealth(updated, previous *Position) {
	updated.Health = entity.PositionHealth_POSITION_HEALTH_UNKNOWN
	updated.CheckedAt, updated.HealthJobID = 0, 0
	if updated.MediaID != previous.MediaID || updated.Path != previous.Path || updated.IsDir != previous.IsDir {
		return
	}
	if updated.Size != previous.Size || !bytes.Equal(updated.Hash, previous.Hash) || !bytes.Equal(updated.Signature, previous.Signature) {
		return
	}
	updated.Health, updated.CheckedAt, updated.HealthJobID = previous.Health, previous.CheckedAt, previous.HealthJobID
}
