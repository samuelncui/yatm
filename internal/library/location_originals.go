package library

import (
	"context"
	"fmt"
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
