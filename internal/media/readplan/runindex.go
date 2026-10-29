package readplan

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/samuelncui/yatm/entity"
)

func (s *Sequence) indexRuns(ctx context.Context, runs []readRun) error {
	// Stable first-member identities survive whole-run moves; only split, merged or changed runs need SQL writes.
	next := make(map[int64]int64, len(runs))
	var changed []readRun
	for _, run := range runs {
		if err := s.check(ctx); err != nil {
			return err
		}
		if run.first <= 0 || run.last <= 0 {
			return errors.New("read run endpoint has an invalid rank")
		}
		if _, exists := next[run.first]; exists {
			return errors.New("read run index contains a duplicate first member")
		}
		next[run.first] = run.last
		if s.runEnds[run.first] != run.last {
			changed = append(changed, run)
		}
	}
	var removed []int64
	for first, last := range s.runEnds {
		if next[first] != last {
			removed = append(removed, first)
		}
	}
	slices.Sort(removed)
	if err := s.check(ctx); err != nil {
		return err
	}
	if len(changed) == 0 && len(removed) == 0 {
		return nil
	}

	// One transaction publishes all changed boundaries without exposing a mixed generation on interruption.
	tx := s.stage.DB.WithContext(ctx).Begin()
	if tx.Error != nil {
		return tx.Error
	}
	err := func() (rerr error) {
		// Successful statement cleanup preserves the soft-budget sentinel for the caller's fallback.
		statements := newStatements(tx.Statement.ConnPool)
		defer func() {
			if err := statements.Close(); err != nil {
				rerr = errors.Join(rerr, err)
			}
		}()
		// Delete only stale stable identities; immutable source rows on sides 0/1 remain untouched.
		const batchRuns = 64
		args := make([]any, 0, batchRuns*8)
		for first := 0; first < len(removed); first += batchRuns {
			if err := s.check(ctx); err != nil {
				return err
			}
			args = args[:0]
			for _, rank := range removed[first:min(first+batchRuns, len(removed))] {
				args = append(args, rank)
			}
			query := "DELETE FROM read_candidates WHERE side >= 2 AND rank IN (" +
				strings.TrimSuffix(strings.Repeat("?,", len(args)), ",") + ")"
			if _, err := statements.ExecContext(ctx, query, args...); err != nil {
				return err
			}
		}

		// Bound inserts while preserving every changed endpoint's source groups, including its stable identity.
		for first := 0; first < len(changed); first += batchRuns {
			if err := s.check(ctx); err != nil {
				return err
			}
			batch := changed[first:min(first+batchRuns, len(changed))]
			args = args[:0]
			for _, run := range batch {
				args = append(args, run.first, 2, run.first, 0, run.first, 3, run.last, 1)
			}

			// One missing source suppresses the entire batch, retaining the existing fatal-error boundary.
			query := readRunIndexQuery(len(batch) * 2)
			result, err := statements.ExecContext(ctx, query, args...)
			if err != nil {
				return err
			}
			if rows, err := result.RowsAffected(); err != nil {
				return err
			} else if rows == 0 {
				return errors.New("read run endpoint is missing from the spatial index")
			}
		}

		// Expiry after the final batch still rolls the complete generation back.
		return s.check(ctx)
	}()

	// Rollback failure remains fatal; a clean rollback leaves deadline expiry eligible for fallback.
	if err != nil {
		if rollback := tx.Rollback().Error; rollback != nil {
			return errors.Join(err, rollback)
		}
		return err
	}
	if err := tx.Commit().Error; err != nil {
		return err
	}
	s.runEnds = next
	s.runGeneration++
	return nil
}

// readRunIndexQuery uses the existing composite primary index for exact rank/side source lookups.
// SQLite may otherwise choose a spatial covering index that scans every source on the same side.
func readRunIndexQuery(endpoints int) string {
	return "WITH endpoints(rank, side, source_rank, source_side) AS (VALUES " +
		strings.TrimSuffix(strings.Repeat("(?, ?, ?, ?),", endpoints), ",") + ") " +
		"INSERT INTO read_candidates (rank, side, group_key, position) " +
		"SELECT endpoint.rank, endpoint.side, source.group_key, source.position " +
		"FROM endpoints AS endpoint JOIN read_candidates AS source INDEXED BY sqlite_autoindex_read_candidates_1 " +
		"ON source.rank = endpoint.source_rank AND source.side = endpoint.source_side " +
		"WHERE NOT EXISTS (SELECT 1 FROM endpoints AS missing WHERE NOT EXISTS " +
		"(SELECT 1 FROM read_candidates AS candidate " +
		"WHERE candidate.rank = missing.source_rank AND candidate.side = missing.source_side))"
}

