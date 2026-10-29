package readplan

import (
	"context"
	"math/rand/v2"
	"slices"
	"time"
)

// perturb reorders up to four natural-run blocks from the best route to leave its local minimum.
func (s *Sequence) perturb(ctx context.Context, route route, initial State, cache *cache, random *rand.Rand) (bool, error) {
	// Natural segments preserve cheap sequential reads during a kick; descent may still split them.
	runs, err := s.naturalRuns(ctx, route, cache)
	if err != nil {
		return false, err
	}
	minimum := 0
	if s.fixedFirst != nil {
		minimum = 1
	}
	count := min(4, len(runs)-minimum)
	if count < 2 {
		return false, nil
	}

	// Directed cost guidance changes cut selection only; legal block orders and complete replay stay shared.
	cuts, err := s.perturbCuts(ctx, runs, initial, cache, random, minimum, count)
	if err != nil {
		return false, err
	}
	slices.Sort(cuts[:count])
	order := [4]int{1, 0, 2, 3}
	if count == 3 {
		order = [4]int{2, 0, 1, 3}
	}
	if count == 4 {
		orders := [4][4]int{{2, 1, 0, 3}, {1, 3, 0, 2}, {3, 1, 2, 0}, {2, 0, 3, 1}}
		order = orders[random.IntN(len(orders))]
	}
	if err := s.check(ctx); err != nil {
		return false, err
	}
	readReconnection{cuts: cuts, order: order, count: count}.apply(route, runs)
	return true, nil
}

func (s *Sequence) perturbCuts(ctx context.Context, runs []readRun, initial State, cache *cache,
	random *rand.Rand, minimum, count int,
) ([4]int, error) {
	// The MIT-licensed JPT/LKH-AMZ KSwapKick.c inspires sampled weak edges and bounded candidate walks.
	// YATM prices directed complete members instead of treating every long file as an expensive connection.
	var cuts [4]int

	// This kick owns one unchanged run graph; stable raw probes share the existing byte-bounded cache.
	if err := s.indexRuns(ctx, runs); err != nil {
		return cuts, err
	}
	byFirst := make(map[int64]int, len(runs))
	for i, run := range runs {
		byFirst[run.first] = i
	}
	costs := &readRunCosts{sequence: s, runs: runs, initial: initial, cache: cache,
		edges: make(map[[2]int]edgeCost)}
	neighborhoods := make(map[int][]int)
	neighbors := func(anchor int) ([]int, error) {
		if nearby, found := neighborhoods[anchor]; found {
			return nearby, nil
		}
		nearby, err := s.nearbyRuns(ctx, anchor, runs, cache, costs, byFirst)
		if err != nil {
			return nil, err
		}
		neighborhoods[anchor] = nearby
		return nearby, nil
	}

	// Normalize each sampled incoming edge by alternative incoming edges to the same complete target.
	best, bestGain := -1, time.Duration(0)
	for range 10 + len(runs)/1000 {
		if err := s.check(ctx); err != nil {
			return cuts, err
		}
		anchor := readUnusedCut(random, minimum, len(runs), nil)
		nearby, err := neighbors(anchor)
		if err != nil {
			return cuts, err
		}
		gain, known, err := costs.incomingGain(ctx, anchor, nearby)
		if err != nil {
			return cuts, err
		}
		if known && (best < 0 || gain > bestGain) {
			best, bestGain = anchor, gain
		}
	}
	if best < 0 {
		best = readUnusedCut(random, minimum, len(runs), nil)
	}
	cuts[0] = best

	// Walk at most 50 candidate edges per remaining cut, without backtracking or reusing a selected boundary.
	for i := 1; i < count; i++ {
		from, previous, selected := cuts[i-1], -1, -1
		for range 50 {
			if err := s.check(ctx); err != nil {
				return cuts, err
			}
			nearby, err := neighbors(from)
			if err != nil {
				return cuts, err
			}
			var storage [8]int
			available := storage[:0]
			for _, next := range nearby {
				if next < minimum || next == previous || slices.Contains(cuts[:i], next) {
					continue
				}
				_, known, err := costs.edge(ctx, from+1, next+1)
				if err != nil {
					return cuts, err
				}
				if known {
					available = append(available, next)
				}
			}
			if len(available) == 0 {
				break
			}
			previous, from = from, available[random.IntN(len(available))]
			selected = from
		}
		if selected < 0 {
			selected = readUnusedCut(random, minimum, len(runs), cuts[:i])
		}
		cuts[i] = selected
	}
	return cuts, nil
}

func (c *readRunCosts) incomingGain(ctx context.Context, anchor int, neighbors []int) (time.Duration, bool, error) {
	// The current predecessor and all alternatives read the same target, cancelling its unavoidable content cost.
	if err := c.sequence.check(ctx); err != nil {
		return 0, false, err
	}
	current, known, err := c.edge(ctx, anchor, anchor+1)
	if err != nil || !known {
		return 0, false, err
	}
	cheapest, available := time.Duration(0), false
	for _, from := range neighbors {
		if err := c.sequence.check(ctx); err != nil {
			return 0, false, err
		}
		cost, known, err := c.edge(ctx, from+1, anchor+1)
		if err != nil {
			return 0, false, err
		}
		if known && (!available || cost < cheapest) {
			cheapest, available = cost, true
		}
	}
	if !available {
		return 0, false, nil
	}
	return current - cheapest, true, nil
}

func readUnusedCut(random *rand.Rand, minimum, limit int, selected []int) int {
	// Select uniformly from unused legal cuts with at most four exclusions and no retry loop.
	var storage [4]int
	excluded := storage[:len(selected)]
	copy(excluded, selected)
	slices.Sort(excluded)
	cut := minimum + random.IntN(limit-minimum-len(selected))
	for _, used := range excluded {
		if used <= cut {
			cut++
		}
	}
	return cut
}
