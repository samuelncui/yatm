package library

import (
	"context"
	"fmt"
)

// MatchingCandidate is a read projection of existing identity facts.
type MatchingCandidate struct {
	FileID   int64
	Original *FileLocation
	Tracking []*FileTrackingKey
}

// DetachedMatchingFileIDsPage lists identities whose retained tracking may reconnect an original.
// Bound originals come from the caller's observed manifest, not a whole-Location Catalog walk.
func (l *Library) DetachedMatchingFileIDsPage(ctx context.Context, executorID string, after int64, limit int) ([]int64, error) {
	if limit <= 0 || limit > 1000 {
		return nil, fmt.Errorf("invalid detached matching page size")
	}
	// Preserve eligibility and File-ID order across every Location on the same Executor.
	db := l.readDB().WithContext(ctx)
	detached := db.Model(&FileTrackingKey{}).Select("file_tracking_keys.file_id").
		Joins("JOIN locations ON locations.id = file_tracking_keys.location_id").
		Where("locations.executor_id = ?", executorID).
		Where("NOT EXISTS (SELECT 1 FROM file_locations WHERE file_locations.file_id = file_tracking_keys.file_id)")
	var ids []int64
	if err := db.Model(ModelFile).Where("id > ? AND id IN (?)", after, detached).Order("id").Limit(limit).Pluck("id", &ids).Error; err != nil {
		return nil, err
	}
	return ids, nil
}

// MatchingCandidates reads current identity facts for a bounded set of relevant File IDs.
func (l *Library) MatchingCandidates(ctx context.Context, ids []int64) ([]MatchingCandidate, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	if len(ids) > 1000 {
		return nil, fmt.Errorf("matching candidate page exceeds 1000 Files")
	}

	// Batch associated facts once per page, including both tracking kinds for each File.
	db := l.readDB().WithContext(ctx)
	var originals []*FileLocation
	if err := db.Where("file_id IN ?", ids).Find(&originals).Error; err != nil {
		return nil, err
	}
	var keys []*FileTrackingKey
	if err := db.Where("file_id IN ?", ids).Order("file_id, kind").Find(&keys).Error; err != nil {
		return nil, err
	}
	rows := make([]MatchingCandidate, len(ids))
	byID := make(map[int64]*MatchingCandidate, len(ids))
	for i, id := range ids {
		rows[i].FileID = id
		byID[id] = &rows[i]
	}
	for _, row := range originals {
		byID[row.FileID].Original = row
	}
	for _, key := range keys {
		row := byID[key.FileID]
		row.Tracking = append(row.Tracking, key)
	}
	return rows, nil
}
