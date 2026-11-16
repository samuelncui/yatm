package observation

import (
	"context"
	"fmt"

	"github.com/samuelncui/yatm/entity"
	"gorm.io/gorm"
)

type matchStore struct {
	db *gorm.DB
}

// NewMatchStore adapts a caller-scoped observation table to continuity matching.
// Both Item and Scan Entry own the same ID/path/file_id/signature/sha256/evidence
// column contract; their remaining fields and persistence lifetimes stay independent.
func NewMatchStore(db *gorm.DB) MatchStore {
	return &matchStore{db: db}
}

func (s *matchStore) FileClaimed(ctx context.Context, fileID int64) (bool, error) {
	var count int64
	err := s.query(ctx).Where("file_id = ?", fileID).Count(&count).Error
	return count > 0, err
}

func (s *matchStore) FindCandidate(ctx context.Context, criteria MatchCriteria) (*MatchCandidate, error) {
	query := s.query(ctx).Where("file_id = 0 AND change != ?", entity.ScanChange_SCAN_CHANGE_REMOVED)
	query, err := matchQuery(query, criteria)
	if err != nil {
		return nil, err
	}

	var row struct {
		ID   int64
		Path string
		Size int64
		Hash []byte `gorm:"column:sha256"`
	}
	if err := query.Order("path").Limit(1).Find(&row).Error; err != nil {
		return nil, err
	}
	if row.ID == 0 {
		return nil, nil
	}
	return &MatchCandidate{ID: row.ID, Path: row.Path, Size: row.Size, Hash: row.Hash}, nil
}

func (s *matchStore) Assign(ctx context.Context, id, fileID int64, signature []byte) error {
	values := map[string]any{"file_id": fileID}
	if len(signature) != 0 {
		values["signature"] = signature
	}
	return s.query(ctx).Where("id = ?", id).Updates(values).Error
}

func (s *matchStore) Pending(ctx context.Context, after string, limit int) ([]Pending, error) {
	var rows []struct {
		Path      string
		Signature []byte
		Evidence  Evidence `gorm:"embedded"`
	}
	err := s.query(ctx).Where("file_id = 0 AND change != ? AND path > ?", entity.ScanChange_SCAN_CHANGE_REMOVED, after).
		Order("path").Limit(limit).Find(&rows).Error
	pending := make([]Pending, 0, len(rows))
	for _, row := range rows {
		pending = append(pending, Pending{Path: row.Path, Signature: row.Signature, Evidence: row.Evidence})
	}
	return pending, err
}

func (s *matchStore) Observed(ctx context.Context, paths []string) (map[string]bool, error) {
	observed := make(map[string]bool, len(paths))
	if len(paths) == 0 {
		return observed, nil
	}
	var found []string
	if err := s.query(ctx).Where("path IN ?", paths).Pluck("path", &found).Error; err != nil {
		return nil, err
	}
	for _, name := range found {
		observed[name] = true
	}
	return observed, nil
}

func (s *matchStore) query(ctx context.Context) *gorm.DB {
	return s.db.WithContext(ctx).Session(&gorm.Session{})
}

func matchQuery(query *gorm.DB, criteria MatchCriteria) (*gorm.DB, error) {
	switch criteria.Kind {
	case MatchPath:
		return query.Where("path = ?", criteria.Path), nil
	case MatchSignature:
		return query.Where("signature = ?", criteria.Signature), nil
	case MatchNative:
		key := criteria.Evidence
		query = query.Where("native_scope = ? AND native_key = ?", key.NativeScope, key.NativeKey)
		// A lifetime guard constrains only observations that recorded one too.
		if key.BirthNS != 0 {
			query = query.Where("birth_ns IN ?", []int64{0, key.BirthNS})
		}
		if key.Generation != 0 {
			query = query.Where("generation IN ?", []uint64{0, key.Generation})
		}
		return query, nil
	default:
		return nil, fmt.Errorf("unsupported continuity match kind %d", criteria.Kind)
	}
}
