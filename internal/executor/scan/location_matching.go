package scan

import (
	"context"
	"slices"

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

	// Selected old identities are already owned by the complete manifest. Keep only scalar IDs;
	// path, content and tracking facts remain in their paged owners rather than a second table.
	var ids []int64
	if err := r.eachEntry(ctx, &Scope{LocationID: r.source.ID}, func(row *Entry) error {
		if id := row.Before.GetFileId(); id > 0 {
			ids = append(ids, id)
		}
		return nil
	}); err != nil {
		return err
	}
	var after int64
	for {
		page, err := r.exe.Lib().DetachedMatchingFileIDsPage(ctx, r.source.ExecutorID, after, batchSize)
		if err != nil {
			return err
		}
		ids = append(ids, page...)
		if len(page) < batchSize {
			break
		}
		after = page[len(page)-1]
	}
	slices.Sort(ids)
	ids = slices.Compact(ids)

	// Every matching round consumes the same global File-ID order, with bounded catalog fact reads.
	candidates := func(ctx context.Context, after int64, limit int) ([]*observation.Original, error) {
		start, found := slices.BinarySearch(ids, after)
		if found {
			start++
		}
		if start == len(ids) {
			return nil, nil
		}
		rows, err := r.exe.Lib().MatchingCandidates(ctx, ids[start:min(start+limit, len(ids))])
		if err != nil {
			return nil, err
		}
		result := make([]*observation.Original, 0, len(rows))
		for _, row := range rows {
			candidate := &observation.Original{FileID: row.FileID}
			result = append(result, candidate)
			if old := row.Original; old != nil {
				if old.LocationID != r.source.ID {
					continue
				}
				candidate.Path = old.Path
				candidate.Signature, candidate.Hash, candidate.Size = old.Signature, old.Hash, old.Size
			}
			candidate.Evidence = observation.FromKeys(row.Tracking)
		}
		return result, nil
	}
	store := observation.NewMatchStore(r.db.Model(&Entry{}).Where("location_id = ?", r.source.ID))
	return observation.Match(ctx, store, candidates, r.logger)
}
