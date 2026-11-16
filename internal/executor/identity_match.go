package executor

import (
	"context"
	"slices"

	"github.com/samuelncui/yatm/internal/executor/observation"
	"github.com/samuelncui/yatm/internal/library"
	"github.com/sirupsen/logrus"
)

// MatchLocationObservations assigns existing File identities to the unassigned observations of one
// Location. Candidates come only from the evidence those observations carry: originals at their
// paths, this Location's originals with their signatures and Files whose native evidence names them.
// A candidate whose original belongs to another Location is ineligible, and one whose original is
// still present at an unobserved path keeps it.
func (e *Executor) MatchLocationObservations(ctx context.Context, location *library.Location, store observation.MatchStore, logger logrus.FieldLogger) error {
	ids, err := e.matchingFileIDs(ctx, location, store)
	if err != nil || len(ids) == 0 {
		return err
	}
	candidates := func(ctx context.Context, after int64, limit int) ([]*observation.Original, error) {
		start, found := slices.BinarySearch(ids, after)
		if found {
			start++
		}
		if start == len(ids) {
			return nil, nil
		}
		rows, err := e.lib.MatchingCandidates(ctx, ids[start:min(start+limit, len(ids))])
		if err != nil {
			return nil, err
		}
		var paths []string
		for _, row := range rows {
			if row.Original != nil && row.Original.LocationID == location.ID {
				paths = append(paths, row.Original.Path)
			}
		}
		observed, err := store.Observed(ctx, paths)
		if err != nil {
			return nil, err
		}
		result := make([]*observation.Original, 0, len(rows))
		for _, row := range rows {
			candidate := &observation.Original{FileID: row.FileID, Evidence: observation.FromKeys(row.Tracking)}
			if old := row.Original; old != nil {
				if old.LocationID != location.ID || !observed[old.Path] && !e.absentAdmissionPath(location, old) {
					continue
				}
				candidate.Path = old.Path
				candidate.Signature, candidate.Hash, candidate.Size = old.Signature, old.Hash, old.Size
			}
			result = append(result, candidate)
		}
		return result, nil
	}
	return observation.Match(ctx, store, candidates, logger)
}

// matchingFileIDs collects, in File-ID order, the identities relevant to the pending observations.
func (e *Executor) matchingFileIDs(ctx context.Context, location *library.Location, store observation.MatchStore) ([]int64, error) {
	var ids []int64
	after := ""
	for {
		pending, err := store.Pending(ctx, after, observation.BatchSize)
		if err != nil {
			return nil, err
		}
		if len(pending) == 0 {
			break
		}
		paths := make([]string, 0, len(pending))
		var signatures [][]byte
		var keys []*library.FileTrackingKey
		for _, row := range pending {
			paths = append(paths, row.Path)
			if len(row.Signature) != 0 {
				signatures = append(signatures, row.Signature)
			}
			keys = append(keys, row.Evidence.Keys()...)
		}
		originals, err := e.lib.ReadFileOriginalsAt(ctx, location.ID, paths)
		if err != nil {
			return nil, err
		}
		for _, old := range originals {
			ids = append(ids, old.FileID)
		}
		for cursor := int64(0); ; {
			rows, err := e.lib.SignatureCandidates(ctx, location.ID, signatures, cursor, observation.BatchSize)
			if err != nil {
				return nil, err
			}
			for _, row := range rows {
				ids = append(ids, row.FileID)
			}
			if len(rows) < observation.BatchSize {
				break
			}
			cursor = rows[len(rows)-1].FileID
		}
		for cursor := int64(0); ; {
			rows, err := e.lib.NativeCandidates(ctx, location.ID, keys, cursor, observation.BatchSize)
			if err != nil {
				return nil, err
			}
			for _, row := range rows {
				ids = append(ids, row.FileID)
			}
			if len(rows) < observation.BatchSize {
				break
			}
			cursor = rows[len(rows)-1].FileID
		}
		after = pending[len(pending)-1].Path
	}
	slices.Sort(ids)
	return slices.Compact(ids), nil
}
