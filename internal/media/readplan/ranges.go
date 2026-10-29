package readplan

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const readRangeBatchItems = 128

type readRangeUpdate struct {
	path   string
	size   int64
	ranges []byte
	hits   int
}

// completeRanges fills rows that collect admitted with a path; it records its own LOGICAL reason.
func (s *Sequence) completeRanges(ctx context.Context) (bool, error) {
	// Collect admits missing ranges only for a RangeSource provider and a selected path.
	source := s.provider.(RangeSource)
	if err := s.check(ctx); err != nil {
		return false, err
	}
	if err := s.stage.DB.WithContext(ctx).Exec("CREATE INDEX read_missing_path ON read_order(path) WHERE path <> ''").Error; err != nil {
		return false, fmt.Errorf("index missing read paths failed, %w", err)
	}

	// The complete current Index must parse successfully before any supplied ranges can be used.
	walkCtx, cancel := context.WithTimeout(ctx, s.deadline.Sub(s.now()))
	defer cancel()
	var sourceErr, callbackErr error
	err := s.stage.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		batch := make(map[string]int, readRangeBatchItems)
		updates := make([]readRangeUpdate, 0, readRangeBatchItems)
		retained, rangeCount := 0, 0
		flush := func() error {
			if err := s.check(ctx); err != nil {
				return err
			}
			if err := updateReadRanges(tx, updates); err != nil {
				return err
			}
			clear(batch)
			clear(updates)
			updates, retained, rangeCount = updates[:0], 0, 0
			return nil
		}
		walkErr := source.WalkReadRanges(walkCtx, func(entry RangeEntry) error {
			// Callback failures survive even a provider that incorrectly discards its callback error.
			if callbackErr != nil {
				return callbackErr
			}
			if callbackErr = s.check(ctx); callbackErr != nil {
				return callbackErr
			}
			if entry.Path == "" || len(entry.Path) > cacheBytes {
				return nil
			}
			if index, duplicate := batch[entry.Path]; duplicate {
				updates[index].hits = 2
				updates[index].ranges = nil
				return nil
			}

			// Encode inside yield: a streaming parser may reuse its entry buffers immediately afterward.
			var encoded []byte
			if entry.Size >= 0 && validRanges(entry.Ranges) {
				encoded, callbackErr = json.Marshal(entry.Ranges)
				if callbackErr != nil {
					return callbackErr
				}
				if len(encoded) > cacheBytes-len(entry.Path) {
					encoded = nil
				}
			}
			bytes := len(entry.Path) + len(encoded)
			if len(updates) != 0 && (len(updates) == readRangeBatchItems ||
				len(entry.Ranges) > MaxRanges-rangeCount || bytes > cacheBytes-retained) {
				if callbackErr = flush(); callbackErr != nil {
					return callbackErr
				}
			}
			batch[entry.Path] = len(updates)
			updates = append(updates, readRangeUpdate{path: entry.Path, size: entry.Size, ranges: encoded, hits: 1})
			retained += bytes
			if encoded != nil {
				rangeCount += len(entry.Ranges)
			}
			return nil
		})
		if callbackErr != nil {
			return callbackErr
		}
		if err := s.check(ctx); err != nil {
			callbackErr = err
			return err
		}
		if walkCtx.Err() != nil {
			callbackErr = ErrBudget
			return callbackErr
		}
		if errors.Is(walkErr, context.Canceled) || errors.Is(walkErr, context.DeadlineExceeded) {
			callbackErr = walkErr
			return callbackErr
		}
		if walkErr != nil {
			sourceErr = walkErr
			return walkErr
		}
		callbackErr = flush()
		return callbackErr
	})
	if err != nil {
		if callbackErr != nil {
			return false, callbackErr
		}
		if sourceErr != nil && errors.Is(err, sourceErr) {
			s.reason = "LOGICAL: current read ranges unavailable: " + sourceErr.Error()
			return false, nil
		}
		return false, fmt.Errorf("complete current read ranges failed, %w", err)
	}

	// Missing, size-mismatched and duplicate paths keep the original complete route without partial optimization.
	var unmatched int64
	if err := s.stage.DB.WithContext(ctx).Model(&row{}).
		Where("ranges IS NULL").Count(&unmatched).Error; err != nil {
		return false, fmt.Errorf("check completed read ranges failed, %w", err)
	}
	if unmatched != 0 {
		s.reason = "LOGICAL: current read ranges are missing, ambiguous or inconsistent"
		return false, nil
	}
	return s.indexCompletedRanges(ctx)
}