func (s *Sequence) nearbyRuns(ctx context.Context, anchor int, runs []readRun,
	cache *cache, costs *readRunCosts, byFirst map[int64]int,
) ([]int, error) {
	// Probe run endpoints directly, so files inside one dense run cannot crowd out other runs.
	if err := s.check(ctx); err != nil {
		return nil, err
	}
	if cache.generation != s.runGeneration {
		clear(cache.runProbes)
		cache.runBytes, cache.generation = 0, s.runGeneration
	}
	first, err := cache.get(ctx, runs[anchor].first)
	if err != nil {
		return nil, err
	}
	last, err := cache.get(ctx, runs[anchor].last)
	if err != nil {
		return nil, err
	}
	var found [2][]int
	for side, position := range [2]*entity.TapeBytePosition{last.canonical.Position, first.span.Ranges[0].Start} {
		if err := s.check(ctx); err != nil {
			return nil, err
		}
		// Run probes depend on the current endpoint generation and use their own byte allowance.
		nearby, err := s.nearby(ctx, runs[anchor].first*2+int64(side), uint8(side+2), position, cache,
			cache.runProbes, &cache.runBytes)
		if err != nil {
			return nil, err
		}
		for _, first := range nearby {
			rank, known := byFirst[first]
			if !known {
				return nil, fmt.Errorf("read run index contains an unknown first member %d", first)
			}
			if rank != anchor {
				found[side] = append(found[side], rank)
			}
		}
	}

	// Rank a directed edge against the target's current incoming edge before bounded truncation.
	// A compound reconnection need not pay the detachment cost of a standalone run insertion.
	type candidate struct {
		run   int
		delta time.Duration
	}
	var priced [2][]candidate
	for side := range found {
		for _, run := range found[side] {
			if err := s.check(ctx); err != nil {
				return nil, err
			}
			from, to := anchor+1, run+1
			if side == 1 {
				from, to = to, from
			}
			if s.fixedFirst != nil && to == 1 {
				continue
			}

			// Both terms read the same target; unknown costs never become a zero-price opportunity.
			proposed, known, err := costs.edge(ctx, from, to)
			if err != nil {
				return nil, err
			}
			if !known {
				continue
			}
			current, known, err := costs.edge(ctx, to-1, to)
			if err != nil {
				return nil, err
			}
			if known {
				priced[side] = append(priced[side], candidate{run: run, delta: proposed - current})
			}
		}
		slices.SortFunc(priced[side], func(a, b candidate) int {
			if order := cmp.Compare(a.delta, b.delta); order != 0 {
				return order
			}
			return cmp.Compare(a.run, b.run)
		})
	}

	// Interleave independently ranked entry/exit opportunities, retaining both directions without duplicates.
	nearby := make([]int, 0, 8)
	seen := make(map[int]struct{}, cap(nearby))
	for i := 0; i < max(len(priced[0]), len(priced[1])) && len(nearby) < cap(nearby); i++ {
		for side := range priced {
			if i >= len(priced[side]) {
				continue
			}
			run := priced[side][i].run
			if _, ok := seen[run]; ok {
				continue
			}
			seen[run] = struct{}{}
			nearby = append(nearby, run)
			if len(nearby) == cap(nearby) {
				break
			}
		}
	}
	return nearby, nil
}

func (s *Sequence) reconnectNearby(ctx context.Context, runs []readRun,
	cache *cache, costs *readRunCosts, minimum int, visit func([4]int, int) error,
	improved func() bool,
) error {
	// Reordering changes only this linear identity map; unchanged endpoint rows survive the next sweep.
	byFirst := make(map[int64]int, len(runs))
	for ordinal, run := range runs {
		byFirst[run.first] = ordinal
	}
	if err := s.indexRuns(ctx, runs); err != nil {
		return err
	}
	for anchor := range runs {
		nearby, err := s.nearbyRuns(ctx, anchor, runs, cache, costs, byFirst)
		if err != nil {
			return err
		}
		for i, a := range nearby {
			for _, b := range nearby[i+1:] {
				if err := readCutCombinations(readNearbyCuts(anchor, a, b, minimum, len(runs)), visit); err != nil {
					return err
				}
			}
		}

		// Complete this anchor's neighbourhood before returning its improvement for immediate full rescoring.
		if err := s.check(ctx); err != nil {
			return err
		}
		if improved() {
			return nil
		}
	}
	return nil
}
