package library

import (
	"context"
	"fmt"
	"strings"
)

// LocationOriginalsPage reads one Location's recorded originals in path order, regardless of
// current accessibility. Each row is the association itself; a caller that also needs retained
// evidence reads it separately through ReadFileTrackingKeys.
func (l *Library) LocationOriginalsPage(ctx context.Context, locationID int64, after string, limit int) ([]*FileLocation, error) {
	if limit <= 0 || limit > 1000 {
		return nil, fmt.Errorf("invalid original manifest page size")
	}
	var values []*FileLocation
	if err := l.readDB().WithContext(ctx).Where("location_id = ? AND path > ?", locationID, after).
		Order("path").Limit(limit).Find(&values).Error; err != nil {
		return nil, err
	}
	return values, nil
}

// ListLocationPaths lists the recorded paths under one parent prefix for Restore ownership checks,
// not filesystem browsing: it derives ancestor names from references without persisting directory
// inventory and keeps the parent's own listing ordered and distinct.
func (l *Library) ListLocationPaths(ctx context.Context, locationID int64, parent, after string, limit int) ([]string, bool, error) {
	// Bound the page and resume after the previously emitted child, rather than its parent.
	if limit <= 0 || limit > 1000 {
		return nil, false, fmt.Errorf("invalid Location page size")
	}
	rows := make([]string, 0, limit+1)
	cursor := parent
	inclusive := false
	if after > cursor {
		cursor = after
		if strings.HasSuffix(after, "/") {
			// A returned directory represents its entire subtree; seek to its binary upper bound.
			cursor, inclusive = strings.TrimSuffix(after, "/")+"0", true
		}
	}

	// Consume only the remaining path range, collapsing descendants into immediate child names.
	previous := ""
	for {
		var originals []*FileLocation
		query := l.readDB().WithContext(ctx).Where("location_id = ?", locationID)
		if inclusive {
			query = query.Where("path >= ?", cursor)
		} else {
			query = query.Where("path > ?", cursor)
		}
		if err := query.Order("path").Limit(batchSize).Find(&originals).Error; err != nil {
			return nil, false, err
		}
		if len(originals) == 0 {
			break
		}
		for _, p := range originals {
			if !strings.HasPrefix(p.Path, parent) {
				return rows, false, nil
			}
			name := p.Path
			if offset := strings.Index(strings.TrimPrefix(p.Path, parent), "/"); offset >= 0 {
				name = p.Path[:len(parent)+offset+1]
			}
			if name <= after || name == previous {
				continue
			}
			previous = name
			rows = append(rows, name)
			if len(rows) > limit {
				return rows[:limit], true, nil
			}
		}
		cursor = originals[len(originals)-1].Path
		inclusive = false
	}
	return rows, false, nil
}