func updateReadRanges(tx *gorm.DB, updates []readRangeUpdate) error {
	if len(updates) == 0 {
		return nil
	}
	// One bounded statement joins current paths to selected rows; unmatched Index entries are never stored.
	var query strings.Builder
	query.WriteString("WITH current(path, size, ranges, hits) AS (VALUES ")
	args := make([]any, 0, 4*len(updates))
	for i, value := range updates {
		if i != 0 {
			query.WriteByte(',')
		}
		query.WriteString("(?,?,?,?)")
		args = append(args, value.path, value.size, value.ranges, value.hits)
	}
	query.WriteString(`) UPDATE read_order SET
 ranges = CASE WHEN range_seen = 0
 AND (SELECT hits FROM current WHERE current.path = read_order.path) = 1
 AND size = (SELECT size FROM current WHERE current.path = read_order.path)
 THEN (SELECT ranges FROM current WHERE current.path = read_order.path) ELSE NULL END,
 range_seen = MIN(2, range_seen + (SELECT hits FROM current WHERE current.path = read_order.path))
 WHERE path <> '' AND path IN (SELECT path FROM current)`)
	if err := tx.Exec(query.String(), args...).Error; err != nil {
		return fmt.Errorf("join current read ranges failed, %w", err)
	}
	return nil
}

func (s *Sequence) indexCompletedRanges(ctx context.Context) (bool, error) {
	// Only newly filled rows need canonical states and spatial candidates; existing valid inputs stay untouched.
	var cursor int64
	for {
		if err := s.check(ctx); err != nil {
			return false, err
		}
		var rows []row
		if err := s.stage.DB.WithContext(ctx).Raw(`WITH pending AS (
 SELECT rank, ranges FROM read_order WHERE rank > ? AND path <> '' ORDER BY rank LIMIT ?
), bounded AS (
 SELECT rank, SUM(length(ranges)) OVER (ORDER BY rank) AS bytes FROM pending
) SELECT read_order.* FROM read_order JOIN bounded USING(rank)
 WHERE bounded.bytes <= ? ORDER BY rank`, cursor, readRangeBatchItems, cacheBytes).Scan(&rows).Error; err != nil {
			return false, fmt.Errorf("read completed ranges after rank %d failed, %w", cursor, err)
		}
		if len(rows) == 0 {
			return true, nil
		}
		cursor = rows[len(rows)-1].Rank
		var candidates []candidate
		for i, value := range rows {
			var ranges []Range
			if err := json.Unmarshal(value.Ranges, &ranges); err != nil {
				return false, fmt.Errorf("decode completed read ranges at rank %d failed, %w", value.Rank, err)
			}
			state, indices, err := s.rangeRow(value.Rank, Span{ID: value.CopyID, Ranges: ranges})
			if err != nil || s.reason != "" {
				return false, err
			}
			rows[i].Canonical = state.Canonical
			candidates = appendCandidates(candidates, value.Rank, indices)
		}

		// Canonical rows and candidates use bounded bulk writes in the existing planning database.
		if err := s.stage.DB.WithContext(ctx).Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "rank"}},
			DoUpdates: clause.AssignmentColumns([]string{"canonical"})}).CreateInBatches(rows, 100).Error; err != nil {
			return false, fmt.Errorf("save completed read states failed, %w", err)
		}
		if len(candidates) != 0 {
			if err := s.stage.DB.WithContext(ctx).CreateInBatches(candidates, 100).Error; err != nil {
				return false, fmt.Errorf("index completed read ranges failed, %w", err)
			}
		}
	}
}
